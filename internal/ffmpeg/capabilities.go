package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Capabilities es lo que ofrece el FFmpeg instalado.
type Capabilities struct {
	Version  string
	Encoders map[Encoder]bool // Codificadores de vídeo H.264 compilados.
	// Working son los que además inicializan de verdad en este equipo.
	Working []Encoder
}

// Probe lee la versión y los codificadores, y prueba cada uno de hardware
// generando un segundo de vídeo (§10.5). Un codificador compilado no
// garantiza que haya GPU ni controlador.
func Probe(ctx context.Context, path string) (*Capabilities, error) {
	out, err := exec.CommandContext(ctx, path, "-hide_banner", "-version").Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
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
	return c, nil
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
		return fmt.Errorf("%s: %v: %s", e, err, strings.TrimSpace(stderr.String()))
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
