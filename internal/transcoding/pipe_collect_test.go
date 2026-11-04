package transcoding

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/events"
	"medianode/internal/testutil"
)

func plainManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(Options{FFmpeg: "no-se-usa", Dir: t.TempDir()}, testutil.NewDB(t), events.NewBus(), NewAdmission(1, 1), zerolog.Nop())
}

func TestPipeDirActive(t *testing.T) {
	m := plainManager(t)
	m.pipes["j1"] = &pipeJob{id: "j1"}
	for name, want := range map[string]bool{
		"pipe-j1":    true,
		"pipe-j2":    false, // Un flujo que ya no está.
		"pipe-":      false,
		"pipe":       false,
		"j1":         false, // Sin el prefijo, no es un directorio de flujo...
		"xxxxxj1":    false, // ...aunque lo que sigue a cinco caracteres cualquiera coincida.
		"PIPE-j1":    false,
		"s1-pipe-j1": false,
	} {
		if got := m.pipeDirActive(name); got != want {
			t.Errorf("pipeDirActive(%q) = %v, se esperaba %v", name, got, want)
		}
	}
}

// El recolector no borra el directorio de un flujo registrado, por viejo que sea,
// y sí el de uno que ya terminó. No depende de que FFmpeg esté corriendo: en
// Windows, el directorio de trabajo de un proceso vivo no se puede borrar y
// taparía el fallo.
func TestElRecolectorRespetaLosFlujosRegistrados(t *testing.T) {
	m := plainManager(t)
	old := time.Now().Add(-time.Hour)
	mk := func(name string) string {
		dir := filepath.Join(m.Dir(), name)
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	active, finished, plain := mk(pipeDirPrefix+"j1"), mk(pipeDirPrefix+"j0"), mk("sesion-sin-flujo")
	m.pipes["j1"] = &pipeJob{id: "j1"}

	m.collect()
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("el recolector borró el directorio de un flujo activo: %v", err)
	}
	for _, gone := range []string{finished, plain} {
		if _, err := os.Stat(gone); err == nil {
			t.Fatalf("el recolector no borró %s, que no es de nadie", filepath.Base(gone))
		}
	}
	// Cuando el flujo termina, el siguiente recorrido lo recoge.
	delete(m.pipes, "j1")
	m.collect()
	if _, err := os.Stat(active); err == nil {
		t.Fatal("un flujo terminado debe poder recogerse")
	}
}

func TestElRecolectorNoBorraLoRecienCreado(t *testing.T) {
	m := plainManager(t)
	fresh := filepath.Join(m.Dir(), "pipe-recien")
	if err := os.Mkdir(fresh, 0o750); err != nil {
		t.Fatal(err)
	}
	m.collect()
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("un directorio nuevo (un flujo que arranca) no es huérfano: %v", err)
	}
}
