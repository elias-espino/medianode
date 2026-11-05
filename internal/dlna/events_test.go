package dlna

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"medianode/internal/testutil"
)

// receiver es un control point: un servidor HTTP que apunta lo que le notifican.
type receiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []notification
	code int
}

type notification struct {
	method, nt, nts, sid, seq, body string
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{code: 200}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, notification{req.Method, req.Header.Get("NT"), req.Header.Get("NTS"), req.Header.Get("SID"), req.Header.Get("SEQ"), string(body)})
		code := r.code
		r.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) callback() string { return "<" + r.srv.URL + "/events>" }

func (r *receiver) notifications() []notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notification(nil), r.got...)
}

func (r *receiver) wait(t *testing.T, n int) []notification {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.notifications(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("se esperaban %d avisos y llegaron %d", n, len(r.notifications()))
	return nil
}

const selfAddr = "127.0.0.1:50000"

func subscribeReq(h http.Handler, svc string, headers ...string) *httptest.ResponseRecorder {
	return doReq(h, "SUBSCRIBE", "/dlna/"+svc+"/event", "", selfAddr, headers...)
}

func TestSuscripcionYEstadoInicial(t *testing.T) {
	s, h := newServer(t, "x")
	rc := newReceiver(t)
	w := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event", "TIMEOUT", "Second-600")
	sid := w.Header().Get("SID")
	if w.Code != 200 || !strings.HasPrefix(sid, "uuid:") || w.Header().Get("TIMEOUT") != "Second-600" || w.Header().Get("Content-Length") != "0" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	got := rc.wait(t, 1)[0]
	if got.method != "NOTIFY" || got.nt != "upnp:event" || got.nts != "upnp:propchange" || got.sid != sid || got.seq != "0" ||
		!strings.Contains(got.body, "<e:propertyset") || !strings.Contains(got.body, "<SystemUpdateID>") {
		t.Fatalf("estado inicial: %+v", got)
	}
	_ = s

	// ConnectionManager entrega las suyas.
	rc2 := newReceiver(t)
	if w := subscribeReq(h, "cms", "CALLBACK", rc2.callback(), "NT", "upnp:event"); w.Code != 200 {
		t.Fatalf("cms: %d", w.Code)
	}
	b := rc2.wait(t, 1)[0].body
	for _, want := range []string{"<SourceProtocolInfo>http-get:*:video/mp4:*", "<SinkProtocolInfo></SinkProtocolInfo>", "<CurrentConnectionIDs>0</CurrentConnectionIDs>"} {
		if !strings.Contains(b, want) {
			t.Errorf("falta %q en %s", want, b)
		}
	}
}

func TestElTiempoDeLaSuscripcionSeAcota(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"Second-600": 600 * time.Second, "Second-10": minSubTimeout, "Second-999999": maxSubTimeout, "infinite": maxSubTimeout,
		"": maxSubTimeout, "Second-0": maxSubTimeout, "Second--5": maxSubTimeout, "Second-abc": maxSubTimeout, "  Second-400 ": 400 * time.Second,
	} {
		if got := parseTimeout(in); got != want {
			t.Errorf("parseTimeout(%q) = %v, se esperaba %v", in, got, want)
		}
	}
}

func TestSoloSeAceptaUnCallbackHaciaQuienSuscribe(t *testing.T) {
	_, h := newServer(t, "x")
	from := netip.MustParseAddr("127.0.0.1")
	cases := []struct {
		name, header string
		want         int
	}{
		{"la propia dirección", "<http://127.0.0.1:8080/e>", 1},
		{"con IPv6 mapeada", "<http://[::ffff:127.0.0.1]:8080/e>", 1},
		{"varios, en orden", "<http://127.0.0.1:1/a> <http://127.0.0.1:2/b>", 2},
		{"otra dirección de la red local", "<http://192.168.1.99:8080/e>", 0},
		{"la nube de metadatos", "<http://169.254.169.254/latest/meta-data>", 0},
		{"un nombre", "<http://localhost:8080/e>", 0},
		{"https", "<https://127.0.0.1:8080/e>", 0},
		{"con credenciales", "<http://user:pw@127.0.0.1:8080/e>", 0},
		{"con fragmento", "<http://127.0.0.1:8080/e#x>", 0},
		{"puerto fuera de rango", "<http://127.0.0.1:99999/e>", 0},
		{"puerto cero", "<http://127.0.0.1:0/e>", 0},
		{"otro esquema", "<ftp://127.0.0.1/e>", 0},
		{"sin corchetes", "http://127.0.0.1:8080/e", 0},
		{"sin cerrar", "<http://127.0.0.1:8080/e", 0},
		{"demasiado largo", "<http://127.0.0.1:8080/" + strings.Repeat("a", maxCallbackURL) + ">", 0},
		{"uno bueno tras uno malo", "<http://10.0.0.1/e><http://127.0.0.1:8080/e>", 1},
		{"vacío", "", 0},
		{"cinco: solo se guardan " + fmt.Sprint(maxCallbacks), strings.Repeat("<http://127.0.0.1:8080/e>", 5), maxCallbacks},
	}
	for _, c := range cases {
		if got := parseCallbacks(c.header, from); len(got) != c.want {
			t.Errorf("%s: %d callbacks, se esperaban %d (%v)", c.name, len(got), c.want, got)
		}
	}
	// Y por HTTP: un callback hacia otro equipo es un 412, y no se conecta a nada.
	w := subscribeReq(h, "cds", "CALLBACK", "<http://192.168.1.99:8080/e>", "NT", "upnp:event")
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("%d", w.Code)
	}
}

func TestLasSuscripcionesRechazanLoMalFormado(t *testing.T) {
	_, h := newServer(t, "x")
	rc := newReceiver(t)
	for name, c := range map[string]struct {
		hdr  []string
		want int
	}{
		"sin nada":                  {nil, 412},
		"sin NT":                    {[]string{"CALLBACK", rc.callback()}, 412},
		"NT equivocado":             {[]string{"CALLBACK", rc.callback(), "NT", "upnp:otra"}, 412},
		"sin CALLBACK":              {[]string{"NT", "upnp:event"}, 412},
		"SID con CALLBACK":          {[]string{"SID", "uuid:x", "CALLBACK", rc.callback()}, 400},
		"SID con NT":                {[]string{"SID", "uuid:x", "NT", "upnp:event"}, 400},
		"renovar una que no existe": {[]string{"SID", "uuid:no-existe"}, 412},
	} {
		if w := subscribeReq(h, "cds", c.hdr...); w.Code != c.want {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if len(rc.notifications()) != 0 {
		t.Fatal("una suscripción rechazada no debe generar avisos")
	}
}

func TestRenovarYCancelar(t *testing.T) {
	s, h := newServer(t, "x")
	clock := &testClock{t: time.Now()}
	s.hub.now = clock.now
	rc := newReceiver(t)
	sid := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event", "TIMEOUT", "Second-300").Header().Get("SID")
	rc.wait(t, 1)

	// Renovar antes de que caduque la alarga.
	clock.add(200 * time.Second)
	w := subscribeReq(h, "cds", "SID", sid, "TIMEOUT", "Second-900")
	if w.Code != 200 || w.Header().Get("SID") != sid || w.Header().Get("TIMEOUT") != "Second-900" {
		t.Fatalf("renovar: %d %v", w.Code, w.Header())
	}
	clock.add(400 * time.Second) // Habría caducado sin la renovación.
	if w := subscribeReq(h, "cds", "SID", sid); w.Code != 200 {
		t.Fatalf("seguía viva: %d", w.Code)
	}
	// Otro servicio no puede renovarla.
	if w := subscribeReq(h, "cms", "SID", sid); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("renovar por otro servicio: %d", w.Code)
	}
	// Caducada, no se renueva ni se notifica.
	clock.add(2000 * time.Second)
	if w := subscribeReq(h, "cds", "SID", sid); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("renovar una caducada: %d", w.Code)
	}

	// Cancelar.
	sid2 := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event").Header().Get("SID")
	if w := doReq(h, "UNSUBSCRIBE", "/dlna/cds/event", "", selfAddr, "SID", sid2); w.Code != 200 {
		t.Fatalf("UNSUBSCRIBE: %d", w.Code)
	}
	if w := doReq(h, "UNSUBSCRIBE", "/dlna/cds/event", "", selfAddr, "SID", sid2); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("cancelar dos veces: %d", w.Code)
	}
	if w := doReq(h, "UNSUBSCRIBE", "/dlna/cds/event", "", selfAddr); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("sin SID: %d", w.Code)
	}
}

func TestUnCambioDelCatalogoSeAvisaConSuSecuencia(t *testing.T) {
	s, h := newServer(t, "x")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Write.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO libraries (id, name, type, dlna_enabled, updated_at) VALUES ('a', 'Cine', 'movies', 1, '2026-01-01T00:00:00.000Z')")
	ctx := context.Background()
	s.pollEvents(ctx) // Valor de partida.
	rc := newReceiver(t)
	subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event")
	rc.wait(t, 1)

	// Sin cambios no hay avisos.
	s.pollEvents(ctx)
	time.Sleep(50 * time.Millisecond)
	if n := len(rc.notifications()); n != 1 {
		t.Fatalf("sin cambios: %d avisos", n)
	}
	// Un cambio: un aviso con SEQ 1 y el nuevo identificador.
	exec("UPDATE libraries SET updated_at = '2026-06-01T00:00:00.000Z' WHERE id = 'a'")
	s.pollEvents(ctx)
	got := rc.wait(t, 2)[1]
	want := fmt.Sprint(s.updateID(ctx))
	if got.seq != "1" || !strings.Contains(got.body, "<SystemUpdateID>"+want+"</SystemUpdateID>") {
		t.Fatalf("%+v (se esperaba SystemUpdateID %s)", got, want)
	}
	// Otro cambio: SEQ 2. Y los de ConnectionManager no reciben nada.
	rc2 := newReceiver(t)
	subscribeReq(h, "cms", "CALLBACK", rc2.callback(), "NT", "upnp:event")
	rc2.wait(t, 1)
	exec("UPDATE libraries SET updated_at = '2027-01-01T00:00:00.000Z' WHERE id = 'a'")
	s.pollEvents(ctx)
	if got := rc.wait(t, 3)[2]; got.seq != "2" {
		t.Fatalf("segundo cambio: %+v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(rc2.notifications()); n != 1 {
		t.Fatalf("ConnectionManager no cambia: %d avisos", n)
	}
}

func TestLaSecuenciaDaLaVueltaA1(t *testing.T) {
	s, h := newServer(t, "x")
	rc := newReceiver(t)
	sid := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event").Header().Get("SID")
	rc.wait(t, 1)
	s.hub.mu.Lock()
	sub := s.hub.subs[sid]
	sub.seq = ^uint32(0)
	s.hub.mu.Unlock()
	s.deliver(context.Background(), sub, []pair{{"SystemUpdateID", "1"}})
	s.deliver(context.Background(), sub, []pair{{"SystemUpdateID", "2"}})
	got := rc.wait(t, 3)
	if got[1].seq != "4294967295" || got[2].seq != "1" {
		t.Fatalf("%s → %s", got[1].seq, got[2].seq)
	}
}

func TestUnCallbackQueNoRespondeCuestaLaSuscripcion(t *testing.T) {
	s, h := newServer(t, "x")
	rc := newReceiver(t)
	sid := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event").Header().Get("SID")
	rc.wait(t, 1)
	rc.mu.Lock()
	rc.code = 500
	rc.mu.Unlock()
	s.hub.mu.Lock()
	sub := s.hub.subs[sid]
	s.hub.mu.Unlock()
	for i := range maxCallbackFails {
		s.deliver(context.Background(), sub, []pair{{"SystemUpdateID", "1"}})
		s.hub.mu.Lock()
		_, alive := s.hub.subs[sid]
		s.hub.mu.Unlock()
		if alive == (i == maxCallbackFails-1) {
			t.Fatalf("tras %d fallos, viva = %v", i+1, alive)
		}
	}
	// Un acierto reinicia la cuenta de fallos.
	rc2 := newReceiver(t)
	sid2 := subscribeReq(h, "cds", "CALLBACK", rc2.callback(), "NT", "upnp:event").Header().Get("SID")
	rc2.wait(t, 1)
	s.hub.mu.Lock()
	sub2 := s.hub.subs[sid2]
	s.hub.mu.Unlock()
	rc2.mu.Lock()
	rc2.code = 503
	rc2.mu.Unlock()
	s.deliver(context.Background(), sub2, nil)
	s.deliver(context.Background(), sub2, nil)
	rc2.mu.Lock()
	rc2.code = 200
	rc2.mu.Unlock()
	s.deliver(context.Background(), sub2, nil)
	rc2.mu.Lock()
	rc2.code = 503
	rc2.mu.Unlock()
	s.deliver(context.Background(), sub2, nil)
	s.deliver(context.Background(), sub2, nil)
	s.hub.mu.Lock()
	_, alive := s.hub.subs[sid2]
	s.hub.mu.Unlock()
	if !alive {
		t.Fatal("un acierto debe reiniciar los fallos seguidos")
	}
}

func TestElCallbackNoSigueRedirecciones(t *testing.T) {
	s, h := newServer(t, "x")
	hit := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- r.URL.Path }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/interno", http.StatusFound)
	}))
	defer redirector.Close()
	w := subscribeReq(h, "cds", "CALLBACK", "<"+redirector.URL+"/e>", "NT", "upnp:event")
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case p := <-hit:
		t.Fatalf("se siguió una redirección hacia %s", p)
	default:
	}
	_ = s
}

func TestLimitesDeSuscripciones(t *testing.T) {
	s, h := newServer(t, "x")
	rc := newReceiver(t)
	for i := range maxPerClient {
		if w := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event"); w.Code != 200 {
			t.Fatalf("suscripción %d: %d", i, w.Code)
		}
	}
	if w := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("pasado el límite por cliente: %d", w.Code)
	}
	// Las caducadas dejan sitio.
	clock := &testClock{t: time.Now().Add(2 * maxSubTimeout)}
	s.hub.now = clock.now
	if w := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event"); w.Code != 200 {
		t.Fatalf("tras caducar las anteriores: %d", w.Code)
	}
	// El límite global.
	s.hub.mu.Lock()
	s.hub.subs = map[string]*subscription{}
	for i := range maxSubscriptions {
		sid := fmt.Sprintf("uuid:%d", i)
		s.hub.subs[sid] = &subscription{sid: sid, service: "cds", client: netip.MustParseAddr(fmt.Sprintf("10.0.%d.%d", i/200, i%200+1)), expires: clock.now().Add(time.Hour)}
	}
	s.hub.mu.Unlock()
	if w := subscribeReq(h, "cds", "CALLBACK", rc.callback(), "NT", "upnp:event"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("pasado el límite global: %d", w.Code)
	}
}

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

func TestElServidorDejaDeAvisarAlParar(t *testing.T) {
	db := testutil.NewDB(t)
	_ = db
	s, _ := newServer(t, "x")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.eventLoop(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("el bucle de eventos no terminó al cancelar")
	}
}
