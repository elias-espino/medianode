package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"medianode/internal/ffmpeg"
	"medianode/internal/testutil"
)

// withFFmpeg activa el transcodificador con los límites dados. Se salta la
// prueba si no hay FFmpeg o con -short.
func withFFmpeg(t *testing.T, heavy, light int) func(*envOptions) {
	t.Helper()
	if testing.Short() {
		t.Skip("necesita FFmpeg y tarda")
	}
	ff, err := ffmpeg.Locate()
	if err != nil {
		t.Skip("FFmpeg no está instalado")
	}
	fp, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("FFprobe no está instalado")
	}
	return func(o *envOptions) { o.ffmpeg, o.ffprobe, o.heavy, o.light = ff, fp, heavy, light }
}

// realFile añade como versión del elemento un vídeo de verdad. codec es lo
// que dice la base: declarar «hevc» sobre un H.264 real obliga a recodificar,
// que es lo que se quiere probar.
func (m *movieFixture) realFile(name, container, codec string, withAudio bool) string {
	m.e.t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "fixtures", "seek_120s_h264.mkv"))
	if err != nil {
		m.e.t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		m.e.t.Skip("falta el fixture; genéralo con scripts/gen-fixtures")
	}
	if err := os.WriteFile(filepath.Join(m.root, name), data, 0o644); err != nil {
		m.e.t.Fatal(err)
	}
	m.size = int64(len(data))
	file, video := m.id(), m.id()
	m.exec(`INSERT INTO media_files (id, library_path_id, relative_path, container, file_size, bitrate, duration_ms, modified_at, probe_status)
		VALUES (?, ?, ?, ?, ?, 900000, 120023, '2026-01-01T00:00:00.000Z', 'done')`, file, m.pathID, name, container, len(data))
	m.exec("INSERT INTO media_file_items VALUES (?, ?, 0)", file, m.item)
	m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, profile, level, width, height, bit_depth)
		VALUES (?, ?, 0, 'video', ?, 'High', '31', 640, 360, 8)`, video, file, codec)
	if withAudio {
		m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, channels, language)
			VALUES (?, ?, 1, 'audio', 'aac', 2, 'spa')`, m.id(), file)
	}
	return file
}

// hlsProfile es un navegador con hls.js: reproduce HLS además de HTTP.
var hlsProfile = map[string]any{"platform": "web", "protocols": []string{"http", "hls"}}

var reSeg = regexp.MustCompile(`seg_\d{5}\.(m4s|ts)`)

func TestHLSRecodificandoPorAPI(t *testing.T) {
	e := newEnv(t)
	opt := withFFmpeg(t, 1, 1)
	e = newEnvOn(t, e.db, opt)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.realFile("Prueba.mkv", "mkv", "hevc", false) // «hevc» no está en el perfil web.

	rec, body := e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile})
	e.want(rec, http.StatusCreated)
	if body["playback_mode"] != "transcode" || body["protocol"] != "hls" || body["video_action"] != "transcode" ||
		body["container"] != "fmp4" || body["audio_action"] != "none" {
		t.Fatalf("%v", body)
	}
	reasons := body["reasons"].([]any)
	if len(reasons) == 0 || reasons[0].(map[string]any)["code"] != "video_codec_unsupported" {
		t.Fatalf("%v", reasons)
	}
	master := body["stream_url"].(string)
	if !strings.HasSuffix(master, "/master.m3u8") {
		t.Fatalf("%s", master)
	}
	base := strings.TrimSuffix(master, "master.m3u8")

	rec = e.get(master, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" ||
		!strings.Contains(rec.Body.String(), "media.m3u8") || !strings.Contains(rec.Body.String(), "RESOLUTION=640x360") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	rec = e.get(base+"media.m3u8", nil)
	segs := reSeg.FindAllString(rec.Body.String(), -1)
	if rec.Code != http.StatusOK || len(segs) != 21 || !strings.Contains(rec.Body.String(), "#EXT-X-ENDLIST") ||
		!strings.Contains(rec.Body.String(), `#EXT-X-MAP:URI="init.mp4"`) {
		t.Fatalf("%d, %d segmentos: %s", rec.Code, len(segs), rec.Body)
	}

	// El primer segmento y el init llegan; un salto lejano reinicia FFmpeg en menos de 5 s.
	if rec = e.get(base+"init.mp4", nil); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "video/mp4" || rec.Body.Len() < 100 {
		t.Fatalf("init: %d, tipo %q, %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec = e.get(base+"seg_00000.m4s", nil); rec.Code != http.StatusOK || rec.Body.Len() < 1000 {
		t.Fatalf("segmento 0: %d", rec.Code)
	}
	start := time.Now()
	rec = e.get(base+"seg_00015.m4s", nil)
	if rec.Code != http.StatusOK || time.Since(start) > 5*time.Second {
		t.Fatalf("salto: %d en %s", rec.Code, time.Since(start))
	}
	// Rango sobre un segmento.
	if rec = e.get(base+"seg_00015.m4s", map[string]string{"Range": "bytes=0-9"}); rec.Code != http.StatusPartialContent || rec.Body.Len() != 10 {
		t.Fatalf("rango: %d", rec.Code)
	}

	// Lo que no es un recurso del trabajo no existe.
	for _, bad := range []string{"seg_99999.m4s", "seg_00001.ts", "ffmpeg_0.m3u8", "list_0.csv", "..%2Fseg_00000.m4s", "seg_1.m4s", "init_0.mp4"} {
		if rec = e.get(base+bad, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// El original no se sirve a una sesión que debía recodificarse.
	if rec = e.get(base+"file", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("/file en una sesión transcodificada: %d", rec.Code)
	}
	// Otro token no abre esta sesión.
	parts := strings.Split(master, "/")
	parts[4] = strings.Repeat("A", len(parts[4]))
	if rec = e.get(strings.Join(parts, "/"), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("token falso: %d", rec.Code)
	}

	// Administración: el trabajo figura y se puede cancelar.
	rec, list := e.do(call{method: http.MethodGet, path: apiBase + "/admin/transcodes", bearer: own.token})
	e.want(rec, http.StatusOK)
	jobs := list["jobs"].([]any)
	capacity := list["capacity"].(map[string]any)["heavy"].(map[string]any)
	if len(jobs) != 1 || capacity["used"] != float64(1) || capacity["limit"] != float64(1) {
		t.Fatalf("%v", list)
	}
	job := jobs[0].(map[string]any)
	if job["session_id"] != body["session_id"] || job["kind"] != "heavy" || job["encoder"] != "libx264" {
		t.Fatalf("%v", job)
	}
	rec, _ = e.do(call{method: http.MethodPost, path: apiBase + "/admin/transcodes/" + job["id"].(string) + "/cancel", bearer: own.token})
	e.want(rec, http.StatusNoContent)
	if rec = e.get(master, nil); rec.Code != http.StatusGone {
		t.Fatalf("tras cancelar: %d", rec.Code)
	}
	rec, _ = e.do(call{method: http.MethodPost, path: apiBase + "/admin/transcodes/" + job["id"].(string) + "/cancel", bearer: own.token})
	e.want(rec, http.StatusNotFound)
	if used, _ := e.transcoder.Admission().Usage(); used["heavy"] != 0 {
		t.Fatal("cancelar debía liberar el cupo")
	}
	// Un usuario normal no administra trabajos.
	e.createUser(own.token, "normal", "user", nil)
	other := e.account("normal")
	rec, _ = e.do(call{method: http.MethodGet, path: apiBase + "/admin/transcodes", bearer: other.token})
	e.want(rec, http.StatusForbidden)
}

// Sin cupo se responde 503 con Retry-After y no se crea la sesión.
func TestSinCupoNoCreaSesion(t *testing.T) {
	opt := withFFmpeg(t, 1, 1)
	e := newEnvOn(t, testutil.NewDB(t), opt)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.realFile("Prueba.mkv", "mkv", "hevc", false)
	play := func() (*httptest.ResponseRecorder, map[string]any) {
		return e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile})
	}

	rec, first := play()
	e.want(rec, http.StatusCreated)
	rec, body := play()
	if rec.Code != http.StatusServiceUnavailable || errorCode(body) != "TRANSCODE_CAPACITY_EXCEEDED" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d %v %v", rec.Code, body, rec.Header())
	}
	if e.sessions.Active() != 1 {
		t.Fatalf("la sesión rechazada no debe existir: %d activas", e.sessions.Active())
	}
	var n int
	if err := e.db.Read.QueryRow("SELECT COUNT(*) FROM playback_sessions").Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d filas: %v", n, err)
	}
	// Cerrar la primera libera el cupo.
	rec, _ = e.do(call{method: http.MethodDelete, path: apiBase + "/playback/sessions/" + first["session_id"].(string), bearer: own.token})
	e.want(rec, http.StatusNoContent)
	rec, _ = play()
	e.want(rec, http.StatusCreated)
}

// Copiando el vídeo, sin y con índice de keyframes: EVENT primero, VOD después.
func TestHLSCopiandoPorAPI(t *testing.T) {
	opt := withFFmpeg(t, 1, 2)
	e := newEnvOn(t, testutil.NewDB(t), opt)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	fileID := m.realFile("Prueba.mkv", "mkv", "h264", false)

	rec, body := e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile})
	e.want(rec, http.StatusCreated)
	if body["playback_mode"] != "direct_stream" || body["video_action"] != "copy" || body["container"] != "ts" {
		t.Fatalf("%v", body)
	}
	base := strings.TrimSuffix(body["stream_url"].(string), "master.m3u8")
	if rec = e.get(base+"seg_00000.ts", nil); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "video/mp2t" || rec.Body.Len() < 1000 {
		t.Fatalf("segmento: %d", rec.Code)
	}
	// Crear la sesión espera al índice de un archivo nuevo (indexWait): la
	// primera ya recibe la playlist VOD y puede saltar, sin pasar por EVENT.
	rec = e.get(base+"media.m3u8", nil)
	if !strings.Contains(rec.Body.String(), "PLAYLIST-TYPE:VOD") || !strings.Contains(rec.Body.String(), "#EXT-X-ENDLIST") ||
		strings.Contains(rec.Body.String(), "EXT-X-MAP") || len(reSeg.FindAllString(rec.Body.String(), -1)) < 10 {
		t.Fatalf("con índice, VOD y sin EXT-X-MAP:\n%s", rec.Body)
	}
	if idx := e.indexer.Get(fileID, m.size, "2026-01-01T00:00:00.000Z"); idx == nil {
		t.Fatal("la sesión no esperó al índice")
	}
	if rec = e.get(base+"seg_00009.ts", nil); rec.Code != http.StatusOK {
		t.Fatalf("salto con índice: %d", rec.Code)
	}
}

// Los cambios de sesión liberan lo que ocupaba el trabajo.
func TestCerrarSesionLimpiaElTrabajo(t *testing.T) {
	opt := withFFmpeg(t, 1, 1)
	e := newEnvOn(t, testutil.NewDB(t), opt)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.realFile("Prueba.mkv", "mkv", "hevc", false)
	rec, body := e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile})
	e.want(rec, http.StatusCreated)
	base := strings.TrimSuffix(body["stream_url"].(string), "master.m3u8")
	if rec = e.get(base+"seg_00000.m4s", nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	dir := filepath.Join(e.transcoder.Dir(), body["session_id"].(string))
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("el directorio del trabajo debía existir: %v", err)
	}
	rec, _ = e.do(call{method: http.MethodDelete, path: apiBase + "/playback/sessions/" + body["session_id"].(string), bearer: own.token})
	e.want(rec, http.StatusNoContent)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("el directorio sigue: %v", err)
	}
	if len(e.transcoder.Jobs()) != 0 {
		t.Fatal("FFmpeg sigue vivo")
	}
}
