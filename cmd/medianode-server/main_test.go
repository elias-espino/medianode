package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func writeConfig(t *testing.T, port int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("server:\n  bind_address: 127.0.0.1\n  http_port: %d\n  shutdown_timeout_seconds: 5\n", port)
	path := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitHealth(t *testing.T, port int) {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("el servidor no respondió a /health")
}

func TestVersionFlag(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, &out, &out); code != exitOK {
		t.Fatalf("código = %d", code)
	}
	if !strings.Contains(out.String(), version) {
		t.Fatalf("salida = %q", out.String())
	}
}

func TestFlagDesconocido(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--nope"}, &out, &out); code != exitUsage {
		t.Fatalf("código = %d", code)
	}
}

func TestConfiguracionInvalidaNoArranca(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("server:\n  http_port: 70000\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--config", path}, &stdout, &stderr); code != exitError {
		t.Fatalf("código = %d", code)
	}
	if !strings.Contains(stderr.String(), "server.http_port") {
		t.Fatalf("el error debe ser claro: %q", stderr.String())
	}
}

func TestPuertoOcupadoTerminaConError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	path := writeConfig(t, ln.Addr().(*net.TCPAddr).Port)

	var out bytes.Buffer
	if code := run(context.Background(), []string{"--config", path}, &out, &out); code != exitError {
		t.Fatalf("código = %d", code)
	}
}

func TestArrancaRespondeYApagaConCodigoCero(t *testing.T) {
	port := freePort(t)
	path := writeConfig(t, port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--config", path}, &out, &out) }()

	waitHealth(t, port)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/server/version", port))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	err = json.NewDecoder(resp.Body).Decode(&body)
	// Leer hasta el final y cerrar antes de apagar: una conexión con el cuerpo
	// a medias cuenta como activa y hace esperar a Shutdown.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	http.DefaultClient.CloseIdleConnections()
	if err != nil {
		t.Fatal(err)
	}
	if body["version"] != version {
		t.Fatalf("cuerpo = %v", body)
	}

	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			logFile, _ := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(path)), "logs", "medianode.log"))
			t.Fatalf("código = %d\nsalida: %s\nlog: %s", code, out.String(), logFile)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("el servidor no se detuvo")
	}

	root := filepath.Dir(filepath.Dir(path))
	data, err := os.ReadFile(filepath.Join(root, "logs", "medianode.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "servidor detenido") {
		t.Fatalf("log = %s", data)
	}

	if _, err := os.Stat(filepath.Join(root, "data", "medianode.db")); err != nil {
		t.Fatalf("no se creó la base: %v", err)
	}
	for _, dir := range []string{"data/metadata", "data/images", "data/cache", "data/transcodes", "backups"} {
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil || !st.IsDir() {
			t.Fatalf("falta el directorio %s: %v", dir, err)
		}
	}
}

func TestSegundoArranqueReutilizaLaBase(t *testing.T) {
	port := freePort(t)
	path := writeConfig(t, port)

	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		var out bytes.Buffer
		done := make(chan int, 1)
		go func() { done <- run(ctx, []string{"--config", path}, &out, &out) }()

		waitHealth(t, port)
		cancel()
		if code := <-done; code != exitOK {
			t.Fatalf("código = %d\n%s", code, out.String())
		}
	}

	entries, err := os.ReadDir(filepath.Join(filepath.Dir(filepath.Dir(path)), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("sin migraciones pendientes no debe haber backups: %v", entries)
	}
}

func TestPrimerArranqueMuestraElCodigoInicial(t *testing.T) {
	port := freePort(t)
	path := writeConfig(t, port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--config", path}, &out, &out) }()
	waitHealth(t, port)
	cancel()
	if code := <-done; code != exitOK {
		t.Fatalf("código = %d\n%s", code, out.String())
	}

	if !strings.Contains(out.String(), "/auth/setup") {
		t.Fatalf("la consola debe mostrar el código: %s", out.String())
	}
	logs, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(path)), "logs", "medianode.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logs), "/auth/setup") {
		t.Fatalf("el log debe mostrar el código: %s", logs)
	}
}

// TestBinarioSeApagaConSenal compila el binario real, comprueba la inyección de
// versión y exige salida 0 ante la señal de apagado del sistema.
func TestBinarioSeApagaConSenal(t *testing.T) {
	if testing.Short() {
		t.Skip("compila el binario")
	}
	bin := filepath.Join(t.TempDir(), "medianode-server")
	if ext := exeSuffix(); ext != "" {
		bin += ext
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version=9.9.9", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compilar: %v\n%s", err, out)
	}

	versionOut, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(versionOut), "9.9.9") {
		t.Fatalf("versión = %q", versionOut)
	}

	port := freePort(t)
	cmd := exec.Command(bin, "--config", writeConfig(t, port))
	prepareStop(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	waitHealth(t, port)
	if err := sendStop(cmd); err != nil {
		t.Skipf("no se pudo enviar la señal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("código de salida = %s", strconv.Itoa(exit.ExitCode()))
		}
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("el binario no se detuvo")
	}
}
