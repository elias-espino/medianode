// Command release compila MediaNode para Windows, Linux y macOS (amd64 y
// arm64), empaqueta cada binario con sus documentos y escribe SHA256SUMS.
//
//	go run ./scripts/release -version v0.1.0 -out dist/release
//
// Antes hay que compilar el panel (cd web && npm ci && npm run build): se
// incrusta en el binario. No usa CGO, así que compila desde cualquier sistema.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// target es un sistema y una arquitectura.
type target struct{ OS, Arch string }

var allTargets = []target{
	{"windows", "amd64"}, {"windows", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
}

// epoch es la fecha de todo lo que entra en un archivo: así dos compilaciones
// del mismo código dan el mismo archivo, y la suma es comparable.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// reVersion limita lo que entra en los nombres de archivo y en -ldflags.
var reVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,40}$`)

// entry es un archivo que va dentro del paquete.
type entry struct {
	Name string // Ruta dentro del archivo, con barras.
	Path string // Origen en disco.
	Mode fs.FileMode
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	version := fs.String("version", "dev", "versión: va en el nombre de los archivos y en el binario")
	out := fs.String("out", filepath.Join("dist", "release"), "carpeta de salida")
	only := fs.String("targets", "", "sistemas/arquitecturas separados por comas, p. ej. linux/amd64 (por defecto, los seis)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !reVersion.MatchString(*version) {
		return fmt.Errorf("versión no válida: %q", *version)
	}
	targets, err := parseTargets(*only)
	if err != nil {
		return err
	}
	if err := checkWeb(); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	var files []string
	for _, t := range targets {
		name, err := build(t, *version, *out)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", t.OS, t.Arch, err)
		}
		fmt.Println("listo", name)
		files = append(files, filepath.Join(*out, name))
	}
	sums, err := sumsFile(files)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "SHA256SUMS"), []byte(sums), 0o644)
}

func parseTargets(spec string) ([]target, error) {
	if strings.TrimSpace(spec) == "" {
		return allTargets, nil
	}
	var out []target
	for _, s := range strings.Split(spec, ",") {
		osName, arch, ok := strings.Cut(strings.TrimSpace(s), "/")
		t := target{osName, arch}
		if !ok || !contains(allTargets, t) {
			return nil, fmt.Errorf("destino no admitido: %q", s)
		}
		out = append(out, t)
	}
	return out, nil
}

func contains(list []target, t target) bool {
	for _, x := range list {
		if x == t {
			return true
		}
	}
	return false
}

// checkWeb comprueba que el panel esté compilado: sin él, el binario arranca
// pero sin panel, y nadie lo notaría hasta abrir el navegador.
func checkWeb() error {
	if _, err := os.Stat(filepath.Join("web", "dist", "index.html")); err != nil {
		return errors.New("falta web/dist/index.html: compila el panel antes (cd web && npm ci && npm run build)")
	}
	return nil
}

// archiveName es el nombre del paquete de un destino.
func archiveName(version string, t target) string {
	ext := ".tar.gz"
	if t.OS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("medianode_%s_%s_%s%s", version, t.OS, t.Arch, ext)
}

func binaryName(t target) string {
	if t.OS == "windows" {
		return "medianode-server.exe"
	}
	return "medianode-server"
}

// build compila un destino y devuelve el nombre del paquete que dejó en out.
func build(t target, version, out string) (string, error) {
	tmp, err := os.MkdirTemp("", "medianode-release-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	bin := filepath.Join(tmp, binaryName(t))
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+version, "-o", bin, "./cmd/medianode-server")
	cmd.Env = append(os.Environ(), "GOOS="+t.OS, "GOARCH="+t.Arch, "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("compilar: %w", err)
	}
	entries, err := contents(t, bin)
	if err != nil {
		return "", err
	}
	name := archiveName(version, t)
	dst := filepath.Join(out, name)
	if t.OS == "windows" {
		err = writeZip(dst, entries)
	} else {
		err = writeTarGz(dst, entries)
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// contents lista lo que lleva el paquete de un destino: el binario, los
// documentos, la configuración de ejemplo y, en Linux, el empaquetado.
func contents(t target, bin string) ([]entry, error) {
	list := []entry{
		{Name: binaryName(t), Path: bin, Mode: 0o755},
		{Name: "README.md", Path: "README.md", Mode: 0o644},
		{Name: "README.es.md", Path: "README.es.md", Mode: 0o644},
		{Name: "LICENSE", Path: "LICENSE", Mode: 0o644},
		{Name: "config/server.example.yaml", Path: filepath.Join("config", "server.example.yaml"), Mode: 0o644},
		{Name: "docs/licencias-ffmpeg.md", Path: filepath.Join("docs", "licencias-ffmpeg.md"), Mode: 0o644},
	}
	if t.OS == "linux" {
		for _, p := range []string{"README.md", "server.packaged.yaml", "systemd/medianode.service"} {
			list = append(list, entry{Name: "packaging/" + p, Path: filepath.Join("packaging", filepath.FromSlash(p)), Mode: 0o644})
		}
	}
	for _, e := range list {
		if _, err := os.Stat(e.Path); err != nil {
			return nil, fmt.Errorf("falta %s: %w", e.Path, err)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// writeTarGz escribe los archivos en orden y con la fecha fija.
func writeTarGz(dst string, entries []entry) (err error) {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		data, err := os.ReadFile(e.Path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: path.Join("medianode", e.Name), Mode: int64(e.Mode), Size: int64(len(data)), ModTime: epoch, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// writeZip escribe los archivos en orden y con la fecha fija.
func writeZip(dst string, entries []entry) (err error) {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		data, err := os.ReadFile(e.Path)
		if err != nil {
			return err
		}
		hdr := &zip.FileHeader{Name: path.Join("medianode", e.Name), Method: zip.Deflate, Modified: epoch}
		hdr.SetMode(e.Mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// sumsFile escribe el contenido de SHA256SUMS: «suma  nombre», ordenado por
// nombre, que es lo que lee «sha256sum -c».
func sumsFile(files []string) (string, error) {
	sorted := append([]string(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return filepath.Base(sorted[i]) < filepath.Base(sorted[j]) })
	var b strings.Builder
	for _, p := range sorted {
		sum, err := fileSum(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s  %s\n", sum, filepath.Base(p))
	}
	return b.String(), nil
}

func fileSum(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
