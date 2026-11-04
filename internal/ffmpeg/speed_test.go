package ffmpeg

import (
	"testing"
	"time"
)

func TestToneMapSpeed(t *testing.T) {
	cases := []struct {
		name      string
		one, many time.Duration
		frames    int
		want      float64
	}{
		// 12 fotogramas extra son 0,48 s de vídeo; en 0,48 s, tiempo real.
		{"tiempo real", 200 * time.Millisecond, 680 * time.Millisecond, 13, 1},
		{"el doble de rápido", 200 * time.Millisecond, 440 * time.Millisecond, 13, 2},
		{"la mitad", 200 * time.Millisecond, 1160 * time.Millisecond, 13, 0.5},
		{"sin diferencia medible", 500 * time.Millisecond, 500 * time.Millisecond, 13, 0},
		{"la pasada larga fue más corta", 500 * time.Millisecond, 400 * time.Millisecond, 13, 0},
		{"un solo fotograma no mide nada", 100 * time.Millisecond, 900 * time.Millisecond, 1, 0},
	}
	for _, c := range cases {
		got := toneMapSpeed(c.one, c.many, c.frames)
		if got < c.want-0.001 || got > c.want+0.001 {
			t.Errorf("%s: %.4f, se esperaba %.4f", c.name, got, c.want)
		}
	}
}
