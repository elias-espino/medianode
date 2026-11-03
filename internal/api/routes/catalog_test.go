package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scanLibrary crea una biblioteca con esos archivos y espera a que termine el
// escaneo. Devuelve su identificador.
func (e *env) scanLibrary(token, kind string, files ...string) string {
	e.t.Helper()
	root := e.t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	rec, lib := e.do(call{method: http.MethodPost, path: apiBase + "/libraries", bearer: token, body: map[string]any{
		"name": kind, "type": kind, "paths": []string{root},
	}})
	e.want(rec, http.StatusCreated)
	id := lib["id"].(string)
	rec, _ = e.do(call{method: http.MethodPost, path: apiBase + "/libraries/" + id + "/scan", bearer: token})
	e.want(rec, http.StatusAccepted)
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec, body := e.do(call{method: http.MethodGet, path: apiBase + "/libraries/" + id + "/scan", bearer: token})
		if rec.Code == http.StatusOK && body["status"] == "completed" {
			return id
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("el escaneo no terminó: %s", rec.Body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) createUser(token, name, role string, maxAge *int) string {
	e.t.Helper()
	body := map[string]any{"username": name, "password": password, "role": role}
	if maxAge != nil {
		body["max_age_rating"] = *maxAge
	}
	rec, user := e.do(call{method: http.MethodPost, path: apiBase + "/admin/users", bearer: token, body: body})
	e.want(rec, http.StatusCreated)
	return user["id"].(string)
}

func (e *env) grant(token, userID, libID, perm string) {
	e.t.Helper()
	rec, _ := e.do(call{method: http.MethodPut, path: apiBase + "/admin/users/" + userID + "/permissions", bearer: token,
		body: map[string]any{"libraries": []map[string]any{{"library_id": libID, "permissions": []string{perm}}}}})
	e.want(rec, http.StatusOK)
}

func decodeList(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return out
}

func TestCatalogoPorAPI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	token := e.login(owner)["access_token"].(string)
	movies := e.scanLibrary(token, "movies", "Película (2001).mkv", "Zeta (1990).mkv")
	tv := e.scanLibrary(token, "tv", "Serie/Season 01/Serie S01E01.mkv", "Serie/Season 01/Serie S01E02.mkv")

	rec, page := e.do(call{method: http.MethodGet, path: apiBase + "/libraries/" + movies + "/items?limit=1", bearer: token})
	e.want(rec, http.StatusOK)
	items := page["items"].([]any)
	first := items[0].(map[string]any)
	if len(items) != 1 || first["title"] != "Película" || page["next_cursor"] == nil || first["poster_url"] != nil {
		t.Fatalf("%v", page)
	}
	next := apiBase + "/libraries/" + movies + "/items?limit=1&cursor=" + url.QueryEscape(page["next_cursor"].(string))
	rec, page = e.do(call{method: http.MethodGet, path: next, bearer: token})
	e.want(rec, http.StatusOK)
	if items := page["items"].([]any); len(items) != 1 || items[0].(map[string]any)["title"] != "Zeta" || page["next_cursor"] != nil {
		t.Fatalf("%v", page)
	}
	rec, body := e.do(call{method: http.MethodGet, path: apiBase + "/libraries/" + movies + "/items?limit=-1", bearer: token})
	if rec.Code != http.StatusBadRequest || errorCode(body) != "VALIDATION_ERROR" {
		t.Fatalf("%d %v", rec.Code, body)
	}

	movieID := first["id"].(string)
	rec, item := e.do(call{method: http.MethodGet, path: apiBase + "/media/" + movieID, bearer: token})
	e.want(rec, http.StatusOK)
	if item["year"] != float64(2001) || item["metadata_status"] != "pending" || item["metadata_locked"] != false {
		t.Fatalf("%v", item)
	}
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/media/" + movieID + "/files", bearer: token})
	e.want(rec, http.StatusOK)
	if files := decodeList(t, rec.Body.Bytes()); len(files) != 1 || files[0]["relative_path"] != "Película (2001).mkv" {
		t.Fatalf("%s", rec.Body)
	}
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/media/" + movieID + "/streams", bearer: token})
	e.want(rec, http.StatusOK)
	if streams := decodeList(t, rec.Body.Bytes()); len(streams) != 1 || streams[0]["type"] != "video" {
		t.Fatalf("%s", rec.Body)
	}
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/media/" + movieID + "/subtitles", bearer: token})
	e.want(rec, http.StatusOK)

	// Serie, temporada y episodios.
	rec, page = e.do(call{method: http.MethodGet, path: apiBase + "/libraries/" + tv + "/items", bearer: token})
	e.want(rec, http.StatusOK)
	seriesID := page["items"].([]any)[0].(map[string]any)["id"].(string)
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/media/" + seriesID + "/children", bearer: token})
	seasons := decodeList(t, rec.Body.Bytes())
	if len(seasons) != 1 || seasons[0]["season_number"] != float64(1) {
		t.Fatalf("%s", rec.Body)
	}
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/media/" + seasons[0]["id"].(string) + "/children", bearer: token})
	if eps := decodeList(t, rec.Body.Bytes()); len(eps) != 2 || eps[1]["episode_number"] != float64(2) {
		t.Fatalf("%s", rec.Body)
	}

	// Búsqueda sin acentos.
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/search?q=pelicula", bearer: token})
	e.want(rec, http.StatusOK)
	if rs := decodeList(t, rec.Body.Bytes()); len(rs) != 1 || rs[0]["id"] != movieID {
		t.Fatalf("%s", rec.Body)
	}

	// Edición manual: bloquea y cambia el título y el índice.
	rec, item = e.do(call{method: http.MethodPatch, path: apiBase + "/media/" + movieID + "/metadata", bearer: token,
		body: map[string]any{"title": "Odisea", "min_age": 0, "overview": nil}})
	e.want(rec, http.StatusOK)
	if item["title"] != "Odisea" || item["metadata_locked"] != true || item["min_age"] != float64(0) {
		t.Fatalf("%v", item)
	}
	rec, body = e.do(call{method: http.MethodPost, path: apiBase + "/media/" + movieID + "/refresh", bearer: token})
	if rec.Code != http.StatusServiceUnavailable || errorCode(body) != "METADATA_PROVIDER_UNAVAILABLE" {
		t.Fatalf("sin proveedor: %d %v", rec.Code, body)
	}

	// Imagen: privada, con ETag y 304.
	if err := os.MkdirAll(filepath.Join(e.imgDir, movieID), 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\n0000")
	if err := os.WriteFile(filepath.Join(e.imgDir, movieID, "poster.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Write.Exec("INSERT INTO media_images (id, media_item_id, type, local_path, source_url) VALUES ('i', ?, 'poster', ?, 'u')",
		movieID, movieID+"/poster.png"); err != nil {
		t.Fatal(err)
	}
	imgPath := apiBase + "/media/" + movieID + "/images/poster"
	rec, _ = e.do(call{method: http.MethodGet, path: imgPath, bearer: token})
	e.want(rec, http.StatusOK)
	etag := rec.Header().Get("ETag")
	if rec.Header().Get("Cache-Control") != "private, max-age=86400" || etag == "" ||
		rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() != len(png) {
		t.Fatalf("%v", rec.Header())
	}
	r := httpRequest(http.MethodGet, imgPath, token)
	r.Header.Set("If-None-Match", etag)
	rec = serve(e, r)
	e.want(rec, http.StatusNotModified)
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/media/" + movieID + "/images/backdrop", bearer: token})
	e.want(rec, http.StatusNotFound)
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/libraries/" + movies + "/items", bearer: token})
	if !strings.Contains(rec.Body.String(), `"poster_url":"/api/v1/media/`+movieID+`/images/poster"`) {
		t.Fatalf("el listado debe ofrecer el póster: %s", rec.Body)
	}

	// Un usuario con view en películas: sin rutas, sin series, sin editar.
	userID := e.createUser(token, "lector", "user", nil)
	e.grant(token, userID, movies, "view")
	userToken := e.login("lector")["access_token"].(string)
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/media/" + movieID + "/files", bearer: userToken})
	if files := decodeList(t, rec.Body.Bytes()); len(files) != 1 || files[0]["relative_path"] != nil {
		t.Fatalf("%s", rec.Body)
	}
	for _, path := range []string{"/media/" + seriesID, "/media/" + seriesID + "/children", "/libraries/" + tv + "/items"} {
		rec, _ = e.do(call{method: http.MethodGet, path: apiBase + path, bearer: userToken})
		e.want(rec, http.StatusNotFound)
	}
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/search?q=serie", bearer: userToken})
	if rs := decodeList(t, rec.Body.Bytes()); len(rs) != 0 {
		t.Fatalf("la búsqueda no debe mostrar bibliotecas sin permiso: %s", rec.Body)
	}
	rec, body = e.do(call{method: http.MethodPatch, path: apiBase + "/media/" + movieID + "/metadata", bearer: userToken,
		body: map[string]any{"title": "x"}})
	if rec.Code != http.StatusForbidden || errorCode(body) != "PERMISSION_LIBRARY_DENIED" {
		t.Fatalf("%d %v", rec.Code, body)
	}

	// Un restringido de 12 años ve la película con min_age 0, pero no la otra.
	kidID := e.createUser(token, "peque", "restricted", intp(12))
	e.grant(token, kidID, movies, "view")
	kidToken := e.login("peque")["access_token"].(string)
	rec, page = e.do(call{method: http.MethodGet, path: apiBase + "/libraries/" + movies + "/items", bearer: kidToken})
	if items := page["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != movieID {
		t.Fatalf("%s", rec.Body)
	}
	zeta := ""
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/search?q=zeta", bearer: token})
	if rs := decodeList(t, rec.Body.Bytes()); len(rs) == 1 {
		zeta = rs[0]["id"].(string)
	}
	for _, path := range []string{"/media/" + zeta, "/media/" + zeta + "/images/poster", "/media/" + zeta + "/files"} {
		rec, _ = e.do(call{method: http.MethodGet, path: apiBase + path, bearer: kidToken})
		e.want(rec, http.StatusNotFound)
	}
}

func TestEstadoYCapacidades(t *testing.T) {
	e := newEnv(t)
	rec, body := e.do(call{method: http.MethodGet, path: apiBase + "/server/capabilities"})
	e.want(rec, http.StatusOK)
	if body["search"] != true || body["metadata_provider"] != nil || body["transcoding"] != false {
		t.Fatalf("%v", body)
	}
	e.setup()
	token := e.login(owner)["access_token"].(string)
	rec, body = e.do(call{method: http.MethodGet, path: apiBase + "/server/status", bearer: token})
	e.want(rec, http.StatusOK)
	admin, _ := body["admin"].(map[string]any)
	md, _ := admin["metadata"].(map[string]any)
	if body["status"] != "ok" || md["configured"] != false || md["pending"] != float64(0) {
		t.Fatalf("%v", body)
	}
	e.createUser(token, "normal", "user", nil)
	userToken := e.login("normal")["access_token"].(string)
	rec, body = e.do(call{method: http.MethodGet, path: apiBase + "/server/status", bearer: userToken})
	e.want(rec, http.StatusOK)
	if _, ok := body["admin"]; ok {
		t.Fatalf("el detalle es para administradores: %v", body)
	}
}

func intp(n int) *int { return &n }

func httpRequest(method, path, bearer string) *http.Request {
	r, _ := http.NewRequest(method, path, nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("Authorization", "Bearer "+bearer)
	return r
}

func serve(e *env, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}
