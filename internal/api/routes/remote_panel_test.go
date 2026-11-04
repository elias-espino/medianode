package routes

import (
	"net/http"
	"strings"
	"testing"

	"medianode/internal/remote"
	"medianode/internal/testutil"
)

func panelEnv(t *testing.T, o remote.Options) *env {
	t.Helper()
	panel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("panel " + r.URL.Path)) })
	e := newEnvOn(t, testutil.NewDB(t), func(eo *envOptions) { eo.remote, eo.web = &o, panel })
	e.setupFrom(lan)
	return e
}

func TestElPanelNoServiraLaAdministracionDesdeFuera(t *testing.T) {
	e := panelEnv(t, remote.Options{Enabled: true, Direct: true})
	for _, path := range []string{"/admin", "/admin/network", "/users", "/metadata"} {
		rec := e.dlnaFrom(outside, http.MethodGet, path, "")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "administración") {
			t.Errorf("%s desde fuera: %d %s", path, rec.Code, rec.Body)
		}
		if rec := e.dlnaFrom(lan, http.MethodGet, path, ""); rec.Code != 200 || rec.Body.String() != "panel "+path {
			t.Errorf("%s desde la red local: %d %s", path, rec.Code, rec.Body)
		}
	}
	// El resto del panel sí se sirve a quien puede entrar.
	if rec := e.dlnaFrom(outside, http.MethodGet, "/movies", ""); rec.Code != 200 {
		t.Errorf("/movies desde fuera: %d", rec.Code)
	}
	// Con la administración remota permitida, también se sirve.
	open := panelEnv(t, remote.Options{Enabled: true, Direct: true, AdminEnabled: true})
	if rec := open.dlnaFrom(outside, http.MethodGet, "/admin", ""); rec.Code != 200 {
		t.Errorf("con admin_enabled: %d", rec.Code)
	}
}

// remote.local_networks vale para todo el servidor, no solo para la puerta: una
// VPN declarada como red local tampoco recibe el límite de bitrate remoto.
func TestLasRedesLocalesExtraValenParaElLimiteDeBitrate(t *testing.T) {
	e := newEnv(t)
	e = newEnvOn(t, e.db, withFFmpeg(t, 1, 1), func(eo *envOptions) {
		eo.remote = &remote.Options{Enabled: true, Direct: true, AdminEnabled: true, LocalNetworks: []string{"100.64.0.0/10"}}
	})
	e.setupFrom(lan)
	token := e.loginFrom(lan)
	m := e.movie(token)
	m.addDLNAFile(m.item, "x.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac) // 900 kbps.
	rec, _ := e.do(call{method: http.MethodPut, path: apiBase + "/users/me/preferences", bearer: token, remote: lan,
		body: map[string]any{"subtitle_mode": "default", "max_bitrate_remote": 500000}})
	e.want(rec, http.StatusOK)
	play := func(addr string) string {
		body := map[string]any{"media_item_id": m.item, "client_profile": map[string]any{"platform": "web", "protocols": []string{"http", "hls"}}}
		rec, resp := e.do(call{method: http.MethodPost, path: apiBase + "/playback/sessions", bearer: token, remote: addr, body: body})
		e.want(rec, http.StatusCreated)
		e.do(call{method: http.MethodDelete, path: apiBase + "/playback/sessions/" + resp["session_id"].(string), bearer: token, remote: addr})
		return resp["playback_mode"].(string)
	}
	if got := play("100.64.1.1:5000"); got != "direct_play" {
		t.Fatalf("una VPN declarada como red local no tiene límite remoto: %s", got)
	}
	if got := play("100.128.0.1:5000"); got != "transcode" {
		t.Fatalf("fuera del rango de la VPN, sí: %s", got)
	}
}
