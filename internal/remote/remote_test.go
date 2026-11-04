package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func req(remote string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func TestClasificaElOrigen(t *testing.T) {
	proxied := NewNetwork([]string{"10.0.0.1", "172.16.0.0/12"})
	vpn := NewNetwork(nil).WithLocal([]string{"100.64.0.0/10"})
	plain := NewNetwork(nil)

	cases := []struct {
		name string
		n    *Network
		r    *http.Request
		want Origin
	}{
		{"bucle", plain, req("127.0.0.1:1"), OriginLocal},
		{"red privada", plain, req("192.168.1.5:1"), OriginLocal},
		{"10/8", plain, req("10.9.9.9:1"), OriginLocal},
		{"enlace local", plain, req("169.254.3.3:1"), OriginLocal},
		{"IP pública", plain, req("203.0.113.9:1"), OriginRemote},
		{"IPv6 bucle", plain, req("[::1]:1"), OriginLocal},
		{"IPv4 mapeada en IPv6", plain, req("[::ffff:192.168.1.5]:1"), OriginLocal},
		{"IPv6 ULA", plain, req("[fd00::1]:1"), OriginLocal},
		{"IPv6 enlace local", plain, req("[fe80::1]:1"), OriginLocal},
		{"IPv6 pública", plain, req("[2001:db8::1]:1"), OriginRemote},
		{"IPv4 mapeada pública", plain, req("[::ffff:203.0.113.9]:1"), OriginRemote},
		{"dirección ilegible", plain, req("basura"), OriginRemote},
		{"sin puerto", plain, req("192.168.1.5"), OriginRemote},

		// §14.4: las cabeceras de reenvío desde un origen que no es proxy de
		// confianza hacen la petición remota, aunque venga de la LAN.
		{"proxy no declarado con X-Forwarded-For", plain, req("192.168.1.9:1", HeaderForwardedFor, "192.168.1.20"), OriginRemote},
		{"proxy no declarado con X-Real-IP", plain, req("127.0.0.1:1", headerRealIP, "192.168.1.20"), OriginRemote},
		{"proxy no declarado con Forwarded", plain, req("127.0.0.1:1", headerForwarded, "for=192.168.1.20"), OriginRemote},
		{"proxy no declarado con X-Forwarded-Proto", plain, req("127.0.0.1:1", HeaderForwardedProto, "https"), OriginRemote},
		{"proxy no declarado con X-Forwarded-Host", plain, req("127.0.0.1:1", headerForwardedHost, "x.example"), OriginRemote},
		{"el proxy declarado no hereda la confianza de otro", proxied, req("192.168.1.9:1", HeaderForwardedFor, "192.168.1.20"), OriginRemote},

		{"proxy de confianza, cliente de fuera", proxied, req("10.0.0.1:1", HeaderForwardedFor, "203.0.113.9"), OriginRemote},
		{"proxy de confianza, cliente de la LAN", proxied, req("10.0.0.1:1", HeaderForwardedFor, "192.168.1.20"), OriginLocal},
		{"proxy en rango, cliente de fuera", proxied, req("172.20.0.4:1", HeaderForwardedFor, "203.0.113.9"), OriginRemote},
		{"proxy sin cabecera: ante la duda, remota", proxied, req("10.0.0.1:1"), OriginRemote},
		{"proxy con cabecera vacía", proxied, req("10.0.0.1:1", HeaderForwardedFor, " "), OriginRemote},
		{"proxy con cabecera ilegible", proxied, req("10.0.0.1:1", HeaderForwardedFor, "basura"), OriginRemote},
		{"el cliente no suplanta añadiendo a la izquierda", proxied, req("10.0.0.1:1", HeaderForwardedFor, "192.168.1.20, 203.0.113.9"), OriginRemote},
		{"el último salto manda aunque el anterior sea público", proxied, req("10.0.0.1:1", HeaderForwardedFor, "203.0.113.9, 192.168.1.20"), OriginLocal},
		{"último salto ilegible tras uno válido", proxied, req("10.0.0.1:1", HeaderForwardedFor, "192.168.1.20, basura"), OriginRemote},
		{"cadena de proxies de confianza", proxied, req("10.0.0.1:1", HeaderForwardedFor, "203.0.113.9, 172.16.0.5"), OriginRemote},
		{"proxy en IPv6 mapeada", NewNetwork([]string{"10.0.0.1"}), req("[::ffff:10.0.0.1]:1", HeaderForwardedFor, "203.0.113.9"), OriginRemote},

		{"VPN sin declarar es remota", plain, req("100.64.1.1:1"), OriginRemote},
		{"VPN declarada es local", vpn, req("100.64.1.1:1"), OriginLocal},
		{"fuera del rango de la VPN", vpn, req("100.128.0.1:1"), OriginRemote},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.n.Classify(c.r); got != c.want {
				t.Fatalf("Classify = %v, se esperaba %v", got, c.want)
			}
		})
	}
}

func TestElOrigenSinClasificarEsRemoto(t *testing.T) {
	var o Origin
	if o != OriginRemote || o.String() != "remote" || OriginLocal.String() != "local" {
		t.Fatalf("el cero debe ser remoto: %v", o)
	}
}

func TestClientIPYSecure(t *testing.T) {
	n := NewNetwork([]string{"10.0.0.1"})
	if got := n.ClientIP(req("10.0.0.1:1", HeaderForwardedFor, "1.2.3.4, 203.0.113.9")); got != "203.0.113.9" {
		t.Errorf("IP = %q", got)
	}
	if got := n.ClientIP(req("192.168.1.2:1", HeaderForwardedFor, "1.2.3.4")); got != "192.168.1.2" {
		t.Errorf("sin proxy de confianza se ignora la cabecera: %q", got)
	}
	if got := n.ClientIP(req("basura")); got != "basura" {
		t.Errorf("dirección ilegible: %q", got)
	}
	if n.Secure(req("10.0.0.1:1", HeaderForwardedProto, "https")) != true {
		t.Error("proxy de confianza con https")
	}
	if n.Secure(req("192.168.1.2:1", HeaderForwardedProto, "https")) {
		t.Error("una cabecera de un origen cualquiera no vale")
	}
	if n.Secure(req("10.0.0.1:1", HeaderForwardedProto, "http")) {
		t.Error("http no es seguro")
	}
	r := req("192.168.1.2:1")
	r.TLS = &tlsState
	if !n.Secure(r) {
		t.Error("una conexión TLS directa es segura")
	}
}

func TestIsLocalIP(t *testing.T) {
	n := NewNetwork(nil).WithLocal([]string{"100.64.0.0/10"})
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "192.168.0.4": true, " 10.1.1.1 ": true, "::1": true, "100.100.100.100": true,
		"8.8.8.8": false, "": false, "basura": false, "2001:db8::1": false,
	} {
		if got := n.IsLocalIP(ip); got != want {
			t.Errorf("IsLocalIP(%q) = %v", ip, got)
		}
	}
}

func newRemote(o Options) *Remote {
	return New(o, zerolog.Nop())
}

func ok200(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPuertaDelAccesoRemoto(t *testing.T) {
	local, outside := req("192.168.1.5:1"), func() *http.Request { return req("203.0.113.9:1") }
	cases := []struct {
		name string
		opts Options
		r    *http.Request
		want int
	}{
		{"desactivado: la red local pasa", Options{}, local, 200},
		{"desactivado: lo de fuera, no", Options{}, outside(), 403},
		{"enabled sin direct: lo de fuera, no", Options{Enabled: true}, outside(), 403},
		{"direct sin enabled: lo de fuera, no", Options{Direct: true}, outside(), 403},
		{"activado: lo de fuera pasa", Options{Enabled: true, Direct: true}, outside(), 200},
		{"con reenvío no declarado, lo de dentro cuenta como fuera", Options{}, req("127.0.0.1:1", HeaderForwardedFor, "192.168.1.5"), 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := do(newRemote(c.opts).Gate(http.HandlerFunc(ok200)), c.r)
			if w.Code != c.want {
				t.Fatalf("estado %d: %s", w.Code, w.Body)
			}
			if c.want == 403 && !strings.Contains(w.Body.String(), "REMOTE_ACCESS_DISABLED") {
				t.Fatalf("código de error: %s", w.Body)
			}
		})
	}
}

func TestHSTSSoloEnRemotoSeguro(t *testing.T) {
	r := newRemote(Options{Enabled: true, Direct: true, TrustedProxies: []string{"10.0.0.1"}})
	h := r.Gate(http.HandlerFunc(ok200))

	w := do(h, req("10.0.0.1:1", HeaderForwardedFor, "203.0.113.9", HeaderForwardedProto, "https"))
	if got := w.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Errorf("HSTS = %q", got)
	}
	if w := do(h, req("10.0.0.1:1", HeaderForwardedFor, "203.0.113.9")); w.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS por una conexión sin cifrar no sirve de nada")
	}
	if w := do(h, req("10.0.0.1:1", HeaderForwardedFor, "192.168.1.5", HeaderForwardedProto, "https")); w.Header().Get("Strict-Transport-Security") != "" {
		t.Error("la red local no recibe HSTS: fijaría https en un nombre que puede ser http")
	}
}

func TestAdministracionSoloLocal(t *testing.T) {
	local, outside := func() *http.Request { return req("192.168.1.5:1") }, func() *http.Request { return req("203.0.113.9:1") }
	cases := []struct {
		name string
		opts Options
		r    *http.Request
		want int
	}{
		{"desde la red local", Options{Enabled: true, Direct: true}, local(), 200},
		{"desde fuera, bloqueada por defecto", Options{Enabled: true, Direct: true}, outside(), 403},
		{"desde fuera, permitida", Options{Enabled: true, Direct: true, AdminEnabled: true}, outside(), 200},
		{"admin_enabled sin acceso remoto no abre nada", Options{AdminEnabled: true}, outside(), 403},
		{"admin_enabled sin direct no abre nada", Options{Enabled: true, AdminEnabled: true}, outside(), 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := do(newRemote(c.opts).RequireLocalAdmin(http.HandlerFunc(ok200)), c.r)
			if w.Code != c.want {
				t.Fatalf("estado %d: %s", w.Code, w.Body)
			}
			if c.want == 403 && !strings.Contains(w.Body.String(), "REMOTE_ADMIN_DISABLED") {
				t.Fatalf("código de error: %s", w.Body)
			}
		})
	}
}

func TestElPanelNoSirveLaAdministracionDesdeFuera(t *testing.T) {
	panel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("panel " + r.URL.Path)) })
	closed := newRemote(Options{Enabled: true, Direct: true}).GuardPanel(panel)
	open := newRemote(Options{Enabled: true, Direct: true, AdminEnabled: true}).GuardPanel(panel)

	get := func(h http.Handler, remote, path string) int {
		r := req(remote)
		r.URL.Path = path
		return do(h, r).Code
	}
	for _, p := range []string{"/admin", "/admin/", "/admin/system", "/users", "/users/x", "/metadata", "/metadata/3"} {
		if got := get(closed, "203.0.113.9:1", p); got != 403 {
			t.Errorf("%s desde fuera: %d", p, got)
		}
		if got := get(closed, "192.168.1.5:1", p); got != 200 {
			t.Errorf("%s desde la LAN: %d", p, got)
		}
		if got := get(open, "203.0.113.9:1", p); got != 200 {
			t.Errorf("%s desde fuera con admin_enabled: %d", p, got)
		}
	}
	// Lo que solo se parece no se bloquea.
	for _, p := range []string{"/", "/movies", "/administrator", "/usersettings", "/metadatax", "/library/admin"} {
		if got := get(closed, "203.0.113.9:1", p); got != 200 {
			t.Errorf("%s no es administración: %d", p, got)
		}
	}
}

// --- Prueba de alcance ---

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time              { return c.t }
func (c *fakeClock) add(d time.Duration)         { c.t = c.t.Add(d) }
func newClock() *fakeClock                       { return &fakeClock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)} }
func (r *Remote) withClock(c *fakeClock) *Remote { r.env.now = c.now; return r }

func probeRemote(clock *fakeClock) *Remote {
	return newRemote(Options{Enabled: true, Direct: true, PublicURL: "https://media.example.com"}).withClock(clock)
}

func hit(r *Remote, remoteAddr, nonce string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.HandleProbe(w, req(remoteAddr), nonce)
	return w
}

func nonceOf(p Probe) string { return p.URL[strings.LastIndex(p.URL, "/")+1:] }

func TestPruebaDesdeFuera(t *testing.T) {
	clock := newClock()
	r := probeRemote(clock)
	if s := r.ProbeStatus(); s.State != ProbeNone {
		t.Fatalf("sin prueba: %+v", s)
	}
	p, err := r.StartProbe()
	if err != nil || p.State != ProbePending {
		t.Fatalf("StartProbe: %+v %v", p, err)
	}
	if !strings.HasPrefix(p.URL, "https://media.example.com/api/v1/remote/probe/") || len(nonceOf(p)) < 43 {
		t.Fatalf("URL = %q: el código debe tener 256 bits", p.URL)
	}
	if !p.ExpiresAt.Equal(clock.t.Add(10 * time.Minute)) {
		t.Errorf("caduca %v", p.ExpiresAt)
	}
	nonce := nonceOf(p)

	// Un código equivocado no avanza nada y no distingue casos.
	for _, bad := range []string{"", "x", nonce + "x", nonce[:len(nonce)-1], strings.ToUpper(nonce)} {
		if w := hit(r, "203.0.113.9:1", bad); w.Code != 404 {
			t.Errorf("%q: %d", bad, w.Code)
		}
	}
	if s := r.ProbeStatus(); s.State != ProbePending {
		t.Fatalf("un código equivocado cambió el estado: %+v", s)
	}

	// Desde dentro no prueba nada, y el código sigue valiendo.
	w := hit(r, "192.168.1.5:1", nonce)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "red local") {
		t.Fatalf("desde la LAN: %d %s", w.Code, w.Body)
	}
	if s := r.ProbeStatus(); s.State != ProbeLocalOnly || s.URL == "" {
		t.Fatalf("desde la LAN: %+v", s)
	}

	// Desde fuera, sí.
	w = hit(r, "203.0.113.9:4444", nonce)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "accesible desde fuera") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("desde fuera: %d %q %v", w.Code, w.Body, w.Header())
	}
	s := r.ProbeStatus()
	if s.State != ProbeReachable || s.From != "203.0.113.9" || !s.At.Equal(clock.t) || s.URL != "" {
		t.Fatalf("estado final: %+v", s)
	}
	// De un solo uso.
	if w := hit(r, "203.0.113.9:1", nonce); w.Code != 404 {
		t.Fatalf("reutilizado: %d", w.Code)
	}
	if s := r.ProbeStatus(); s.State != ProbeReachable {
		t.Fatalf("reutilizar no debe cambiar el resultado: %+v", s)
	}
}

func TestLaPruebaCaduca(t *testing.T) {
	clock := newClock()
	r := probeRemote(clock)
	p, _ := r.StartProbe()
	clock.add(10*time.Minute - time.Second)
	if s := r.ProbeStatus(); s.State != ProbePending {
		t.Fatalf("aún vale: %+v", s)
	}
	clock.add(time.Second)
	if s := r.ProbeStatus(); s.State != ProbeExpired {
		t.Fatalf("caducada: %+v", s)
	}
	if w := hit(r, "203.0.113.9:1", nonceOf(p)); w.Code != 404 {
		t.Fatalf("un código caducado no vale: %d", w.Code)
	}
}

func TestUnaPruebaNuevaSustituyeALaAnterior(t *testing.T) {
	r := probeRemote(newClock())
	first, _ := r.StartProbe()
	second, _ := r.StartProbe()
	if nonceOf(first) == nonceOf(second) {
		t.Fatal("el código debe cambiar")
	}
	if w := hit(r, "203.0.113.9:1", nonceOf(first)); w.Code != 404 {
		t.Fatalf("el código anterior sigue valiendo: %d", w.Code)
	}
	if w := hit(r, "203.0.113.9:1", nonceOf(second)); w.Code != 200 {
		t.Fatalf("el nuevo no vale: %d", w.Code)
	}
}

func TestNoSePuedeProbarSinAccesoRemotoNiDireccion(t *testing.T) {
	if _, err := newRemote(Options{PublicURL: "https://x.example"}).StartProbe(); err == nil {
		t.Error("con el acceso remoto desactivado no hay prueba")
	}
	if _, err := newRemote(Options{Enabled: true, Direct: true}).StartProbe(); err == nil || !strings.Contains(err.Error(), "public_url") {
		t.Errorf("sin dirección pública: %v", err)
	}
	// Sin prueba iniciada, ningún código vale, ni el vacío.
	r := probeRemote(newClock())
	if w := hit(r, "203.0.113.9:1", ""); w.Code != 404 {
		t.Errorf("código vacío sin prueba: %d", w.Code)
	}
}

// --- Informe de conectividad ---

func certFile(t *testing.T, mutate func(*x509.Certificate)) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "media.example.com"},
		DNSNames:  []string{"media.example.com"},
		NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if mutate != nil {
		mutate(tmpl)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckCertificate(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	garbage := filepath.Join(t.TempDir(), "x.pem")
	_ = os.WriteFile(garbage, []byte("no es un certificado"), 0o600)

	cases := []struct {
		name   string
		file   string
		host   string
		status string
		text   string
	}{
		{"válido", certFile(t, nil), "media.example.com", StatusOK, "Válido hasta el 2027-01-01"},
		{"sin host que comprobar", certFile(t, nil), "", StatusOK, "Válido"},
		{"caducado", certFile(t, func(c *x509.Certificate) { c.NotAfter = now.Add(-time.Hour) }), "media.example.com", StatusFail, "caducó el"},
		{"aún no vale", certFile(t, func(c *x509.Certificate) { c.NotBefore = now.Add(time.Hour) }), "media.example.com", StatusFail, "aún no es válido"},
		{"otro nombre", certFile(t, nil), "otro.example.com", StatusFail, "no cubre otro.example.com"},
		{"caduca pronto", certFile(t, func(c *x509.Certificate) { c.NotAfter = now.Add(13 * 24 * time.Hour) }), "media.example.com", StatusWarn, "caduca en 13 días"},
		{"justo en el margen", certFile(t, func(c *x509.Certificate) { c.NotAfter = now.Add(14 * 24 * time.Hour) }), "media.example.com", StatusOK, "Válido"},
		{"basura", garbage, "", StatusFail, "no hay ningún certificado"},
		{"no existe", filepath.Join(t.TempDir(), "no.pem"), "", StatusFail, "No se puede leer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckCertificate(c.file, c.host, now)
			if got.Status != c.status || !strings.Contains(got.Detail, c.text) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func byID(rep *Report, id string) Check {
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func TestInformeConElAccesoRemotoDesactivadoNoSaleALaRed(t *testing.T) {
	r := newRemote(Options{PublicURL: "https://media.example.com"})
	r.env.lookup = func(context.Context, string) ([]netip.Addr, error) { t.Fatal("no debe resolver DNS"); return nil, nil }
	r.env.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("no debe abrir conexiones")
		return nil, nil
	})}
	rep, err := r.CheckReachability(context.Background())
	if err != nil || len(rep.Checks) != 1 || rep.Checks[0].Status != StatusSkipped || rep.Enabled {
		t.Fatalf("%+v %v", rep, err)
	}
	if rep.Checks == nil || rep.Probe.State != ProbeNone {
		t.Errorf("%+v", rep)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInformeDeConectividad(t *testing.T) {
	healthy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()
	broken := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer broken.Close()
	closed := httptest.NewTLSServer(http.HandlerFunc(ok200))
	closedURL := closed.URL
	closed.Close()

	build := func(url string, client *http.Client, lookup func(context.Context, string) ([]netip.Addr, error)) *Report {
		r := newRemote(Options{Enabled: true, Direct: true, PublicURL: url, BindAddress: "0.0.0.0", TrustedProxies: []string{"10.0.0.1"}})
		r.env.client = client
		if lookup != nil {
			r.env.lookup = lookup
		}
		rep, err := r.CheckReachability(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}

	t.Run("todo bien", func(t *testing.T) {
		rep := build(healthy.URL, healthy.Client(), nil)
		if c := byID(rep, "self"); c.Status != StatusOK {
			t.Errorf("self: %+v", c)
		}
		if c := byID(rep, "dns"); c.Status != StatusOK || !strings.Contains(c.Detail, "es una IP") {
			t.Errorf("dns: %+v", c)
		}
		if c := byID(rep, "admin"); c.Status != StatusOK {
			t.Errorf("admin: %+v", c)
		}
		if c := byID(rep, "outside"); c.Status != StatusWarn || !strings.Contains(c.Detail, "Sin comprobar") {
			t.Errorf("outside: %+v", c)
		}
	})
	t.Run("certificado no fiable", func(t *testing.T) {
		rep := build(healthy.URL, &http.Client{Timeout: time.Second}, nil)
		if c := byID(rep, "self"); c.Status != StatusFail || !strings.Contains(c.Detail, "no es de confianza") {
			t.Errorf("self: %+v", c)
		}
	})
	t.Run("el proxy apunta a otro sitio", func(t *testing.T) {
		rep := build(broken.URL, broken.Client(), nil)
		if c := byID(rep, "self"); c.Status != StatusFail || !strings.Contains(c.Detail, "502") {
			t.Errorf("self: %+v", c)
		}
	})
	t.Run("no conecta: aviso, no fallo", func(t *testing.T) {
		rep := build(closedURL, &http.Client{Timeout: time.Second}, nil)
		if c := byID(rep, "self"); c.Status != StatusWarn || !strings.Contains(c.Detail, "prueba desde fuera") {
			t.Errorf("self: %+v", c)
		}
	})
	t.Run("redirección no se sigue", func(t *testing.T) {
		redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.example/health", http.StatusFound)
		}))
		defer redirect.Close()
		r := newRemote(Options{Enabled: true, Direct: true, PublicURL: redirect.URL})
		var rep Report
		r.checkSelf(context.Background(), func(id, name, status, format string, args ...any) {
			rep.Checks = append(rep.Checks, Check{ID: id, Status: status, Detail: format})
		})
		if rep.Checks[0].Status != StatusFail {
			t.Errorf("%+v", rep.Checks)
		}
	})

	dns := func(addrs ...string) func(context.Context, string) ([]netip.Addr, error) {
		return func(context.Context, string) ([]netip.Addr, error) {
			var out []netip.Addr
			for _, a := range addrs {
				out = append(out, netip.MustParseAddr(a))
			}
			return out, nil
		}
	}
	for _, c := range []struct {
		name   string
		lookup func(context.Context, string) ([]netip.Addr, error)
		status string
		text   string
	}{
		{"pública", dns("203.0.113.9"), StatusOK, "resuelve a 203.0.113.9"},
		{"IPv4 mapeada pública", dns("::ffff:203.0.113.9"), StatusOK, "203.0.113.9"},
		{"solo privada", dns("192.168.1.10"), StatusWarn, "solo a direcciones privadas"},
		{"mezcla", dns("203.0.113.9", "10.0.0.5"), StatusWarn, "públicas (203.0.113.9) y privadas (10.0.0.5)"},
		{"sin respuesta", dns(), StatusFail, "no resuelve"},
		{"error", func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("NXDOMAIN") }, StatusFail, "NXDOMAIN"},
	} {
		t.Run("dns "+c.name, func(t *testing.T) {
			r := newRemote(Options{Enabled: true, Direct: true, PublicURL: "https://media.example.com"})
			r.env.lookup = c.lookup
			r.env.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("sin red") })}
			rep, _ := r.CheckReachability(context.Background())
			if got := byID(rep, "dns"); got.Status != c.status || !strings.Contains(got.Detail, c.text) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestInformeAvisaDeLaAdministracionYDeLaEscucha(t *testing.T) {
	r := newRemote(Options{Enabled: true, Direct: true, AdminEnabled: true, BindAddress: "127.0.0.1"})
	rep, _ := r.CheckReachability(context.Background())
	if c := byID(rep, "admin"); c.Status != StatusWarn || !strings.Contains(c.Detail, "Internet") {
		t.Errorf("admin: %+v", c)
	}
	if c := byID(rep, "bind"); c.Status != StatusWarn || !strings.Contains(c.Detail, "127.0.0.1") {
		t.Errorf("bind: %+v", c)
	}
	if c := byID(rep, "public_url"); c.Status != StatusWarn {
		t.Errorf("public_url: %+v", c)
	}
	if c := byID(rep, "tls"); c.Status != StatusOK || !strings.Contains(c.Detail, "proxy") {
		t.Errorf("tls sin certificado propio: %+v", c)
	}
}

func TestInformeUsaElCertificadoPropio(t *testing.T) {
	file := certFile(t, nil)
	clock := newClock()
	r := newRemote(Options{Enabled: true, Direct: true, HTTPSEnabled: true, TLSCertFile: file, PublicURL: "https://otro.example.com"}).withClock(clock)
	r.env.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
	}
	r.env.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("sin red") })}
	rep, _ := r.CheckReachability(context.Background())
	if c := byID(rep, "tls"); c.Status != StatusFail || !strings.Contains(c.Detail, "otro.example.com") {
		t.Errorf("el certificado no cubre la dirección pública: %+v", c)
	}
}

func TestInformeReflejaLaPrueba(t *testing.T) {
	clock := newClock()
	r := probeRemote(clock)
	r.env.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
	}
	r.env.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("sin red") })}
	state := func() Check {
		rep, _ := r.CheckReachability(context.Background())
		return byID(rep, "outside")
	}
	if c := state(); c.Status != StatusWarn || !strings.Contains(c.Detail, "Sin comprobar") {
		t.Errorf("none: %+v", c)
	}
	p, _ := r.StartProbe()
	if c := state(); c.Status != StatusWarn || !strings.Contains(c.Detail, "pendiente") {
		t.Errorf("pending: %+v", c)
	}
	hit(r, "192.168.1.5:1", nonceOf(p))
	if c := state(); c.Status != StatusWarn || !strings.Contains(c.Detail, "pendiente") {
		t.Errorf("local_only: %+v", c)
	}
	hit(r, "203.0.113.9:1", nonceOf(p))
	if c := state(); c.Status != StatusOK || !strings.Contains(c.Detail, "203.0.113.9") {
		t.Errorf("reachable: %+v", c)
	}
	_, _ = r.StartProbe()
	clock.add(time.Hour)
	if c := state(); c.Status != StatusWarn || !strings.Contains(c.Detail, "caducó") {
		t.Errorf("expired: %+v", c)
	}
}

var tlsState = tls.ConnectionState{}
