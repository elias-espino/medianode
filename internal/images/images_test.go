package images_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/images"
	"medianode/internal/media"
	"medianode/internal/testutil"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	db  *database.DB
	m   *images.Manager
	dir string
	srv *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	var pic bytes.Buffer
	if err := jpeg.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/404":
			w.WriteHeader(http.StatusNotFound)
		case "/503":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/html":
			// La cabecera miente: se decide por el contenido.
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("<!doctype html><script>alert(1)</script>"))
		case "/huge":
			_, _ = w.Write(pic.Bytes())
			_, _ = w.Write(make([]byte, 11<<20))
		default:
			_, _ = w.Write(pic.Bytes())
		}
	}))
	t.Cleanup(srv.Close)
	db := testutil.NewDB(t)
	dir := t.TempDir()
	return &fixture{t: t, ctx: context.Background(), db: db, m: images.New(dir, db, srv.Client()), dir: dir, srv: srv}
}

// item crea un elemento con una imagen pendiente de descargar.
func (f *fixture) item(path string) (string, string) {
	lib, id := "lib", "item-"+strings.Trim(path, "/")
	f.exec("INSERT OR IGNORE INTO libraries (id, name, type) VALUES (?, 'l', 'movies')", lib)
	f.exec("INSERT INTO media_items (id, library_id, type, title) VALUES (?, ?, 'movie', 't')", id, lib)
	url := f.srv.URL + path
	f.exec("INSERT INTO media_images (id, media_item_id, type, source_url) VALUES (?, ?, 'poster', ?)", id+"-img", id, url)
	return id, url
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Write.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) images(id string) []sqlc.ListItemImagesRow {
	rows, err := f.db.R.ListItemImages(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func TestDescargaYApertura(t *testing.T) {
	f := newFixture(t)
	id, _ := f.item("/ok")
	n, err := f.m.FetchPending(f.ctx)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	rows := f.images(id)
	if len(rows) != 1 || rows[0].LocalPath == nil || *rows[0].LocalPath != id+"/poster.jpg" {
		t.Fatalf("%+v", rows)
	}
	file, err := f.m.Open(*rows[0].LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if file.Info.Size() == 0 {
		t.Fatal("imagen vacía")
	}
	// No quedan temporales.
	entries, _ := os.ReadDir(filepath.Join(f.dir, id))
	if len(entries) != 1 {
		t.Fatalf("%v", entries)
	}
}

func TestFallosPermanentesOlvidanLaImagen(t *testing.T) {
	f := newFixture(t)
	gone, _ := f.item("/404")
	html, _ := f.item("/html")
	huge, _ := f.item("/huge")
	if _, err := f.m.FetchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{gone, html, huge} {
		if rows := f.images(id); len(rows) != 0 {
			t.Fatalf("%s: %+v", id, rows)
		}
		if _, err := os.Stat(filepath.Join(f.dir, id)); err == nil {
			entries, _ := os.ReadDir(filepath.Join(f.dir, id))
			if len(entries) != 0 {
				t.Fatalf("%s: quedó %v", id, entries)
			}
		}
	}
}

func TestFalloPasajeroLaDejaPendiente(t *testing.T) {
	f := newFixture(t)
	id, _ := f.item("/503")
	_, err := f.m.FetchPending(f.ctx)
	if !errors.Is(err, images.ErrTransient) {
		t.Fatalf("%v", err)
	}
	if rows := f.images(id); len(rows) != 1 || rows[0].LocalPath != nil {
		t.Fatalf("%+v", rows)
	}
}

// Sin conexión con el servidor de imágenes, la imagen sigue pendiente.
func TestSinConexionLaDejaPendiente(t *testing.T) {
	f := newFixture(t)
	id, _ := f.item("/ok")
	f.exec("UPDATE media_images SET source_url = 'http://127.0.0.1:1/poster.jpg' WHERE media_item_id = ?", id)
	if _, err := f.m.FetchPending(f.ctx); !errors.Is(err, images.ErrTransient) {
		t.Fatalf("%v", err)
	}
	if rows := f.images(id); len(rows) != 1 {
		t.Fatalf("la fila no debe borrarse: %+v", rows)
	}
}

// Si la URL cambia durante la descarga, la fila sigue pidiendo la nueva.
func TestURLCambiadaNoSeRegistra(t *testing.T) {
	f := newFixture(t)
	id, url := f.item("/ok")
	f.exec("UPDATE media_images SET source_url = 'https://otra' WHERE media_item_id = ?", id)
	if err := f.m.Fetch(f.ctx, id, media.ImagePoster, url); err != nil {
		t.Fatal(err)
	}
	if rows := f.images(id); rows[0].LocalPath != nil {
		t.Fatalf("%+v", rows)
	}
}

func TestOpenNoSaleDelDirectorio(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(filepath.Dir(f.dir), "secreto.jpg")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })
	for _, rel := range []string{"../secreto.jpg", "..\\secreto.jpg", outside, "/etc/passwd", ""} {
		if _, err := f.m.Open(rel); !errors.Is(err, fs.ErrNotExist) && err == nil {
			t.Fatalf("%q se abrió", rel)
		}
	}
	if err := f.m.Fetch(f.ctx, "../fuera", media.ImagePoster, f.srv.URL+"/ok"); err == nil {
		t.Fatal("un identificador con ruta no debe aceptarse")
	}
}

func TestEvictBorraLoQueNadieReferencia(t *testing.T) {
	f := newFixture(t)
	keep, _ := f.item("/ok")
	drop, _ := f.item("/ok2")
	if _, err := f.m.FetchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Restos: un temporal interrumpido y una imagen sustituida.
	for _, name := range []string{"poster-123.tmp", "backdrop.png"} {
		if err := os.WriteFile(filepath.Join(f.dir, keep, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.exec("DELETE FROM media_items WHERE id = ?", drop)

	n, err := f.m.Evict(f.ctx)
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.dir, keep))
	if len(entries) != 1 || entries[0].Name() != "poster.jpg" {
		t.Fatalf("%v", entries)
	}
	if _, err := os.Stat(filepath.Join(f.dir, drop)); !os.IsNotExist(err) {
		t.Fatalf("la carpeta del elemento borrado sigue: %v", err)
	}
}
