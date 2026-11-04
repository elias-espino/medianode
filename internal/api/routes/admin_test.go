package routes

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func (e *env) adminGet(token, path string) (*http.Request, []byte, int) {
	e.t.Helper()
	rec, _ := e.do(call{method: http.MethodGet, path: apiBase + path, bearer: token})
	return nil, rec.Body.Bytes(), rec.Code
}

func parseJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return v
}

func TestAdminSistema(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.file("Cine.mp4", "mp4", "h264", "aac", 360)

	_, body, code := e.adminGet(own.token, "/admin/system")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	sys := parseJSON[map[string]any](t, body)
	if sys["version"] != "1.2.3" || sys["os"] != runtime.GOOS || sys["arch"] != runtime.GOARCH || sys["cpus"].(float64) < 1 {
		t.Fatalf("%v", sys)
	}
	counts := sys["counts"].(map[string]any)
	if counts["libraries"].(float64) != 1 || counts["files"].(float64) != 1 || counts["users"].(float64) != 1 {
		t.Fatalf("counts = %v", counts)
	}
	// Sin FFmpeg en este entorno.
	if ff := sys["ffmpeg"].(map[string]any); ff["available"] != false {
		t.Fatalf("ffmpeg = %v", ff)
	}
	if hw := sys["hardware"].(map[string]any); hw["encoders_compiled"] == nil || hw["encoders_working"] == nil {
		t.Fatalf("las listas de codificadores no deben ser null: %v", hw)
	}
	if mem := sys["memory"].(map[string]any); mem["alloc_bytes"].(float64) <= 0 {
		t.Fatalf("memory = %v", mem)
	}
	if sys["database"].(map[string]any)["size_bytes"].(float64) < 0 {
		t.Fatalf("database = %v", sys["database"])
	}
}

func TestAdminSesionesYConexiones(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.file("Cine.mp4", "mp4", "h264", "aac", 360)

	play := func(remote string) {
		t.Helper()
		rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/playback/sessions", bearer: own.token, remote: remote,
			body: map[string]any{"media_item_id": m.item, "client_profile": webProfile}})
		e.want(rec, http.StatusCreated)
	}
	play("192.168.1.50:5000")
	play("8.8.8.8:5000")

	_, body, code := e.adminGet(own.token, "/admin/sessions")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	list := parseJSON[[]map[string]any](t, body)
	if len(list) != 2 {
		t.Fatalf("sesiones = %d: %s", len(list), body)
	}
	byIP := map[string]map[string]any{}
	for _, s := range list {
		byIP[s["client_ip"].(string)] = s
	}
	lan, wan := byIP["192.168.1.50"], byIP["8.8.8.8"]
	if lan == nil || wan == nil {
		t.Fatalf("faltan las IP de los clientes: %s", body)
	}
	if lan["local"] != true || wan["local"] != false {
		t.Fatalf("local: red local %v, internet %v", lan["local"], wan["local"])
	}
	for _, s := range []map[string]any{lan, wan} {
		if s["user"] != owner || s["mode"] != "direct_play" || s["title"] == "" || s["job"] != "" || s["bitrate_bps"].(float64) != 360000 {
			t.Fatalf("%v", s)
		}
	}

	// Las métricas cuentan lo mismo.
	_, body, _ = e.adminGet(own.token, "/admin/metrics")
	mt := parseJSON[map[string]any](t, body)
	if mt["active_sessions"].(float64) != 2 || mt["connections_local"].(float64) != 1 || mt["connections_remote"].(float64) != 1 ||
		mt["bitrate_bps"].(float64) != 720000 {
		t.Fatalf("%v", mt)
	}
	_, body, _ = e.adminGet(own.token, "/admin/network")
	nw := parseJSON[map[string]any](t, body)
	if nw["connections_local"].(float64) != 1 || nw["connections_remote"].(float64) != 1 {
		t.Fatalf("%v", nw)
	}
}

func TestAdminMetricas(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.file("Cine.mp4", "mp4", "h264", "aac", 360)
	for range 3 {
		e.adminGet(own.token, "/admin/system")
	}

	_, body, code := e.adminGet(own.token, "/admin/metrics")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	mt := parseJSON[map[string]any](t, body)
	if mt["files_indexed"].(float64) != 1 || mt["goroutines"].(float64) < 1 || mt["memory_bytes"].(float64) <= 0 {
		t.Fatalf("%v", mt)
	}
	if mt["disk_free_bytes"].(float64) <= 0 || mt["disk_total_bytes"].(float64) < mt["disk_free_bytes"].(float64) {
		t.Fatalf("disco: %v", mt)
	}
	for _, k := range []string{"last_scans", "endpoints"} {
		if _, ok := mt[k].([]any); !ok {
			t.Fatalf("%s debe ser una lista, no null: %v", k, mt[k])
		}
	}
	var sysEndpoint map[string]any
	for _, ep := range mt["endpoints"].([]any) {
		if r := ep.(map[string]any); r["route"] == "GET /api/v1/admin/system" {
			sysEndpoint = r
		}
	}
	if sysEndpoint == nil || sysEndpoint["count"].(float64) != 3 || sysEndpoint["p95_ms"].(float64) < sysEndpoint["p50_ms"].(float64) {
		t.Fatalf("latencia de /admin/system: %v en %v", sysEndpoint, mt["endpoints"])
	}
	cpu := mt["cpu_percent"].(float64)
	if cpu < 0 || cpu > 100 {
		t.Fatalf("cpu_percent = %v", cpu)
	}
}

func TestAdminAlmacenamiento(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	if err := os.WriteFile(filepath.Join(e.imgDir, "a.jpg"), make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}

	_, body, code := e.adminGet(own.token, "/admin/storage")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	st := parseJSON[map[string]any](t, body)
	folders := map[string]map[string]any{}
	for _, f := range st["folders"].([]any) {
		fm := f.(map[string]any)
		folders[fm["key"].(string)] = fm
	}
	for _, k := range []string{"metadata", "images", "cache", "transcodes", "logs", "backups"} {
		if folders[k] == nil {
			t.Fatalf("falta la carpeta %s: %s", k, body)
		}
	}
	if folders["images"]["size_bytes"].(float64) < 5000 || folders["images"]["files"].(float64) < 1 || folders["images"]["partial"] != false {
		t.Fatalf("images = %v", folders["images"])
	}
	if folders["cache"]["size_bytes"].(float64) != 0 { // No existe todavía.
		t.Fatalf("cache = %v", folders["cache"])
	}
	data := st["data"].(map[string]any)
	if data["readable"] != true || data["free_bytes"].(float64) <= 0 {
		t.Fatalf("data = %v", data)
	}
	libs := st["libraries"].([]any)
	if len(libs) != 1 || libs[0].(map[string]any)["path"] != m.root || libs[0].(map[string]any)["library"] != "Cine" ||
		libs[0].(map[string]any)["readable"] != true {
		t.Fatalf("libraries = %v", libs)
	}
}

func TestAdminRegistros(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	logs := e.admin.Config.Paths.Logs

	// Sin archivo, una lista vacía y no un error.
	_, body, code := e.adminGet(own.token, "/admin/logs")
	if code != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("sin log: %d %s", code, body)
	}
	if err := os.MkdirAll(logs, 0o750); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"level":"info","module":"http","time":"2026-01-01T00:00:01Z","message":"uno"}`,
		`{"level":"warn","module":"scanner","time":"2026-01-01T00:00:02Z","message":"dos","path":"/stream/session/s1/TOKEN-SECRETO/file"}`,
		`{"level":"error","module":"transcoding","time":"2026-01-01T00:00:03Z","message":"tres","password":"hunter2"}`,
	}
	if err := os.WriteFile(filepath.Join(logs, "medianode.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, body, _ = e.adminGet(own.token, "/admin/logs?level=warn")
	got := parseJSON[[]map[string]any](t, body)
	if len(got) != 2 || got[0]["message"] != "tres" || got[1]["message"] != "dos" {
		t.Fatalf("level=warn: %s", body)
	}
	for _, secret := range []string{"TOKEN-SECRETO", "hunter2"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("el log sacó %q: %s", secret, body)
		}
	}
	_, body, _ = e.adminGet(own.token, "/admin/logs?q=scanner&lines=5")
	if got = parseJSON[[]map[string]any](t, body); len(got) != 1 || got[0]["module"] != "scanner" {
		t.Fatalf("q=scanner: %s", body)
	}
	_, body, _ = e.adminGet(own.token, "/admin/logs?lines=1")
	if got = parseJSON[[]map[string]any](t, body); len(got) != 1 || got[0]["message"] != "tres" {
		t.Fatalf("lines=1: %s", body)
	}
	for _, bad := range []string{"lines=0", "lines=-3", "lines=abc", "lines=5000", "level=ruido", "q=" + strings.Repeat("x", 201)} {
		if _, body, code = e.adminGet(own.token, "/admin/logs?"+bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
}

func TestAdminAjustesSinSecretos(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	e.admin.Config.Metadata.TMDBAPIKey = "clave-tmdb-secreta"

	_, body, code := e.adminGet(own.token, "/admin/settings")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(string(body), "clave-tmdb-secreta") {
		t.Fatalf("la clave de TMDB salió en los ajustes: %s", body)
	}
	cfg := parseJSON[map[string]any](t, body)
	if cfg["metadata"].(map[string]any)["tmdb_api_key"] != "(configurada)" {
		t.Fatalf("metadata = %v", cfg["metadata"])
	}
	if cfg["server"].(map[string]any)["http_port"].(float64) != 8420 {
		t.Fatalf("server = %v", cfg["server"])
	}
}

func TestAdminDiagnostico(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	e.movie(own.token)

	_, body, code := e.adminGet(own.token, "/admin/system-check")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	checks := parseJSON[[]map[string]any](t, body)
	if len(checks) != 12 {
		t.Fatalf("%d comprobaciones: %s", len(checks), body)
	}
	by := map[string]map[string]any{}
	for _, c := range checks {
		by[c["id"].(string)] = c
	}
	if by["ffmpeg"]["status"] != "fail" || by["database"]["status"] != "ok" || by["libraries"]["status"] != "ok" {
		t.Fatalf("%s", body)
	}
	// La carpeta de la biblioteca desaparece: la comprobación lo dice.
	rec, libs := e.do(call{method: http.MethodGet, path: apiBase + "/libraries", bearer: own.token})
	_ = libs
	e.want(rec, http.StatusOK)
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	root := list[0]["paths"].([]any)[0].(map[string]any)["path"].(string)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	_, body, _ = e.adminGet(own.token, "/admin/system-check")
	for _, c := range parseJSON[[]map[string]any](t, body) {
		if c["id"] == "libraries" && (c["status"] != "fail" || !strings.Contains(c["message"].(string), "no existe")) {
			t.Fatalf("biblioteca desaparecida: %v", c)
		}
	}
}

func TestAdminSoloParaAdministradores(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/admin/users", bearer: own.token, body: map[string]any{
		"username": "normal", "password": password, "role": "user",
	}})
	e.want(rec, http.StatusCreated)
	user := e.login("normal")["access_token"].(string)
	for _, p := range []string{"system", "metrics", "sessions", "network", "storage", "logs", "settings", "system-check"} {
		if _, body, code := e.adminGet(user, "/admin/"+p); code != http.StatusForbidden {
			t.Errorf("/admin/%s con un usuario normal: %d %s", p, code, body)
		}
		if _, _, code := e.adminGet("", "/admin/"+p); code != http.StatusUnauthorized {
			t.Errorf("/admin/%s sin sesión: %d", p, code)
		}
	}
}
