package transcoding

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

var (
	sharedDir  string
	sharedOnce sync.Once
	sharedPath string
)

// TestMain borra el vídeo compartido de las pruebas del flujo progresivo:
// generarlo cuesta unos diez segundos en un equipo modesto, y todas lo leen sin
// modificarlo.
func TestMain(m *testing.M) {
	code := m.Run()
	if sharedDir != "" {
		_ = os.RemoveAll(sharedDir)
	}
	os.Exit(code)
}

// sharedIrregular es irregular(), generado una sola vez por ejecución.
func sharedIrregular(t *testing.T, tl tools) string {
	t.Helper()
	sharedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "medianode-pipe-*")
		if err != nil {
			t.Fatal(err)
		}
		sharedDir = dir
		src := irregular(t, tl)
		sharedPath = filepath.Join(dir, "irregular.mkv")
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sharedPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if sharedPath == "" {
		t.Skip("no se pudo generar el vídeo compartido")
	}
	return sharedPath
}
