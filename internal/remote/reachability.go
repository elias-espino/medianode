package remote

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Estados de una comprobación.
const (
	StatusOK      = "ok"
	StatusWarn    = "warn"
	StatusFail    = "fail"
	StatusSkipped = "skipped"
)

const (
	probeTTL        = 10 * time.Minute
	probePath       = "/api/v1/remote/probe/"
	networkTimeout  = 5 * time.Second
	certWarnWindow  = 14 * 24 * time.Hour
	probeNonceBytes = 32
)

// Check es el resultado de una comprobación de conectividad.
type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Probe es la prueba de alcance desde fuera de la red local: el administrador
// abre su URL desde otra red, por ejemplo con datos móviles, y el servidor
// anota que esa petición llegó clasificada como remota. No hay servicios de
// terceros: la prueba la hace quien está fuera.
type Probe struct {
	State     string    `json:"state"` // none, pending, reachable, local_only o expired.
	URL       string    `json:"url,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	From      string    `json:"from,omitempty"` // Dirección que llegó desde fuera.
	At        time.Time `json:"at,omitzero"`
}

// Probe estados.
const (
	ProbeNone      = "none"
	ProbePending   = "pending"
	ProbeReachable = "reachable"
	ProbeLocalOnly = "local_only"
	ProbeExpired   = "expired"
)

// Report es el resultado de CheckReachability.
type Report struct {
	Enabled      bool    `json:"enabled"`
	Direct       bool    `json:"direct_enabled"`
	AdminEnabled bool    `json:"admin_enabled"`
	PublicURL    string  `json:"public_url"`
	Checks       []Check `json:"checks"`
	Probe        Probe   `json:"probe"`
}

// environment reúne lo que sale del equipo, para sustituirlo en las pruebas.
type environment struct {
	now    func() time.Time
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	client *http.Client
}

func defaultEnvironment() environment {
	return environment{
		now: time.Now,
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		client: &http.Client{
			Timeout: networkTimeout,
			// Un redireccionamiento no es una respuesta: se informa tal cual.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type probeState struct {
	mu     sync.Mutex
	nonce  string
	expiry time.Time
	state  string
	from   string
	at     time.Time
}

// StartProbe genera un código de un solo uso y devuelve la URL que hay que
// abrir desde fuera de la red local. Sustituye a la prueba anterior.
func (r *Remote) StartProbe() (Probe, error) {
	if !r.Allowed() {
		return Probe{}, fmt.Errorf("el acceso remoto no está activo")
	}
	if r.Opts.PublicURL == "" {
		return Probe{}, fmt.Errorf("falta remote.public_url: sin la dirección pública no hay a dónde llamar")
	}
	raw := make([]byte, probeNonceBytes)
	if _, err := rand.Read(raw); err != nil {
		return Probe{}, err
	}
	p := &r.probe
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonce = base64.RawURLEncoding.EncodeToString(raw)
	p.expiry = r.env.now().Add(probeTTL)
	p.state, p.from, p.at = ProbePending, "", time.Time{}
	return r.snapshotLocked(), nil
}

func (r *Remote) snapshotLocked() Probe {
	p := &r.probe
	switch {
	case p.state == "":
		return Probe{State: ProbeNone}
	case p.state == ProbePending && !r.env.now().Before(p.expiry):
		return Probe{State: ProbeExpired}
	}
	out := Probe{State: p.state, From: p.from, At: p.at}
	if p.state == ProbePending || p.state == ProbeLocalOnly {
		out.ExpiresAt = p.expiry
		out.URL = r.Opts.PublicURL + probePath + p.nonce
	}
	return out
}

// ProbeStatus devuelve el estado de la última prueba.
func (r *Remote) ProbeStatus() Probe {
	r.probe.mu.Lock()
	defer r.probe.mu.Unlock()
	return r.snapshotLocked()
}

// HandleProbe atiende la URL de la prueba. Es pública a propósito: la abre
// quien está fuera, sin sesión. El código tiene 256 bits y caduca, y toda
// respuesta distinta de un acierto es un 404 igual para todos los fallos.
func (r *Remote) HandleProbe(w http.ResponseWriter, req *http.Request, nonce string) {
	p := &r.probe
	p.mu.Lock()
	valid := p.nonce != "" && subtle.ConstantTimeCompare([]byte(nonce), []byte(p.nonce)) == 1 &&
		(p.state == ProbePending || p.state == ProbeLocalOnly) && r.env.now().Before(p.expiry)
	if !valid {
		p.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Classify(req) == OriginLocal {
		// Llegó desde dentro: no prueba nada y el código sigue valiendo.
		p.state = ProbeLocalOnly
		p.mu.Unlock()
		_, _ = io.WriteString(w, "Esta petición llegó desde la red local, así que no prueba el acceso desde fuera.\n"+
			"Ábrela de nuevo desde otra red, por ejemplo con datos móviles.\n")
		return
	}
	p.state, p.from, p.at = ProbeReachable, r.Net.ClientIP(req), r.env.now()
	p.nonce = "" // De un solo uso.
	p.mu.Unlock()
	_, _ = io.WriteString(w, "MediaNode es accesible desde fuera de la red local. Ya puedes cerrar esta página.\n")
}

// CheckReachability ejecuta las comprobaciones de conectividad. Con el acceso
// remoto desactivado solo informa de eso: no sale a la red.
func (r *Remote) CheckReachability(ctx context.Context) (*Report, error) {
	rep := &Report{
		Enabled: r.Opts.Enabled, Direct: r.Opts.Direct, AdminEnabled: r.Opts.AdminEnabled,
		PublicURL: r.Opts.PublicURL, Checks: []Check{},
	}
	add := func(id, name, status, format string, args ...any) {
		rep.Checks = append(rep.Checks, Check{ID: id, Name: name, Status: status, Detail: fmt.Sprintf(format, args...)})
	}
	if !r.Allowed() {
		add("enabled", "Acceso remoto", StatusSkipped, "Está desactivado (remote.enabled y remote.direct_enabled): el servidor solo responde en la red local.")
		rep.Probe = r.ProbeStatus()
		return rep, nil
	}
	add("enabled", "Acceso remoto", StatusOK, "Activo: el servidor acepta conexiones de fuera de la red local.")

	if r.Opts.AdminEnabled {
		add("admin", "Administración remota", StatusWarn, "remote.admin_enabled está activo: el panel de administración es accesible desde Internet. Desactívalo salvo que lo necesites.")
	} else {
		add("admin", "Administración remota", StatusOK, "Bloqueada: solo se administra desde la red local.")
	}
	r.checkBind(add)
	r.checkTLS(add)

	u, ok := r.publicURL()
	if !ok {
		add("public_url", "Dirección pública", StatusWarn, "Falta remote.public_url: sin ella no se pueden comprobar el DNS ni el certificado, ni lanzar la prueba desde fuera.")
	} else {
		add("public_url", "Dirección pública", StatusOK, "%s", r.Opts.PublicURL)
		r.checkDNS(ctx, u.Hostname(), add)
		r.checkSelf(ctx, add)
	}

	rep.Probe = r.ProbeStatus()
	switch rep.Probe.State {
	case ProbeReachable:
		add("outside", "Alcance desde fuera", StatusOK, "Una petición llegó desde %s, fuera de la red local.", rep.Probe.From)
	case ProbePending, ProbeLocalOnly:
		add("outside", "Alcance desde fuera", StatusWarn, "Prueba pendiente: abre la URL de la prueba desde otra red.")
	case ProbeExpired:
		add("outside", "Alcance desde fuera", StatusWarn, "La última prueba caducó sin que nadie la abriera desde fuera.")
	default:
		add("outside", "Alcance desde fuera", StatusWarn, "Sin comprobar: inicia la prueba y ábrela desde datos móviles u otra red.")
	}
	return rep, nil
}

type adder func(id, name, status, format string, args ...any)

func (r *Remote) publicURL() (*url.URL, bool) {
	if r.Opts.PublicURL == "" {
		return nil, false
	}
	u, err := url.Parse(r.Opts.PublicURL)
	return u, err == nil && u.Hostname() != ""
}

func (r *Remote) checkBind(add adder) {
	ip, err := netip.ParseAddr(r.Opts.BindAddress)
	if err == nil && ip.IsLoopback() {
		add("bind", "Dirección de escucha", StatusWarn,
			"El servidor solo escucha en %s: desde fuera solo se llega mediante un proxy en este mismo equipo.", ip)
		return
	}
	add("bind", "Dirección de escucha", StatusOK, "%s", r.Opts.BindAddress)
}

// checkTLS valida el certificado propio, o recuerda que el proxy es quien pone
// el TLS cuando no hay certificado.
func (r *Remote) checkTLS(add adder) {
	if !r.Opts.HTTPSEnabled {
		add("tls", "Certificado TLS", StatusOK, "El TLS lo termina el reverse proxy declarado en server.trusted_proxies; desde aquí no se puede comprobar su certificado salvo con la comprobación propia.")
		return
	}
	host := ""
	if u, ok := r.publicURL(); ok {
		host = u.Hostname()
	}
	c := CheckCertificate(r.Opts.TLSCertFile, host, r.env.now())
	add(c.ID, c.Name, c.Status, "%s", c.Detail)
}

// CheckCertificate valida un certificado en disco: que se lea, que esté en
// vigor, que cubra host (si se indica) y cuánto le queda.
func CheckCertificate(file, host string, now time.Time) Check {
	c := Check{ID: "tls", Name: "Certificado TLS"}
	fail := func(status, format string, args ...any) Check {
		c.Status, c.Detail = status, fmt.Sprintf(format, args...)
		return c
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fail(StatusFail, "No se puede leer el certificado: %v", err)
	}
	cert, err := firstCertificate(data)
	if err != nil {
		return fail(StatusFail, "El certificado no es válido: %v", err)
	}
	switch {
	case now.After(cert.NotAfter):
		return fail(StatusFail, "El certificado caducó el %s.", cert.NotAfter.Format("2006-01-02"))
	case now.Before(cert.NotBefore):
		return fail(StatusFail, "El certificado aún no es válido: empieza el %s.", cert.NotBefore.Format("2006-01-02"))
	}
	if host != "" {
		if err := cert.VerifyHostname(host); err != nil {
			return fail(StatusFail, "El certificado no cubre %s: %v", host, err)
		}
	}
	if left := cert.NotAfter.Sub(now); left < certWarnWindow {
		return fail(StatusWarn, "El certificado caduca en %d días (%s): renuévalo.", int(left.Hours()/24), cert.NotAfter.Format("2006-01-02"))
	}
	return fail(StatusOK, "Válido hasta el %s.", cert.NotAfter.Format("2006-01-02"))
}

func firstCertificate(data []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, errors.New("no hay ningún certificado en el archivo")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// checkDNS comprueba que el nombre resuelve y a qué tipo de dirección.
func (r *Remote) checkDNS(ctx context.Context, host string, add adder) {
	const id, name = "dns", "Resolución DNS"
	if ip, err := netip.ParseAddr(host); err == nil {
		add(id, name, StatusOK, "La dirección pública es una IP (%s); no hay nada que resolver.", ip)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, networkTimeout)
	defer cancel()
	addrs, err := r.env.lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		add(id, name, StatusFail, "%s no resuelve a ninguna dirección: %v", host, err)
		return
	}
	var public, private []string
	for _, a := range addrs {
		a = a.Unmap()
		if r.Net.localAddr(a) {
			private = append(private, a.String())
		} else {
			public = append(public, a.String())
		}
	}
	switch {
	case len(public) == 0:
		add(id, name, StatusWarn, "%s resuelve solo a direcciones privadas (%s): desde fuera no se llegará, salvo por una VPN.", host, strings.Join(private, ", "))
	case len(private) > 0:
		add(id, name, StatusWarn, "%s resuelve a direcciones públicas (%s) y privadas (%s): comprueba el DNS.", host, strings.Join(public, ", "), strings.Join(private, ", "))
	default:
		add(id, name, StatusOK, "%s resuelve a %s.", host, strings.Join(public, ", "))
	}
}

// checkSelf pide /health a la dirección pública, con verificación del
// certificado como lo haría un cliente. Desde dentro no demuestra que se llegue
// desde fuera (el router puede no reenviar la conexión propia), pero un
// certificado inválido o un proxy mal configurado sí se ven.
func (r *Remote) checkSelf(ctx context.Context, add adder) {
	const id, name = "self", "Conexión a la dirección pública"
	ctx, cancel := context.WithTimeout(ctx, networkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Opts.PublicURL+"/health", nil)
	if err != nil {
		add(id, name, StatusFail, "Dirección no válida: %v", err)
		return
	}
	resp, err := r.env.client.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			add(id, name, StatusFail, "El certificado que presenta %s no es de confianza: %v", r.Opts.PublicURL, certErr.Err)
			return
		}
		add(id, name, StatusWarn, "No se pudo conectar con %s desde este equipo: %v. Puede ser el router (conexión propia) y no un fallo real: usa la prueba desde fuera.", r.Opts.PublicURL, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		add(id, name, StatusFail, "%s respondió %d a /health: ¿el proxy apunta a este servidor?", r.Opts.PublicURL, resp.StatusCode)
		return
	}
	add(id, name, StatusOK, "%s responde y su certificado es de confianza.", r.Opts.PublicURL)
}
