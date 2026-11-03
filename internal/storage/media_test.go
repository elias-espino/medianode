package storage

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateLibraryRoot(t *testing.T) {
	base := t.TempDir()
	media := filepath.Join(base, "media")
	data := filepath.Join(base, "data")
	file := filepath.Join(base, "archivo.txt")
	for _, d := range []string{media, data, filepath.Join(data, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reserved := []string{data}

	cases := []struct {
		name string
		path string
		want error
	}{
		{"válida", media, nil},
		{"con barra y punto", media + string(filepath.Separator) + ".", nil},
		{"relativa", "media", ErrRootNotAbsolute},
		{"no existe", filepath.Join(base, "nada"), ErrRootUnavailable},
		{"es un archivo", file, ErrRootUnavailable},
		{"es el directorio de datos", data, ErrRootReserved},
		{"dentro del de datos", filepath.Join(data, "sub"), ErrRootReserved},
		{"contiene el de datos", base, ErrRootReserved},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ValidateLibraryRoot(c.path, reserved)
			if !errors.Is(err, c.want) {
				t.Fatalf("error = %v, se esperaba %v", err, c.want)
			}
			if err == nil && got != filepath.Clean(c.path) {
				t.Fatalf("ruta = %q", got)
			}
		})
	}
}

func TestRootEmpty(t *testing.T) {
	dir := t.TempDir()
	if empty, err := RootEmpty(dir); err != nil || !empty {
		t.Fatalf("%v %v", empty, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "x"), nil, 0o644)
	if empty, _ := RootEmpty(dir); empty {
		t.Fatal("tiene una entrada")
	}
	if _, err := RootEmpty(filepath.Join(dir, "nada")); err == nil {
		t.Fatal("una raíz inexistente da error")
	}
}

func TestWithinYOverlaps(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep+"a", "b")
	cases := []struct {
		parent, child string
		want          bool
	}{
		{root, root, true},
		{root, filepath.Join(root, "c"), true},
		{root, filepath.Join(sep+"a", "bc"), false},
		{root, filepath.Join(sep + "a"), false},
		{root, filepath.Join(root, "..", "x"), false},
		{root, filepath.Join(root, "..b"), true},
	}
	for _, c := range cases {
		if got := Within(c.parent, c.child); got != c.want {
			t.Fatalf("Within(%q, %q) = %v", c.parent, c.child, got)
		}
	}
	if !Overlaps(filepath.Join(root, "c"), root) || Overlaps(filepath.Join(sep+"x"), root) {
		t.Fatal("Overlaps")
	}
}

func TestOpenMediaNoSaleDeLaRaiz(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sub", "a.mkv"), []byte("dentro"), 0o644)
	_ = os.WriteFile(filepath.Join(base, "fuera.mkv"), []byte("fuera"), 0o644)

	f, err := OpenMedia(root, "sub/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	_ = f.Close()
	if string(data) != "dentro" {
		t.Fatalf("%q", data)
	}
	for _, rel := range []string{"../fuera.mkv", "sub/../../fuera.mkv", filepath.Join(base, "fuera.mkv"), ""} {
		if f, err := OpenMedia(root, rel); err == nil {
			_ = f.Close()
			t.Fatalf("%q no debe abrirse", rel)
		}
	}
}

func TestOpenMediaRechazaSymlinkExterno(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	_ = os.MkdirAll(root, 0o755)
	outside := filepath.Join(base, "fuera.mkv")
	_ = os.WriteFile(outside, []byte("fuera"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "enlace.mkv")); err != nil {
		t.Skipf("este sistema no permite crear symlinks: %v", err)
	}
	if f, err := OpenMedia(root, "enlace.mkv"); err == nil {
		_ = f.Close()
		t.Fatal("os.Root debe impedir seguir el symlink fuera de la raíz")
	}
	inside, err := IsWithinRoot(root, filepath.Join(root, "enlace.mkv"))
	if err != nil || inside {
		t.Fatalf("IsWithinRoot = %v, %v", inside, err)
	}
}

func TestIsWithinRoot(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.mkv")
	_ = os.WriteFile(p, nil, 0o644)
	if ok, err := IsWithinRoot(root, p); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := IsWithinRoot(root, filepath.Join(root, "nada")); err == nil {
		t.Fatal("un destino inexistente da error")
	}
}
