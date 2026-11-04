package playback

import "testing"

func TestRemoteLimit(t *testing.T) {
	cases := []struct {
		name  string
		req   Request
		prefs Preferences
		want  *int64
	}{
		{"local sin límite", Request{}, Preferences{}, nil},
		{"local ignora el límite remoto", Request{}, Preferences{MaxBitrateRemote: pi64(1000)}, nil},
		{"local conserva el de la petición", Request{MaxBitrate: pi64(5000)}, Preferences{MaxBitrateRemote: pi64(1000)}, pi64(5000)},
		{"remoto sin límite del usuario", Request{Remote: true}, Preferences{}, nil},
		{"remoto sin límite del usuario conserva el de la petición", Request{Remote: true, MaxBitrate: pi64(5000)}, Preferences{}, pi64(5000)},
		{"remoto aplica el del usuario", Request{Remote: true}, Preferences{MaxBitrateRemote: pi64(1000)}, pi64(1000)},
		{"remoto: el del usuario es más bajo", Request{Remote: true, MaxBitrate: pi64(5000)}, Preferences{MaxBitrateRemote: pi64(1000)}, pi64(1000)},
		{"remoto: el de la petición es más bajo", Request{Remote: true, MaxBitrate: pi64(500)}, Preferences{MaxBitrateRemote: pi64(1000)}, pi64(500)},
		{"remoto: iguales", Request{Remote: true, MaxBitrate: pi64(1000)}, Preferences{MaxBitrateRemote: pi64(1000)}, pi64(1000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := remoteLimit(c.req, c.prefs)
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Fatalf("remoteLimit = %v, se esperaba %v", deref64(got), deref64(c.want))
			}
		})
	}
	// Devuelve una copia: tocarla no cambia la preferencia del usuario.
	p := Preferences{MaxBitrateRemote: pi64(1000)}
	got := remoteLimit(Request{Remote: true}, p)
	*got = 1
	if *p.MaxBitrateRemote != 1000 {
		t.Fatal("remoteLimit debe devolver una copia")
	}
}

func pi64(n int64) *int64 { return &n }
