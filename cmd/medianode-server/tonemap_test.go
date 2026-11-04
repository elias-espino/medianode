package main

import (
	"testing"

	"github.com/rs/zerolog"

	"medianode/internal/ffmpeg"
)

func TestPoliticaDeToneMapping(t *testing.T) {
	cases := []struct {
		name   string
		policy string
		caps   ffmpeg.Capabilities
		want   bool
	}{
		{"auto con equipo rápido", "auto", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 3}, true},
		{"auto con equipo lento sigue permitiendo", "auto", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 0.4}, true},
		{"auto sin medir", "auto", ffmpeg.Capabilities{ToneMapping: true}, true},
		{"realtime con equipo rápido", "realtime", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 1.2}, true},
		{"realtime exacto a tiempo real", "realtime", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 1}, true},
		{"realtime con equipo lento rechaza", "realtime", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 0.44}, false},
		{"realtime sin medir no rechaza", "realtime", ffmpeg.Capabilities{ToneMapping: true}, true},
		{"off nunca", "off", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 9}, false},
		{"sin filtros sigue sin ellos", "auto", ffmpeg.Capabilities{}, false},
	}
	for _, c := range cases {
		caps := c.caps
		applyToneMapPolicy(&caps, c.policy, zerolog.Nop())
		if caps.ToneMapping != c.want {
			t.Errorf("%s: ToneMapping = %v, se esperaba %v", c.name, caps.ToneMapping, c.want)
		}
	}
}
