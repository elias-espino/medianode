package admin

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"medianode/internal/database"
	"medianode/internal/monitoring"
)

// walkBudget limita cuánto tarda en sumar un directorio: uno con millones de
// archivos no debe colgar el panel.
const walkBudget = 3 * time.Second

// Folder es un directorio de datos del servidor.
type Folder struct {
	Key       string `json:"key"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Files     int    `json:"files"`
	Partial   bool   `json:"partial"` // Se cortó la suma por tiempo: el tamaño es un mínimo.
}

// Volume es un disco con su espacio.
type Volume struct {
	Path       string `json:"path"`
	Readable   bool   `json:"readable"`
	FreeBytes  uint64 `json:"free_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
	Library    string `json:"library,omitempty"`
}

// Storage es el uso de disco de los datos del servidor y de las bibliotecas.
type Storage struct {
	Folders   []Folder `json:"folders"`
	Database  int64    `json:"database_bytes"`
	Data      Volume   `json:"data"`
	Libraries []Volume `json:"libraries"`
}

// dataDir es el directorio donde vive la base de datos: el volumen de los
// datos.
func (s *Service) dataDir() string { return filepath.Dir(s.Config.Database.Path) }

// StorageNow calcula el uso de disco.
func (s *Service) StorageNow(ctx context.Context) Storage {
	p := s.Config.Paths
	st := Storage{Folders: []Folder{}, Libraries: []Volume{}, Database: s.dbSize()}
	for _, f := range []struct{ key, path string }{
		{"metadata", p.Metadata}, {"images", p.Images}, {"cache", p.Cache},
		{"transcodes", p.Transcodes}, {"logs", p.Logs}, {"backups", p.Backups},
	} {
		st.Folders = append(st.Folders, folderSize(ctx, f.key, f.path))
	}
	st.Data = volume(s.dataDir())
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT l.name, lp.path FROM library_paths lp JOIN libraries l ON l.id = lp.library_id ORDER BY l.name, lp.path`)
	if err != nil {
		return st
	}
	defer rows.Close()
	for rows.Next() {
		var name, path string
		if rows.Scan(&name, &path) != nil {
			continue
		}
		v := volume(path)
		v.Library = name
		st.Libraries = append(st.Libraries, v)
	}
	_ = rows.Err() // Una lectura a medias deja la lista como esté: es informativa.
	return st
}

func volume(path string) Volume {
	v := Volume{Path: path}
	if _, err := os.Stat(path); err != nil {
		return v
	}
	v.Readable = true
	if free, total, err := monitoring.DiskSpace(path); err == nil {
		v.FreeBytes, v.TotalBytes = free, total
	}
	return v
}

func folderSize(ctx context.Context, key, path string) Folder {
	f := Folder{Key: key, Path: path}
	deadline := time.Now().Add(walkBudget)
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			//nolint:nilerr // un archivo que desaparece mientras se suma no es un error
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			f.Partial = true
			return fs.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				f.SizeBytes += info.Size()
				f.Files++
			}
		}
		return nil
	})
	return f
}

// duration son los segundos entre dos instantes en el formato de la base de
// datos; cero si alguno no se entiende.
func duration(start, end string) float64 {
	a, err1 := database.ParseTime(start)
	b, err2 := database.ParseTime(end)
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0
	}
	return b.Sub(a).Seconds()
}
