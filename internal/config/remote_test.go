package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCert escribe un par certificado/clave autofirmado y devuelve sus rutas.
func writeCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "media.example.com"}, DNSNames: []string{"media.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func validate(t *testing.T, mutate func(*Config)) error {
	t.Helper()
	c := Default()
	// Default() deja las rutas relativas: Load las resuelve antes de validar.
	if err := c.resolvePaths(filepath.Join(t.TempDir(), "config", "server.yaml")); err != nil {
		t.Fatal(err)
	}
	mutate(c)
	return c.Validate()
}

func TestHTTPSExigeUnCertificadoCargable(t *testing.T) {
	cert, key := writeCert(t)
	if err := validate(t, func(c *Config) { c.Server.HTTPSEnabled, c.Server.TLSCertFile, c.Server.TLSKeyFile = true, cert, key }); err != nil {
		t.Fatalf("con certificado válido: %v", err)
	}
	_, other := writeCert(t)
	cases := map[string]func(*Config){
		"sin rutas":       func(c *Config) { c.Server.HTTPSEnabled = true },
		"sin clave":       func(c *Config) { c.Server.HTTPSEnabled, c.Server.TLSCertFile = true, cert },
		"sin certificado": func(c *Config) { c.Server.HTTPSEnabled, c.Server.TLSKeyFile = true, key },
		"rutas inexistentes": func(c *Config) {
			c.Server.HTTPSEnabled, c.Server.TLSCertFile, c.Server.TLSKeyFile = true, cert+".no", key+".no"
		},
		"clave de otro par": func(c *Config) { c.Server.HTTPSEnabled, c.Server.TLSCertFile, c.Server.TLSKeyFile = true, cert, other },
	}
	for name, mutate := range cases {
		err := validate(t, mutate)
		if err == nil || (!strings.Contains(err.Error(), "https_enabled") && !strings.Contains(err.Error(), "certificado TLS")) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Sin HTTPS, las rutas no se miran.
	if err := validate(t, func(c *Config) { c.Server.TLSCertFile = "/no/existe" }); err != nil {
		t.Errorf("rutas sin HTTPS: %v", err)
	}
}

func TestRutasDeCertificadoRelativasPartenDeLaRaiz(t *testing.T) {
	cert, key := writeCert(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	for src, name := range map[string]string{cert: "cert.pem", key: "key.pem"} {
		data, _ := os.ReadFile(src)
		if err := os.WriteFile(filepath.Join(root, "config", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "config", "server.yaml")
	body := "server:\n  https_enabled: true\n  tls_cert_file: config/cert.pem\n  tls_key_file: config/key.pem\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.Server.TLSCertFile) || !strings.HasPrefix(cfg.Server.TLSCertFile, root) {
		t.Fatalf("ruta del certificado = %q", cfg.Server.TLSCertFile)
	}
}

func TestAccesoRemotoDirectoExigeCifrado(t *testing.T) {
	cert, key := writeCert(t)
	direct := func(c *Config) { c.Remote.Enabled, c.Remote.DirectEnabled = true, true }
	if err := validate(t, direct); err == nil || !strings.Contains(err.Error(), "exige HTTPS") {
		t.Fatalf("remoto sin cifrado: %v", err)
	}
	if err := validate(t, func(c *Config) { direct(c); c.Server.TrustedProxies = []string{"127.0.0.1"} }); err != nil {
		t.Fatalf("remoto tras un reverse proxy: %v", err)
	}
	if err := validate(t, func(c *Config) {
		direct(c)
		c.Server.HTTPSEnabled, c.Server.TLSCertFile, c.Server.TLSKeyFile = true, cert, key
	}); err != nil {
		t.Fatalf("remoto con certificado propio: %v", err)
	}
	// Los demás interruptores dependen del principal y de Direct.
	if err := validate(t, func(c *Config) { c.Remote.DirectEnabled = true }); err == nil || !strings.Contains(err.Error(), "remote.enabled") {
		t.Errorf("direct sin enabled: %v", err)
	}
	if err := validate(t, func(c *Config) { c.Remote.AdminEnabled = true }); err == nil || !strings.Contains(err.Error(), "remote.enabled") {
		t.Errorf("admin sin enabled: %v", err)
	}
	if err := validate(t, func(c *Config) {
		c.Remote.Enabled, c.Remote.AdminEnabled = true, true
		c.Server.TrustedProxies = []string{"127.0.0.1"}
	}); err == nil || !strings.Contains(err.Error(), "admin_enabled necesita remote.direct_enabled") {
		t.Errorf("admin sin direct: %v", err)
	}
	if err := validate(t, func(c *Config) { c.Remote.Enabled, c.Remote.RelayEnabled = true, true }); err == nil || !strings.Contains(err.Error(), "relay_enabled aún no está disponible") {
		t.Errorf("relay: %v", err)
	}
}

func TestDireccionPublica(t *testing.T) {
	for url, ok := range map[string]bool{
		"": true, "https://media.example.com": true, "https://media.example.com:8443": true, "https://203.0.113.9": true,
		"http://media.example.com": false, "https://media.example.com/": false, "https://media.example.com/x": false,
		"https://user:pass@media.example.com": false, "https://media.example.com?a=1": false, "https://media.example.com#f": false,
		"media.example.com": false, "https://": false, "ftp://media.example.com": false,
	} {
		err := validate(t, func(c *Config) { c.Remote.PublicURL = url })
		if (err == nil) != ok {
			t.Errorf("public_url %q: %v", url, err)
		}
	}
}

func TestRedesLocalesExtra(t *testing.T) {
	if err := validate(t, func(c *Config) { c.Remote.LocalNetworks = []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"} }); err != nil {
		t.Fatalf("%v", err)
	}
	for _, bad := range []string{"100.64.0.0", "100.64.0.0/33", "basura", "0.0.0.0/0x", ""} {
		if err := validate(t, func(c *Config) { c.Remote.LocalNetworks = []string{bad} }); err == nil {
			t.Errorf("%q debía rechazarse", bad)
		}
	}
}

func TestOpcionesDeDLNA(t *testing.T) {
	d := Default().DLNA
	if d.Mode != "auto" || d.BurnSubtitles != "auto" || d.IdleTimeoutSeconds != 120 {
		t.Fatalf("defaults: %+v", d)
	}
	if Default().Discovery.DLNAEnabled {
		t.Fatal("DLNA debe estar desactivado por defecto (§13.2)")
	}
	for mode, ok := range map[string]bool{"auto": true, "direct": true, "transcode": true, "": false, "AUTO": false, "otro": false} {
		if err := validate(t, func(c *Config) { c.DLNA.Mode = mode }); (err == nil) != ok {
			t.Errorf("mode %q: %v", mode, err)
		}
	}
	for v, ok := range map[string]bool{"auto": true, "off": true, "": false, "on": false} {
		if err := validate(t, func(c *Config) { c.DLNA.BurnSubtitles = v }); (err == nil) != ok {
			t.Errorf("burn_subtitles %q: %v", v, err)
		}
	}
	for n, ok := range map[int]bool{10: true, 120: true, 9: false, 0: false, -1: false} {
		if err := validate(t, func(c *Config) { c.DLNA.IdleTimeoutSeconds = n }); (err == nil) != ok {
			t.Errorf("idle_timeout_seconds %d: %v", n, err)
		}
	}
}

func TestDLNAPorEntornoYArchivo(t *testing.T) {
	t.Setenv("MEDIANODE_DISCOVERY_DLNA_ENABLED", "true")
	t.Setenv("MEDIANODE_DLNA_MODE", "transcode")
	t.Setenv("MEDIANODE_REMOTE_LOCAL_NETWORKS", "100.64.0.0/10, 10.8.0.0/24")
	cfg, err := Load(writeConfig(t, "dlna:\n  idle_timeout_seconds: 300\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Discovery.DLNAEnabled || cfg.DLNA.Mode != "transcode" || cfg.DLNA.IdleTimeoutSeconds != 300 ||
		strings.Join(cfg.Remote.LocalNetworks, ",") != "100.64.0.0/10,10.8.0.0/24" {
		t.Fatalf("%+v %+v %+v", cfg.Discovery, cfg.DLNA, cfg.Remote)
	}
}
