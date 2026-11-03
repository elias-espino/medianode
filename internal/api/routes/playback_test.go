package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/api/middleware"
	mnws "medianode/internal/api/websocket"
	"medianode/internal/authorization"
	"medianode/internal/events"
	"medianode/internal/sessions"
)

type account struct {
	token, userID, deviceID string
}

func (e *env) account(username string) account {
	e.t.Helper()
	body := e.login(username)
	return account{
		token: body["access_token"].(string), userID: body["user"].(map[string]any)["id"].(string),
		deviceID: body["device_id"].(string),
	}
}

// firstMovie escanea una biblioteca con una película y devuelve su elemento y
// su archivo.
func (e *env) firstMovie(token string) (lib, item, file string) {
	e.t.Helper()
	lib = e.scanLibrary(token, "movies", "Película (2001).mkv")
	_, page := e.do(call{method: http.MethodGet, path: apiBase + "/libraries/" + lib + "/items", bearer: token})
	item = page["items"].([]any)[0].(map[string]any)["id"].(string)
	rec, _ := e.do(call{method: http.MethodGet, path: apiBase + "/media/" + item + "/files", bearer: token})
	file = decodeList(e.t, rec.Body.Bytes())[0]["id"].(string)
	return lib, item, file
}

func TestReproduccionPorAPI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	_, item, file := e.firstMovie(own.token)

	// POST /playback/sessions llega en la Fase 7: la sesión se crea a mano.
	s, err := e.sessions.Create(context.Background(), sessions.CreateParams{
		UserID: own.userID, DeviceID: own.deviceID, ItemID: item, FileID: file, Mode: "direct_play",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := apiBase + "/playback/sessions/" + s.ID
	rec, body := e.do(call{method: http.MethodGet, path: path, bearer: own.token})
	e.want(rec, http.StatusOK)
	if body["status"] != "initializing" || body["idle_timeout_seconds"] != float64(90) {
		t.Fatalf("%v", body)
	}
	rec, body = e.do(call{method: http.MethodPatch, path: path, bearer: own.token, body: map[string]any{"state": "playing", "position_ms": 1500}})
	e.want(rec, http.StatusOK)
	if body["status"] != "playing" || body["position_ms"] != float64(1500) {
		t.Fatalf("%v", body)
	}
	rec, body = e.do(call{method: http.MethodPatch, path: path, bearer: own.token, body: map[string]any{"state": "stopped", "position_ms": 1}})
	if rec.Code != http.StatusBadRequest || errorCode(body) != "VALIDATION_ERROR" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	rec, _ = e.do(call{method: http.MethodPatch, path: path, bearer: own.token, body: map[string]any{"state": "paused"}})
	e.want(rec, http.StatusBadRequest)

	// Otro usuario no la ve ni la controla.
	e.createUser(own.token, "otro", "user", nil)
	other := e.account("otro")
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		rec, _ = e.do(call{method: m, path: path, bearer: other.token})
		e.want(rec, http.StatusNotFound)
	}

	rec, _ = e.do(call{method: http.MethodDelete, path: path, bearer: own.token})
	e.want(rec, http.StatusNoContent)
	rec, body = e.do(call{method: http.MethodPatch, path: path, bearer: own.token, body: map[string]any{"state": "playing", "position_ms": 2000}})
	if rec.Code != http.StatusGone || errorCode(body) != "SESSION_EXPIRED" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	rec, body = e.do(call{method: http.MethodGet, path: path, bearer: own.token})
	if rec.Code != http.StatusOK || body["status"] != "stopped" || body["position_ms"] != float64(1500) || body["ended_at"] == nil {
		t.Fatalf("una sesión terminada se consulta para retomar: %v", body)
	}
	rec, _ = e.do(call{method: http.MethodDelete, path: path, bearer: own.token})
	e.want(rec, http.StatusNoContent)

	// Progreso, continuar viendo e historial.
	rec, body = e.do(call{method: http.MethodPut, path: apiBase + "/playback/progress", bearer: own.token,
		body: map[string]any{"media_item_id": item, "position_ms": 900, "client_time": "2020-01-01T00:00:00Z"}})
	if rec.Code != http.StatusOK || body["position_ms"] != float64(1500) {
		t.Fatalf("un dato de 2020 no sustituye al de la sesión: %d %v", rec.Code, body)
	}
	rec, body = e.do(call{method: http.MethodPut, path: apiBase + "/playback/progress", bearer: own.token,
		body: map[string]any{"media_item_id": item, "position_ms": 3000}})
	if rec.Code != http.StatusOK || body["position_ms"] != float64(3000) {
		t.Fatalf("%d %v", rec.Code, body)
	}
	rec, body = e.do(call{method: http.MethodPut, path: apiBase + "/playback/progress", bearer: own.token,
		body: map[string]any{"media_item_id": item, "position_ms": 1, "client_time": "ayer"}})
	if rec.Code != http.StatusBadRequest || errorCode(body) != "VALIDATION_ERROR" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	rec, _ = e.do(call{method: http.MethodPut, path: apiBase + "/playback/progress", bearer: other.token,
		body: map[string]any{"media_item_id": item, "position_ms": 1}})
	e.want(rec, http.StatusNotFound)

	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/playback/continue-watching", bearer: own.token})
	e.want(rec, http.StatusOK)
	if list := decodeList(t, rec.Body.Bytes()); len(list) != 1 || list[0]["id"] != item || list[0]["position_ms"] != float64(3000) {
		t.Fatalf("%s", rec.Body)
	}
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/playback/continue-watching", bearer: other.token})
	if list := decodeList(t, rec.Body.Bytes()); len(list) != 0 {
		t.Fatalf("%s", rec.Body)
	}
	rec, body = e.do(call{method: http.MethodGet, path: apiBase + "/playback/history", bearer: own.token})
	e.want(rec, http.StatusOK)
	if items := body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["title"] != "Película" || body["next_cursor"] != nil {
		t.Fatalf("%v", body)
	}
}

// Eliminar la biblioteca o revocar el dispositivo cierra sus sesiones (§6.3).
func TestBorradosCierranSesiones(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	lib, item, file := e.firstMovie(own.token)
	create := func(a account) string {
		s, err := e.sessions.Create(context.Background(), sessions.CreateParams{
			UserID: a.userID, DeviceID: a.deviceID, ItemID: item, FileID: file, Mode: "direct_play",
		})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	tv := e.account(owner) // Otro dispositivo del mismo usuario.
	create(tv)
	rec, _ := e.do(call{method: http.MethodDelete, path: apiBase + "/devices/" + tv.deviceID, bearer: own.token})
	e.want(rec, http.StatusNoContent)
	if e.sessions.Active() != 0 {
		t.Fatal("revocar el dispositivo cierra sus sesiones")
	}
	create(own)
	rec, _ = e.do(call{method: http.MethodPatch, path: apiBase + "/libraries/" + lib, bearer: own.token, body: map[string]any{"enabled": false}})
	e.want(rec, http.StatusOK)
	if e.sessions.Active() != 0 {
		t.Fatal("desactivar la biblioteca cierra sus sesiones")
	}
	rec, _ = e.do(call{method: http.MethodPatch, path: apiBase + "/libraries/" + lib, bearer: own.token, body: map[string]any{"enabled": true}})
	e.want(rec, http.StatusOK)

	// Desactivar, cambiar la contraseña o eliminar un usuario cierra las suyas.
	userID := e.createUser(own.token, "visita", "user", nil)
	e.grant(own.token, userID, lib, "view")
	patchUser := func(body map[string]any) {
		t.Helper()
		rec, _ := e.do(call{method: http.MethodPatch, path: apiBase + "/admin/users/" + userID, bearer: own.token, body: body})
		e.want(rec, http.StatusOK)
	}
	create(e.account("visita"))
	patchUser(map[string]any{"is_active": false})
	if e.sessions.Active() != 0 {
		t.Fatal("desactivar al usuario cierra sus sesiones")
	}
	patchUser(map[string]any{"is_active": true})
	create(e.account("visita"))
	patchUser(map[string]any{"password": password + "-nueva"})
	if e.sessions.Active() != 0 {
		t.Fatal("cambiar la contraseña revoca sus dispositivos y cierra sus sesiones")
	}
	patchUser(map[string]any{"password": password})
	create(e.account("visita"))
	rec, _ = e.do(call{method: http.MethodDelete, path: apiBase + "/admin/users/" + userID, bearer: own.token})
	e.want(rec, http.StatusNoContent)
	if e.sessions.Active() != 0 {
		t.Fatal("eliminar el usuario cierra sus sesiones")
	}

	create(own)
	rec, _ = e.do(call{method: http.MethodDelete, path: apiBase + "/libraries/" + lib, bearer: own.token})
	e.want(rec, http.StatusNoContent)
	if e.sessions.Active() != 0 {
		t.Fatal("eliminar la biblioteca cierra sus sesiones")
	}
}

func (e *env) dial(srv *httptest.Server, token, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		h.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+apiBase+"/events", &websocket.DialOptions{HTTPHeader: h})
}

func read(t *testing.T, c *websocket.Conn, wait time.Duration) (map[string]any, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m, true
}

// El WebSocket no entrega eventos de otro usuario ni de bibliotecas sin
// permiso, y rechaza un Origin ajeno.
func TestEventosPorWebSocket(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	lib := e.scanLibrary(own.token, "movies")
	e.createUser(own.token, "lector", "user", nil)
	reader := e.account("lector")
	srv := httptest.NewServer(e.h)
	defer srv.Close()

	if _, resp, err := e.dial(srv, "", ""); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sin sesión: %v", err)
	}
	if _, resp, err := e.dial(srv, own.token, "https://malicioso.example"); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Origin ajeno: %v", err)
	}
	ownConn, _, err := e.dial(srv, own.token, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ownConn.CloseNow()
	readerConn, _, err := e.dial(srv, reader.token, srv.URL) // El propio origen se acepta.
	if err != nil {
		t.Fatal(err)
	}
	defer readerConn.CloseNow()
	deadline := time.Now().Add(5 * time.Second)
	for e.bus.Subscribers() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("las conexiones no se suscribieron")
		}
		time.Sleep(10 * time.Millisecond)
	}

	e.bus.Publish(events.Event{Name: events.PlaybackProgress, UserID: own.userID, Data: map[string]any{"position_ms": 1}})
	e.bus.Publish(events.Event{Name: events.LibraryUpdated, LibraryID: lib, Data: map[string]any{"library_id": lib}})
	e.bus.Publish(events.Event{Name: "transcode.progress", AdminOnly: true, Data: map[string]any{}})
	e.bus.Publish(events.Event{Name: events.SessionUpdated, UserID: reader.userID, Data: map[string]any{"status": "playing"}})

	var got []string
	for range 3 {
		m, ok := read(t, ownConn, 2*time.Second)
		if !ok {
			break
		}
		got = append(got, m["event"].(string))
		if m["timestamp"] == "" || m["data"] == nil {
			t.Fatalf("%v", m)
		}
	}
	if strings.Join(got, ",") != "playback.progress,library.updated,transcode.progress" {
		t.Fatalf("propietario: %v", got)
	}
	m, ok := read(t, readerConn, 2*time.Second)
	if !ok || m["event"] != events.SessionUpdated {
		t.Fatalf("el lector solo recibe lo suyo: %v", m)
	}
	if m, ok := read(t, readerConn, 300*time.Millisecond); ok {
		t.Fatalf("evento ajeno entregado: %v", m)
	}
}

// Revocar el dispositivo cierra su conexión en la siguiente revalidación.
func TestRevocarCierraElWebSocket(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	tv := e.account(owner)
	srv := httptest.NewServer(fastRecheck(e))
	defer srv.Close()
	conn, _, err := e.dial(srv, tv.token, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	rec, _ := e.do(call{method: http.MethodDelete, path: apiBase + "/devices/" + tv.deviceID, bearer: own.token})
	e.want(rec, http.StatusNoContent)
	// device.revoked llega al dueño (el propio usuario) y después se cierra.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("cierre: %v", err)
			}
			return
		}
	}
}

// fastRecheck monta /events con una revalidación cada 50 ms.
func fastRecheck(e *env) http.Handler {
	wh := &mnws.Handler{
		Bus: e.bus, Libs: authorization.NewAuthorizer(e.db), Authenticate: e.auth.Authenticate,
		RecheckPeriod: 50 * time.Millisecond, Log: zerolog.Nop(),
	}
	r := chi.NewRouter()
	r.With(middleware.Authenticate(e.auth, zerolog.Nop())).Get(apiBase+"/events", wh.ServeHTTP)
	return r
}

// Criterio de salida de la Fase 6: un cliente simulado reproduce, el
// servidor cae sin apagarse, y el cliente recibe SESSION_EXPIRED y retoma
// desde la última posición conocida en una sesión nueva.
func TestClienteSimuladoConCaida(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	_, item, file := e.firstMovie(own.token)
	// El FFprobe simulado no da duración, y sin ella nada se completa.
	if _, err := e.db.Write.Exec("UPDATE media_items SET duration_ms = 100000 WHERE id = ?", item); err != nil {
		t.Fatal(err)
	}
	open := func(sm *sessions.Manager, start int64) string {
		s, err := sm.Create(context.Background(), sessions.CreateParams{
			UserID: own.userID, DeviceID: own.deviceID, ItemID: item, FileID: file, Mode: "direct_play", StartPositionMS: start,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	first := open(e.sessions, 0)
	for _, b := range []map[string]any{
		{"state": "playing", "position_ms": 0},
		{"state": "playing", "position_ms": 10_000},
		{"state": "paused", "position_ms": 15_000}, // Vuelca al momento.
		{"state": "playing", "position_ms": 15_000},
		{"state": "playing", "position_ms": 20_000}, // Solo en memoria cuando cae.
	} {
		rec, _ := e.do(call{method: http.MethodPatch, path: apiBase + "/playback/sessions/" + first, bearer: own.token, body: b})
		e.want(rec, http.StatusOK)
	}

	// Caída: el proceso muere sin Stop. Otro servidor arranca sobre la misma base.
	restarted := newEnvOn(t, e.db)
	rec, body := restarted.do(call{method: http.MethodPatch, path: apiBase + "/playback/sessions/" + first, bearer: own.token,
		body: map[string]any{"state": "playing", "position_ms": 21_000}})
	if rec.Code != http.StatusGone || errorCode(body) != "SESSION_EXPIRED" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	// La última posición conocida es la volcada al pausar: lo que solo estaba
	// en memoria se pierde con un kill -9.
	rec, body = restarted.do(call{method: http.MethodGet, path: apiBase + "/playback/sessions/" + first, bearer: own.token})
	e.want(rec, http.StatusOK)
	resume := int64(body["position_ms"].(float64))
	if body["status"] != "expired" || resume != 15_000 {
		t.Fatalf("%v", body)
	}

	second := open(restarted.sessions, resume)
	rec, body = restarted.do(call{method: http.MethodGet, path: apiBase + "/playback/sessions/" + second, bearer: own.token})
	if rec.Code != http.StatusOK || body["start_position_ms"] != float64(15_000) {
		t.Fatalf("%v", body)
	}
	for _, b := range []map[string]any{{"state": "playing", "position_ms": 15_000}, {"state": "playing", "position_ms": 95_000}} {
		rec, _ = restarted.do(call{method: http.MethodPatch, path: apiBase + "/playback/sessions/" + second, bearer: own.token, body: b})
		e.want(rec, http.StatusOK)
	}
	rec, _ = restarted.do(call{method: http.MethodDelete, path: apiBase + "/playback/sessions/" + second, bearer: own.token})
	e.want(rec, http.StatusNoContent)

	rec, body = restarted.do(call{method: http.MethodGet, path: apiBase + "/playback/history", bearer: own.token})
	items := body["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["position_ms"] != float64(95_000) ||
		items[1].(map[string]any)["position_ms"] != float64(15_000) {
		t.Fatalf("una entrada por sesión: %v", body)
	}
	rec, _ = restarted.do(call{method: http.MethodGet, path: apiBase + "/playback/continue-watching", bearer: own.token})
	if list := decodeList(t, rec.Body.Bytes()); len(list) != 0 {
		t.Fatalf("la película ya no se reanuda si está terminada: %s", rec.Body)
	}
}
