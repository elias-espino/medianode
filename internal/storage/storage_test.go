package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirs(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a", "b")
	c := filepath.Join(base, "c")

	if err := EnsureDirs(a, c, a); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, c} {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

func TestEnsureDirsSobreArchivoFalla(t *testing.T) {
	file := filepath.Join(t.TempDir(), "archivo")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirs(filepath.Join(file, "sub")); err == nil {
		t.Fatal("se esperaba un error")
	}
}

func TestDirectorioTemporalEsLocal(t *testing.T) {
	network, err := IsNetworkPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if network {
		t.Fatal("el directorio temporal no debería ser de red")
	}
}
