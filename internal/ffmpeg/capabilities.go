package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Capabilities es lo que ofrece el FFmpeg instalado.
type Capabilities struct {
	Version  string
	Encoders map[Encoder]bool // Codificadores de vídeo H.264 compilados.
	// Working son los que además inicializan de verdad en este equipo.
	Working []Encoder
	// Filtros que necesitan los subtítulos grabados y el tone mapping (Fase 9).
	BurnInText  bool // subtitles (libass)
	BurnInImage bool // overlay
	ToneMapping bool // zscale + tonemap, probados con una imagen real
	// ToneMapSpeed es cuántas veces el tiempo real convierte este equipo un
	// HDR de 720p a SDR (por software). Menos de 1 significa que la imagen se
	// cortaría. Cero si no se pudo medir.
	ToneMapSpeed float64
}

// Probe lee la versión y los codificadores, y prueba cada uno de hardware
// generando un segundo de vídeo (§10.5). Un codificador compilado no
// garantiza que haya GPU ni controlador.
func Probe(ctx context.Context, path string) (*Capabilities, error) {
	out, err := exec.CommandContext(ctx, path, "-hide_banner", "-version").Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	c := &Capabilities{Encoders: map[Encoder]bool{}}
	if line, _, _ := strings.Cut(string(out), "\n"); strings.HasPrefix(line, "ffmpeg version ") {
		c.Version, _, _ = strings.Cut(strings.TrimPrefix(line, "ffmpeg version "), " ")
	}
	enc, err := exec.CommandContext(ctx, path, "-hide_banner", "-encoders").Output()
	if err != nil {
		return nil, fmt.Errorf("listar codificadores: %w", err)
	}
	c.Encoders = parseEncoders(enc)
	for _, hw := range Hardware {
		if !c.Encoders[hw.Encoder] {
			continue
		}
		if err := TestEncoder(ctx, path, hw.Encoder); err == nil {
			c.Working = append(c.Working, hw.Encoder)
		}
	}
	if c.Encoders[X264] {
		c.Working = append(c.Working, X264)
	}
	if out, err := exec.CommandContext(ctx, path, "-hide_banner", "-filters").Output(); err == nil {
		have := parseFilters(out)
		c.BurnInText, c.BurnInImage = have["subtitles"], have["overlay"]
		if have["zscale"] && have["tonemap"] {
			c.ToneMapSpeed, err = MeasureToneMap(ctx, path)
			c.ToneMapping = err == nil
		}
	}
	return c, nil
}

// parseFilters lee la salida de -filters: « TS overlay  VV->V  ...».
func parseFilters(out []byte) map[string]bool {
	got := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && strings.Contains(f[2], "->") {
			got[f[1]] = true
		}
	}
	return got
}

// TestToneMap pasa un fotograma sintético por la cadena de tone mapping: un
// zscale compilado sin libzimg falla aquí y no a mitad de una película.
func TestToneMap(ctx context.Context, path string) error {
	_, err := toneMapRun(ctx, path, 1)
	return err
}

// toneMapFrames es cuántos fotogramas de 720p se convierten al medir.
const toneMapFrames = 13

// MeasureToneMap comprueba que el tone mapping funciona y mide cuántas veces el
// tiempo real lo hace este equipo, a 720p y a 25 fotogramas por segundo.
// Compara una pasada de un fotograma con otra de toneMapFrames: la diferencia
// no incluye el arranque de FFmpeg.
func MeasureToneMap(ctx context.Context, path string) (float64, error) {
	one, err := toneMapRun(ctx, path, 1)
	if err != nil {
		return 0, err
	}
	many, err := toneMapRun(ctx, path, toneMapFrames)
	if err != nil {
		return 0, err
	}
	return toneMapSpeed(one, many, toneMapFrames), nil
}

// toneMapSpeed es cuántas veces el tiempo real convierte frames fotogramas a
// 25 fps, dado lo que tardó una pasada de un fotograma y otra de frames. Cero
// si la diferencia no es medible.
func toneMapSpeed(one, many time.Duration, frames int) float64 {
	extra := (many - one).Seconds()
	if extra <= 0 || frames < 2 {
		return 0
	}
	return float64(frames-1) / 25 / extra
}

func toneMapRun(ctx context.Context, path string, frames int) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25:duration=2", "-vf", "format=yuv420p10le,"+toneMapFilter(TransferPQ),
		"-frames:v", strconv.Itoa(frames), "-f", "null", "-")
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("tone mapping: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return time.Since(start), nil
}

// parseEncoders lee la salida de -encoders: «V....D libx264  ...».
func parseEncoders(out []byte) map[Encoder]bool {
	got := map[Encoder]bool{}
	known := map[string]bool{string(X264): true}
	for _, hw := range Hardware {
		known[string(hw.Encoder)] = true
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.HasPrefix(f[0], "V") && known[f[1]] {
			got[Encoder(f[1])] = true
		}
	}
	return got
}

// TestEncoder codifica un segundo de vídeo sintético y descarta el resultado.
// VAAPI necesita subir los fotogramas a la GPU.
func TestEncoder(ctx context.Context, path string, e Encoder) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25:duration=1"}
	if e == VAAPI {
		args = append(args, "-vaapi_device", "/dev/dri/renderD128", "-vf", "format=nv12,hwupload")
	}
	args = append(args, "-c:v", string(e), "-f", "null", "-")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", e, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Choose elige el codificador según transcoding.hardware_acceleration: none
// usa la CPU; auto, el primer codificador de hardware que funcione; un nombre
// concreto, ese si funciona. Si no, libx264.
func (c *Capabilities) Choose(setting string) Encoder {
	if c == nil || setting == "none" {
		return X264
	}
	works := func(e Encoder) bool {
		for _, w := range c.Working {
			if w == e {
				return true
			}
		}
		return false
	}
	for _, hw := range Hardware {
		if (setting == "auto" || setting == hw.Name) && works(hw.Encoder) {
			return hw.Encoder
		}
	}
	return X264
}
