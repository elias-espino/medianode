package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"medianode/internal/transcoding"
)

var multiAudioLayout = []streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8},
	{typ: "audio", codec: "aac", lang: "spa"}, {typ: "audio", codec: "aac", lang: "eng"}}

func (e *env) changeTracks(own accountToken, session string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	return e.do(call{method: http.MethodPost, path: apiBase + "/playback/sessions/" + session + "/tracks", bearer: own.token, body: body})
}

// audioLanguage devuelve el idioma de la pista de audio de un segmento TS.
func audioLanguage(t *testing.T, seg []byte) string {
	t.Helper()
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("falta ffprobe")
	}
	p := filepath.Join(t.TempDir(), "s.ts")
	if err := os.WriteFile(p, seg, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(probe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream_tags=language", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// Cambiar de pista de audio con la sesión en marcha: FFmpeg se reinicia con
// la pista nueva, en la posición pedida, sin pedir otro cupo.
func TestCambiarElAudioDeUnaSesionHLS(t *testing.T) {
	e, own, m := phase9Env(t, 1, 1)
	_, ids := m.fixtureFile("Cine.mkv", "mkv_multi_audio.mkv", multiAudioLayout)
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "selected_audio_stream_id": ids[1]})
	sid := str(resp["session_id"])
	if resp["selected_audio_stream_id"] != ids[1] || !strings.HasSuffix(str(resp["stream_url"]), "master.m3u8") {
		t.Fatalf("%v", resp)
	}
	base := strings.TrimSuffix(str(resp["stream_url"]), "master.m3u8")
	if rec := e.get(base+"seg_00000.ts", nil); rec.Code != http.StatusOK || audioLanguage(t, rec.Body.Bytes()) != "spa" {
		t.Fatalf("audio inicial: %d", rec.Code)
	}
	before, _ := e.transcoder.Admission().Usage()

	rec, changed := e.changeTracks(own, sid, map[string]any{"selected_audio_stream_id": ids[2], "position_ms": 2500})
	e.want(rec, http.StatusOK)
	if changed["selected_audio_stream_id"] != ids[2] || changed["session_id"] != sid {
		t.Fatalf("%v", changed)
	}
	url := str(changed["stream_url"])
	if !strings.HasSuffix(url, "master.m3u8?rev=1") || !strings.HasPrefix(url, base) {
		t.Fatalf("stream_url = %q", url)
	}
	// La lista nueva sirve la pista nueva.
	if rec = e.get(base+"media.m3u8", nil); rec.Code != http.StatusOK {
		t.Fatalf("media.m3u8: %d", rec.Code)
	}
	if rec = e.get(base+"seg_00000.ts", nil); rec.Code != http.StatusOK || audioLanguage(t, rec.Body.Bytes()) != "eng" {
		t.Fatalf("audio tras el cambio: %d %q", rec.Code, audioLanguage(t, rec.Body.Bytes()))
	}
	// El cupo es el mismo: no se pidió otro ni se perdió.
	if after, _ := e.transcoder.Admission().Usage(); after[transcoding.Light] != before[transcoding.Light] || after[transcoding.Heavy] != before[transcoding.Heavy] {
		t.Fatalf("cupos antes %v, después %v", before, after)
	}
	// La selección quedó guardada en la sesión.
	var stored string
	if err := e.db.Write.QueryRow("SELECT selected_audio_stream_id FROM playback_sessions WHERE id = ?", sid).Scan(&stored); err != nil || stored != ids[2] {
		t.Fatalf("guardado %q: %v", stored, err)
	}
	// Otro cambio: la revisión sube.
	rec, again := e.changeTracks(own, sid, map[string]any{"selected_audio_stream_id": ids[1]})
	e.want(rec, http.StatusOK)
	if !strings.HasSuffix(str(again["stream_url"]), "?rev=2") {
		t.Fatalf("stream_url = %q", again["stream_url"])
	}
	if rec = e.get(base+"seg_00000.ts", nil); rec.Code != http.StatusOK || audioLanguage(t, rec.Body.Bytes()) != "spa" {
		t.Fatalf("audio al volver: %d", rec.Code)
	}
}

func TestCambiarPistasValidaLaPeticion(t *testing.T) {
	e, own, m := phase9Env(t, 1, 1)
	_, ids := m.fixtureFile("Cine.mkv", "mkv_multi_audio.mkv", multiAudioLayout)
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile})
	sid := str(resp["session_id"])

	for name, body := range map[string]map[string]any{
		"sin nada que cambiar":    {},
		"audio de otro archivo":   {"selected_audio_stream_id": "no-es-de-este-archivo"},
		"posición negativa":       {"selected_audio_stream_id": ids[1], "position_ms": -5},
		"subtítulo que no existe": {"selected_subtitle_id": "nada"},
		"campo desconocido":       {"selected_audio_stream_id": ids[1], "otro": true},
	} {
		rec, _ := e.changeTracks(own, sid, body)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec, _ := e.changeTracks(own, "no-existe", map[string]any{"selected_audio_stream_id": ids[1]}); rec.Code != http.StatusNotFound {
		t.Errorf("sesión inexistente: %d", rec.Code)
	}
	// La sesión de otro usuario no se toca.
	rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/admin/users", bearer: own.token, body: map[string]any{
		"username": "otra", "password": password, "role": "user",
	}})
	e.want(rec, http.StatusCreated)
	other := e.login("otra")["access_token"].(string)
	if rec, _ := e.changeTracks(accountToken{other}, sid, map[string]any{"selected_audio_stream_id": ids[1]}); rec.Code != http.StatusNotFound {
		t.Errorf("sesión ajena: %d", rec.Code)
	}
	// Una sesión terminada no cambia de pistas.
	e.closeSession(own, resp)
	if rec, _ := e.changeTracks(own, sid, map[string]any{"selected_audio_stream_id": ids[1]}); rec.Code != http.StatusGone {
		t.Errorf("sesión terminada: %d %s", rec.Code, rec.Body)
	}
}

// Si no cabe en el cupo, el cambio se rechaza y la sesión sigue como estaba.
func TestCambiarPistasSinCupoConservaLaSesion(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", srtLayout)
	// Sin ninguna entrega de subtítulos: solo se pueden grabar (recodificar).
	prof := profileWith(map[string]any{"subtitle_delivery": []string{}, "subtitle_formats": []string{}})
	light := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": prof, "selected_subtitle_id": "none"})
	if light["playback_mode"] != "direct_stream" {
		t.Fatalf("%v", light)
	}
	// Otra sesión ocupa el único cupo pesado.
	heavy := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": prof, "selected_subtitle_id": "none", "max_bitrate": 600000})
	if heavy["playback_mode"] != "transcode" {
		t.Fatalf("%v", heavy)
	}
	base := strings.TrimSuffix(str(light["stream_url"]), "master.m3u8")

	rec, _ := e.changeTracks(own, str(light["session_id"]), map[string]any{"selected_subtitle_id": ids[2]})
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "TRANSCODE_CAPACITY_EXCEEDED") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	// La sesión conserva su cupo ligero y su flujo.
	if used, _ := e.transcoder.Admission().Usage(); used[transcoding.Light] != 1 || used[transcoding.Heavy] != 1 {
		t.Fatalf("cupos: %v", used)
	}
	if rec = e.get(base+"seg_00000.ts", nil); rec.Code != http.StatusOK {
		t.Fatalf("la sesión dejó de servir: %d", rec.Code)
	}
	// Liberado el cupo pesado, el mismo cambio funciona y la sesión pasa a pesada.
	e.closeSession(own, heavy)
	rec, changed := e.changeTracks(own, str(light["session_id"]), map[string]any{"selected_subtitle_id": ids[2]})
	e.want(rec, http.StatusOK)
	if changed["subtitle_mode"] != "burn_in" || changed["container"] != "fmp4" || changed["playback_mode"] != "transcode" {
		t.Fatalf("%v", changed)
	}
	if used, _ := e.transcoder.Admission().Usage(); used[transcoding.Heavy] != 1 || used[transcoding.Light] != 0 {
		t.Fatalf("la sesión debía pasar al cupo pesado: %v", used)
	}
	if rec = e.get(base+"init.mp4", nil); rec.Code != http.StatusOK {
		t.Fatalf("init del flujo nuevo: %d", rec.Code)
	}
}

// En Direct Play el archivo llega entero: cambiar la selección es solo
// guardarla; lo que exigiría transcodificar pide otra sesión.
func TestCambiarPistasEnDirectPlay(t *testing.T) {
	e, own, m := phase9Env(t, 1, 1)
	file, audio := m.file("Cine.mp4", "mp4", "h264", "aac", 360)
	ass, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "sample.ass"))
	if err != nil {
		t.Skip("falta sample.ass")
	}
	sub := m.subtitle(file, "Cine.es.ass", string(ass))
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": webProfile, "selected_subtitle_id": "none"})
	if resp["playback_mode"] != "direct_play" || resp["subtitle_url"] != nil {
		t.Fatalf("%v", resp)
	}
	rec, changed := e.changeTracks(own, str(resp["session_id"]), map[string]any{"selected_audio_stream_id": audio, "selected_subtitle_id": sub})
	e.want(rec, http.StatusOK)
	if changed["stream_url"] != resp["stream_url"] || changed["selected_subtitle_id"] != sub ||
		!strings.HasSuffix(str(changed["subtitle_url"]), "/subtitles/"+sub+".vtt") {
		t.Fatalf("%v", changed)
	}
	if rec = e.get(str(changed["subtitle_url"]), nil); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "WEBVTT") {
		t.Fatalf("subtítulo: %d", rec.Code)
	}
	// Desactivarlos vuelve a quitar la URL.
	rec, off := e.changeTracks(own, str(resp["session_id"]), map[string]any{"selected_subtitle_id": "none"})
	e.want(rec, http.StatusOK)
	if off["subtitle_url"] != nil || off["selected_subtitle_id"] != nil {
		t.Fatalf("%v", off)
	}
}
