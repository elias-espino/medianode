package routes

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"medianode/internal/remote"
)

var reBandwidth = regexp.MustCompile(`BANDWIDTH=([0-9]+)`)

func TestElLimiteDeBitrateRemotoSoloAplicaFueraDeLaRedLocal(t *testing.T) {
	e := newEnv(t)
	e = newEnvOn(t, e.db, withFFmpeg(t, 1, 1), func(eo *envOptions) { eo.remote = &remote.Options{Enabled: true, Direct: true, AdminEnabled: true} })
	e.setupFrom(lan)
	token := e.loginFrom(lan)
	m := e.movie(token)
	m.addDLNAFile(m.item, "x.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac) // 900 kbps.

	rec, _ := e.do(call{method: http.MethodPut, path: apiBase + "/users/me/preferences", bearer: token, remote: lan,
		body: map[string]any{"subtitle_mode": "default", "max_bitrate_remote": 500000}})
	e.want(rec, http.StatusOK)
	play := func(addr string, extra map[string]any) map[string]any {
		body := map[string]any{"media_item_id": m.item, "client_profile": map[string]any{"platform": "web", "protocols": []string{"http", "hls"}}}
		for k, v := range extra {
			body[k] = v
		}
		rec, resp := e.do(call{method: http.MethodPost, path: apiBase + "/playback/sessions", bearer: token, remote: addr, body: body})
		e.want(rec, http.StatusCreated)
		if url, _ := resp["stream_url"].(string); strings.HasSuffix(url, "/master.m3u8") {
			master := e.get(url, nil)
			if m := reBandwidth.FindStringSubmatch(master.Body.String()); m != nil {
				resp["bandwidth"], _ = strconv.ParseFloat(m[1], 64)
			}
		}
		e.do(call{method: http.MethodDelete, path: apiBase + "/playback/sessions/" + resp["session_id"].(string), bearer: token, remote: addr})
		return resp
	}
	hasReason := func(resp map[string]any, code string) bool {
		rs, _ := resp["reasons"].([]any)
		for _, r := range rs {
			if r.(map[string]any)["code"] == code {
				return true
			}
		}
		return false
	}

	// En la red local no hay límite: el archivo (900 kbps) va tal cual.
	if r := play(lan, nil); r["playback_mode"] != "direct_play" {
		t.Fatalf("red local: %v", r)
	}
	// Desde fuera, el límite del usuario (500 kbps) obliga a recodificar.
	r := play(outside, nil)
	if r["playback_mode"] != "transcode" || !hasReason(r, "bitrate_limit") {
		t.Fatalf("remoto: %v", r)
	}
	// El ancho de banda que anuncia la lista maestra es el vídeo que se genera más
	// el audio: con el límite del usuario (500 kbps) sale 500 000.
	if r["bandwidth"] != 500000.0 {
		t.Fatalf("con el límite del usuario, el ancho de banda = %v", r["bandwidth"])
	}
	// Un límite más bajo pedido por el cliente manda sobre el del usuario...
	if r := play(outside, map[string]any{"max_bitrate": 300000}); r["bandwidth"].(float64) >= 500000 {
		t.Fatalf("el límite de la petición debe respetarse: %v", r["bandwidth"])
	}
	// ...y uno más alto no lo supera.
	if r := play(outside, map[string]any{"max_bitrate": 50000000}); r["playback_mode"] != "transcode" || r["bandwidth"] != 500000.0 {
		t.Fatalf("un límite de la petición mayor no debe saltarse el del usuario: %v", r)
	}
	// Sin límite del usuario, desde fuera también va tal cual.
	rec, _ = e.do(call{method: http.MethodPut, path: apiBase + "/users/me/preferences", bearer: token, remote: lan,
		body: map[string]any{"subtitle_mode": "default", "max_bitrate_remote": nil}})
	e.want(rec, http.StatusOK)
	if r := play(outside, nil); r["playback_mode"] != "direct_play" {
		t.Fatalf("remoto sin límite: %v", r)
	}
}
