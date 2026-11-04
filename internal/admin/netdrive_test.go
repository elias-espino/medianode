package admin

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"medianode/internal/testutil"
)

// Una biblioteca en una unidad de red con letra avisa, porque un servicio de
// Windows no la verá (§21.2); una local no.
func TestComprobacionDeUnidadesDeRed(t *testing.T) {
	db := testutil.NewDB(t)
	dir := t.TempDir()
	if _, err := db.Write.Exec("INSERT INTO libraries (id, name, type) VALUES ('a', 'Red', 'movies')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write.Exec("INSERT INTO library_paths (id, library_id, path) VALUES ('pa', 'a', ?)", dir); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	if c := s.checkLibraries(context.Background()); c.Status != CheckOK {
		t.Fatalf("carpeta local: %+v", c)
	}
	old := networkDrive
	t.Cleanup(func() { networkDrive = old })
	networkDrive = func(string) bool { return true }
	c := s.checkLibraries(context.Background())
	if c.Status != CheckWarn || !strings.Contains(c.Message, "UNC") || !strings.Contains(c.Message, "Red") ||
		!strings.Contains(c.Message, `\\servidor\recurso`) || strings.ContainsRune(c.Message, '\r') {
		t.Fatalf("unidad de red: %+v", c)
	}
	// Una carpeta ilegible sigue siendo un fallo, aunque además fuera de red.
	if _, err := db.Write.Exec("INSERT INTO library_paths (id, library_id, path) VALUES ('pb', 'a', ?)", filepath.Join(dir, "no-existe")); err != nil {
		t.Fatal(err)
	}
	if c := s.checkLibraries(context.Background()); c.Status != CheckFail {
		t.Fatalf("un fallo manda sobre el aviso: %+v", c)
	}
}

func TestIsMappedNetworkDrive(t *testing.T) {
	for _, p := range []string{"", "relativa/ruta", t.TempDir(), "/tmp/x", `\\servidor\recurso\peliculas`} {
		if isMappedNetworkDrive(p) {
			t.Errorf("%q no es una unidad de red con letra", p)
		}
	}
	if runtime.GOOS == "windows" {
		for _, p := range []string{`C:\`, `C:\Windows`, `c:\users`} {
			if isMappedNetworkDrive(p) {
				t.Errorf("%q es un disco local", p)
			}
		}
		if isMappedNetworkDrive(`9:\x`) || isMappedNetworkDrive(`CC:\x`) {
			t.Error("una letra que no es letra de unidad no cuenta")
		}
	}
}
