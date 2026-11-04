package routes

import (
	"net/http"
	"strings"
	"testing"

	"medianode/internal/playback"
)

// Un formato que FFmpeg no convierte a texto se rechaza con un motivo, en vez
// de dar una pista vacía.
func TestSubtituloDeFormatoDesconocidoSeRechaza(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	layout := []streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"},
		{typ: "subtitle", codec: "eia_608", lang: "spa"}}
	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", layout)
	rec, _ := e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "selected_subtitle_id": ids[2]})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "PLAYBACK_UNSUPPORTED") ||
		!strings.Contains(rec.Body.String(), "no se puede convertir") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// Y no dejó cupos ocupados.
	if used, _ := e.transcoder.Admission().Usage(); len(used) > 0 && (used["heavy"] != 0 || used["light"] != 0) {
		t.Fatalf("cupos: %v", used)
	}
}

// Un subtítulo de imagen solo se puede grabar si FFmpeg tiene el filtro
// overlay. Sin él, se rechaza; con él, la decisión es quemarlo.
func TestSubtituloDeImagenExigeElFiltro(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	layout := []streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"},
		{typ: "subtitle", codec: "hdmv_pgs_subtitle", lang: "spa"}}
	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", layout)
	body := map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "selected_subtitle_id": ids[2]}

	e.playback.Features = playback.Features{BurnInText: true, BurnInImage: false, ToneMapping: true}
	rec, _ := e.play(own.token, body)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "imagen") {
		t.Fatalf("sin overlay: %d %s", rec.Code, rec.Body)
	}

	e.playback.Features = playback.Features{BurnInText: false, BurnInImage: true, ToneMapping: true}
	resp := e.session(own, body)
	if resp["subtitle_mode"] != "burn_in" || resp["playback_mode"] != "transcode" || resp["subtitle_url"] != nil {
		t.Fatalf("con overlay: %v", resp)
	}
	e.closeSession(own, resp)
}

// Un subtítulo de texto sin el filtro subtitles, o sin gestor de extracción,
// tampoco se puede grabar.
func TestSubtituloDeTextoGrabadoExigeElFiltro(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", srtLayout)
	prof := profileWith(map[string]any{"subtitle_delivery": []string{}, "subtitle_formats": []string{}})
	body := map[string]any{"media_item_id": m.item, "client_profile": prof, "selected_subtitle_id": ids[2]}
	e.playback.Features = playback.Features{BurnInText: false, BurnInImage: true, ToneMapping: true}
	rec, _ := e.play(own.token, body)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "texto") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// En Direct Play, un cambio que exigiría transcodificar pide otra sesión y no
// toca la selección guardada.
func TestCambiarPistasEnDirectPlayQueExigeTranscodificar(t *testing.T) {
	e, own, m := phase9Env(t, 1, 1)
	file, _ := m.file("Cine.mp4", "mp4", "h264", "aac", 360)
	pgs := m.id()
	m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, language)
		VALUES (?, ?, 2, 'subtitle', 'hdmv_pgs_subtitle', 'spa')`, pgs, file)
	// Con HLS declarado, el cambio llega a la comprobación de la sesión.
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "selected_subtitle_id": "none"})
	if resp["playback_mode"] != "direct_play" {
		t.Fatalf("%v", resp)
	}
	rec, _ := e.changeTracks(own, str(resp["session_id"]), map[string]any{"selected_subtitle_id": pgs})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "sesión nueva") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var stored *string
	if err := e.db.Write.QueryRow("SELECT selected_subtitle_id FROM playback_sessions WHERE id = ?", str(resp["session_id"])).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != nil && *stored == pgs {
		t.Fatal("la selección guardada no debía cambiar")
	}
}
