// Package images descarga y sirve las imágenes del catálogo (§8.2). Se guardan
// en {dir}/{item_id}/{type}.{ext} y se descargan una sola vez, desde el
// Enricher; abrir un detalle nunca provoca una descarga.
package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
)

const (
	maxImageBytes   = 10 << 20
	downloadTimeout = 30 * time.Second
	fetchBatch      = 50
)

// ErrTransient indica un fallo de red pasajero: la imagen sigue pendiente.
var ErrTransient = errors.New("descarga de imagen interrumpida")

// Tipos aceptados según el contenido, no según la cabecera del servidor.
var extensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// Manager gestiona el directorio de imágenes.
type Manager struct {
	dir  string
	db   *database.DB
	http *http.Client
}

// New crea el gestor sobre dir, que debe existir.
func New(dir string, db *database.DB, client *http.Client) *Manager {
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}
	return &Manager{dir: dir, db: db, http: client}
}

// File es una imagen abierta para servirla.
type File struct {
	*os.File
	Info fs.FileInfo
}

// Open abre una imagen por su ruta relativa, sin salir del directorio.
func (m *Manager) Open(rel string) (*File, error) {
	if !filepath.IsLocal(rel) {
		return nil, fs.ErrNotExist
	}
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{File: f, Info: info}, nil
}

// FetchPending descarga las imágenes que aún no tienen archivo. Una imagen
// que el servidor de origen ya no tiene se olvida; un fallo de red detiene la
// tanda para reintentar más tarde.
func (m *Manager) FetchPending(ctx context.Context) (int, error) {
	done := 0
	for {
		rows, err := m.db.R.ListImagesToFetch(ctx, fetchBatch)
		if err != nil {
			return done, fmt.Errorf("listar imágenes: %w", err)
		}
		if len(rows) == 0 {
			return done, nil
		}
		for _, r := range rows {
			t := media.ImageType(r.Type)
			err := m.Fetch(ctx, r.MediaItemID, t, *r.SourceUrl)
			switch {
			case err == nil:
				done++
			case errors.Is(err, ErrTransient) || ctx.Err() != nil:
				return done, err
			default:
				// Sin borrar la fila, la siguiente tanda volvería a pedirla.
				if derr := m.db.Q.DeleteItemImage(ctx, sqlc.DeleteItemImageParams{MediaItemID: r.MediaItemID, Type: r.Type}); derr != nil {
					return done, derr
				}
			}
		}
	}
}

// Fetch descarga url como imagen t del elemento y la registra. El archivo se
// escribe aparte y se renombra, para que nunca se sirva a medias.
func (m *Manager) Fetch(ctx context.Context, itemID string, t media.ImageType, url string) error {
	if !t.Valid() || !filepath.IsLocal(itemID) || strings.ContainsAny(itemID, `/\`) {
		return fmt.Errorf("imagen no válida: %q %q", itemID, t)
	}
	data, ext, err := m.download(ctx, url)
	if err != nil {
		return err
	}
	dir := filepath.Join(m.dir, itemID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, string(t)+"-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	name := string(t) + ext
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	// Si la URL cambió mientras tanto, no se actualiza nada y la fila sigue
	// pidiendo la nueva; si el elemento desapareció, Evict borrará el archivo.
	rel := path.Join(itemID, name)
	_, err = m.db.Q.SetImageLocalPath(ctx, sqlc.SetImageLocalPathParams{
		MediaItemID: itemID, Type: string(t), LocalPath: &rel, SourceUrl: &url,
	})
	if err != nil {
		return fmt.Errorf("registrar imagen: %w", err)
	}
	return nil
}

func (m *Manager) download(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, "", fmt.Errorf("%w: HTTP %d", ErrTransient, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("imagen no disponible: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrTransient, err)
	}
	if len(data) > maxImageBytes {
		return nil, "", errors.New("imagen demasiado grande")
	}
	ext, ok := extensions[http.DetectContentType(data)]
	if !ok {
		return nil, "", errors.New("el contenido no es una imagen admitida")
	}
	return data, ext, nil
}

// Evict borra los archivos que ninguna fila referencia: imágenes de elementos
// eliminados, sustituidas o descargas interrumpidas. Las imágenes vigentes no
// caducan, porque volver a pedirlas exigiría descargar al abrir un detalle.
func (m *Manager) Evict(ctx context.Context) (int, error) {
	paths, err := m.db.R.ListImageLocalPaths(ctx)
	if err != nil {
		return 0, fmt.Errorf("listar imágenes: %w", err)
	}
	keep := make(map[string]bool, len(paths))
	for _, p := range paths {
		keep[*p] = true
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, dir := range entries {
		if !dir.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(m.dir, dir.Name()))
		if err != nil {
			return removed, err
		}
		left := len(files)
		for _, f := range files {
			if keep[path.Join(dir.Name(), f.Name())] {
				continue
			}
			if err := os.Remove(filepath.Join(m.dir, dir.Name(), f.Name())); err != nil {
				return removed, err
			}
			removed++
			left--
		}
		if left == 0 {
			_ = os.Remove(filepath.Join(m.dir, dir.Name()))
		}
	}
	return removed, nil
}
