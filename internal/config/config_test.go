package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSinArchivoUsaDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTPPort != 8420 || cfg.Transcoding.SegmentSeconds != 6 {
		t.Fatalf("defaults inesperados: %+v", cfg)
	}
	if !filepath.IsAbs(cfg.Database.Path) {
		t.Fatalf("ruta no absoluta: %s", cfg.Database.Path)
	}
}

func TestLoadArchivoSobrescribeDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "server:\n  http_port: 9000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTPPort != 9000 {
		t.Fatalf("puerto = %d", cfg.Server.HTTPPort)
	}
	if cfg.Server.Name != "MediaNode" {
		t.Fatalf("se perdió un default: %q", cfg.Server.Name)
	}
}

func TestLoadEntornoGanaAlArchivo(t *testing.T) {
	path := writeConfig(t, "server:\n  http_port: 9000\n")
	t.Setenv("MEDIANODE_SERVER_HTTP_PORT", "9100")
	t.Setenv("MEDIANODE_SERVER_TRUSTED_PROXIES", "127.0.0.1, 10.0.0.0/8")
	t.Setenv("MEDIANODE_DISCOVERY_MDNS_ENABLED", "false")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTPPort != 9100 {
		t.Fatalf("puerto = %d", cfg.Server.HTTPPort)
	}
	if len(cfg.Server.TrustedProxies) != 2 || cfg.Server.TrustedProxies[1] != "10.0.0.0/8" {
		t.Fatalf("proxies = %v", cfg.Server.TrustedProxies)
	}
	if cfg.Discovery.MDNSEnabled {
		t.Fatal("mdns debería estar desactivado")
	}
}

func TestLoadVariableConfigEligeArchivo(t *testing.T) {
	t.Setenv("MEDIANODE_CONFIG", writeConfig(t, "server:\n  http_port: 9200\n"))
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.HTTPPort != 9200 {
		t.Fatalf("puerto = %d", cfg.Server.HTTPPort)
	}
}

func TestLoadRutasRelativasDesdeRaizDeInstalacion(t *testing.T) {
	path := writeConfig(t, "")
	root := filepath.Dir(filepath.Dir(path))

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "data", "medianode.db"); cfg.Database.Path != want {
		t.Fatalf("base = %s, se esperaba %s", cfg.Database.Path, want)
	}
}

func TestLoadErrores(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"clave desconocida", "server:\n  htp_port: 1\n", "htp_port"},
		{"yaml roto", "server: [", "leer configuración"},
		{"puerto fuera de rango", "server:\n  http_port: 70000\n", "server.http_port"},
		{"puertos iguales", "server:\n  http_port: 9000\n  https_port: 9000\n", "no pueden coincidir"},
		{"https no disponible", "server:\n  https_enabled: true\n", "Fase 11"},
		{"direccion invalida", "server:\n  bind_address: nope\n", "bind_address"},
		{"proxy invalido", "server:\n  trusted_proxies: [\"x\"]\n", "trusted_proxies"},
		{"origen con barra final", "server:\n  cors_origins: [\"https://a.com/\"]\n", "cors_origins"},
		{"origen sin esquema", "server:\n  cors_origins: [\"a.com\"]\n", "cors_origins"},
		{"origen ftp", "server:\n  cors_origins: [\"ftp://a.com\"]\n", "cors_origins"},
		{"journal", "database:\n  journal_mode: DELETE\n", "journal_mode"},
		{"limite", "transcoding:\n  max_concurrent_jobs: 0\n", "max_concurrent_jobs"},
		{"aceleracion", "transcoding:\n  hardware_acceleration: cuantica\n", "hardware_acceleration"},
		{"nivel de log", "logging:\n  level: ruido\n", "logging.level"},
		{"remoto incoherente", "remote:\n  direct_enabled: true\n", "remote.enabled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, c.body))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, se esperaba %q", err, c.want)
			}
		})
	}
}

func TestLoadArchivoExplicitoDebeExistir(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "no-existe.yaml")); err == nil {
		t.Fatal("se esperaba un error")
	}
}

func TestLoadEntornoInvalido(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MEDIANODE_SERVER_HTTP_PORT", "abc")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "MEDIANODE_SERVER_HTTP_PORT") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateAcumulaProblemas(t *testing.T) {
	cfg := Default()
	cfg.Server.HTTPPort = 0
	cfg.Logging.Level = "x"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("se esperaba un error")
	}
	for _, want := range []string{"http_port", "logging.level", "database.path"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("falta %q en %v", want, err)
		}
	}
}

func TestEjemploDelRepositorioEsValido(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "server.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(writeConfig(t, string(data))); err != nil {
		t.Fatal(err)
	}
}
