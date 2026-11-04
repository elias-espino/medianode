package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveName(t *testing.T) {
	cases := map[target]string{
		{"windows", "amd64"}: "medianode_v1.2.3_windows_amd64.zip",
		{"linux", "arm64"}:   "medianode_v1.2.3_linux_arm64.tar.gz",
		{"darwin", "amd64"}:  "medianode_v1.2.3_darwin_amd64.tar.gz",
	}
	for tg, want := range cases {
		if got := archiveName("v1.2.3", tg); got != want {
			t.Errorf("%v = %q, se esperaba %q", tg, got, want)
		}
	}
	if binaryName(target{"windows", "amd64"}) != "medianode-server.exe" || binaryName(target{"linux", "amd64"}) != "medianode-server" {
		t.Error("nombre del binario")
	}
}

func TestVersionNoValida(t *testing.T) {
	for _, v := range []string{"", "v1 2", "v1;rm", "../x", "v1\"", "-v1", strings.Repeat("a", 60), "v1/2", "v1$(x)"} {
		if reVersion.MatchString(v) {
			t.Errorf("%q no debía admitirse: va a un nombre de archivo y a -ldflags", v)
		}
	}
	for _, v := range []string{"dev", "v0.1.0", "1.2.3-rc.1", "v1.0.0+build5", "0.0.0_x"} {
		if !reVersion.MatchString(v) {
			t.Errorf("%q debía admitirse", v)
		}
	}
	if err := run([]string{"-version", "v 1"}); err == nil || !strings.Contains(err.Error(), "versión no válida") {
		t.Errorf("run con versión mala: %v", err)
	}
}

func TestParseTargets(t *testing.T) {
	all, err := parseTargets("")
	if err != nil || len(all) != 6 {
		t.Fatalf("por defecto, los seis: %v %v", all, err)
	}
	two, err := parseTargets("linux/amd64, darwin/arm64")
	if err != nil || len(two) != 2 || two[1] != (target{"darwin", "arm64"}) {
		t.Fatalf("%v %v", two, err)
	}
	for _, bad := range []string{"linux", "linux/386", "plan9/amd64", "linux/amd64,x", "windows/amd64/extra"} {
		if _, err := parseTargets(bad); err == nil {
			t.Errorf("%q debía fallar", bad)
		}
	}
}

func fixtureEntries(t *testing.T) []entry {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return []entry{
		{Name: "medianode-server", Path: write("bin", "binario"), Mode: 0o755},
		{Name: "README.md", Path: write("r", "léeme"), Mode: 0o644},
		{Name: "config/server.example.yaml", Path: write("c", "server: {}"), Mode: 0o644},
	}
}

func TestWriteTarGz(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "x.tar.gz")
	if err := writeTarGz(dst, fixtureEntries(t)); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	modes := map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[h.Name], modes[h.Name] = string(b), h.Mode
		if !h.ModTime.Equal(epoch) {
			t.Errorf("%s: fecha %v, debía ser la fija", h.Name, h.ModTime)
		}
	}
	if got["medianode/medianode-server"] != "binario" || got["medianode/README.md"] != "léeme" || got["medianode/config/server.example.yaml"] != "server: {}" {
		t.Fatalf("contenido: %v", got)
	}
	if modes["medianode/medianode-server"] != 0o755 || modes["medianode/README.md"] != 0o644 {
		t.Fatalf("permisos: %v", modes)
	}
}

func TestWriteZip(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "x.zip")
	if err := writeZip(dst, fixtureEntries(t)); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := map[string]string{}
	for _, f := range r.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
		if !f.Modified.UTC().Equal(epoch) {
			t.Errorf("%s: fecha %v", f.Name, f.Modified)
		}
		if strings.Contains(f.Name, `\`) {
			t.Errorf("%s: las rutas del zip llevan barras normales", f.Name)
		}
	}
	if got["medianode/medianode-server"] != "binario" || got["medianode/config/server.example.yaml"] != "server: {}" {
		t.Fatalf("contenido: %v", got)
	}
}

// Dos escrituras del mismo contenido dan el mismo archivo: la suma de una
// versión se puede comprobar reconstruyéndola.
func TestLosArchivosSonReproducibles(t *testing.T) {
	entries := fixtureEntries(t)
	dir := t.TempDir()
	for _, w := range []struct {
		ext string
		fn  func(string, []entry) error
	}{{".tar.gz", writeTarGz}, {".zip", writeZip}} {
		a, b := filepath.Join(dir, "a"+w.ext), filepath.Join(dir, "b"+w.ext)
		if err := w.fn(a, entries); err != nil {
			t.Fatal(err)
		}
		if err := w.fn(b, entries); err != nil {
			t.Fatal(err)
		}
		sa, _ := fileSum(a)
		sb, _ := fileSum(b)
		if sa != sb {
			t.Errorf("%s: no es reproducible", w.ext)
		}
	}
}

func TestSumsFile(t *testing.T) {
	dir := t.TempDir()
	b := filepath.Join(dir, "b.zip")
	a := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(a, []byte("uno"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("dos"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := sumsFile([]string{b, a})
	if err != nil {
		t.Fatal(err)
	}
	h1 := sha256.Sum256([]byte("uno"))
	h2 := sha256.Sum256([]byte("dos"))
	want := hex.EncodeToString(h1[:]) + "  a.tar.gz\n" + hex.EncodeToString(h2[:]) + "  b.zip\n"
	if got != want {
		t.Fatalf("%q, se esperaba %q", got, want)
	}
	if _, err := sumsFile([]string{filepath.Join(dir, "no-existe")}); err == nil {
		t.Error("un archivo que falta debía fallar")
	}
}

func TestContentsIncluyeElEmpaquetadoSoloEnLinux(t *testing.T) {
	root := t.TempDir()
	// contents lee rutas relativas a la raíz del repositorio.
	old, _ := os.Getwd()
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	bin := filepath.Join(root, "x")
	if err := os.WriteFile(bin, []byte("b"), 0o755); err != nil {
		t.Fatal(err)
	}
	names := func(tg target) string {
		e, err := contents(tg, bin)
		if err != nil {
			t.Fatalf("%v: %v", tg, err)
		}
		var n []string
		for _, x := range e {
			n = append(n, x.Name)
		}
		return strings.Join(n, " ")
	}
	linux, windows := names(target{"linux", "amd64"}), names(target{"windows", "amd64"})
	for _, want := range []string{"packaging/systemd/medianode.service", "packaging/server.packaged.yaml", "medianode-server", "LICENSE", "docs/licencias-ffmpeg.md"} {
		if !strings.Contains(linux, want) {
			t.Errorf("linux sin %s: %s", want, linux)
		}
	}
	if strings.Contains(windows, "packaging/") || !strings.Contains(windows, "medianode-server.exe") {
		t.Errorf("windows: %s", windows)
	}
}

func TestCheckWebExigeElPanelCompilado(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := checkWeb(); err == nil || !strings.Contains(err.Error(), "npm run build") {
		t.Fatalf("sin panel: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("web", "dist"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("web", "dist", "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkWeb(); err != nil {
		t.Fatalf("con panel: %v", err)
	}
}
