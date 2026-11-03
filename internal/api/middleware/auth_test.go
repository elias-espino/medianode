package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"medianode/internal/auth"
	"medianode/internal/authorization"
	"medianode/internal/media"
)

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

func TestRequireCSRF(t *testing.T) {
	h := RequireCSRF(http.HandlerFunc(ok))
	const value = "valor"
	viaCookie := func(r *http.Request) *http.Request {
		return r.WithContext(contextWithCookie(r))
	}

	cases := []struct {
		name         string
		method       string
		cookieAuth   bool
		cookie, head string
		status       int
	}{
		{"cookie sin cabecera", http.MethodPost, true, value, "", http.StatusForbidden},
		{"cookie sin nada", http.MethodPost, true, "", "", http.StatusForbidden},
		{"valores distintos", http.MethodDelete, true, value, "otro", http.StatusForbidden},
		{"valores iguales", http.MethodPost, true, value, value, http.StatusNoContent},
		{"lectura no lo necesita", http.MethodGet, true, "", "", http.StatusNoContent},
		{"Bearer no lo necesita", http.MethodPost, false, "", "", http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/", nil)
			if c.cookie != "" {
				r.AddCookie(&http.Cookie{Name: CookieCSRF, Value: c.cookie})
			}
			if c.head != "" {
				r.Header.Set(HeaderCSRF, c.head)
			}
			if c.cookieAuth {
				r = viaCookie(r)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != c.status {
				t.Fatalf("estado = %d", rec.Code)
			}
		})
	}
}

func TestCSRFRechazaCookieYCabeceraVacias(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Cookie", CookieCSRF+"=")
	r = r.WithContext(contextWithCookie(r))
	rec := httptest.NewRecorder()
	RequireCSRF(http.HandlerFunc(ok)).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("estado = %d", rec.Code)
	}
}

func TestRequireRole(t *testing.T) {
	h := RequireRole(media.RoleAdmin)(http.HandlerFunc(ok))
	cases := []struct {
		role   media.Role
		status int
	}{
		{media.RoleOwner, http.StatusNoContent},
		{media.RoleAdmin, http.StatusNoContent},
		{media.RoleUser, http.StatusForbidden},
		{media.RoleRestricted, http.StatusForbidden},
		{media.Role("desconocido"), http.StatusForbidden},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(authorization.WithActor(r.Context(), authorization.Actor{Role: c.role}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.status {
			t.Fatalf("%s: estado = %d", c.role, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin actor: estado = %d", rec.Code)
	}
}

func TestRateLimitPorDireccion(t *testing.T) {
	h := RateLimit(auth.NewLimiter(1), NewNetwork(nil))(http.HandlerFunc(ok))
	send := func(ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = ip + ":1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if send("10.0.0.1").Code != http.StatusNoContent {
		t.Fatal("el primer intento debe pasar")
	}
	rec := send("10.0.0.1")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if send("10.0.0.2").Code != http.StatusNoContent {
		t.Fatal("cada dirección tiene su cupo")
	}
}

func TestCORSSoloOrigenesConfigurados(t *testing.T) {
	const allowed = "https://media.example.com"
	h := CORS([]string{allowed})(http.HandlerFunc(ok))

	send := func(method, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := send(http.MethodOptions, allowed)
	if rec.Header().Get("Access-Control-Allow-Origin") != allowed ||
		rec.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatalf("cabeceras = %v", rec.Header())
	}

	for _, origin := range []string{"https://malo.example.com", "http://media.example.com", "null"} {
		if got := send(http.MethodOptions, origin).Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%s: permitido %q", origin, got)
		}
	}
	if got := send(http.MethodGet, "https://malo.example.com").Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q", got)
	}
}

func TestClientIPNoCreeCabecerasDeOrigenesNoConfiables(t *testing.T) {
	n := NewNetwork([]string{"10.0.0.1", "192.168.0.0/24"})
	cases := []struct {
		name, remote, forwarded, want string
	}{
		{"sin proxy se ignora la cabecera", "203.0.113.9:1", "1.2.3.4", "203.0.113.9"},
		{"proxy de confianza", "10.0.0.1:1", "203.0.113.9", "203.0.113.9"},
		{"rango de confianza", "192.168.0.7:1", "203.0.113.9", "203.0.113.9"},
		{"el cliente no suplanta añadiendo a la izquierda", "10.0.0.1:1", "1.2.3.4, 203.0.113.9", "203.0.113.9"},
		{"varios proxies de confianza", "10.0.0.1:1", "203.0.113.9, 192.168.0.7", "203.0.113.9"},
		{"cabecera vacía", "10.0.0.1:1", "", "10.0.0.1"},
		{"entrada inválida se detiene", "10.0.0.1:1", "basura", "10.0.0.1"},
		{"IPv6", "[2001:db8::1]:1", "", "2001:db8::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remote
			if c.forwarded != "" {
				r.Header.Set(headerForwardedFor, c.forwarded)
			}
			if got := n.ClientIP(r); got != c.want {
				t.Fatalf("IP = %q", got)
			}
		})
	}
}

func TestSecureSoloDesdeProxyDeConfianza(t *testing.T) {
	n := NewNetwork([]string{"10.0.0.1"})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(headerForwardedProto, "https")

	r.RemoteAddr = "203.0.113.9:1"
	if n.Secure(r) {
		t.Fatal("la cabecera de un origen no confiable no cuenta")
	}
	r.RemoteAddr = "10.0.0.1:1"
	if !n.Secure(r) {
		t.Fatal("debe aceptar la cabecera del proxy")
	}
}
