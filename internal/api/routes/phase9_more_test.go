package routes

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Un subtítulo que se entrega aparte no se graba aunque el vídeo se
// recodifique: solo el modo burn_in toca la imagen.
func TestSubtituloConvertidoNoSeGrabaAunqueSeRecodifique(t *testing.T) {
	e, own, m := phase9Env(t, 1, 1)
	// «hevc» declarado sobre un H.264 real fuerza recodificar el vídeo.
	layout := []streamRow{{typ: "video", codec: "hevc", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"},
		{typ: "subtitle", codec: "subrip", lang: "spa"}}
	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", layout)
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "selected_subtitle_id": ids[2]})
	if resp["playback_mode"] != "transcode" || resp["subtitle_mode"] != "convert" || resp["subtitle_url"] == nil {
		t.Fatalf("%v", resp)
	}
	dir := filepath.Join(e.transcoder.Dir(), str(resp["session_id"]))
	if _, err := os.Stat(filepath.Join(dir, "sub.ass")); err == nil {
		t.Fatal("el subtítulo se preparó para grabarlo, y va aparte")
	}
}

// Sin position_ms, el cambio de pista continúa donde iba la sesión.
func TestCambiarPistasContinuaDondeIbaLaSesion(t *testing.T) {
	e, own, m := phase9Env(t, 1, 1)
	layout := []streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"}}
	file, ids := m.fixtureFile("Largo.mkv", "seek_120s_h264.mkv", layout)
	m.exec("UPDATE media_files SET duration_ms = 120023 WHERE id = ?", file)
	// Un bitrate bajo fuerza recodificar: la rejilla de segmentos es fija (6 s).
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "max_bitrate": 400000})
	if resp["playback_mode"] != "transcode" {
		t.Fatalf("%v", resp)
	}
	sid := str(resp["session_id"])
	// El reproductor informa que va por el segundo 60.
	rec, _ := e.do(call{method: http.MethodPatch, path: apiBase + "/playback/sessions/" + sid, bearer: own.token,
		body: map[string]any{"state": "playing", "position_ms": 60000}})
	e.want(rec, http.StatusOK)

	rec, _ = e.changeTracks(own, sid, map[string]any{"selected_audio_stream_id": ids[1]})
	e.want(rec, http.StatusOK)
	jobs := e.transcoder.Jobs()
	if len(jobs) != 1 || jobs[0].StartSegment != 10 {
		t.Fatalf("el trabajo debía empezar en el segmento 10 (60 s): %+v", jobs)
	}
}
