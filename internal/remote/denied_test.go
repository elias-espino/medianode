package remote

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func loggedRemote(opts Options) (*Remote, *bytes.Buffer, *fakeClock) {
	var buf bytes.Buffer
	clock := newClock()
	r := New(opts, zerolog.New(&buf)).withClock(clock)
	return r, &buf, clock
}

func lines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestUnaConexionRechazadaSeAvisaUnaVezPorMinutoYOrigen(t *testing.T) {
	r, buf, clock := loggedRemote(Options{})
	h := r.Gate(http.HandlerFunc(ok200))
	hit := func(addr string) int { return do(h, req(addr)).Code }

	for range 5 {
		if hit("203.0.113.9:1") != 403 {
			t.Fatal("debe rechazarse")
		}
	}
	if got := lines(buf); len(got) != 1 || !strings.Contains(got[0], "203.0.113.9") || !strings.Contains(got[0], "conexión remota rechazada") ||
		!strings.Contains(got[0], "trusted_proxies") || strings.Contains(got[0], "reenviada") {
		t.Fatalf("un aviso por origen: %v", got)
	}
	// Otro origen tiene el suyo.
	hit("198.51.100.7:1")
	if got := lines(buf); len(got) != 2 {
		t.Fatalf("segundo origen: %v", got)
	}
	// Pasado el minuto, vuelve a avisar.
	clock.add(denyLogEvery - time.Second)
	hit("203.0.113.9:1")
	if len(lines(buf)) != 2 {
		t.Fatal("avisó antes del minuto")
	}
	clock.add(time.Second)
	hit("203.0.113.9:1")
	if len(lines(buf)) != 3 {
		t.Fatalf("no volvió a avisar al cumplirse el minuto: %v", lines(buf))
	}
	// Lo local no se rechaza ni se avisa.
	before := len(lines(buf))
	if hit("192.168.1.5:1") != 200 || len(lines(buf)) != before {
		t.Fatal("la red local no genera avisos")
	}
}

func TestElAvisoDistingueLasCabecerasDeReenvio(t *testing.T) {
	r, buf, _ := loggedRemote(Options{})
	h := r.Gate(http.HandlerFunc(ok200))
	// Un proxy sin declarar: pasa por local, pero trae X-Forwarded-For.
	if do(h, req("192.168.1.9:1", HeaderForwardedFor, "203.0.113.9")).Code != 403 {
		t.Fatal("debe rechazarse")
	}
	got := lines(buf)
	if len(got) != 1 || !strings.Contains(got[0], `"reenviada":true`) || !strings.Contains(got[0], "192.168.1.9") {
		t.Fatalf("%v", got)
	}
}

func TestElRegistroDeRechazosNoCreceSinLimite(t *testing.T) {
	r, buf, _ := loggedRemote(Options{})
	for i := range maxDenied * 3 {
		w := do(r.Gate(http.HandlerFunc(ok200)), req("203.0.113.9:1"))
		_ = w
		r.noteDenied(req("10.9." + itoa(i/250) + "." + itoa(i%250) + ":1"))
	}
	r.deny.mu.Lock()
	n := len(r.deny.last)
	r.deny.mu.Unlock()
	if n > maxDenied {
		t.Fatalf("el mapa de orígenes creció hasta %d", n)
	}
	if len(lines(buf)) == 0 {
		t.Fatal("no se avisó de nada")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
