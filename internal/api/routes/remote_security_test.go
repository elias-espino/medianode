package routes

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"medianode/internal/remote"
	"medianode/internal/testutil"
)

// Direcciones de prueba: una de la red local y una pública (TEST-NET-3).
const (
	lan     = "192.168.1.50:5000"
	outside = "203.0.113.9:5000"
	proxy   = "10.0.0.1:5000"
)

func envWith(t *testing.T, o remote.Options) *env {
	t.Helper()
	e := newEnvOn(t, testutil.NewDB(t), func(eo *envOptions) { eo.remote = &o })
	e.setupFrom(lan)
	return e
}

func (e *env) setupFrom(addr string) {
	e.t.Helper()
	rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/auth/setup", remote: addr, body: map[string]any{
		"code": e.code, "username": owner, "password": password,
	}})
	e.want(rec, http.StatusCreated)
}

func (e *env) loginFrom(addr string) string {
	e.t.Helper()
	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", remote: addr, body: credentials(owner, "android")})
	e.want(rec, http.StatusOK)
	return body["access_token"].(string)
}

func (e *env) getFrom(token, path, addr string, headers ...string) (int, map[string]any) {
	e.t.Helper()
	h := map[string]string{}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	rec, body := e.do(call{method: http.MethodGet, path: path, bearer: token, remote: addr, headers: h})
	return rec.Code, body
}

func TestSinAccesoRemotoSoloResponderLaRedLocal(t *testing.T) {
	e := envWith(t, remote.Options{})
	if code, _ := e.getFrom("", "/health", lan); code != 200 {
		t.Fatalf("la red local debe poder: %d", code)
	}
	for _, path := range []string{"/health", apiBase + "/server/info", apiBase + "/server/version", "/", apiBase + "/auth/me"} {
		code, body := e.getFrom("", path, outside)
		if code != http.StatusForbidden || errorCode(body) != "REMOTE_ACCESS_DISABLED" {
			t.Errorf("%s desde fuera: %d %v", path, code, body)
		}
	}
	// Ni siquiera una cuenta válida abre la puerta: el login desde fuera tampoco.
	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", remote: outside, body: credentials(owner, "android")})
	if rec.Code != http.StatusForbidden || errorCode(body) != "REMOTE_ACCESS_DISABLED" {
		t.Errorf("login desde fuera: %d %v", rec.Code, body)
	}
	// La cabecera de un proxy no declarado no hace local lo de fuera ni remoto lo de dentro.
	if code, _ := e.getFrom("", "/health", "127.0.0.1:1", "X-Forwarded-For", "192.168.1.9"); code != http.StatusForbidden {
		t.Errorf("X-Forwarded-For desde un origen que no es proxy de confianza: %d", code)
	}
	if code, _ := e.getFrom("", "/health", "127.0.0.1:1"); code != 200 {
		t.Errorf("la propia máquina: %d", code)
	}
}

func TestAdministracionBloqueadaDesdeFuera(t *testing.T) {
	e := envWith(t, remote.Options{Enabled: true, Direct: true})
	remoteToken, lanToken := e.loginFrom(outside), e.loginFrom(lan)

	// Lo normal funciona desde fuera con una cuenta válida.
	for _, path := range []string{"/auth/me", "/libraries", "/server/status", "/playback/continue-watching"} {
		if code, body := e.getFrom(remoteToken, apiBase+path, outside); code != 200 {
			t.Errorf("%s desde fuera: %d %v", path, code, body)
		}
	}
	// La administración, no, aunque el token sea del propietario.
	admin := []string{"/admin/system", "/admin/metrics", "/admin/sessions", "/admin/network", "/admin/storage", "/admin/logs",
		"/admin/settings", "/admin/system-check", "/admin/transcodes", "/admin/users", "/admin/remote"}
	for _, path := range admin {
		code, body := e.getFrom(remoteToken, apiBase+path, outside)
		if code != http.StatusForbidden || errorCode(body) != "REMOTE_ADMIN_DISABLED" {
			t.Errorf("%s desde fuera: %d %v", path, code, body)
		}
		if code, body := e.getFrom(lanToken, apiBase+path, lan); code != 200 {
			t.Errorf("%s desde la red local: %d %v", path, code, body)
		}
	}
	// Sin sesión no se averigua nada: es un 401, no un 403 que confirme la ruta.
	if code, body := e.getFrom("", apiBase+"/admin/users", outside); code != http.StatusUnauthorized || errorCode(body) != "AUTH_REQUIRED" {
		t.Errorf("administración sin sesión: %d %v", code, body)
	}
	// Decidir qué carpetas se publican también es administración.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/libraries"}, {http.MethodPatch, "/libraries/x"}, {http.MethodDelete, "/libraries/x"},
		{http.MethodPost, "/libraries/x/paths"}, {http.MethodDelete, "/libraries/x/paths/y"},
		{http.MethodPost, "/admin/users"}, {http.MethodPost, "/admin/remote/probe"},
		{http.MethodPost, "/admin/transcodes/x/cancel"}, {http.MethodPut, "/admin/users/x/permissions"},
	} {
		rec, body := e.do(call{method: c.method, path: apiBase + c.path, bearer: remoteToken, remote: outside, body: map[string]any{}})
		if rec.Code != http.StatusForbidden || errorCode(body) != "REMOTE_ADMIN_DISABLED" {
			t.Errorf("%s %s desde fuera: %d %v", c.method, c.path, rec.Code, body)
		}
	}
	// El panel avisa de si hay administración disponible.
	if _, body := e.getFrom("", apiBase+"/server/info", outside); body["admin_available"] != false {
		t.Errorf("admin_available desde fuera: %v", body)
	}
	if _, body := e.getFrom("", apiBase+"/server/info", lan); body["admin_available"] != true {
		t.Errorf("admin_available desde la red local: %v", body)
	}
}

func TestAdministracionRemotaSoloSiSePermite(t *testing.T) {
	e := envWith(t, remote.Options{Enabled: true, Direct: true, AdminEnabled: true})
	token := e.loginFrom(outside)
	if code, body := e.getFrom(token, apiBase+"/admin/system", outside); code != 200 {
		t.Fatalf("con admin_enabled: %d %v", code, body)
	}
	// Un usuario normal sigue sin poder: el rol se comprueba igual.
	rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/admin/users", bearer: token, remote: outside, body: map[string]any{
		"username": "normal", "password": password, "role": "user",
	}})
	e.want(rec, http.StatusCreated)
	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", remote: outside, body: map[string]any{
		"username": "normal", "password": password, "device_name": "x", "platform": "android",
	}})
	e.want(rec, http.StatusOK)
	if code, body := e.getFrom(body["access_token"].(string), apiBase+"/admin/system", outside); code != http.StatusForbidden || errorCode(body) != "FORBIDDEN" {
		t.Errorf("usuario normal: %d %v", code, body)
	}
}

func TestProxyDeConfianzaClasificaAlClienteReal(t *testing.T) {
	e := envWith(t, remote.Options{Enabled: true, Direct: true, TrustedProxies: []string{"10.0.0.1"}})
	token := e.loginFrom(lan)
	// El cliente de fuera, a través del proxy, no administra.
	if code, body := e.getFrom(token, apiBase+"/admin/system", proxy, "X-Forwarded-For", "203.0.113.9"); code != http.StatusForbidden || errorCode(body) != "REMOTE_ADMIN_DISABLED" {
		t.Errorf("cliente de fuera por el proxy: %d %v", code, body)
	}
	// El de la red local, a través del proxy, sí.
	if code, body := e.getFrom(token, apiBase+"/admin/system", proxy, "X-Forwarded-For", "192.168.1.20"); code != 200 {
		t.Errorf("cliente local por el proxy: %d %v", code, body)
	}
	// Un cliente que añade una IP local a su cabecera no cambia lo que ve el proxy.
	if code, body := e.getFrom(token, apiBase+"/admin/system", proxy, "X-Forwarded-For", "192.168.1.20, 203.0.113.9"); code != http.StatusForbidden {
		t.Errorf("suplantación por la izquierda: %d %v", code, body)
	}
	// Sin cabecera, el proxy es la única pista: ante la duda, remoto.
	if code, _ := e.getFrom(token, apiBase+"/admin/system", proxy); code != http.StatusForbidden {
		t.Errorf("proxy sin X-Forwarded-For: %d", code)
	}
	// Quien no es proxy de confianza no puede decir de dónde viene.
	if code, _ := e.getFrom(token, apiBase+"/admin/system", "192.168.1.77:1", "X-Forwarded-For", "192.168.1.20"); code != http.StatusForbidden {
		t.Errorf("origen que no es proxy con X-Forwarded-For: %d", code)
	}
}

func TestFuerzaBrutaContraElLoginRemoto(t *testing.T) {
	e := envWith(t, remote.Options{Enabled: true, Direct: true})
	bad := func(addr, user string) (int, string) {
		rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", remote: addr, body: map[string]any{
			"username": user, "password": "incorrecta-" + user, "device_name": "x", "platform": "android",
		}})
		return rec.Code, errorCode(body)
	}

	// Desde una sola dirección, contra cuentas distintas: el límite por IP corta.
	var limited bool
	for i := range 40 {
		if code, c := bad("203.0.113.50:1", "usuario"+strconv.Itoa(i)); code == http.StatusTooManyRequests {
			if c != "RATE_LIMITED" {
				t.Fatalf("código = %q", c)
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("40 intentos desde una IP no activaron el límite")
	}

	// Desde muchas direcciones contra la misma cuenta: el límite por cuenta corta
	// aunque ninguna IP supere el suyo, y la contraseña buena tampoco pasa mientras dure.
	var hit bool
	for i := range 30 {
		if code, _ := bad("198.51.100."+strconv.Itoa(i+1)+":1", owner); code == http.StatusTooManyRequests {
			hit = true
			break
		}
	}
	if !hit {
		t.Fatal("el límite por cuenta no actuó con IP distintas")
	}
	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", remote: "198.51.100.99:1", body: credentials(owner, "android")})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("con la cuenta bloqueada la contraseña buena no debe pasar: %d %v", rec.Code, body)
	}
}

func TestCORSRestrictivoDesdeFuera(t *testing.T) {
	e := envWith(t, remote.Options{Enabled: true, Direct: true})
	req := func(method, origin string, extra map[string]string) (int, http.Header) {
		h := map[string]string{"Origin": origin}
		for k, v := range extra {
			h[k] = v
		}
		rec, _ := e.do(call{method: method, path: apiBase + "/server/info", remote: outside, headers: h})
		return rec.Code, rec.Header()
	}
	// Ningún origen ajeno recibe permiso, ni «*», ni el eco del origen, ni credenciales.
	for _, origin := range []string{"https://evil.example", "http://evil.example", "null", "https://media.example.com.evil.example", "*"} {
		_, h := req(http.MethodGet, origin, nil)
		for _, k := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Methods"} {
			if v := h.Get(k); v != "" {
				t.Errorf("origen %q: %s = %q", origin, k, v)
			}
		}
		// El preflight de un origen ajeno tampoco se concede.
		code, h := req(http.MethodOptions, origin, map[string]string{"Access-Control-Request-Method": "DELETE"})
		if h.Get("Access-Control-Allow-Origin") != "" || code == http.StatusNoContent {
			t.Errorf("preflight de %q: %d %v", origin, code, h)
		}
		if !strings.Contains(strings.Join(h.Values("Vary"), ","), "Origin") {
			t.Errorf("falta Vary: Origin con %q", origin)
		}
	}
}

func TestCORSSoloConcedeLosOrigenesConfigurados(t *testing.T) {
	cfgOrigins := []string{"https://media.example.com"}
	e := newEnvOn(t, testutil.NewDB(t), func(eo *envOptions) {
		eo.remote = &remote.Options{Enabled: true, Direct: true}
		eo.origins = cfgOrigins
	})
	do := func(origin string) http.Header {
		rec, _ := e.do(call{method: http.MethodGet, path: apiBase + "/server/info", remote: outside, headers: map[string]string{"Origin": origin}})
		return rec.Header()
	}
	if got := do("https://media.example.com").Get("Access-Control-Allow-Origin"); got != "https://media.example.com" {
		t.Errorf("origen configurado: %q", got)
	}
	for _, origin := range []string{"https://media.example.com.evil.example", "http://media.example.com", "https://MEDIA.example.com", "https://media.example.com:8443", "https://evil.example"} {
		if got := do(origin).Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origen %q recibió %q", origin, got)
		}
	}
}

func TestSondeoDeAlcanceDesdeFuera(t *testing.T) {
	e := envWith(t, remote.Options{Enabled: true, Direct: true, PublicURL: "https://media.example.com"})
	token := e.loginFrom(lan)

	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/admin/remote/probe", bearer: token, remote: lan})
	e.want(rec, http.StatusOK)
	url, _ := body["url"].(string)
	if !strings.HasPrefix(url, "https://media.example.com/api/v1/remote/probe/") {
		t.Fatalf("url = %q", url)
	}
	path := strings.TrimPrefix(url, "https://media.example.com")

	// Sin sesión y desde fuera: es justo lo que se quiere probar.
	r2, _ := e.do(call{method: http.MethodGet, path: path, remote: outside})
	e.want(r2, http.StatusOK)
	if !strings.Contains(r2.Body.String(), "accesible desde fuera") {
		t.Fatalf("respuesta: %s", r2.Body)
	}
	if rec, rep := e.do(call{method: http.MethodGet, path: apiBase + "/admin/remote", bearer: token, remote: lan}); rec.Code != 200 || rep["probe"].(map[string]any)["state"] != "reachable" {
		t.Fatalf("estado de la prueba: %d %v", rec.Code, rep)
	}
	// Un código inventado es un 404, igual que uno caducado o ya usado.
	if r3, _ := e.do(call{method: http.MethodGet, path: apiBase + "/remote/probe/inventado", remote: outside}); r3.Code != http.StatusNotFound {
		t.Fatalf("código inventado: %d", r3.Code)
	}
	if r4, _ := e.do(call{method: http.MethodGet, path: path, remote: outside}); r4.Code != http.StatusNotFound {
		t.Fatalf("código reutilizado: %d", r4.Code)
	}
}

func TestSondeoNoSeAbreConElAccesoRemotoDesactivado(t *testing.T) {
	e := envWith(t, remote.Options{PublicURL: "https://media.example.com"})
	token := e.loginFrom(lan)
	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/admin/remote/probe", bearer: token, remote: lan})
	if rec.Code != http.StatusBadRequest || errorCode(body) != "VALIDATION_ERROR" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	// Y desde fuera la ruta pública no existe: la puerta de acceso la cierra antes.
	if rec, body := e.do(call{method: http.MethodGet, path: apiBase + "/remote/probe/x", remote: outside}); rec.Code != http.StatusForbidden || errorCode(body) != "REMOTE_ACCESS_DISABLED" {
		t.Fatalf("%d %v", rec.Code, body)
	}
}
