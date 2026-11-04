package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"

	"medianode/internal/admin"
	"medianode/internal/api/middleware"
	"medianode/internal/auth"
	"medianode/internal/authorization"
	"medianode/internal/config"
	"medianode/internal/database"
	"medianode/internal/devices"
	"medianode/internal/dlna"
	"medianode/internal/events"
	"medianode/internal/ffmpeg"
	"medianode/internal/images"
	"medianode/internal/libraries"
	"medianode/internal/media"
	"medianode/internal/metadata"
	"medianode/internal/monitoring"
	"medianode/internal/playback"
	"medianode/internal/probe"
	"medianode/internal/readmodel"
	"medianode/internal/remote"
	"medianode/internal/scanner"
	"medianode/internal/sessions"
	"medianode/internal/subtitles"
	"medianode/internal/testutil"
	"medianode/internal/transcoding"
	"medianode/internal/users"
)

// stubProber evita depender de FFprobe en las pruebas de rutas.
type stubProber struct{}

func (stubProber) Run(context.Context, string) (*probe.Result, error) {
	return &probe.Result{FormatName: "matroska,webm", Streams: []probe.Stream{{Type: media.StreamVideo}}}, nil
}

const (
	owner    = "propietario"
	password = "contraseña-segura"
	apiBase  = "/api/v1"
)

type env struct {
	t        *testing.T
	h        http.Handler
	code     string
	logs     *syncBuffer
	db       *database.DB
	imgDir   string
	sessions *sessions.Manager
	bus      *events.Bus
	auth     *auth.Service
	devices  *devices.Service
	playback *playback.Service

	transcoder *transcoding.Manager
	indexer    *transcoding.Indexer
	admin      *admin.Service
	dataDir    string
	dlna       *dlna.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvOn(t, testutil.NewDB(t))
}

// newEnvOn monta el servidor sobre una base existente: sirve para simular un
// reinicio tras una caída.
func newEnvOn(t *testing.T, db *database.DB, opts ...func(*envOptions)) *env {
	t.Helper()
	var eo envOptions
	for _, o := range opts {
		o(&eo)
	}
	hasher := auth.NewHasher(auth.Params{MemoryKiB: 8, Iterations: 1, Threads: 1})
	logs := &syncBuffer{}
	log := zerolog.New(logs)
	a := auth.New(db, hasher, log)
	code, err := a.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sc := scanner.New(db, stubProber{}, scanner.Options{Workers: 1}, log)
	t.Cleanup(sc.Close)
	authz := authorization.NewAuthorizer(db)
	imgDir := t.TempDir()
	imgs := images.New(imgDir, db, nil)
	enricher := metadata.NewEnricher(db, nil, imgs, "es-ES", metadata.Options{}, log)
	bus := events.NewBus()
	sm := sessions.New(db, bus, sessions.Options{FlushInterval: time.Hour, SweepInterval: time.Hour}, log)
	if err := sm.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sm.Stop(context.Background()) })
	libs := libraries.New(db, authz, sc, []string{t.TempDir()})
	libs.Sessions = sm
	us := users.New(db, hasher)
	us.Sessions = sm
	ds := devices.New(db)
	ds.Sessions, ds.Bus = sm, bus
	ps := playback.NewService(db, authz, sm, log)
	sm.AddListener(ps)
	var tm *transcoding.Manager
	var indexer *transcoding.Indexer
	var caps *ffmpeg.Capabilities
	if eo.ffmpeg != "" {
		tm = transcoding.NewManager(transcoding.Options{
			FFmpeg: eo.ffmpeg, Dir: t.TempDir(), SegmentSeconds: 6, RestartSegments: 5, WaitTimeout: 30 * time.Second,
		}, db, bus, transcoding.NewAdmission(eo.heavy, eo.light), log)
		if err := tm.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tm.Stop(context.Background()) })
		ps.Transcoder = tm
		sm.AddListener(sessions.ListenerFunc(func(s sessions.Session) { tm.OnSessionEnded(s.ID) }))
		indexer = transcoding.NewIndexer(t.TempDir(), eo.ffprobe, log)
		if err := indexer.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = indexer.Stop(context.Background()) })
		ps.Indexer = indexer
		ps.Subtitles = subtitles.NewManager(eo.ffmpeg, t.TempDir(), log)
		caps = probeFFmpeg(t, eo.ffmpeg)
		ps.Features = playback.FeaturesOf(caps)
	}
	dataDir := t.TempDir()
	cfg := *config.Default()
	cfg.Database.Path = filepath.Join(dataDir, "medianode.db")
	cfg.Paths = config.Paths{
		Metadata: filepath.Join(dataDir, "metadata"), Images: imgDir, Cache: filepath.Join(dataDir, "cache"),
		Transcodes: filepath.Join(dataDir, "transcodes"), Logs: filepath.Join(dataDir, "logs"), Backups: filepath.Join(dataDir, "backups"),
	}
	metrics := monitoring.NewMetrics()
	remoteOpts := remote.Options{Enabled: true, Direct: true, AdminEnabled: true}
	if eo.remote != nil {
		remoteOpts = *eo.remote
	}
	remoteAccess := remote.New(remoteOpts, log)
	var dlnaServer *dlna.Server
	var dlnaHandler http.Handler
	if eo.dlna != nil {
		o := *eo.dlna
		if o.Name == "" {
			o.Name = "Prueba"
		}
		dlnaServer = dlna.New(o, db, ps, sm, tm, remoteAccess.Net, log)
		dlnaServer.Images = imgs
		if err := dlnaServer.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		dlnaHandler = dlnaServer.Handler()
	}
	adminSvc := &admin.Service{
		Version: "1.2.3", Started: time.Now(), ConfigPath: filepath.Join(dataDir, "server.yaml"), Config: cfg, DB: db,
		FFmpegPath: eo.ffmpeg, FFprobePath: eo.ffprobe, FFmpeg: caps, Sessions: sm, Transcoder: tm,
		Metrics: metrics, CPU: monitoring.NewProcessSampler(),
	}
	return &env{t: t, code: code, logs: logs, db: db, imgDir: imgDir, sessions: sm, bus: bus, auth: a, devices: ds, playback: ps,
		admin: adminSvc, dataDir: dataDir,
		dlna:       dlnaServer,
		transcoder: tm, indexer: indexer, h: NewRouter(Deps{
			Admin: adminSvc, Metrics: metrics,
			Log: log, Name: "Prueba", Version: "1.2.3",
			Auth: a, Users: us, Devices: ds, Libraries: libs,
			Catalog: readmodel.New(db, authz), Metadata: metadata.NewService(db, authz, enricher),
			Enricher: enricher, Images: imgs, Started: time.Now(),
			Sessions: sm, Progress: sessions.NewProgress(db, authz, bus, 0.9), Bus: bus, Authorizer: authz,
			Playback: ps, DB: db, Transcoder: tm, FFmpeg: caps,
			Remote: remoteAccess, TrustedProxies: remoteOpts.TrustedProxies,
			DLNA:        dlnaHandler,
			CORSOrigins: eo.origins,
			Web:         eo.web,
		})}
}

// envOptions activa FFmpeg en el entorno. Sin él, el servidor solo hace
// Direct Play, como uno arrancado sin FFmpeg.
type envOptions struct {
	ffmpeg, ffprobe string
	heavy, light    int
	// remote es la política de acceso remoto; nil, la más permisiva: todo vale
	// desde cualquier origen, para las pruebas que no tratan de red.
	remote *remote.Options
	// dlna activa el servidor DLNA con estas opciones.
	dlna *dlna.Options
	// origins son los orígenes web admitidos además del propio (server.cors_origins).
	origins []string
	// web es el panel que sirve el enrutador; nil: sin panel.
	web http.Handler
}

type call struct {
	method, path string
	body         any
	bearer       string
	cookies      []*http.Cookie
	csrf         string
	remote       string
	headers      map[string]string
}

func (e *env) do(c call) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	var body bytes.Buffer
	if c.body != nil {
		if err := json.NewEncoder(&body).Encode(c.body); err != nil {
			e.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(c.method, c.path, &body)
	if c.body == nil {
		r.ContentLength = 0
	}
	if c.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	for _, ck := range c.cookies {
		r.AddCookie(ck)
	}
	if c.csrf != "" {
		r.Header.Set(middleware.HeaderCSRF, c.csrf)
	}
	if c.remote != "" {
		r.RemoteAddr = c.remote
	}
	for k, v := range c.headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func (e *env) want(rec *httptest.ResponseRecorder, status int) {
	e.t.Helper()
	if rec.Code != status {
		e.t.Fatalf("estado = %d, se esperaba %d: %s", rec.Code, status, rec.Body)
	}
}

func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func device(platform string) map[string]any {
	return map[string]any{"device_name": "Cliente", "platform": platform}
}

func credentials(username, platform string) map[string]any {
	body := device(platform)
	body["username"], body["password"] = username, password
	return body
}

func (e *env) setup() {
	e.t.Helper()
	rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/auth/setup", body: map[string]any{
		"code": e.code, "username": owner, "password": password,
	}})
	e.want(rec, http.StatusCreated)
}

func (e *env) login(username string) map[string]any {
	e.t.Helper()
	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", body: credentials(username, "android")})
	e.want(rec, http.StatusOK)
	return body
}

func cookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	rec, body := e.do(call{method: http.MethodGet, path: "/health"})
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get(monitoring.HeaderRequestID) == "" {
		t.Fatal("falta el identificador de petición")
	}
}

func TestServerInfo(t *testing.T) {
	e := newEnv(t)
	rec, body := e.do(call{method: http.MethodGet, path: apiBase + "/server/info"})
	if rec.Code != http.StatusOK || body["name"] != "Prueba" || body["version"] != "1.2.3" || body["setup_required"] != true {
		t.Fatalf("%d %v", rec.Code, body)
	}
	e.setup()
	if _, body := e.do(call{method: http.MethodGet, path: apiBase + "/server/info"}); body["setup_required"] != false {
		t.Fatalf("%v", body)
	}
}

func TestServerVersion(t *testing.T) {
	e := newEnv(t)
	rec, body := e.do(call{method: http.MethodGet, path: apiBase + "/server/version"})
	if rec.Code != http.StatusOK || body["version"] != "1.2.3" || body["protocol_version"] != float64(1) {
		t.Fatalf("%d %v", rec.Code, body)
	}
}

func TestErroresEstructurados(t *testing.T) {
	cases := []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodGet, "/no-existe", http.StatusNotFound, "NOT_FOUND"},
		{http.MethodGet, "/api/v1/no-existe", http.StatusNotFound, "NOT_FOUND"},
		{http.MethodPost, "/health", http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
	}
	e := newEnv(t)
	for _, c := range cases {
		rec, body := e.do(call{method: c.method, path: c.path})
		if rec.Code != c.status {
			t.Fatalf("%s %s: %d", c.method, c.path, rec.Code)
		}
		er, ok := body["error"].(map[string]any)
		if !ok || er["code"] != c.code || er["request_id"] == "" {
			t.Fatalf("%s %s: %v", c.method, c.path, body)
		}
	}
}

// Rutas sin sesión. Añadir una aquí debe ser una decisión consciente: toda ruta
// nueva queda protegida o esta prueba falla.
var publicRoutes = []string{
	"GET /health",
	"GET /api/v1/server/info",
	"GET /api/v1/server/version",
	"GET /api/v1/server/capabilities",
	"POST /api/v1/auth/setup",
	"POST /api/v1/auth/login",
	"POST /api/v1/auth/refresh",
	"POST /api/v1/auth/pairing",
	"POST /api/v1/auth/pairing/token",
	"GET /api/v1/remote/probe/{nonce}",
}

func walkRoutes(t *testing.T, h http.Handler, fn func(method, route string)) {
	t.Helper()
	err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		fn(method, strings.TrimSuffix(route, "/"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNingunaRutaQuedaSinControlDeAcceso(t *testing.T) {
	e := newEnv(t)
	seen := 0
	walkRoutes(t, e.h, func(method, route string) {
		key := method + " " + route
		if slices.Contains(publicRoutes, key) {
			return
		}
		seen++
		path := strings.ReplaceAll(route, "{id}", "x")
		rec, body := e.do(call{method: method, path: path})
		// Las URL de streaming se autentican con el token que llevan: sin uno
		// válido, el recurso no existe.
		if strings.HasPrefix(route, "/stream/") {
			if rec.Code != http.StatusNotFound || (method != http.MethodHead && errorCode(body) != "NOT_FOUND") {
				t.Errorf("%s: %d %v", key, rec.Code, body)
			}
			return
		}
		if rec.Code != http.StatusUnauthorized || errorCode(body) != "AUTH_REQUIRED" {
			t.Errorf("%s: %d %v", key, rec.Code, body)
		}
	})
	if seen < 14 {
		t.Fatalf("solo se comprobaron %d rutas", seen)
	}
}

func TestAdministracionExigeRol(t *testing.T) {
	e := newEnv(t)
	e.setup()
	ownerToken := e.login(owner)["access_token"].(string)

	rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/admin/users", bearer: ownerToken, body: map[string]any{
		"username": "normal", "password": password, "role": "user",
	}})
	e.want(rec, http.StatusCreated)

	userToken := e.login("normal")["access_token"].(string)
	walkRoutes(t, e.h, func(method, route string) {
		if !strings.Contains(route, "/admin/") {
			return
		}
		rec, body := e.do(call{method: method, path: strings.ReplaceAll(route, "{id}", "x"), bearer: userToken})
		if rec.Code != http.StatusForbidden || errorCode(body) != "FORBIDDEN" {
			t.Errorf("%s %s: %d %v", method, route, rec.Code, body)
		}
	})
}

func TestContratoOpenAPICubreLasRutas(t *testing.T) {
	data, err := os.ReadFile("../../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	documented := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			// Campos de la ruta que no son métodos (OpenAPI 3.0, Path Item).
			if !slices.Contains([]string{"parameters", "servers", "summary", "description"}, method) {
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	walkRoutes(t, newEnv(t).h, func(method, route string) {
		key := method + " " + strings.TrimPrefix(route, apiBase)
		if !documented[key] {
			t.Errorf("sin documentar: %s", key)
		}
		delete(documented, key)
	})
	for key := range documented {
		t.Errorf("documentada sin ruta: %s", key)
	}
}

func TestFlujoNativoCompleto(t *testing.T) {
	e := newEnv(t)

	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/setup", body: map[string]any{
		"code": "erroneo", "username": owner, "password": password,
	}})
	e.want(rec, http.StatusUnauthorized)
	if errorCode(body) != "AUTH_INVALID_CREDENTIALS" {
		t.Fatalf("%v", body)
	}
	e.setup()

	session := e.login(owner)
	access := session["access_token"].(string)
	refresh := session["refresh_token"].(string)
	if session["user"].(map[string]any)["role"] != "owner" {
		t.Fatalf("%v", session)
	}

	rec, body = e.do(call{method: http.MethodGet, path: apiBase + "/auth/me", bearer: access})
	e.want(rec, http.StatusOK)
	if body["username"] != owner {
		t.Fatalf("%v", body)
	}
	if strings.Contains(rec.Body.String(), "argon2") {
		t.Fatal("la respuesta contiene el hash")
	}

	rec, body = e.do(call{method: http.MethodPost, path: apiBase + "/auth/refresh", body: map[string]any{"refresh_token": refresh}})
	e.want(rec, http.StatusOK)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("una respuesta con tokens no debe almacenarse")
	}
	access = body["access_token"].(string)

	rec, _ = e.do(call{method: http.MethodPost, path: apiBase + "/auth/logout", bearer: access})
	e.want(rec, http.StatusNoContent)

	rec, body = e.do(call{method: http.MethodGet, path: apiBase + "/auth/me", bearer: access})
	e.want(rec, http.StatusUnauthorized)
	if errorCode(body) != "AUTH_DEVICE_REVOKED" {
		t.Fatalf("%v", body)
	}
}

func TestFlujoWebConCookies(t *testing.T) {
	e := newEnv(t)
	e.setup()

	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", body: credentials(owner, "web")})
	e.want(rec, http.StatusOK)
	if _, ok := body["access_token"]; ok {
		t.Fatal("la web no debe recibir tokens accesibles desde JavaScript")
	}
	access := cookie(rec, middleware.CookieAccess)
	refresh := cookie(rec, middleware.CookieRefresh)
	csrf := cookie(rec, middleware.CookieCSRF)
	if access == nil || refresh == nil || csrf == nil {
		t.Fatalf("cookies = %v", rec.Result().Cookies())
	}
	if !access.HttpOnly || !refresh.HttpOnly || csrf.HttpOnly {
		t.Fatal("access y refresh son HttpOnly; CSRF debe poder leerse")
	}
	for _, c := range []*http.Cookie{access, refresh, csrf} {
		if c.SameSite != http.SameSiteStrictMode {
			t.Fatalf("%s: SameSite = %v", c.Name, c.SameSite)
		}
		if c.Secure {
			t.Fatalf("%s: Secure sin TLS impediría el login por HTTP", c.Name)
		}
	}
	if refresh.Path != apiBase+"/auth" {
		t.Fatalf("ruta del refresh = %q", refresh.Path)
	}

	all := []*http.Cookie{access, csrf}
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/auth/me", cookies: all})
	e.want(rec, http.StatusOK)

	prefs := map[string]any{"subtitle_mode": "off"}
	rec, body = e.do(call{method: http.MethodPut, path: apiBase + "/users/me/preferences", cookies: all, body: prefs})
	e.want(rec, http.StatusForbidden)
	if errorCode(body) != "CSRF_INVALID" {
		t.Fatalf("%v", body)
	}
	rec, _ = e.do(call{method: http.MethodPut, path: apiBase + "/users/me/preferences", cookies: all, csrf: csrf.Value, body: prefs})
	e.want(rec, http.StatusOK)

	withRefresh := []*http.Cookie{refresh, csrf}
	rec, body = e.do(call{method: http.MethodPost, path: apiBase + "/auth/refresh", cookies: withRefresh})
	e.want(rec, http.StatusForbidden)
	if errorCode(body) != "CSRF_INVALID" {
		t.Fatalf("%v", body)
	}

	rec, body = e.do(call{method: http.MethodPost, path: apiBase + "/auth/refresh", cookies: withRefresh, csrf: csrf.Value})
	e.want(rec, http.StatusOK)
	if _, ok := body["refresh_token"]; ok {
		t.Fatal("un refresh por cookie debe seguir en cookies")
	}
	newCSRF := cookie(rec, middleware.CookieCSRF)
	all = []*http.Cookie{cookie(rec, middleware.CookieAccess), newCSRF}

	rec, _ = e.do(call{method: http.MethodPost, path: apiBase + "/auth/logout", cookies: all, csrf: newCSRF.Value})
	e.want(rec, http.StatusNoContent)
	if c := cookie(rec, middleware.CookieAccess); c == nil || c.MaxAge >= 0 {
		t.Fatalf("logout debe borrar la cookie: %v", c)
	}
}

func TestBearerInvalidoNoCaeEnLaCookie(t *testing.T) {
	e := newEnv(t)
	e.setup()
	rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", body: credentials(owner, "web")})
	access := cookie(rec, middleware.CookieAccess)

	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/auth/me", bearer: "falso", cookies: []*http.Cookie{access}})
	e.want(rec, http.StatusUnauthorized)
}

func TestBearerConCookieAjenaNoExigeCSRF(t *testing.T) {
	e := newEnv(t)
	e.setup()
	token := e.login(owner)["access_token"].(string)
	rec, _ := e.do(call{
		method: http.MethodPut, path: apiBase + "/users/me/preferences", bearer: token,
		body: map[string]any{"subtitle_mode": "off"},
	})
	e.want(rec, http.StatusOK)
}

func TestVinculacionPorAPI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	token := e.login(owner)["access_token"].(string)

	rec, pairing := e.do(call{method: http.MethodPost, path: apiBase + "/auth/pairing", body: device("androidtv")})
	e.want(rec, http.StatusCreated)
	poll := map[string]any{"poll_token": pairing["poll_token"]}

	rec, _ = e.do(call{method: http.MethodPost, path: apiBase + "/auth/pairing/token", body: poll})
	e.want(rec, http.StatusAccepted)

	claim := map[string]any{"code": pairing["code"]}
	rec, _ = e.do(call{method: http.MethodPost, path: apiBase + "/auth/pairing/claim", body: claim})
	e.want(rec, http.StatusUnauthorized)
	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/pairing/claim", bearer: token, body: claim})
	e.want(rec, http.StatusOK)
	if body["platform"] != "androidtv" {
		t.Fatalf("%v", body)
	}

	rec, session := e.do(call{method: http.MethodPost, path: apiBase + "/auth/pairing/token", body: poll})
	e.want(rec, http.StatusOK)
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/auth/me", bearer: session["access_token"].(string)})
	e.want(rec, http.StatusOK)

	rec, body = e.do(call{method: http.MethodPost, path: apiBase + "/auth/pairing/token", body: poll})
	e.want(rec, http.StatusBadRequest)
	if errorCode(body) != "AUTH_PAIRING_INVALID" {
		t.Fatalf("%v", body)
	}
}

func TestDispositivosPorAPI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	session := e.login(owner)
	token, id := session["access_token"].(string), session["device_id"].(string)

	rec, _ := e.do(call{method: http.MethodGet, path: apiBase + "/devices", bearer: token})
	e.want(rec, http.StatusOK)
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["id"] != id || list[0]["current"] != true {
		t.Fatalf("%v", list)
	}

	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/devices?scope=otro", bearer: token})
	e.want(rec, http.StatusBadRequest)
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/devices?scope=all", bearer: token})
	e.want(rec, http.StatusOK)

	rec, body := e.do(call{method: http.MethodPatch, path: apiBase + "/devices/" + id, bearer: token, body: map[string]any{"device_name": "Salón"}})
	e.want(rec, http.StatusOK)
	if body["device_name"] != "Salón" {
		t.Fatalf("%v", body)
	}
	rec, _ = e.do(call{method: http.MethodDelete, path: apiBase + "/devices/" + id, bearer: token})
	e.want(rec, http.StatusNoContent)

	rec, body = e.do(call{method: http.MethodGet, path: apiBase + "/devices/" + id, bearer: token})
	e.want(rec, http.StatusUnauthorized)
	if errorCode(body) != "AUTH_DEVICE_REVOKED" {
		t.Fatalf("%v", body)
	}
}

func TestUsuariosPorAPI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	token := e.login(owner)["access_token"].(string)

	rec, created := e.do(call{method: http.MethodPost, path: apiBase + "/admin/users", bearer: token, body: map[string]any{
		"username": "peque", "password": password, "role": "restricted", "max_age_rating": 12,
	}})
	e.want(rec, http.StatusCreated)
	path := apiBase + "/admin/users/" + created["id"].(string)

	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/admin/users", bearer: token})
	e.want(rec, http.StatusOK)
	rec, _ = e.do(call{method: http.MethodGet, path: path, bearer: token})
	e.want(rec, http.StatusOK)

	// Un null borra el valor; un campo ausente lo conserva.
	rec, body := e.do(call{method: http.MethodPatch, path: path, bearer: token, body: map[string]any{"username": "peque2"}})
	e.want(rec, http.StatusOK)
	if body["max_age_rating"] != float64(12) {
		t.Fatalf("%v", body)
	}
	rec, body = e.do(call{method: http.MethodPatch, path: path, bearer: token, body: map[string]any{"max_age_rating": nil}})
	e.want(rec, http.StatusOK)
	if body["max_age_rating"] != nil {
		t.Fatalf("%v", body)
	}

	rec, _ = e.do(call{method: http.MethodPatch, path: path, bearer: token, body: map[string]any{"rol": "owner"}})
	e.want(rec, http.StatusBadRequest)

	rec, _ = e.do(call{method: http.MethodDelete, path: path, bearer: token})
	e.want(rec, http.StatusNoContent)
	rec, _ = e.do(call{method: http.MethodGet, path: path, bearer: token})
	e.want(rec, http.StatusNotFound)
}

func TestBibliotecasPorAPI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	token := e.login(owner)["access_token"].(string)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Peli (2001).mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, lib := e.do(call{method: http.MethodPost, path: apiBase + "/libraries", bearer: token, body: map[string]any{
		"name": "Películas", "type": "movies", "paths": []string{root},
	}})
	e.want(rec, http.StatusCreated)
	libPath := apiBase + "/libraries/" + lib["id"].(string)
	if paths, _ := lib["paths"].([]any); len(paths) != 1 {
		t.Fatalf("el administrador ve las rutas: %v", lib)
	}
	rec, other := e.do(call{method: http.MethodPost, path: apiBase + "/libraries", bearer: token, body: map[string]any{
		"name": "Series", "type": "tv",
	}})
	e.want(rec, http.StatusCreated)

	// Una ruta inválida no repite la ruta en el mensaje.
	secret := filepath.Join(root, "no-existe-secreto")
	rec, body := e.do(call{method: http.MethodPost, path: libPath + "/paths", bearer: token, body: map[string]any{"path": secret}})
	e.want(rec, http.StatusUnprocessableEntity)
	if errorCode(body) != "LIBRARY_PATH_UNAVAILABLE" || strings.Contains(rec.Body.String(), "secreto") {
		t.Fatalf("%s", rec.Body)
	}

	rec, _ = e.do(call{method: http.MethodPost, path: libPath + "/scan", bearer: token})
	e.want(rec, http.StatusAccepted)
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec, body = e.do(call{method: http.MethodGet, path: libPath + "/scan", bearer: token})
		if rec.Code == http.StatusOK && body["status"] == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("el escaneo no terminó: %s", rec.Body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if body["files_discovered"] != float64(1) {
		t.Fatalf("%v", body)
	}

	// Un usuario con view ve la biblioteca sin rutas y no puede escanear.
	rec, user := e.do(call{method: http.MethodPost, path: apiBase + "/admin/users", bearer: token, body: map[string]any{
		"username": "lector", "password": password, "role": "user",
	}})
	e.want(rec, http.StatusCreated)
	permsPath := apiBase + "/admin/users/" + user["id"].(string) + "/permissions"
	rec, _ = e.do(call{method: http.MethodPut, path: permsPath, bearer: token, body: map[string]any{
		"libraries": []map[string]any{{"library_id": lib["id"], "permissions": []string{"view"}}},
	}})
	e.want(rec, http.StatusOK)
	rec, _ = e.do(call{method: http.MethodGet, path: permsPath, bearer: token})
	e.want(rec, http.StatusOK)

	userToken := e.login("lector")["access_token"].(string)
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/libraries", bearer: userToken})
	e.want(rec, http.StatusOK)
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["paths"] != nil {
		t.Fatalf("%s", rec.Body)
	}
	rec, body = e.do(call{method: http.MethodPost, path: libPath + "/scan", bearer: userToken})
	e.want(rec, http.StatusForbidden)
	if errorCode(body) != "PERMISSION_LIBRARY_DENIED" {
		t.Fatalf("%v", body)
	}
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/libraries/" + other["id"].(string), bearer: userToken})
	e.want(rec, http.StatusNotFound)
	rec, _ = e.do(call{method: http.MethodDelete, path: libPath, bearer: userToken})
	e.want(rec, http.StatusForbidden)
	rec, _ = e.do(call{method: http.MethodGet, path: permsPath, bearer: userToken})
	e.want(rec, http.StatusForbidden)

	rec, body = e.do(call{method: http.MethodPatch, path: libPath, bearer: token, body: map[string]any{"name": "Cine"}})
	e.want(rec, http.StatusOK)
	if body["name"] != "Cine" {
		t.Fatalf("%v", body)
	}
	pathID := lib["paths"].([]any)[0].(map[string]any)["id"].(string)
	rec, _ = e.do(call{method: http.MethodDelete, path: libPath + "/paths/" + pathID, bearer: token})
	e.want(rec, http.StatusNoContent)
	rec, _ = e.do(call{method: http.MethodDelete, path: libPath, bearer: token})
	e.want(rec, http.StatusNoContent)
	rec, _ = e.do(call{method: http.MethodGet, path: libPath + "/scan", bearer: token})
	e.want(rec, http.StatusNotFound)
}

func TestCambioDeContrasenaPorAPI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	token := e.login(owner)["access_token"].(string)
	rec, _ := e.do(call{method: http.MethodPut, path: apiBase + "/users/me/password", bearer: token, body: map[string]any{
		"current_password": password, "new_password": "contraseña-nueva",
	}})
	e.want(rec, http.StatusNoContent)
}

func TestPreferenciasPorAPI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	token := e.login(owner)["access_token"].(string)
	path := apiBase + "/users/me/preferences"

	rec, body := e.do(call{method: http.MethodGet, path: path, bearer: token})
	e.want(rec, http.StatusOK)
	if body["subtitle_mode"] != "default" {
		t.Fatalf("%v", body)
	}
	rec, _ = e.do(call{method: http.MethodPut, path: path, bearer: token, body: map[string]any{"subtitle_mode": "raro"}})
	e.want(rec, http.StatusBadRequest)
	rec, _ = e.do(call{method: http.MethodPut, path: path, bearer: token, body: map[string]any{
		"subtitle_mode": "always", "audio_language": "es",
	}})
	e.want(rec, http.StatusOK)
	if _, body = e.do(call{method: http.MethodGet, path: path, bearer: token}); body["audio_language"] != "es" {
		t.Fatalf("%v", body)
	}
}

func TestLoginLimitadoPorDireccion(t *testing.T) {
	e := newEnv(t)
	for i := range loginPerMinute {
		rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", remote: "10.1.1.1:1", body: credentials("u"+string(rune('a'+i))+"xx", "ios")})
		e.want(rec, http.StatusUnauthorized)
	}
	rec, body := e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", remote: "10.1.1.1:1", body: credentials("otro", "ios")})
	e.want(rec, http.StatusTooManyRequests)
	if errorCode(body) != "RATE_LIMITED" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%v %v", body, rec.Header())
	}
}

func TestLimiteDeCuentaLlevaRetryAfter(t *testing.T) {
	e := newEnv(t)
	var rec *httptest.ResponseRecorder
	var body map[string]any
	for range 6 {
		rec, body = e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", body: credentials("alguien", "ios")})
	}
	e.want(rec, http.StatusTooManyRequests)
	if errorCode(body) != "RATE_LIMITED" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%v %v", body, rec.Header())
	}
}

func TestCuerpoInvalido(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{"", "{", `{"campo_desconocido":1}`} {
		r := httptest.NewRequest(http.MethodPost, apiBase+"/auth/login", strings.NewReader(body))
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		e.want(rec, http.StatusBadRequest)
	}
}

func TestLogsNoContienenSecretos(t *testing.T) {
	e := newEnv(t)
	e.setup()
	session := e.login(owner)
	refresh := session["refresh_token"].(string)
	access := session["access_token"].(string)
	e.do(call{method: http.MethodPost, path: apiBase + "/auth/login", body: credentials(owner, "ios")})
	e.do(call{method: http.MethodPost, path: apiBase + "/auth/refresh", body: map[string]any{"refresh_token": refresh}})
	e.do(call{method: http.MethodGet, path: apiBase + "/auth/me", bearer: access})

	logs := e.logs.String()
	if !strings.Contains(logs, "inicio de sesión") {
		t.Fatalf("falta el registro de accesos: %s", logs)
	}
	for _, secret := range []string{password, refresh, access, e.code} {
		if strings.Contains(logs, secret) {
			t.Fatalf("el log contiene un secreto: %s", logs)
		}
	}
	if regexp.MustCompile(`\$argon2id\$`).MatchString(logs) {
		t.Fatal("el log contiene un hash")
	}
}

// Mide la lectura de la base que Authenticate hace en cada petición.
func BenchmarkPeticionAutenticada(b *testing.B) {
	db := testutil.NewDB(b)
	hasher := auth.NewHasher(auth.Params{MemoryKiB: 8, Iterations: 1, Threads: 1})
	a := auth.New(db, hasher, zerolog.Nop())
	ctx := context.Background()
	code, err := a.Bootstrap(ctx)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := a.Setup(ctx, code, owner, password); err != nil {
		b.Fatal(err)
	}
	pair, err := a.Login(ctx, owner, password, auth.DeviceInfo{Name: "x", Platform: "ios"})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := a.Authenticate(ctx, pair.AccessToken); err != nil {
			b.Fatal(err)
		}
	}
}
