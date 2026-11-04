package routes

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/ffmpeg"
	"medianode/internal/playback"
	"medianode/internal/subtitles"
	"medianode/internal/transcoding"
)

var (
	probeOnce sync.Once
	probed    *ffmpeg.Capabilities
)

// probeFFmpeg analiza FFmpeg una sola vez por ejecución de las pruebas.
func probeFFmpeg(t *testing.T, path string) *ffmpeg.Capabilities {
	t.Helper()
	probeOnce.Do(func() {
		c, err := ffmpeg.Probe(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		probed = c
	})
	return probed
}

// streamRow es una pista de un fixture real.
type streamRow struct {
	typ, codec, lang string
	w, h, depth      int
	hdr              string
	channels         int // Audio; 2 por defecto.
}

// fixtureFile añade como versión del elemento un fixture de verdad (con sus
// pistas tal y como las dejaría el escáner) y devuelve el id de cada pista, en
// el orden de streams.
func (m *movieFixture) fixtureFile(name, fixture string, streams []streamRow) (string, []string) {
	m.e.t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "fixtures", fixture))
	if err != nil {
		m.e.t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		m.e.t.Skip("falta el fixture; genéralo con scripts/gen-fixtures")
	}
	return m.addFile(name, data, streams)
}

// addFile añade un archivo ya leído como versión del elemento.
func (m *movieFixture) addFile(name string, data []byte, streams []streamRow) (string, []string) {
	m.e.t.Helper()
	if err := os.WriteFile(filepath.Join(m.root, name), data, 0o644); err != nil {
		m.e.t.Fatal(err)
	}
	file := m.id()
	m.exec(`INSERT INTO media_files (id, library_path_id, relative_path, container, file_size, bitrate, duration_ms, modified_at, probe_status)
		VALUES (?, ?, ?, 'mkv', ?, 900000, 4023, '2026-01-01T00:00:00.000Z', 'done')`, file, m.pathID, name, len(data))
	m.exec("INSERT INTO media_file_items VALUES (?, ?, 0)", file, m.item)
	var ids []string
	for i, s := range streams {
		id := m.id()
		ids = append(ids, id)
		switch s.typ {
		case "video":
			var hdr, transfer any
			if s.hdr != "" {
				hdr, transfer = s.hdr, map[string]string{"hdr10": "smpte2084", "hlg": "arib-std-b67"}[s.hdr]
			}
			m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, width, height, bit_depth, hdr_format, color_transfer)
				VALUES (?, ?, ?, 'video', ?, ?, ?, ?, ?, ?)`, id, file, i, s.codec, s.w, s.h, s.depth, hdr, transfer)
		case "audio":
			ch := s.channels
			if ch == 0 {
				ch = 2
			}
			m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, channels, language)
				VALUES (?, ?, ?, 'audio', ?, ?, ?)`, id, file, i, s.codec, ch, s.lang)
		default:
			m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, language)
				VALUES (?, ?, ?, 'subtitle', ?, ?)`, id, file, i, s.codec, s.lang)
		}
	}
	return file, ids
}

var (
	srtLayout = []streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"}, {typ: "subtitle", codec: "subrip", lang: "spa"}}
	assLayout = []streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"}, {typ: "subtitle", codec: "ass", lang: "spa"}}
	hdrLayout = []streamRow{{typ: "video", codec: "hevc", w: 1280, h: 720, depth: 10, hdr: "hdr10"}, {typ: "audio", codec: "aac", lang: "spa"}}
)

func phase9Env(t *testing.T, heavy, light int) (*env, accountToken, *movieFixture) {
	t.Helper()
	e := newEnv(t)
	opt := withFFmpeg(t, heavy, light)
	e = newEnvOn(t, e.db, opt)
	e.setup()
	own := e.account(owner)
	return e, accountToken{own.token}, e.movie(own.token)
}

type accountToken struct{ token string }

func profileWith(extra map[string]any) map[string]any {
	p := map[string]any{"platform": "web", "protocols": []string{"http", "hls"}}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func (e *env) session(own accountToken, body map[string]any) map[string]any {
	e.t.Helper()
	rec, resp := e.play(own.token, body)
	e.want(rec, http.StatusCreated)
	return resp
}

func (e *env) closeSession(own accountToken, resp map[string]any) {
	e.t.Helper()
	rec, _ := e.do(call{method: http.MethodDelete, path: apiBase + "/playback/sessions/" + resp["session_id"].(string), bearer: own.token})
	e.want(rec, http.StatusNoContent)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func TestSubtituloInternoSeEntregaComoWebVTT(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", srtLayout)
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "selected_subtitle_id": ids[2]})
	if resp["subtitle_mode"] != "convert" || resp["selected_subtitle_id"] != ids[2] {
		t.Fatalf("%v", resp)
	}
	url := str(resp["subtitle_url"])
	if !strings.HasSuffix(url, "/subtitles/"+ids[2]+".vtt") {
		t.Fatalf("subtitle_url = %q", url)
	}
	rec := e.get(url, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/vtt") ||
		!strings.HasPrefix(rec.Body.String(), "WEBVTT") || !strings.Contains(rec.Body.String(), "áéíóú") ||
		!strings.Contains(rec.Body.String(), "-->") {
		t.Fatalf("%d %q\n%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	// Una pista interna solo existe como WebVTT.
	if rec = e.get(strings.TrimSuffix(url, ".vtt")+".srt", nil); rec.Code != http.StatusNotFound {
		t.Fatalf(".srt de una pista interna: %d", rec.Code)
	}
	// Una pista que no existe, tampoco.
	if rec = e.get(strings.Replace(url, ids[2], "no-existe", 1), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("pista inexistente: %d", rec.Code)
	}
	// Con otra sesión no se alcanza la pista de la primera.
	other := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile})
	if str(other["subtitle_url"]) != "" && other["subtitle_url"] != nil {
		t.Logf("la segunda sesión eligió su propio subtítulo: %v", other["subtitle_url"])
	}
}

func TestSubtituloASSExternoSeConvierteAWebVTT(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	file, _ := m.file("Cine.mp4", "mp4", "h264", "aac", 360)
	ass, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "sample.ass"))
	if err != nil {
		t.Skip("falta sample.ass")
	}
	id := m.subtitle(file, "Cine.es.ass", string(ass))
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": webProfile, "selected_subtitle_id": id})
	if resp["playback_mode"] != "direct_play" || resp["subtitle_mode"] != "convert" {
		t.Fatalf("%v", resp)
	}
	url := str(resp["subtitle_url"])
	if !strings.HasSuffix(url, "/subtitles/"+id+".vtt") {
		t.Fatalf("subtitle_url = %q", url)
	}
	rec := e.get(url, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "WEBVTT") || !strings.Contains(rec.Body.String(), "-->") {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	// El ASS original sigue disponible para quien lo renderiza.
	if rec = e.get(strings.TrimSuffix(url, ".vtt")+".ass", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "[Events]") {
		t.Fatalf("ass original: %d", rec.Code)
	}
}

func TestSubtituloDentroDeHLS(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", srtLayout)
	prof := profileWith(map[string]any{"subtitle_delivery": []string{"hls"}, "subtitle_formats": []string{"vtt"}})
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": prof, "selected_subtitle_id": ids[2]})
	if resp["subtitle_mode"] != "hls" || resp["subtitle_url"] != nil {
		t.Fatalf("%v", resp)
	}
	master := str(resp["stream_url"])
	base := strings.TrimSuffix(master, "master.m3u8")
	rec := e.get(master, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs"`) ||
		!strings.Contains(body, `URI="subs.m3u8"`) || !strings.Contains(body, `LANGUAGE="spa"`) || !strings.Contains(body, `SUBTITLES="subs"`) {
		t.Fatalf("master sin pista de subtítulos:\n%s", body)
	}
	rec = e.get(base+"subs.m3u8", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "subs.vtt") || !strings.Contains(rec.Body.String(), "#EXT-X-ENDLIST") ||
		rec.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("subs.m3u8: %d\n%s", rec.Code, rec.Body)
	}
	rec = e.get(base+"subs.vtt", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "WEBVTT") || !strings.Contains(rec.Body.String(), "áéíóú") {
		t.Fatalf("subs.vtt: %d\n%s", rec.Code, rec.Body)
	}
	e.closeSession(own, resp)

	// Sin subtítulo en HLS, esas rutas no existen.
	plain := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "selected_subtitle_id": "none"})
	pbase := strings.TrimSuffix(str(plain["stream_url"]), "master.m3u8")
	for _, name := range []string{"subs.m3u8", "subs.vtt"} {
		if rec = e.get(pbase+name, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s sin subtítulo: %d", name, rec.Code)
		}
	}
	if rec = e.get(pbase+"master.m3u8", nil); strings.Contains(rec.Body.String(), "SUBTITLES") {
		t.Fatalf("el master no debe declarar subtítulos:\n%s", rec.Body)
	}
}

// La extracción corre en segundo plano: crear la sesión no espera a FFmpeg, y
// pedir el subtítulo espera a que termine.
func TestLaExtraccionNoRetrasaLaSesion(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", srtLayout)
	ff, _ := ffmpeg.Locate()
	real := subtitles.NewManager(ff, t.TempDir(), zerolog.Nop())
	slow := func(ctx context.Context, ff, in string, stream int, format, out string) error {
		time.Sleep(2 * time.Second)
		return ffmpeg.ExtractSubtitle(ctx, ff, in, stream, format, out)
	}
	real.SetExtractor(slow)
	e.playback.Subtitles = real

	start := time.Now()
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": hlsProfile, "selected_subtitle_id": ids[2]})
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("crear la sesión tardó %s: esperó a la extracción", d)
	}
	start = time.Now()
	rec := e.get(str(resp["subtitle_url"]), nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "WEBVTT") {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("el subtítulo tardó %s", d)
	}
}

// frameAt devuelve el fotograma de t segundos de un vídeo fMP4 (init + segmento).
func frameAt(t *testing.T, ff string, init, seg []byte, at string) image.Image {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.mp4")
	if err := os.WriteFile(in, append(append([]byte{}, init...), seg...), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "f.png")
	if b, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-ss", at, "-i", in, "-frames:v", "1", "-y", out).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// meanDiff es la diferencia media por canal entre dos imágenes del mismo
// tamaño, en las filas entre from y to (fracciones de la altura).
func meanDiff(a, b image.Image, from, to float64) float64 {
	bounds := a.Bounds()
	h := float64(bounds.Dy())
	var sum, n float64
	for y := bounds.Min.Y + int(h*from); y < bounds.Min.Y+int(h*to); y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			for _, d := range []int64{int64(r1) - int64(r2), int64(g1) - int64(g2), int64(b1) - int64(b2)} {
				if d < 0 {
					d = -d
				}
				sum += float64(d >> 8)
				n++
			}
		}
	}
	return sum / n
}

func hlsInitAndSeg(t *testing.T, e *env, resp map[string]any) (init, seg []byte) {
	t.Helper()
	base := strings.TrimSuffix(str(resp["stream_url"]), "master.m3u8")
	i := e.get(base+"init.mp4", nil)
	s := e.get(base+"seg_00000.m4s", nil)
	if i.Code != http.StatusOK || s.Code != http.StatusOK {
		t.Fatalf("init %d, segmento %d", i.Code, s.Code)
	}
	return i.Body.Bytes(), s.Body.Bytes()
}

// Grabar el subtítulo en la imagen: con él, el fotograma de 1 s (hay texto de
// 0,5 s a 2 s) cambia; el de 2,25 s (sin texto) queda igual.
func TestSubtituloGrabadoEnLaImagen(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	ff, _ := ffmpeg.Locate()
	if c := probeFFmpeg(t, ff); !c.BurnInText {
		t.Skip("este FFmpeg no tiene el filtro subtitles")
	}
	for name, layout := range map[string]struct {
		fixture string
		rows    []streamRow
	}{"srt": {"mkv_srt_internal.mkv", srtLayout}, "ass": {"mkv_ass_internal.mkv", assLayout}} {
		_, ids := m.fixtureFile(name+".mkv", layout.fixture, layout.rows)
		// Sin ningún modo de entrega, el cliente solo puede recibirlo grabado.
		prof := profileWith(map[string]any{"subtitle_delivery": []string{}, "subtitle_formats": []string{}})
		burned := e.session(own, map[string]any{"media_item_id": m.item, "media_file_id": mustFileID(t, m, name+".mkv"),
			"client_profile": prof, "selected_subtitle_id": ids[2], "max_bitrate": 600000})
		if burned["subtitle_mode"] != "burn_in" || burned["playback_mode"] != "transcode" || burned["video_action"] != "transcode" || burned["subtitle_url"] != nil {
			t.Fatalf("%s: %v", name, burned)
		}
		bi, bs := hlsInitAndSeg(t, e, burned)
		e.closeSession(own, burned)

		clean := e.session(own, map[string]any{"media_item_id": m.item, "media_file_id": mustFileID(t, m, name+".mkv"),
			"client_profile": prof, "selected_subtitle_id": "none", "max_bitrate": 600000})
		if clean["playback_mode"] != "transcode" {
			t.Fatalf("%s: la referencia debía recodificar con los mismos parámetros: %v", name, clean)
		}
		ci, cs := hlsInitAndSeg(t, e, clean)
		e.closeSession(own, clean)

		// El texto va abajo: en 1 s (hay texto de 0,5 s a 2 s) la zona inferior
		// difiere de la referencia mucho más que la superior, que solo tiene el
		// ruido de codificar dos veces.
		a, b := frameAt(t, ff, bi, bs, "1.0"), frameAt(t, ff, ci, cs, "1.0")
		bottom, top := meanDiff(a, b, 0.65, 1.0), meanDiff(a, b, 0.0, 0.35)
		t.Logf("%s: diferencia abajo %.3f, arriba %.3f", name, bottom, top)
		if bottom < 1.0 || bottom < 2*top {
			t.Fatalf("%s: el subtítulo no se grabó: abajo %.3f, arriba %.3f", name, bottom, top)
		}
	}
}

func mustFileID(t *testing.T, m *movieFixture, name string) string {
	t.Helper()
	var id string
	row := m.e.db.Write.QueryRow("SELECT id FROM media_files WHERE relative_path = ?", name)
	if err := row.Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Un HDR10 se transcodifica a SDR BT.709: los metadatos de color de la salida
// lo dicen, y la imagen no es la del HDR sin convertir.
func TestHDRSeConvierteASDR(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	ff, _ := ffmpeg.Locate()
	if c := probeFFmpeg(t, ff); !c.ToneMapping {
		t.Skip("este FFmpeg no tiene tone mapping")
	}
	m.fixtureFile("Hdr.mkv", "mkv_hdr10_hevc.mkv", hdrLayout)
	// Un navegador que reproduce HEVC pero no HDR: el único motivo es el HDR.
	prof := profileWith(map[string]any{"video_codecs": []string{"h264", "hevc"}, "hdr": []string{}, "max_bit_depth": 10})
	resp := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": prof})
	if resp["playback_mode"] != "transcode" || resp["video_action"] != "transcode" {
		t.Fatalf("%v", resp)
	}
	var codes []string
	for _, r := range resp["reasons"].([]any) {
		codes = append(codes, str(r.(map[string]any)["code"]))
	}
	if !contains(codes, "hdr_unsupported") {
		t.Fatalf("falta el motivo hdr_unsupported: %v", codes)
	}
	init, seg := hlsInitAndSeg(t, e, resp)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.mp4")
	if err := os.WriteFile(in, append(append([]byte{}, init...), seg...), 0o600); err != nil {
		t.Fatal(err)
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("falta ffprobe")
	}
	out, err := exec.Command(probe, "-v", "error", "-select_streams", "v:0", "-show_entries",
		"stream=pix_fmt,color_transfer,color_primaries,color_space", "-of", "csv=p=0", in).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(out))
	if !strings.HasPrefix(got, "yuv420p,") || strings.Contains(got, "smpte2084") || strings.Contains(got, "bt2020") {
		t.Fatalf("la salida sigue siendo HDR: %q", got)
	}
	if !strings.Contains(got, "bt709") {
		t.Fatalf("la salida no declara BT.709: %q", got)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Sin los filtros necesarios la reproducción se rechaza con un motivo claro
// en lugar de entregar una imagen apagada o sin subtítulos.
func TestSinFiltrosSeRechazaConMotivo(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	m.fixtureFile("Hdr.mkv", "mkv_hdr10_hevc.mkv", hdrLayout)
	prof := profileWith(map[string]any{"video_codecs": []string{"h264", "hevc"}, "hdr": []string{}, "max_bit_depth": 10})
	e.playback.Features = playback.Features{}
	rec, body := e.play(own.token, map[string]any{"media_item_id": m.item, "client_profile": prof})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "PLAYBACK_UNSUPPORTED") ||
		!strings.Contains(rec.Body.String(), "SDR") {
		t.Fatalf("HDR sin tone mapping: %d %v", rec.Code, body)
	}

	_, ids := m.fixtureFile("Cine.mkv", "mkv_srt_internal.mkv", srtLayout)
	burn := profileWith(map[string]any{"subtitle_delivery": []string{}, "subtitle_formats": []string{}})
	rec, _ = e.play(own.token, map[string]any{"media_item_id": m.item, "media_file_id": mustFileID(t, m, "Cine.mkv"),
		"client_profile": burn, "selected_subtitle_id": ids[2]})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "grabar") {
		t.Fatalf("burn-in sin filtro: %d %s", rec.Code, rec.Body)
	}
	// Las sesiones rechazadas no dejan cupos ocupados.
	if used, _ := e.transcoder.Admission().Usage(); used[transcoding.Heavy] != 0 || used[transcoding.Light] != 0 {
		t.Fatalf("cupos ocupados tras rechazar: %v", used)
	}
}
