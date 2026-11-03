package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"medianode/internal/database"
)

// movieFixture crea a mano una película con archivos de verdad en disco: el
// FFprobe simulado de las rutas no da códecs.
type movieFixture struct {
	e       *env
	lib     string
	root    string
	pathID  string
	item    string
	content []byte
}

func (e *env) movie(token string) *movieFixture {
	e.t.Helper()
	m := &movieFixture{e: e, root: e.t.TempDir()}
	rec, lib := e.do(call{method: http.MethodPost, path: apiBase + "/libraries", bearer: token, body: map[string]any{
		"name": "Cine", "type": "movies", "paths": []string{m.root},
	}})
	e.want(rec, http.StatusCreated)
	m.lib = lib["id"].(string)
	m.pathID = lib["paths"].([]any)[0].(map[string]any)["id"].(string)
	m.item = m.id()
	m.exec("INSERT INTO media_items (id, library_id, type, title, sort_title, min_age) VALUES (?, ?, 'movie', 'Prueba', 'prueba', 0)", m.item, m.lib)
	m.content = bytes.Repeat([]byte("0123456789"), 1000)
	return m
}

func (m *movieFixture) id() string {
	id, err := database.NewID()
	if err != nil {
		m.e.t.Fatal(err)
	}
	return id
}

func (m *movieFixture) exec(q string, args ...any) {
	m.e.t.Helper()
	if _, err := m.e.db.Write.Exec(q, args...); err != nil {
		m.e.t.Fatal(err)
	}
}

// file añade una versión con una pista de vídeo y otra de audio, y devuelve
// el archivo y la pista de audio.
func (m *movieFixture) file(name, container, vcodec, acodec string, height int64) (string, string) {
	m.e.t.Helper()
	if err := os.WriteFile(filepath.Join(m.root, name), m.content, 0o644); err != nil {
		m.e.t.Fatal(err)
	}
	file, video, audio := m.id(), m.id(), m.id()
	m.exec(`INSERT INTO media_files (id, library_path_id, relative_path, container, file_size, bitrate, duration_ms, probe_status)
		VALUES (?, ?, ?, ?, ?, ?, 100000, 'done')`, file, m.pathID, name, container, len(m.content), height*1000)
	m.exec("INSERT INTO media_file_items VALUES (?, ?, 0)", file, m.item)
	m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, width, height, bit_depth)
		VALUES (?, ?, 0, 'video', ?, ?, ?, 8)`, video, file, vcodec, height*16/9, height)
	m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, channels, language)
		VALUES (?, ?, 1, 'audio', ?, 2, 'spa')`, audio, file, acodec)
	return file, audio
}

func (m *movieFixture) subtitle(file, name, content string) string {
	m.e.t.Helper()
	if err := os.WriteFile(filepath.Join(m.root, name), []byte(content), 0o644); err != nil {
		m.e.t.Fatal(err)
	}
	id := m.id()
	m.exec("INSERT INTO subtitles (id, media_file_id, relative_path, language, format) VALUES (?, ?, ?, 'es', ?)",
		id, file, name, strings.TrimPrefix(filepath.Ext(name), "."))
	return id
}

var webProfile = map[string]any{"platform": "web"}

func (e *env) play(token string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	return e.do(call{method: http.MethodPost, path: apiBase + "/playback/sessions", bearer: token, body: body})
}

func (e *env) get(path string, headers map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func TestDirectPlayYEntregaDelArchivo(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	file, audio := m.file("Prueba.mp4", "mp4", "h264", "aac", 1080)
	srt := m.subtitle(file, "Prueba.es.srt", "1\r\n00:00:01,000 --> 00:00:02,000\r\nHola\r\n")

	rec, body := e.play(own.token, map[string]any{
		"media_item_id": m.item, "client_profile": webProfile, "selected_subtitle_id": srt, "start_position_ms": 5000,
	})
	e.want(rec, http.StatusCreated)
	if body["playback_mode"] != "direct_play" || body["protocol"] != "http" || body["video_action"] != "copy" ||
		body["subtitle_mode"] != "convert" || body["selected_audio_stream_id"] != audio || body["media_file_id"] != file ||
		body["status"] != "initializing" || body["start_position_ms"] != float64(5000) || len(body["reasons"].([]any)) != 0 {
		t.Fatalf("%v", body)
	}
	stream := body["stream_url"].(string)
	if !strings.HasPrefix(stream, "/stream/session/"+body["session_id"].(string)+"/") || strings.Contains(stream, m.root) {
		t.Fatalf("URL opaca sin rutas del servidor: %s", stream)
	}

	// Archivo completo, HEAD y rangos.
	rec = e.get(stream, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), m.content) || rec.Header().Get("Content-Type") != "video/mp4" ||
		rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	head := httptest.NewRequest(http.MethodHead, stream, nil)
	hrec := httptest.NewRecorder()
	e.h.ServeHTTP(hrec, head)
	if hrec.Code != http.StatusOK || hrec.Body.Len() != 0 || hrec.Header().Get("Content-Length") != "10000" {
		t.Fatalf("HEAD: %d %v", hrec.Code, hrec.Header())
	}
	for _, c := range []struct {
		rng   string
		want  []byte
		crang string
	}{
		{"bytes=0-9", m.content[:10], "bytes 0-9/10000"},
		{"bytes=5000-5009", m.content[5000:5010], "bytes 5000-5009/10000"},
		{"bytes=-5", m.content[9995:], "bytes 9995-9999/10000"},
		{"bytes=9990-", m.content[9990:], "bytes 9990-9999/10000"},
	} {
		rec = e.get(stream, map[string]string{"Range": c.rng})
		if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), c.want) || rec.Header().Get("Content-Range") != c.crang {
			t.Fatalf("%s: %d %q %v", c.rng, rec.Code, rec.Body.String(), rec.Header())
		}
	}
	if rec = e.get(stream, map[string]string{"Range": "bytes=20000-"}); rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("rango fuera del archivo: %d", rec.Code)
	}

	// Subtítulo convertido a WebVTT.
	sub := body["subtitle_url"].(string)
	if !strings.HasSuffix(sub, "/subtitles/"+srt+".vtt") {
		t.Fatalf("%s", sub)
	}
	rec = e.get(sub, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/vtt; charset=utf-8" ||
		rec.Body.String() != "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\nHola\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if rec = e.get(strings.Replace(sub, ".vtt", ".ass", 1), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("formato inventado: %d", rec.Code)
	}

	// Un token válido de otra sesión no abre esta.
	rec, other := e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": webProfile})
	e.want(rec, http.StatusCreated)
	otherToken := strings.Split(other["stream_url"].(string), "/")[4]
	parts := strings.Split(stream, "/")
	parts[4] = otherToken
	if rec = e.get(strings.Join(parts, "/"), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("token de otra sesión: %d", rec.Code)
	}

	// Cerrar la sesión invalida la URL.
	rec, _ = e.do(call{method: http.MethodDelete, path: apiBase + "/playback/sessions/" + body["session_id"].(string), bearer: own.token})
	e.want(rec, http.StatusNoContent)
	rec = e.get(stream, nil)
	if rec.Code != http.StatusGone || errorCode(decode(rec)) != "SESSION_EXPIRED" {
		t.Fatalf("tras cerrar: %d %s", rec.Code, rec.Body)
	}
}

func decode(rec *httptest.ResponseRecorder) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestEligeLaMejorVersionCompatible(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.file("Prueba 720p.mp4", "mp4", "h264", "aac", 720)
	best, _ := m.file("Prueba 1080p.mp4", "mp4", "h264", "aac", 1080)
	m.file("Prueba 4K.mkv", "mkv", "hevc", "dts", 2160)

	rec, body := e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": webProfile})
	e.want(rec, http.StatusCreated)
	if body["media_file_id"] != best {
		t.Fatalf("la de mayor calidad que se reproduce directa: %v", body)
	}
	// Con un límite de resolución gana la de 720p.
	rec, body = e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": webProfile, "max_resolution": "1280x720"})
	e.want(rec, http.StatusCreated)
	if body["media_file_id"] == best || body["playback_mode"] != "direct_play" {
		t.Fatalf("%v", body)
	}
}

func TestRechazosDeReproduccion(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	mkv, _ := m.file("Prueba.mkv", "mkv", "h264", "aac", 1080)
	_, foreignAudio := m.file("Otra.mp4", "mp4", "h264", "aac", 720)

	// Hasta la Fase 8 no hay remux: la decisión se explica.
	rec, body := e.play(own.token, map[string]any{"media_item_id": m.item, "media_file_id": mkv, "client_profile": webProfile})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(body) != "PLAYBACK_UNSUPPORTED" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	details := body["error"].(map[string]any)["details"].(map[string]any)
	if details["playback_mode"] != "direct_stream" || !strings.Contains(rec.Body.String(), "container_unsupported") {
		t.Fatalf("%v", details)
	}
	// Una pista de otro archivo se rechaza.
	rec, body = e.play(own.token, map[string]any{"media_item_id": m.item, "media_file_id": mkv,
		"client_profile": map[string]any{"platform": "android"}, "selected_audio_stream_id": foreignAudio})
	if rec.Code != http.StatusBadRequest || errorCode(body) != "VALIDATION_ERROR" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	// Mismo archivo en Android: Direct Play.
	rec, body = e.play(own.token, map[string]any{"media_item_id": m.item, "media_file_id": mkv, "client_profile": map[string]any{"platform": "android"}})
	if rec.Code != http.StatusCreated || body["playback_mode"] != "direct_play" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	for name, c := range map[string]struct {
		body   map[string]any
		status int
		code   string
	}{
		"dispositivo ajeno":  {map[string]any{"media_item_id": m.item, "device_id": "otro"}, 400, "VALIDATION_ERROR"},
		"perfil inválido":    {map[string]any{"media_item_id": m.item, "client_profile": map[string]any{"max_resolution": "x"}}, 400, "VALIDATION_ERROR"},
		"campo desconocido":  {map[string]any{"media_item_id": m.item, "client_profile": map[string]any{"codecs": []string{"h264"}}}, 400, "VALIDATION_ERROR"},
		"posición negativa":  {map[string]any{"media_item_id": m.item, "start_position_ms": -1}, 400, "VALIDATION_ERROR"},
		"elemento inventado": {map[string]any{"media_item_id": "no-existe"}, 404, "NOT_FOUND"},
		"archivo de otro":    {map[string]any{"media_item_id": m.item, "media_file_id": "no-existe"}, 404, "NOT_FOUND"},
	} {
		rec, body := e.play(own.token, c.body)
		if rec.Code != c.status || errorCode(body) != c.code {
			t.Errorf("%s: %d %v", name, rec.Code, body)
		}
	}
	// Archivo ausente en disco.
	m.exec("UPDATE media_files SET missing_since = '2026-01-01T00:00:00.000Z'")
	rec, body = e.play(own.token, map[string]any{"media_item_id": m.item})
	if rec.Code != http.StatusConflict || errorCode(body) != "MEDIA_FILE_UNAVAILABLE" {
		t.Fatalf("%d %v", rec.Code, body)
	}
}

// Retirar el permiso corta el streaming de una sesión ya abierta.
func TestPermisoRetiradoCortaElStreaming(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.file("Prueba.mp4", "mp4", "h264", "aac", 1080)
	userID := e.createUser(own.token, "lector", "user", nil)
	e.grant(own.token, userID, m.lib, "view")
	reader := e.account("lector")
	e.playback.AuthTTL = 0

	rec, body := e.play(reader.token, map[string]any{"media_item_id": m.item, "client_profile": webProfile})
	e.want(rec, http.StatusCreated)
	stream := body["stream_url"].(string)
	if rec = e.get(stream, map[string]string{"Range": "bytes=0-0"}); rec.Code != http.StatusPartialContent {
		t.Fatalf("%d", rec.Code)
	}
	rec, _ = e.do(call{method: http.MethodPut, path: apiBase + "/admin/users/" + userID + "/permissions", bearer: own.token,
		body: map[string]any{"libraries": []any{}}})
	e.want(rec, http.StatusOK)
	if rec = e.get(stream, map[string]string{"Range": "bytes=0-0"}); rec.Code != http.StatusGone {
		t.Fatalf("sin permiso: %d", rec.Code)
	}
	if e.sessions.Active() != 0 {
		t.Fatal("la sesión debía cerrarse")
	}

}

// Casos de la entrega con la sesión ya abierta: archivo desaparecido,
// subtítulo de otro archivo y usuario desactivado sin pasar por la API.
func TestEntregaConLaSesionAbierta(t *testing.T) {
	e := newEnv(t)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	file, _ := m.file("Prueba.mp4", "mp4", "h264", "aac", 1080)
	other, _ := m.file("Otra.mp4", "mp4", "h264", "aac", 720)
	foreign := m.subtitle(other, "Otra.es.vtt", "WEBVTT\n")
	userID := e.createUser(own.token, "lector", "user", nil)
	e.grant(own.token, userID, m.lib, "view")
	reader := e.account("lector")
	e.playback.AuthTTL = 0

	rec, body := e.play(reader.token, map[string]any{"media_item_id": m.item, "media_file_id": file, "client_profile": webProfile})
	e.want(rec, http.StatusCreated)
	stream := body["stream_url"].(string)
	base := strings.TrimSuffix(stream, "/file")
	if rec = e.get(base+"/subtitles/"+foreign+".vtt", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("subtítulo de otro archivo: %d", rec.Code)
	}

	m.exec("UPDATE media_files SET missing_since = '2026-01-01T00:00:00.000Z' WHERE id = ?", file)
	if rec = e.get(stream, nil); rec.Code != http.StatusConflict || errorCode(decode(rec)) != "MEDIA_FILE_UNAVAILABLE" {
		t.Fatalf("archivo desaparecido: %d %s", rec.Code, rec.Body)
	}
	m.exec("UPDATE media_files SET missing_since = NULL WHERE id = ?", file)
	if rec = e.get(stream, map[string]string{"Range": "bytes=0-0"}); rec.Code != http.StatusPartialContent {
		t.Fatalf("%d", rec.Code)
	}

	m.exec("UPDATE users SET is_active = 0 WHERE id = ?", userID)
	if rec = e.get(stream, map[string]string{"Range": "bytes=0-0"}); rec.Code != http.StatusGone {
		t.Fatalf("usuario desactivado: %d", rec.Code)
	}
}
