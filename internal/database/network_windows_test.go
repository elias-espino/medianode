//go:build windows

package database

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// La ruta local se abre como recurso de red mediante el recurso administrativo
// de la unidad. Sin permisos sobre él la prueba se omite.
func TestOpenRechazaRutaDeRed(t *testing.T) {
	dir := t.TempDir()
	unc := `\\localhost\` + strings.Replace(dir, ":", "$", 1)
	if _, err := os.Stat(unc); err != nil {
		t.Skipf("recurso administrativo no disponible: %v", err)
	}

	db, err := Open(testContext(t), testConfig(filepath.Join(unc, "t.db")))
	if err == nil {
		_ = db.Close()
		t.Fatal("una ruta de red debe rechazarse")
	}
	if !errors.Is(err, ErrNetworkPath) {
		t.Fatalf("error = %v", err)
	}
}
