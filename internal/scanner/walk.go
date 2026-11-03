package scanner

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"

	"medianode/internal/database"
	"medianode/internal/storage"
)

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".mov": true, ".avi": true, ".wmv": true,
	".ts": true, ".m2ts": true, ".mts": true, ".webm": true, ".mpg": true, ".mpeg": true,
	".flv": true, ".ogv": true, ".3gp": true,
}

var subtitleExts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".vtt": true}

// Carpetas de sistema o de papelera que nunca contienen la biblioteca.
var skippedDirs = map[string]bool{
	"$recycle.bin": true, "system volume information": true, "@eadir": true, "#recycle": true,
	"lost+found": true,
}

// found es un archivo visto en el recorrido.
type found struct {
	rel   string // relativa a la raíz, con «/»
	abs   string
	size  int64
	mtime string
}

// walkResult es lo que deja el recorrido de una raíz.
type walkResult struct {
	videos    []found
	subtitles map[string][]string // carpeta relativa → subtítulos que contiene
	failed    []string            // carpetas que no se pudieron leer
}

// walk recorre root sin seguir symlinks de carpeta ni uniones, y solo acepta
// un symlink de archivo si su destino queda dentro de la raíz (§7.4).
func walk(ctx context.Context, root string, maxDepth int, log zerolog.Logger) (*walkResult, error) {
	res := &walkResult{subtitles: map[string][]string{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			if p == root {
				return err
			}
			// Una carpeta ilegible no debe hacer que sus archivos parezcan
			// borrados: se anota para excluirla de las ausencias.
			res.failed = append(res.failed, relPath(root, p))
			log.Warn().Err(err).Str("ruta", p).Msg("no se pudo leer")
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel := relPath(root, p)
		if d.IsDir() {
			if skippedDirs[strings.ToLower(name)] || strings.Count(rel, "/")+1 > maxDepth {
				return fs.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(name))
		isVideo, isSub := videoExts[ext], subtitleExts[ext]
		if !isVideo && !isSub {
			return nil
		}
		info, ok := fileInfo(root, p, d, log)
		if !ok {
			return nil
		}
		if isSub {
			dir := path.Dir(rel)
			res.subtitles[dir] = append(res.subtitles[dir], rel)
			return nil
		}
		if isSample(name) {
			return nil
		}
		res.videos = append(res.videos, found{
			rel: rel, abs: p, size: info.Size(), mtime: database.FormatTime(info.ModTime()),
		})
		return nil
	})
	return res, err
}

// fileInfo devuelve la información de un archivo regular o de un symlink que
// apunta a uno dentro de la raíz.
func fileInfo(root, p string, d fs.DirEntry, log zerolog.Logger) (fs.FileInfo, bool) {
	if d.Type()&fs.ModeSymlink != 0 {
		inside, err := storage.IsWithinRoot(root, p)
		if err != nil || !inside {
			log.Debug().Str("ruta", p).Msg("symlink fuera de la raíz, se ignora")
			return nil, false
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		return info, true
	}
	if !d.Type().IsRegular() {
		return nil, false
	}
	info, err := d.Info()
	if err != nil {
		return nil, false
	}
	return info, true
}

func isSample(name string) bool {
	s := strings.ToLower(stem(name))
	return s == "sample" || strings.HasSuffix(s, "-sample") || strings.HasSuffix(s, ".sample")
}

func relPath(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}

// underAny indica si rel está dentro de alguna de las carpetas dirs.
func underAny(rel string, dirs []string) bool {
	for _, d := range dirs {
		if d == "." || rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}
