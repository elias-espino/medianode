package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// writeCert escribe un par autofirmado para host y devuelve sus rutas y el
// certificado, para que el cliente de la prueba confíe en él.
func writeCert(t *testing.T, dir, host string, serial int64) (certFile, keyFile string, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return writeCertWithKey(t, dir, host, serial, key)
}

// writeCertWithKey firma un certificado nuevo con una clave que ya existe: así
// renueva certbot con --reuse-key, y solo cambia el certificado.
func writeCertWithKey(t *testing.T, dir, host string, serial int64, key *ecdsa.PrivateKey) (certFile, keyFile string, cert *x509.Certificate) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, _ := x509.ParseCertificate(der)
	return certFile, keyFile, parsed
}

// testClock es un reloj que se puede adelantar mientras el servidor lo lee desde
// otras goroutines.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// useClock sustituye el reloj del certificado, bajo el mismo cerrojo que usa el
// servidor para leerlo.
func useClock(srv *TLSServer) *testClock {
	c := &testClock{t: time.Now()}
	srv.certs.mu.Lock()
	srv.certs.now = c.now
	srv.certs.loadedAt = c.t // El plazo se cuenta desde aquí, sin holgura.
	srv.certs.mu.Unlock()
	return c
}

func startTLS(t *testing.T, certFile, keyFile string) *TLSServer {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok " + r.Proto))
	})
	srv, err := NewTLSServer("127.0.0.1:0", h, certFile, keyFile, zerolog.Nop(), func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	return srv
}

// serial devuelve el número de serie del certificado que presenta el servidor.
func serial(t *testing.T, srv *TLSServer, host string, minVersion, maxVersion uint16) (int64, uint16) {
	t.Helper()
	conn, err := tls.Dial("tcp", srv.Addr(), &tls.Config{InsecureSkipVerify: true, ServerName: host, MinVersion: minVersion, MaxVersion: maxVersion})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64(), conn.ConnectionState().Version
}

func TestServidorHTTPSConElCertificadoDelUsuario(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, cert := writeCert(t, dir, "media.example.com", 7)
	srv := startTLS(t, certFile, keyFile)

	pool := x509.NewCertPool()
	pool.AddCert(cert)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "media.example.com"}, ForceAttemptHTTP2: true}}
	resp, err := client.Get("https://" + srv.Addr() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ProtoMajor != 2 {
		t.Fatalf("%d %s", resp.StatusCode, resp.Proto)
	}
	// Quien no confía en el certificado, o pide otro nombre, no pasa.
	bad := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "otro.example.com"}}}
	if _, err := bad.Get("https://" + srv.Addr() + "/"); err == nil {
		t.Fatal("el certificado no cubre otro.example.com")
	}
	// HTTP en claro contra el puerto TLS no responde nada útil.
	if r, err := http.Get("http://" + srv.Addr() + "/"); err == nil {
		r.Body.Close()
		if r.StatusCode == 200 {
			t.Fatal("el puerto HTTPS no debe servir HTTP en claro")
		}
	}
}

func TestSoloTLS12OSuperior(t *testing.T) {
	certFile, keyFile, _ := writeCert(t, t.TempDir(), "media.example.com", 1)
	srv := startTLS(t, certFile, keyFile)
	if _, v := serial(t, srv, "media.example.com", tls.VersionTLS12, tls.VersionTLS13); v < tls.VersionTLS12 {
		t.Fatalf("versión negociada %x", v)
	}
	for _, old := range []uint16{tls.VersionTLS10, tls.VersionTLS11} {
		conn, err := tls.Dial("tcp", srv.Addr(), &tls.Config{InsecureSkipVerify: true, MinVersion: old, MaxVersion: old})
		if err == nil {
			conn.Close()
			t.Fatalf("el servidor aceptó la versión %x", old)
		}
	}
}

func TestElCertificadoSeRecargaSinReiniciar(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := writeCert(t, dir, "media.example.com", 1)
	srv := startTLS(t, certFile, keyFile)
	clock := useClock(srv)
	if got, _ := serial(t, srv, "media.example.com", tls.VersionTLS12, 0); got != 1 {
		t.Fatalf("serie = %d", got)
	}

	// Una renovación escribe otro par en las mismas rutas.
	writeCert(t, dir, "media.example.com", 2)
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(certFile, future, future)
	_ = os.Chtimes(keyFile, future, future)

	// Antes del plazo de comprobación se sigue con el anterior: no se lee el
	// disco en cada conexión.
	clock.add(certReloadEvery - time.Second)
	if got, _ := serial(t, srv, "media.example.com", tls.VersionTLS12, 0); got != 1 {
		t.Fatalf("recargó antes de tiempo: %d", got)
	}
	// Justo en el plazo ya se comprueba (>=, no >).
	clock.add(time.Second)
	if got, _ := serial(t, srv, "media.example.com", tls.VersionTLS12, 0); got != 2 {
		t.Fatalf("no recargó el certificado renovado en el plazo exacto: %d", got)
	}
}

func TestUnaRenovacionQueReutilizaLaClaveSeRecarga(t *testing.T) {
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile, _ := writeCertWithKey(t, dir, "media.example.com", 1, key)
	keyInfo, _ := os.Stat(keyFile)
	srv := startTLS(t, certFile, keyFile)
	clock := useClock(srv)
	if got, _ := serial(t, srv, "media.example.com", tls.VersionTLS12, 0); got != 1 {
		t.Fatalf("serie = %d", got)
	}
	// Solo cambia el certificado; la clave sigue siendo el mismo archivo.
	writeCertWithKey(t, dir, "media.example.com", 5, key)
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(certFile, future, future)
	_ = os.Chtimes(keyFile, keyInfo.ModTime(), keyInfo.ModTime())
	clock.add(certReloadEvery + time.Second)
	if got, _ := serial(t, srv, "media.example.com", tls.VersionTLS12, 0); got != 5 {
		t.Fatalf("no recargó una renovación que solo cambia el certificado: %d", got)
	}
}

func TestUnCertificadoRotoNoTumbaElServicio(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := writeCert(t, dir, "media.example.com", 1)
	srv := startTLS(t, certFile, keyFile)
	clock := useClock(srv)

	// Un certificado a medio escribir.
	if err := os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\ntruncado"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(certFile, future, future)
	clock.add(certReloadEvery + time.Second)
	if got, _ := serial(t, srv, "media.example.com", tls.VersionTLS12, 0); got != 1 {
		t.Fatalf("debía seguir con el certificado anterior: %d", got)
	}
	// Sin cambios en disco no se vuelve a intentar a cada conexión.
	clock.add(certReloadEvery + time.Second)
	if got, _ := serial(t, srv, "media.example.com", tls.VersionTLS12, 0); got != 1 {
		t.Fatalf("%d", got)
	}
}

func TestUnParInvalidoFallaAlArrancar(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := writeCert(t, dir, "media.example.com", 1)
	if _, err := NewTLSServer("127.0.0.1:0", http.NotFoundHandler(), certFile, keyFile+".no", zerolog.Nop(), func(error) {}); err == nil {
		t.Fatal("una clave que no existe debe fallar al crear el servidor")
	}
	other := t.TempDir()
	_, otherKey, _ := writeCert(t, other, "x", 3)
	if _, err := NewTLSServer("127.0.0.1:0", http.NotFoundHandler(), certFile, otherKey, zerolog.Nop(), func(error) {}); err == nil {
		t.Fatal("una clave de otro certificado debe fallar")
	}
}

func TestElPuertoOcupadoFallaAlArrancar(t *testing.T) {
	certFile, keyFile, _ := writeCert(t, t.TempDir(), "x", 1)
	first := startTLS(t, certFile, keyFile)
	second, err := NewTLSServer(first.Addr(), http.NotFoundHandler(), certFile, keyFile, zerolog.Nop(), func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err == nil {
		_ = second.Stop(context.Background())
		t.Fatal("un puerto ocupado debe fallar en el arranque, no después")
	}
	if first.Name() != "https" {
		t.Fatal(first.Name())
	}
}
