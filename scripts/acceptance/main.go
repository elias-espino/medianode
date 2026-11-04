// Command acceptance comprueba los 14 criterios del MVP (§23.5) contra un
// servidor en marcha, instalado como binario o como contenedor.
//
// Prepara una carpeta de bibliotecas con los vídeos de testdata/fixtures
// (go run ./scripts/gen-fixtures), crea la cuenta propietaria con el código
// inicial y recorre el producto como lo haría una persona, por la API.
//
//	go run ./scripts/acceptance -url http://127.0.0.1:8420 \
//	    -media-dir /datos/prueba -media-path /datos/prueba -setup-code CODIGO
//
// Con un contenedor, -media-dir es la carpeta del equipo y -media-path la misma
// vista desde dentro (por ejemplo /media). -restart es el comando que reinicia
// el servidor ("docker restart -t 20 medianode"); sin él se omiten los
// criterios que lo exigen y se avisa.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type acceptance struct {
	base   string
	client *http.Client
	token  string
	failed int
	total  int
}

func main() {
	url := flag.String("url", "http://127.0.0.1:8420", "dirección del servidor")
	mediaDir := flag.String("media-dir", "", "carpeta de bibliotecas, vista desde este equipo")
	mediaPath := flag.String("media-path", "", "la misma carpeta, vista desde el servidor (por defecto, igual que -media-dir)")
	fixtures := flag.String("fixtures", filepath.Join("testdata", "fixtures"), "vídeos de prueba generados con scripts/gen-fixtures")
	code := flag.String("setup-code", "", "código inicial del primer arranque (log del servidor)")
	password := flag.String("password", "Prueba-aceptacion-1234", "contraseña que se pone a la cuenta propietaria")
	restart := flag.String("restart", "", "comando que reinicia el servidor, sin shell; vacío: omitir los criterios de reinicio")
	flag.Parse()
	if *mediaDir == "" || *code == "" {
		fmt.Fprintln(os.Stderr, "faltan -media-dir y -setup-code")
		os.Exit(2)
	}
	if *mediaPath == "" {
		*mediaPath = *mediaDir
	}
	a := &acceptance{base: strings.TrimRight(*url, "/"), client: &http.Client{Timeout: 2 * time.Minute}}
	if err := a.run(*mediaDir, *mediaPath, *fixtures, *code, *password, *restart); err != nil {
		fmt.Fprintln(os.Stderr, "acceptance:", err)
		os.Exit(2)
	}
	fmt.Printf("\n%d/%d comprobaciones superadas\n", a.total-a.failed, a.total)
	if a.failed > 0 {
		os.Exit(1)
	}
}

// video es un fixture con el nombre con el que se instala en la biblioteca.
var videos = map[string]string{
	"Subtitulo Srt (2001).mkv": "mkv_srt_internal.mkv",
	"Subtitulo Ass (2002).mkv": "mkv_ass_internal.mkv",
	"Dos Audios (2003).mkv":    "mkv_multi_audio.mkv",
	"Hevc Aac (2005).mkv":      "mkv_hevc_aac.mkv",
	"Largo Seek (2006).mkv":    "seek_120s_h264.mkv",
	"Directo (2007).mp4":       "mp4_h264_aac.mp4",
}

// expectedTitles son los títulos que el catálogo debe sacar de los nombres de
// archivo de videos.
var expectedTitles = []string{"Subtitulo Srt", "Subtitulo Ass", "Dos Audios", "Hevc Aac", "Largo Seek", "Directo"}

var hls = map[string]any{"platform": "web", "protocols": []string{"http", "hls"}}

func (a *acceptance) check(criterion, name string, ok bool, detail any) {
	a.total++
	mark := "OK  "
	if !ok {
		mark = "FAIL"
		a.failed++
	}
	d := ""
	if detail != nil && detail != "" {
		d = fmt.Sprintf("  [%v]", detail)
	}
	fmt.Printf("%s %-3s %s%s\n", mark, criterion, name, d)
}

func (a *acceptance) call(method, path string, body any, token string) (int, http.Header, []byte, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.base+path, r)
	if err != nil {
		return 0, nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, data, err
}

func (a *acceptance) json(method, path string, body any, token string, out any) (int, error) {
	st, _, data, err := a.call(method, path, body, token)
	if err != nil {
		return st, err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return st, fmt.Errorf("%s %s: %w: %s", method, path, err, data)
		}
	}
	return st, nil
}

type obj = map[string]any

func str(m obj, k string) string { s, _ := m[k].(string); return s }

func (a *acceptance) login(user, pass, device string) (string, error) {
	var s obj
	st, err := a.json(http.MethodPost, "/api/v1/auth/login", obj{"username": user, "password": pass, "device_name": device, "platform": "android"}, "", &s)
	if err != nil || st != http.StatusOK {
		return "", fmt.Errorf("login %s: %d: %w", user, st, errors.Join(err))
	}
	return str(s, "access_token"), nil
}

func (a *acceptance) waitHealthy() error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if st, _, _, err := a.call(http.MethodGet, "/health", nil, ""); err == nil && st == http.StatusOK {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("el servidor no responde")
}

func (a *acceptance) items(lib string) map[string]string {
	var raw json.RawMessage
	if _, err := a.json(http.MethodGet, "/api/v1/libraries/"+lib+"/items?limit=100", nil, a.token, &raw); err != nil {
		return nil
	}
	var page struct {
		Items []obj `json:"items"`
	}
	if json.Unmarshal(raw, &page) != nil {
		return nil
	}
	out := map[string]string{}
	for _, it := range page.Items {
		out[str(it, "title")] = str(it, "id")
	}
	return out
}

func (a *acceptance) play(item string, profile any, extra obj) (obj, int) {
	body := obj{"media_item_id": item, "client_profile": profile}
	for k, v := range extra {
		body[k] = v
	}
	var s obj
	st, _ := a.json(http.MethodPost, "/api/v1/playback/sessions", body, a.token, &s)
	return s, st
}

// segment pide la lista de medios de una sesión HLS y su primer segmento.
func (a *acceptance) segment(s obj) (int, int) {
	url := str(s, "stream_url")
	base := url[:strings.LastIndex(url, "/")]
	st, _, pl, err := a.call(http.MethodGet, base+"/media.m3u8", nil, "")
	if err != nil || st != http.StatusOK {
		return st, 0
	}
	m := regexp.MustCompile(`(?m)^(seg_\d+\.(?:m4s|ts))$`).FindSubmatch(pl)
	if m == nil {
		return st, 0
	}
	if i := regexp.MustCompile(`URI="([^"]+)"`).FindSubmatch(pl); i != nil {
		_, _, _, _ = a.call(http.MethodGet, base+"/"+string(i[1]), nil, "")
	}
	st, _, seg, err := a.call(http.MethodGet, base+"/"+string(m[1]), nil, "")
	if err != nil {
		return 0, 0
	}
	return st, len(seg)
}

func (a *acceptance) closeSession(s obj) {
	_, _, _, _ = a.call(http.MethodDelete, "/api/v1/playback/sessions/"+str(s, "session_id"), nil, a.token)
}

func (a *acceptance) run(mediaDir, mediaPath, fixtures, code, password, restart string) error {
	if err := a.waitHealthy(); err != nil {
		return err
	}
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return err
	}
	for name, src := range videos {
		data, err := os.ReadFile(filepath.Join(fixtures, src))
		if err != nil {
			return fmt.Errorf("falta el vídeo de prueba %s (go run ./scripts/gen-fixtures): %w", src, err)
		}
		if err := os.WriteFile(filepath.Join(mediaDir, name), data, 0o644); err != nil {
			return err
		}
	}

	// 2. Configuración inicial desde el navegador: el primer arranque pide el
	// código y crea la cuenta propietaria.
	var info obj
	_, _ = a.json(http.MethodGet, "/api/v1/server/info", nil, "", &info)
	setupRequired := info["setup_required"] == true
	st, _ := a.json(http.MethodPost, "/api/v1/auth/setup", obj{"code": code, "username": "propietario", "password": password}, "", nil)
	a.check("2", "el primer arranque pide configurarse y el código crea al propietario", setupRequired && st == http.StatusCreated, fmt.Sprintf("setup_required=%v, POST %d", setupRequired, st))
	st, _ = a.json(http.MethodPost, "/api/v1/auth/setup", obj{"code": code, "username": "otro", "password": password}, "", nil)
	a.check("2", "el código no sirve una segunda vez", st != http.StatusCreated, st)
	tok, err := a.login("propietario", password, "aceptacion")
	if err != nil {
		return err
	}
	a.token = tok

	// 3. Creación y escaneo de bibliotecas.
	var lib obj
	st, _ = a.json(http.MethodPost, "/api/v1/libraries", obj{"name": "Aceptación", "type": "movies", "paths": []string{mediaPath}}, a.token, &lib)
	a.check("3", "crear una biblioteca con una carpeta", st == http.StatusCreated, st)
	libID := str(lib, "id")
	_, _ = a.json(http.MethodPost, "/api/v1/libraries/"+libID+"/scan", nil, a.token, nil)
	var scan obj
	for i := 0; i < 180; i++ {
		_, _ = a.json(http.MethodGet, "/api/v1/libraries/"+libID+"/scan", nil, a.token, &scan)
		if s := str(scan, "status"); s == "completed" || s == "failed" {
			break
		}
		time.Sleep(time.Second)
	}
	items := a.items(libID)
	missing := []string{}
	for _, title := range expectedTitles {
		if items[title] == "" {
			missing = append(missing, title)
		}
	}
	a.check("3", "el escaneo termina y encuentra los archivos", str(scan, "status") == "completed" && len(missing) == 0, fmt.Sprintf("%v archivos, faltan %v", scan["files_processed"], missing))

	// 4 y 5. FFprobe y catálogo.
	var streams []obj
	_, _ = a.json(http.MethodGet, "/api/v1/media/"+items["Hevc Aac"]+"/streams", nil, a.token, &streams)
	codecs := map[string]bool{}
	for _, s := range streams {
		codecs[str(s, "codec")] = true
	}
	a.check("4", "FFprobe extrae las pistas (HEVC y AAC)", codecs["hevc"] && codecs["aac"], codecs)
	a.check("5", "el catálogo lista las películas encontradas", len(items) >= len(videos), len(items))
	var found []obj
	_, _ = a.json(http.MethodGet, "/api/v1/search?q=seek", nil, a.token, &found)
	a.check("5", "la búsqueda encuentra un título", len(found) >= 1, len(found))

	// 6. Login y permisos.
	var user obj
	st, _ = a.json(http.MethodPost, "/api/v1/admin/users", obj{"username": "invitada", "password": password, "role": "user"}, a.token, &user)
	a.check("6", "el propietario crea una cuenta sin acceso", st == http.StatusCreated, st)
	guest, _ := a.login("invitada", password, "invitada")
	st, _ = a.json(http.MethodGet, "/api/v1/libraries/"+libID+"/items", nil, guest, nil)
	a.check("6", "una cuenta sin permiso no ve la biblioteca", st == http.StatusForbidden || st == http.StatusNotFound, st)
	st, _ = a.json(http.MethodGet, "/api/v1/admin/system", nil, guest, nil)
	a.check("6", "una cuenta normal no entra en administración", st == http.StatusForbidden, st)
	st, _ = a.json(http.MethodGet, "/api/v1/admin/system", nil, "", nil)
	a.check("6", "sin sesión, 401", st == http.StatusUnauthorized, st)
	st, _ = a.json(http.MethodPut, "/api/v1/admin/users/"+str(user, "id")+"/permissions", obj{"libraries": []obj{{"library_id": libID, "permissions": []string{"view"}}}}, a.token, nil)
	st2, _ := a.json(http.MethodGet, "/api/v1/libraries/"+libID+"/items", nil, guest, nil)
	a.check("6", "dar permiso de ver abre la biblioteca", st == http.StatusOK && st2 == http.StatusOK, fmt.Sprintf("PUT %d, GET %d", st, st2))

	// 7. Direct Play, con Range.
	direct, st := a.play(items["Directo"], obj{"platform": "web"}, nil)
	a.check("7", "MP4 H.264/AAC: Direct Play", st == http.StatusCreated && str(direct, "playback_mode") == "direct_play", str(direct, "playback_mode"))
	if st == http.StatusCreated {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, a.base+str(direct, "stream_url"), nil)
		req.Header.Set("Range", "bytes=0-99")
		resp, err := a.client.Do(req)
		okRange := err == nil && resp.StatusCode == http.StatusPartialContent
		if resp != nil {
			resp.Body.Close()
		}
		a.check("7", "el archivo se sirve con Range, para poder saltar", okRange, nil)
		a.closeSession(direct)
	}

	// 8. Direct Stream.
	ds, st := a.play(items["Largo Seek"], hls, nil)
	segStatus, segBytes := a.segment(ds)
	a.check("8", "H.264 en MKV: Direct Stream por HLS con segmentos", st == http.StatusCreated && str(ds, "playback_mode") == "direct_stream" && segStatus == http.StatusOK && segBytes > 1000,
		fmt.Sprintf("%s, segmento %d (%d bytes)", str(ds, "playback_mode"), segStatus, segBytes))
	a.closeSession(ds)

	// 9. Transcodificación de un perfil.
	tr, st := a.play(items["Hevc Aac"], hls, nil)
	segStatus, segBytes = a.segment(tr)
	a.check("9", "HEVC: transcodificación a H.264 por HLS", st == http.StatusCreated && str(tr, "playback_mode") == "transcode" && segStatus == http.StatusOK && segBytes > 1000,
		fmt.Sprintf("%s, segmento %d (%d bytes)", str(tr, "playback_mode"), segStatus, segBytes))

	// 12. Dos clientes con sesiones independientes, a la vez: la transcodificación
	// sigue abierta mientras otro cliente hace Direct Play.
	second, _ := a.login("propietario", password, "segundo-cliente")
	var other obj
	body := obj{"media_item_id": items["Directo"], "client_profile": obj{"platform": "web"}}
	st2, _ = a.json(http.MethodPost, "/api/v1/playback/sessions", body, second, &other)
	a.check("12", "dos clientes a la vez: Direct Play y transcodificación, sesiones distintas",
		st2 == http.StatusCreated && str(other, "session_id") != str(tr, "session_id") && str(other, "playback_mode") == "direct_play" && str(tr, "playback_mode") == "transcode", nil)
	var list []obj
	_, _ = a.json(http.MethodGet, "/api/v1/admin/sessions", nil, a.token, &list)
	devices := map[string]bool{}
	for _, l := range list {
		devices[str(l, "device_id")] = true
	}
	a.check("12", "administración ve las dos sesiones, de dispositivos distintos", len(list) >= 2 && len(devices) >= 2, fmt.Sprintf("%d sesiones, %d dispositivos", len(list), len(devices)))
	// Pausar una no toca la otra.
	st, _ = a.json(http.MethodPatch, "/api/v1/playback/sessions/"+str(other, "session_id"), obj{"state": "paused", "position_ms": 1000}, second, nil)
	var paused, running obj
	_, _ = a.json(http.MethodGet, "/api/v1/playback/sessions/"+str(other, "session_id"), nil, second, &paused)
	_, _ = a.json(http.MethodGet, "/api/v1/playback/sessions/"+str(tr, "session_id"), nil, a.token, &running)
	a.check("12", "pausar una sesión no cambia el estado de la otra",
		st == http.StatusOK && str(paused, "status") == "paused" && paused["position_ms"] == float64(1000) && str(running, "status") != "paused",
		fmt.Sprintf("%s y %s", str(paused, "status"), str(running, "status")))
	// La sesión de un dispositivo no la maneja otro.
	st, _ = a.json(http.MethodDelete, "/api/v1/playback/sessions/"+str(other, "session_id"), nil, guest, nil)
	a.check("12", "una cuenta ajena no puede cerrar la sesión de otra", st == http.StatusNotFound || st == http.StatusForbidden, st)
	if st2 == http.StatusCreated {
		_, _, _, _ = a.call(http.MethodDelete, "/api/v1/playback/sessions/"+str(other, "session_id"), nil, second)
	}
	a.closeSession(tr)

	// 10. Audio y subtítulos.
	var srtStreams []obj
	_, _ = a.json(http.MethodGet, "/api/v1/media/"+items["Subtitulo Srt"]+"/streams", nil, a.token, &srtStreams)
	subID := ""
	for _, s := range srtStreams {
		if str(s, "type") == "subtitle" {
			subID = str(s, "id")
		}
	}
	sub, st := a.play(items["Subtitulo Srt"], hls, obj{"selected_subtitle_id": subID})
	vtt := []byte(nil)
	if st == http.StatusCreated && str(sub, "subtitle_url") != "" {
		_, _, vtt, _ = a.call(http.MethodGet, str(sub, "subtitle_url"), nil, "")
	}
	a.check("10", "subtítulo interno entregado como WebVTT", bytes.HasPrefix(vtt, []byte("WEBVTT")) && bytes.Contains(vtt, []byte("-->")), len(vtt))
	a.closeSession(sub)
	var audioStreams []obj
	_, _ = a.json(http.MethodGet, "/api/v1/media/"+items["Dos Audios"]+"/streams", nil, a.token, &audioStreams)
	var audios []string
	for _, s := range audioStreams {
		if str(s, "type") == "audio" {
			audios = append(audios, str(s, "id"))
		}
	}
	if len(audios) >= 2 {
		au, st := a.play(items["Dos Audios"], hls, obj{"selected_audio_stream_id": audios[0]})
		var ch obj
		stc, _ := a.json(http.MethodPost, "/api/v1/playback/sessions/"+str(au, "session_id")+"/tracks", obj{"selected_audio_stream_id": audios[1]}, a.token, &ch)
		a.check("10", "cambiar de pista de audio con la sesión en marcha", st == http.StatusCreated && stc == http.StatusOK && str(ch, "selected_audio_stream_id") == audios[1], stc)
		a.closeSession(au)
	} else {
		a.check("10", "el archivo de dos audios tiene dos pistas", false, len(audios))
	}

	// 11. Guardado y recuperación de progreso.
	prog, st := a.play(items["Largo Seek"], hls, nil)
	st2, _ = a.json(http.MethodPatch, "/api/v1/playback/sessions/"+str(prog, "session_id"), obj{"state": "paused", "position_ms": 45000}, a.token, nil)
	a.closeSession(prog)
	cw := a.continueWatching("Largo Seek")
	a.check("11", "la posición se guarda al pausar y aparece en «continuar viendo»", st == http.StatusCreated && st2 == http.StatusOK && cw == 45000, cw)

	// 13. Reinicio sin pérdida de catálogo ni progreso.
	if restart == "" {
		fmt.Println("OMIT 13  reinicio: falta -restart")
	} else {
		running, _ := a.play(items["Hevc Aac"], hls, nil)
		_ = running
		if out, err := runRestart(restart); err != nil {
			a.check("13", "reiniciar el servidor", false, strings.TrimSpace(out)+" "+err.Error())
		} else if err := a.waitHealthy(); err != nil {
			a.check("13", "el servidor vuelve a responder tras reiniciar", false, err)
		} else {
			a.token, _ = a.login("propietario", password, "tras-reinicio")
			again := a.items(libID)
			same := len(again) == len(items)
			for t, id := range items {
				same = same && again[t] == id
			}
			a.check("13", "el catálogo sigue igual tras reiniciar durante una reproducción", same, len(again))
			a.check("13", "el progreso se conserva tras reiniciar", a.continueWatching("Largo Seek") == 45000, nil)
			var old obj
			_, _ = a.json(http.MethodGet, "/api/v1/playback/sessions/"+str(running, "session_id"), nil, a.token, &old)
			a.check("13", "la sesión interrumpida queda como expirada", str(old, "status") == "expired", str(old, "status"))
			resume, st := a.play(items["Largo Seek"], hls, obj{"start_position_ms": a.continueWatching("Largo Seek")})
			a.check("13", "una sesión nueva reanuda desde el progreso guardado", st == http.StatusCreated && resume["start_position_ms"] == float64(45000), resume["start_position_ms"])
			a.closeSession(resume)
		}
	}

	// 14. Logs útiles para diagnosticar, y sin secretos.
	var logs []obj
	_, _ = a.json(http.MethodGet, "/api/v1/admin/logs?lines=1000&level=info", nil, a.token, &logs)
	var text strings.Builder
	messages := map[string]bool{}
	for _, l := range logs {
		m := str(l, "message")
		messages[m] = true
		text.WriteString(m + " ")
		if f, ok := l["fields"].(obj); ok {
			for k, v := range f {
				text.WriteString(k + "=" + fmt.Sprint(v) + " ")
			}
		}
	}
	a.check("14", "el log cuenta el arranque, FFmpeg y las sesiones",
		messages["iniciando MediaNode"] && messages["FFmpeg listo"] && messages["sesión de reproducción creada"], len(logs))
	a.check("14", "el log no contiene contraseñas ni tokens de reproducción",
		!strings.Contains(text.String(), password) && !regexp.MustCompile(`/stream/session/[^/\s]+/[A-Za-z0-9_-]{20,}`).MatchString(text.String()), nil)
	var checks []obj
	_, _ = a.json(http.MethodGet, "/api/v1/admin/system-check", nil, a.token, &checks)
	failing := []string{}
	for _, c := range checks {
		if str(c, "status") == "fail" {
			failing = append(failing, str(c, "id"))
		}
	}
	a.check("14", "el diagnóstico del sistema no tiene fallos", len(checks) == 12 && len(failing) == 0, failing)
	return nil
}

// runRestart ejecuta el comando de reinicio con su salida en un archivo, no en
// una tubería: el servidor que arranca hereda los descriptores del comando, y
// esperar a que se cierre una tubería sería esperar a que el servidor muera.
func runRestart(command string) (string, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", errors.New("comando vacío")
	}
	f, err := os.CreateTemp("", "acceptance-restart-*.log")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer f.Close()
	cmd := exec.CommandContext(context.Background(), fields[0], fields[1:]...) //nolint:gosec // el comando lo da quien ejecuta la herramienta
	cmd.Stdout, cmd.Stderr = f, f
	runErr := cmd.Run()
	out, _ := os.ReadFile(f.Name())
	return string(out), runErr
}

func (a *acceptance) continueWatching(title string) float64 {
	var list []obj
	_, _ = a.json(http.MethodGet, "/api/v1/playback/continue-watching", nil, a.token, &list)
	for _, it := range list {
		if str(it, "title") == title {
			p, _ := it["position_ms"].(float64)
			return p
		}
	}
	return -1
}
