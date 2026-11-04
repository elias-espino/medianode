package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Formatos de salida de ExtractSubtitle.
const (
	FormatWebVTT = "webvtt"
	FormatASS    = "ass"
)

const extractTimeout = 2 * time.Minute

// ExtractSubtitle convierte una pista de subtítulos de texto de input a
// WebVTT o ASS. stream es el índice de la pista en el archivo (0 en un
// subtítulo externo). Escribe en un archivo temporal y lo renombra: nunca
// queda a medias en la ruta final.
func ExtractSubtitle(ctx context.Context, ffmpegPath, input string, stream int, format, out string) error {
	if !filepath.IsAbs(input) || !filepath.IsAbs(out) {
		return errors.New("las rutas deben ser absolutas")
	}
	if stream < 0 || (format != FormatWebVTT && format != FormatASS) {
		return errors.New("parámetros de extracción no válidos")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	tmp := out + ".tmp"
	ctx, cancel := context.WithTimeout(ctx, extractTimeout)
	defer cancel()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-i", "file:" + input, "-map", "0:" + strconv.Itoa(stream), "-c:s", format, "-f", format, "file:" + tmp}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("extraer subtítulo: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
