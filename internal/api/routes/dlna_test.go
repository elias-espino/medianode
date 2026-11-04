package routes

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"medianode/internal/dlna"
	"medianode/internal/playback"
	"medianode/internal/remote"
)

const dlnaLAN = "192.168.1.50:4000"

var (
	h264aac = []streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"}}
	hevcaac = []streamRow{{typ: "video", codec: "hevc", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"}}
)

// dlnaEnv monta el servidor con DLNA. FFmpeg solo si heavy o light son
// positivos; la biblioteca de películas se publica por DLNA.
func dlnaEnv(t *testing.T, mode string, heavy, light int) (*env, *movieFixture) {
	t.Helper()
	e := newEnv(t)
	opts := []func(*envOptions){func(eo *envOptions) {
		eo.dlna = &dlna.Options{HTTPPort: 8420, Mode: mode, BurnSubtitles: true, Language: "es-ES"}
	}}
	if heavy > 0 || light > 0 {
		opts = append(opts, withFFmpeg(t, heavy, light))
	}
	e = newEnvOn(t, e.db, opts...)
	e.setup()
	own := e.account(owner)
	m := e.movie(own.token)
	m.exec("UPDATE libraries SET dlna_enabled = 1 WHERE id = ?", m.lib)
	return e, m
}

// addDLNAFile añade como versión de un elemento un archivo real, con la
// duración y las pistas que se indiquen.
func (m *movieFixture) addDLNAFile(item, name, fixture, container string, durationMS int64, streams []streamRow) string {
	m.e.t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "fixtures", fixture))
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
	file := m.id()
	m.exec(`INSERT INTO media_files (id, library_path_id, relative_path, container, file_size, bitrate, duration_ms, modified_at, probe_status)
		VALUES (?, ?, ?, ?, ?, 900000, ?, '2026-01-01T00:00:00.000Z', 'done')`, file, m.pathID, name, container, len(data), durationMS)
	m.exec("INSERT INTO media_file_items VALUES (?, ?, 0)", file, item)
	for i, s := range streams {
		switch s.typ {
		case "video":
			var hdr, transfer any
			if s.hdr != "" {
				hdr, transfer = s.hdr, map[string]string{"hdr10": "smpte2084", "hlg": "arib-std-b67"}[s.hdr]
			}
			m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, profile, level, width, height, bit_depth, hdr_format, color_transfer)
				VALUES (?, ?, ?, 'video', ?, 'High', '31', ?, ?, ?, ?, ?)`, m.id(), file, i, s.codec, s.w, s.h, s.depth, hdr, transfer)
		case "audio":
			ch := s.channels
			if ch == 0 {
				ch = 2
			}
			m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, channels, language)
				VALUES (?, ?, ?, 'audio', ?, ?, ?)`, m.id(), file, i, s.codec, ch, s.lang)
		case "subtitle":
			m.exec(`INSERT INTO media_streams (id, media_file_id, stream_index, stream_type, codec, language, is_forced)
				VALUES (?, ?, ?, 'subtitle', ?, ?, ?)`, m.id(), file, i, s.codec, s.lang, s.forced)
		}
	}
	return file
}

func (m *movieFixture) addMovie(title string) string {
	id := m.id()
	m.exec("INSERT INTO media_items (id, library_id, type, title, sort_title, min_age) VALUES (?, ?, 'movie', ?, ?, 0)", id, m.lib, title, strings.ToLower(title))
	return id
}

func (e *env) dlnaReq(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.dlnaFrom(dlnaLAN, method, path, body, headers...)
}

func (e *env) dlnaFrom(addr, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr, r.Host = addr, "192.168.1.10:8420"
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

const cdsNS = "urn:schemas-upnp-org:service:ContentDirectory:1"

type browsed struct {
	Result   string
	Returned int
	Total    int
	items    []didlObject
}

type didlObject struct {
	XMLName    xml.Name
	ID         string `xml:"id,attr"`
	ParentID   string `xml:"parentID,attr"`
	ChildCount string `xml:"childCount,attr"`
	Title      string `xml:"title"`
	Class      string `xml:"class"`
	Art        string `xml:"albumArtURI"`
	Res        []struct {
		Protocol string `xml:"protocolInfo,attr"`
		Duration string `xml:"duration,attr"`
		Size     string `xml:"size,attr"`
		URL      string `xml:",chardata"`
	} `xml:"res"`
}

func (e *env) browse(object, flag string, start, count int, sort string) (*browsed, int) {
	e.t.Helper()
	args := fmt.Sprintf("<ObjectID>%s</ObjectID><BrowseFlag>%s</BrowseFlag><Filter>*</Filter><StartingIndex>%d</StartingIndex><RequestedCount>%d</RequestedCount><SortCriteria>%s</SortCriteria>",
		object, flag, start, count, sort)
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Browse xmlns:u="` + cdsNS + `">` + args + `</u:Browse></s:Body></s:Envelope>`
	rec := e.dlnaReq(http.MethodPost, "/dlna/cds/control", body, "SOAPACTION", `"`+cdsNS+`#Browse"`)
	if rec.Code != http.StatusOK {
		return nil, rec.Code
	}
	var env struct {
		Body struct {
			Resp struct {
				Result   string
				Returned int `xml:"NumberReturned"`
				Total    int `xml:"TotalMatches"`
			} `xml:"BrowseResponse"`
		}
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("respuesta SOAP inválida: %v\n%s", err, rec.Body)
	}
	b := &browsed{Result: env.Body.Resp.Result, Returned: env.Body.Resp.Returned, Total: env.Body.Resp.Total}
	var doc struct {
		Objects []didlObject `xml:",any"`
	}
	if err := xml.Unmarshal([]byte(b.Result), &doc); err != nil {
		e.t.Fatalf("DIDL-Lite inválido: %v\n%s", err, b.Result)
	}
	b.items = doc.Objects
	return b, rec.Code
}

func (e *env) mustBrowse(object, flag string, start, count int) *browsed {
	e.t.Helper()
	b, code := e.browse(object, flag, start, count, "")
	if code != http.StatusOK {
		e.t.Fatalf("Browse %s: %d", object, code)
	}
	return b
}

func titles(b *browsed) []string {
	var out []string
	for _, o := range b.items {
		out = append(out, o.Title)
	}
	return out
}

func TestDLNAPublicaSoloLasBibliotecasMarcadas(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	own := e.account(owner)
	file := m.addDLNAFile(m.item, "Prueba.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	_ = file

	root := e.mustBrowse("0", "BrowseDirectChildren", 0, 0)
	if root.Total != 1 || len(root.items) != 1 || root.items[0].Title != "Cine" || root.items[0].ParentID != "0" || root.items[0].ChildCount != "1" {
		t.Fatalf("raíz: %+v\n%s", root, root.Result)
	}
	meta := e.mustBrowse("0", "BrowseMetadata", 0, 0)
	if len(meta.items) != 1 || meta.items[0].ID != "0" || meta.items[0].ParentID != "-1" || meta.items[0].ChildCount != "1" || meta.items[0].Title != "Prueba" {
		t.Fatalf("metadatos de la raíz: %s", meta.Result)
	}

	// Una biblioteca sin la marca no aparece, aunque tenga contenido.
	other := e.movie(own.token)
	other.exec("UPDATE libraries SET name = 'Privada' WHERE id = ?", other.lib)
	other.addDLNAFile(other.item, "x.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	if root := e.mustBrowse("0", "BrowseDirectChildren", 0, 0); root.Total != 1 || titles(root)[0] != "Cine" {
		t.Fatalf("una biblioteca sin dlna_enabled se publicó: %v", titles(root))
	}
	// Y no se puede abrir por su identificador.
	if _, code := e.browse("lib:"+other.lib, "BrowseDirectChildren", 0, 0, ""); code != http.StatusInternalServerError {
		t.Fatalf("lib de una biblioteca no publicada: %d", code)
	}
	if _, code := e.browse("item:"+other.item, "BrowseMetadata", 0, 0, ""); code != http.StatusInternalServerError {
		t.Fatalf("item de una biblioteca no publicada: %d", code)
	}

	// Una biblioteca deshabilitada tampoco, aunque lleve la marca.
	other.exec("UPDATE libraries SET dlna_enabled = 1, enabled = 0 WHERE id = ?", other.lib)
	if root := e.mustBrowse("0", "BrowseDirectChildren", 0, 0); root.Total != 1 {
		t.Fatalf("una biblioteca deshabilitada se publicó: %v", titles(root))
	}
	// Habilitada, sí; por orden alfabético.
	other.exec("UPDATE libraries SET enabled = 1 WHERE id = ?", other.lib)
	if root := e.mustBrowse("0", "BrowseDirectChildren", 0, 0); strings.Join(titles(root), ",") != "Cine,Privada" {
		t.Fatalf("%v", titles(root))
	}
	// Quitar la marca la oculta de inmediato.
	other.exec("UPDATE libraries SET dlna_enabled = 0 WHERE id = ?", other.lib)
	if root := e.mustBrowse("0", "BrowseDirectChildren", 0, 0); root.Total != 1 {
		t.Fatalf("%v", titles(root))
	}
}

func TestDLNAOcultaLoQueNoSePuedeReproducir(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	m.addDLNAFile(m.item, "Prueba.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	sin := m.addMovie("Sin archivo")
	perdida := m.addMovie("Perdida")
	f := m.addDLNAFile(perdida, "perdida.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	m.exec("UPDATE media_files SET missing_since = '2026-01-01T00:00:00.000Z' WHERE id = ?", f)
	noProbe := m.addMovie("Sin analizar")
	g := m.addDLNAFile(noProbe, "x.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	m.exec("UPDATE media_files SET probe_status = 'pending' WHERE id = ?", g)
	_ = sin

	b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
	if b.Total != 1 || strings.Join(titles(b), ",") != "Prueba" {
		t.Fatalf("solo debe verse el que se puede reproducir: total=%d %v", b.Total, titles(b))
	}
}

func TestDLNAEligeEntregaDirectaOMPEGTS(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		ffmpeg    bool
		container string
		fixture   string
		streams   []streamRow
		wantPath  string
		wantMIME  string
		wantOP    string
		wantCI    string
	}{
		{"MP4 H.264+AAC va directo", "auto", true, "mp4", "mp4_h264_aac.mp4", h264aac, "/dlna/media/", "video/mp4", "01", "0"},
		{"MKV pasa a MPEG-TS", "auto", true, "mkv", "mkv_h264_aac.mkv", h264aac, "/dlna/ts/", "video/mpeg", "10", "1"},
		{"HEVC se recodifica", "auto", true, "mp4", "mp4_h264_aac.mp4", hevcaac, "/dlna/ts/", "video/mpeg", "10", "1"},
		{"modo direct nunca recodifica", "direct", true, "mkv", "mkv_h264_aac.mkv", h264aac, "/dlna/media/", "video/x-matroska", "01", "0"},
		{"modo transcode siempre pasa por TS", "transcode", true, "mp4", "mp4_h264_aac.mp4", h264aac, "/dlna/ts/", "video/mpeg", "10", "1"},
		{"sin FFmpeg se entrega tal cual", "auto", false, "mkv", "mkv_h264_aac.mkv", h264aac, "/dlna/media/", "video/x-matroska", "01", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			heavy := 0
			if c.ffmpeg {
				heavy = 1
			}
			e, m := dlnaEnv(t, c.mode, heavy, heavy)
			file := m.addDLNAFile(m.item, "Prueba."+c.container, c.fixture, c.container, 4023, c.streams)
			b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
			if len(b.items) != 1 || len(b.items[0].Res) != 1 {
				t.Fatalf("%s", b.Result)
			}
			it, res := b.items[0], b.items[0].Res[0]
			if it.Class != "object.item.videoItem.movie" || it.ParentID != "lib:"+m.lib || it.ID != "item:"+m.item {
				t.Errorf("objeto: %+v", it)
			}
			want := "http://192.168.1.10:8420" + c.wantPath + file + "/"
			if !strings.HasPrefix(res.URL, want) {
				t.Errorf("URL = %q, se esperaba que empezara por %q", res.URL, want)
			}
			if !strings.HasPrefix(res.Protocol, "http-get:*:"+c.wantMIME+":") ||
				!strings.Contains(res.Protocol, "DLNA.ORG_OP="+c.wantOP+";") || !strings.Contains(res.Protocol, "DLNA.ORG_CI="+c.wantCI+";") {
				t.Errorf("protocolInfo = %q", res.Protocol)
			}
			if res.Duration != "0:00:04.023" {
				t.Errorf("duración = %q", res.Duration)
			}
			if c.wantPath == "/dlna/media/" && res.Size == "" {
				t.Error("un archivo original anuncia su tamaño")
			}
			if c.wantPath == "/dlna/ts/" && res.Size != "" {
				t.Error("un flujo en directo no tiene tamaño")
			}
		})
	}
}

func TestDLNAJerarquiaDeSeries(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	m.exec("UPDATE libraries SET type = 'series', name = 'Series' WHERE id = ?", m.lib)
	series, season1, season2 := m.id(), m.id(), m.id()
	m.exec("INSERT INTO media_items (id, library_id, type, title, sort_title, min_age) VALUES (?, ?, 'series', 'Mi Serie', 'mi serie', 0)", series, m.lib)
	m.exec("INSERT INTO media_items (id, library_id, parent_id, type, title, sort_title, season_number) VALUES (?, ?, ?, 'season', 'Temporada 1', 't1', 1)", season1, m.lib, series)
	m.exec("INSERT INTO media_items (id, library_id, parent_id, type, title, sort_title, season_number) VALUES (?, ?, ?, 'season', 'Temporada 2', 't2', 2)", season2, m.lib, series)
	for n := 1; n <= 3; n++ {
		ep := m.id()
		m.exec("INSERT INTO media_items (id, library_id, parent_id, type, title, sort_title, season_number, episode_number) VALUES (?, ?, ?, 'episode', ?, ?, 1, ?)",
			ep, m.lib, season1, fmt.Sprintf("Capítulo %d", n), fmt.Sprintf("c%d", n), n)
		m.addDLNAFile(ep, fmt.Sprintf("s01e0%d.mp4", n), "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	}
	// Los episodios vienen ordenados por número, no por título.
	m.exec("UPDATE media_items SET title = 'Z primero' WHERE parent_id = ? AND episode_number = 1", season1)

	lib := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
	if len(lib.items) != 1 || lib.items[0].XMLName.Local != "container" || lib.items[0].Title != "Mi Serie" || lib.items[0].ChildCount != "2" || lib.items[0].ParentID != "lib:"+m.lib {
		t.Fatalf("%s", lib.Result)
	}
	seasons := e.mustBrowse("item:"+series, "BrowseDirectChildren", 0, 0)
	if strings.Join(titles(seasons), ",") != "Temporada 1,Temporada 2" || seasons.items[0].ParentID != "item:"+series || seasons.items[0].ChildCount != "3" || seasons.items[1].ChildCount != "0" {
		t.Fatalf("%s", seasons.Result)
	}
	eps := e.mustBrowse("item:"+season1, "BrowseDirectChildren", 0, 0)
	if strings.Join(titles(eps), ",") != "1x01 Z primero,1x02 Capítulo 2,1x03 Capítulo 3" || eps.items[0].XMLName.Local != "item" ||
		eps.items[0].Class != "object.item.videoItem" || eps.items[0].ParentID != "item:"+season1 || len(eps.items[0].Res) != 1 {
		t.Fatalf("%s", eps.Result)
	}
	// Paginación: el total es el de todos, lo devuelto es la página.
	page := e.mustBrowse("item:"+season1, "BrowseDirectChildren", 1, 1)
	if page.Total != 3 || page.Returned != 1 || titles(page)[0] != "1x02 Capítulo 2" {
		t.Fatalf("página: %+v %v", page, titles(page))
	}
	if past := e.mustBrowse("item:"+season1, "BrowseDirectChildren", 5, 10); past.Total != 3 || past.Returned != 0 {
		t.Fatalf("más allá del final: %+v", past)
	}
	// Un episodio no es un contenedor.
	if _, code := e.browse("item:"+eps.items[0].ID[5:], "BrowseDirectChildren", 0, 0, ""); code != http.StatusInternalServerError {
		t.Fatalf("hijos de un episodio: %d", code)
	}
	meta := e.mustBrowse(eps.items[1].ID, "BrowseMetadata", 0, 0)
	if len(meta.items) != 1 || meta.items[0].Title != "1x02 Capítulo 2" || len(meta.items[0].Res) != 1 {
		t.Fatalf("metadatos de un episodio: %s", meta.Result)
	}
}

func TestDLNAOrdenYPaginacionDeLaBiblioteca(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	m.exec("DELETE FROM media_items WHERE id = ?", m.item)
	for _, title := range []string{"Beta", "Alfa", "Gamma", "Delta"} {
		id := m.addMovie(title)
		m.addDLNAFile(id, title+".mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	}
	b, _ := e.browse("lib:"+m.lib, "BrowseDirectChildren", 0, 0, "+dc:title")
	if strings.Join(titles(b), ",") != "Alfa,Beta,Delta,Gamma" {
		t.Fatalf("ascendente: %v", titles(b))
	}
	b, _ = e.browse("lib:"+m.lib, "BrowseDirectChildren", 0, 0, "-dc:title")
	if strings.Join(titles(b), ",") != "Gamma,Delta,Beta,Alfa" {
		t.Fatalf("descendente: %v", titles(b))
	}
	b, _ = e.browse("lib:"+m.lib, "BrowseDirectChildren", 1, 2, "-dc:title")
	if b.Total != 4 || b.Returned != 2 || strings.Join(titles(b), ",") != "Delta,Beta" {
		t.Fatalf("página descendente: %+v %v", b, titles(b))
	}
	// Un criterio que no se soporta no deja la biblioteca vacía.
	b, _ = e.browse("lib:"+m.lib, "BrowseDirectChildren", 0, 0, "+upnp:originalTrackNumber")
	if b.Total != 4 {
		t.Fatalf("criterio desconocido: %+v", b)
	}
	meta := e.mustBrowse("lib:"+m.lib, "BrowseMetadata", 0, 0)
	if len(meta.items) != 1 || meta.items[0].Title != "Cine" || meta.items[0].ChildCount != "4" {
		t.Fatalf("%s", meta.Result)
	}
}

// sessionRows cuenta las sesiones de DLNA en la base.
func (e *env) dlnaSessions() (n int, ip, status, mode string, userNull bool) {
	e.t.Helper()
	rows, err := e.db.Read.Query("SELECT client_ip, status, playback_mode, user_id IS NULL AND device_id IS NULL FROM playback_sessions WHERE client_kind = 'dlna' ORDER BY started_at")
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		n++
		if err := rows.Scan(&ip, &status, &mode, &userNull); err != nil {
			e.t.Fatal(err)
		}
	}
	return
}

func TestDLNADirectPlayConRangosYSesion(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	file := m.addDLNAFile(m.item, "Prueba.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
	url := strings.TrimPrefix(b.items[0].Res[0].URL, "http://192.168.1.10:8420")
	if !strings.HasPrefix(url, "/dlna/media/"+file+"/") || !strings.HasSuffix(url, ".mp4") {
		t.Fatalf("url = %q", url)
	}
	want, err := os.ReadFile(filepath.Join(m.root, "Prueba.mp4"))
	if err != nil {
		t.Fatal(err)
	}

	// HEAD: sondeo, no es una reproducción.
	head := e.dlnaReq(http.MethodHead, url, "")
	if head.Code != 200 || head.Header().Get("Content-Type") != "video/mp4" || head.Header().Get("Content-Length") != fmt.Sprint(len(want)) ||
		head.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header())
	}
	if n, _, _, _, _ := e.dlnaSessions(); n != 0 {
		t.Fatalf("un HEAD abrió %d sesiones", n)
	}

	rec := e.dlnaReq(http.MethodGet, url, "", "Range", "bytes=10-99", "getcontentFeatures.dlna.org", "1")
	if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Range") != fmt.Sprintf("bytes 10-99/%d", len(want)) ||
		rec.Body.String() != string(want[10:100]) {
		t.Fatalf("GET con rango: %d %v", rec.Code, rec.Header())
	}
	h := rec.Header()
	if h.Get("transferMode.dlna.org") != "Streaming" || !strings.Contains(h.Get("contentFeatures.dlna.org"), "DLNA.ORG_OP=01") ||
		h.Get("Content-Type") != "video/mp4" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-cache" {
		t.Fatalf("cabeceras DLNA: %v", h)
	}
	if full := e.dlnaReq(http.MethodGet, url, ""); full.Code != 200 || full.Body.Len() != len(want) {
		t.Fatalf("GET completo: %d %d", full.Code, full.Body.Len())
	}

	// Todas las conexiones del mismo televisor son una sola sesión, sin usuario ni dispositivo.
	n, ip, status, mode, anon := e.dlnaSessions()
	if n != 1 || ip != "192.168.1.50" || status != "playing" || mode != "direct_play" || !anon {
		t.Fatalf("sesiones: n=%d ip=%q status=%q mode=%q sin usuario=%v", n, ip, status, mode, anon)
	}
	// Otro televisor, otra sesión.
	if rec := e.dlnaFrom("192.168.1.51:1", http.MethodGet, url, "", "Range", "bytes=0-9"); rec.Code != http.StatusPartialContent {
		t.Fatalf("segundo cliente: %d", rec.Code)
	}
	if n, _, _, _, _ := e.dlnaSessions(); n != 2 {
		t.Fatalf("sesiones = %d", n)
	}
	// No genera progreso ni historial por usuario.
	var progress, history int
	_ = e.db.Read.QueryRow("SELECT (SELECT COUNT(*) FROM playback_progress), (SELECT COUNT(*) FROM playback_history)").Scan(&progress, &history)
	if progress != 0 || history != 0 {
		t.Fatalf("progreso=%d historial=%d", progress, history)
	}
	// El administrador la ve en la lista de sesiones activas.
	own := e.account(owner)
	rec2, _ := e.do(call{method: http.MethodGet, path: apiBase + "/admin/sessions", bearer: own.token})
	var list []map[string]any
	_ = decodeJSON(rec2.Body.Bytes(), &list)
	var dl int
	for _, s := range list {
		if s["client_kind"] == "dlna" {
			dl++
			if s["user"] != "" || s["device_id"] != "" || s["local"] != true {
				t.Errorf("sesión DLNA en el panel: %v", s)
			}
		}
	}
	if dl != 2 {
		t.Fatalf("sesiones DLNA en el panel: %d de %d", dl, len(list))
	}
}

func TestDLNANoEntregaLoQueNoEstaPublicado(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	file := m.addDLNAFile(m.item, "Prueba.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	url := "/dlna/media/" + file + "/x.mp4"
	if rec := e.dlnaReq(http.MethodGet, url, "", "Range", "bytes=0-9"); rec.Code != http.StatusPartialContent {
		t.Fatalf("publicado: %d", rec.Code)
	}
	before, _, _, _, _ := e.dlnaSessions()

	// Sin la marca: nada.
	m.exec("UPDATE libraries SET dlna_enabled = 0 WHERE id = ?", m.lib)
	for _, p := range []string{url, "/dlna/ts/" + file + "/x.ts"} {
		if rec := e.dlnaReq(http.MethodGet, p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s sin dlna_enabled: %d", p, rec.Code)
		}
	}
	m.exec("UPDATE libraries SET dlna_enabled = 1, enabled = 0 WHERE id = ?", m.lib)
	if rec := e.dlnaReq(http.MethodGet, url, ""); rec.Code != http.StatusNotFound {
		t.Errorf("biblioteca deshabilitada: %d", rec.Code)
	}
	m.exec("UPDATE libraries SET enabled = 1 WHERE id = ?", m.lib)
	// Archivo que ya no está.
	m.exec("UPDATE media_files SET missing_since = '2026-01-01T00:00:00.000Z' WHERE id = ?", file)
	if rec := e.dlnaReq(http.MethodGet, url, ""); rec.Code != http.StatusNotFound {
		t.Errorf("archivo perdido: %d", rec.Code)
	}
	m.exec("UPDATE media_files SET missing_since = NULL WHERE id = ?", file)
	// Archivo borrado del disco.
	if err := os.Remove(filepath.Join(m.root, "Prueba.mp4")); err != nil {
		t.Fatal(err)
	}
	if rec := e.dlnaReq(http.MethodGet, url, ""); rec.Code != http.StatusNotFound {
		t.Errorf("archivo borrado: %d", rec.Code)
	}
	// Identificadores que no son de la base de datos no llegan a consultarla.
	for _, id := range []string{"..", "..%2F..%2Fetc%2Fpasswd", "x'%20OR%201=1--", "no-existe", "%00"} {
		for _, kind := range []string{"media", "ts"} {
			// El enrutador limpia «..» y redirige: no entrega nada, pero tampoco es un 404.
			if rec := e.dlnaReq(http.MethodGet, "/dlna/"+kind+"/"+id+"/x", ""); rec.Code != http.StatusNotFound && rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("/dlna/%s/%s: %d", kind, id, rec.Code)
			}
		}
		if rec := e.dlnaReq(http.MethodGet, "/dlna/art/"+id+".jpg", ""); rec.Code != http.StatusNotFound {
			t.Errorf("art %s: %d", id, rec.Code)
		}
	}
	if after, _, _, _, _ := e.dlnaSessions(); after != before {
		t.Fatalf("lo que no se entrega no abre sesiones: %d → %d", before, after)
	}
}

func TestDLNANoSePuedeAlcanzarDesdeFuera(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	file := m.addDLNAFile(m.item, "Prueba.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	// Con el acceso remoto abierto de par en par, DLNA sigue sin existir fuera.
	e = newEnvOn(t, e.db, func(eo *envOptions) {
		eo.remote = &remote.Options{Enabled: true, Direct: true, AdminEnabled: true, TrustedProxies: []string{"10.0.0.1"}}
		eo.dlna = &dlna.Options{HTTPPort: 8420, Mode: "auto"}
	})
	for _, p := range []string{"/dlna/device.xml", "/dlna/cds.xml", "/dlna/media/" + file + "/x.mp4", "/dlna/art/x.jpg"} {
		if rec := e.dlnaFrom("203.0.113.9:1", http.MethodGet, p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s desde fuera: %d", p, rec.Code)
		}
		if rec := e.dlnaFrom("10.0.0.1:1", http.MethodGet, p, "", "X-Forwarded-For", "203.0.113.9"); rec.Code != http.StatusNotFound {
			t.Errorf("%s por un proxy, cliente de fuera: %d", p, rec.Code)
		}
		if rec := e.dlnaFrom("10.0.0.1:1", http.MethodGet, p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s por un proxy sin X-Forwarded-For: %d", p, rec.Code)
		}
	}
	if rec := e.dlnaFrom("10.0.0.1:1", http.MethodGet, "/dlna/device.xml", "", "X-Forwarded-For", "192.168.1.20"); rec.Code != 200 {
		t.Errorf("cliente local por el proxy: %d", rec.Code)
	}
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="x"><s:Body><u:Browse xmlns:u="` + cdsNS + `"><ObjectID>0</ObjectID><BrowseFlag>BrowseMetadata</BrowseFlag></u:Browse></s:Body></s:Envelope>`
	if rec := e.dlnaFrom("203.0.113.9:1", http.MethodPost, "/dlna/cds/control", body); rec.Code != http.StatusNotFound {
		t.Errorf("Browse desde fuera: %d", rec.Code)
	}
}

func TestDLNADesactivadoNoExiste(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/dlna/device.xml", "/dlna/cds.xml"} {
		if rec := e.dlnaReq(http.MethodGet, p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s con DLNA desactivado: %d", p, rec.Code)
		}
	}
}

func TestDLNACaratulas(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	m.addDLNAFile(m.item, "Prueba.mp4", "mp4_h264_aac.mp4", "mp4", 4023, h264aac)
	if b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0); b.items[0].Art != "" {
		t.Fatalf("sin carátula no se anuncia ninguna: %q", b.items[0].Art)
	}
	if err := os.WriteFile(filepath.Join(e.imgDir, "poster.jpg"), []byte("\xff\xd8\xff\xe0jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.exec("INSERT INTO media_images (id, media_item_id, type, local_path) VALUES (?, ?, 'poster', 'poster.jpg')", m.id(), m.item)
	b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
	art := strings.TrimPrefix(b.items[0].Art, "http://192.168.1.10:8420")
	if art != "/dlna/art/"+m.item+".jpg" {
		t.Fatalf("art = %q", b.items[0].Art)
	}
	rec := e.dlnaReq(http.MethodGet, art, "")
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "\xff\xd8") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	// Con la biblioteca sin publicar, la carátula tampoco se entrega.
	m.exec("UPDATE libraries SET dlna_enabled = 0 WHERE id = ?", m.lib)
	if rec := e.dlnaReq(http.MethodGet, art, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("carátula de una biblioteca no publicada: %d", rec.Code)
	}
	// Una ruta de imagen que se sale del directorio no se abre.
	m.exec("UPDATE libraries SET dlna_enabled = 1 WHERE id = ?", m.lib)
	m.exec("UPDATE media_images SET local_path = '../secreto.txt' WHERE media_item_id = ?", m.item)
	if rec := e.dlnaReq(http.MethodGet, art, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("ruta fuera del directorio: %d", rec.Code)
	}
}

func decodeJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// --- Transcodificación a MPEG-TS, con FFmpeg de verdad ---

// liveServer sirve el enrutador por un socket real: un flujo en directo no
// cabe en un ResponseRecorder, que lo guardaría entero.
func (e *env) liveServer() *httptest.Server {
	srv := httptest.NewServer(e.h)
	e.t.Cleanup(srv.Close)
	return srv
}

// readTS lee n paquetes MPEG-TS de la respuesta y comprueba su sincronía.
func readTS(t *testing.T, body io.Reader, packets int) []byte {
	t.Helper()
	buf := make([]byte, 188*packets)
	if _, err := io.ReadFull(body, buf); err != nil {
		t.Fatalf("leer %d paquetes TS: %v", packets, err)
	}
	for i := 0; i < len(buf); i += 188 {
		if buf[i] != 0x47 {
			t.Fatalf("el paquete %d no empieza por el byte de sincronía: %#x", i/188, buf[i])
		}
	}
	return buf
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("no ocurrió: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDLNAMPEGTSCopiandoElVideo(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 1, 1)
	file := m.addDLNAFile(m.item, "Larga.mkv", "seek_120s_h264.mkv", "mkv", 120023, h264aac)
	srv := e.liveServer()

	resp, err := http.Get(srv.URL + "/dlna/ts/" + file + "/Larga.ts")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mpeg" || resp.Header.Get("Accept-Ranges") != "none" ||
		resp.Header.Get("transferMode.dlna.org") != "Streaming" || !strings.Contains(resp.Header.Get("contentFeatures.dlna.org"), "DLNA.ORG_OP=10") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	readTS(t, resp.Body, 300)
	n, _, status, mode, anon := e.dlnaSessions()
	if n != 1 || mode != "direct_stream" || !anon || (status != "playing") {
		t.Fatalf("sesión: n=%d status=%s mode=%s sin usuario=%v", n, status, mode, anon)
	}
	jobs := e.transcoder.Jobs()
	if len(jobs) != 1 || jobs[0].Kind != "light" {
		t.Fatalf("trabajos: %+v", jobs)
	}
	// Cerrar la conexión para FFmpeg y devuelve el cupo.
	resp.Body.Close()
	waitFor(t, "que se pare FFmpeg al cerrar la conexión", func() bool { return len(e.transcoder.Jobs()) == 0 })
	waitFor(t, "que se libere el cupo", func() bool { used, _ := e.transcoder.Admission().Usage(); return used["light"] == 0 })
}

func TestDLNAMPEGTSRecodificando(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 1, 1)
	file := m.addDLNAFile(m.item, "Larga.mkv", "seek_120s_h264.mkv", "mkv", 120023, hevcaac) // «hevc» obliga a recodificar.
	srv := e.liveServer()
	resp, err := http.Get(srv.URL + "/dlna/ts/" + file + "/Larga.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%d", resp.StatusCode)
	}
	readTS(t, resp.Body, 100)
	if n, _, _, mode, _ := e.dlnaSessions(); n != 1 || mode != "transcode" {
		t.Fatalf("sesión: n=%d mode=%s", n, mode)
	}
	jobs := e.transcoder.Jobs()
	if len(jobs) != 1 || jobs[0].Kind != "heavy" || jobs[0].Encoder == "" {
		t.Fatalf("trabajos: %+v", jobs)
	}
	// El administrador puede cancelarlo: la sesión termina y FFmpeg se para.
	own := e.account(owner)
	rec, _ := e.do(call{method: http.MethodPost, path: apiBase + "/admin/transcodes/" + jobs[0].ID + "/cancel", bearer: own.token})
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("cancelar: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, "que se pare FFmpeg al cancelar", func() bool { return len(e.transcoder.Jobs()) == 0 })
	waitFor(t, "que se libere el cupo", func() bool { used, _ := e.transcoder.Admission().Usage(); return used["heavy"] == 0 })
}

func TestDLNASaltoPorTiempo(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 1, 1)
	file := m.addDLNAFile(m.item, "Larga.mkv", "seek_120s_h264.mkv", "mkv", 120023, h264aac)
	srv := e.liveServer()
	get := func(seek string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/dlna/ts/"+file+"/Larga.ts", nil)
		if seek != "" {
			req.Header.Set("TimeSeekRange.dlna.org", seek)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := get("npt=60-")
	if resp.StatusCode != 200 || resp.Header.Get("TimeSeekRange.dlna.org") != "npt=60.000-120.023/120.023" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	readTS(t, resp.Body, 100)
	resp.Body.Close()
	waitFor(t, "que termine el flujo", func() bool { return len(e.transcoder.Jobs()) == 0 })

	// Formato horario.
	resp = get("npt=0:01:30.5-")
	if resp.Header.Get("TimeSeekRange.dlna.org") != "npt=90.500-120.023/120.023" {
		t.Fatalf("%v", resp.Header)
	}
	resp.Body.Close()
	waitFor(t, "que termine el flujo", func() bool { return len(e.transcoder.Jobs()) == 0 })

	// Fuera de la duración o ilegible: 406, y no se lanza FFmpeg.
	for _, bad := range []string{"npt=121-", "npt=120.023-", "npt=abc-", "bytes=0-", "npt=-5"} {
		resp := get(bad)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotAcceptable {
			t.Errorf("%q: %d", bad, resp.StatusCode)
		}
	}
	if jobs := e.transcoder.Jobs(); len(jobs) != 0 {
		t.Fatalf("un salto inválido lanzó FFmpeg: %+v", jobs)
	}
	// HEAD anuncia las cabeceras sin lanzar nada.
	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/dlna/ts/"+file+"/Larga.ts", nil)
	head, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	head.Body.Close()
	if head.StatusCode != 200 || head.Header.Get("Content-Type") != "video/mpeg" || len(e.transcoder.Jobs()) != 0 {
		t.Fatalf("HEAD: %d %v", head.StatusCode, head.Header)
	}
}

func TestDLNASinCupoResponde503(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 1, 1)
	a := m.addDLNAFile(m.item, "A.mkv", "seek_120s_h264.mkv", "mkv", 120023, hevcaac)
	second := m.addMovie("Segunda")
	b := m.addDLNAFile(second, "B.mkv", "seek_120s_h264.mkv", "mkv", 120023, hevcaac)
	srv := e.liveServer()

	first, err := http.Get(srv.URL + "/dlna/ts/" + a + "/A.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	readTS(t, first.Body, 20)

	// Otro televisor pide otro vídeo pesado: con un cupo, espera.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/dlna/ts/"+b+"/B.ts", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("sin cupo: %d %v", resp.StatusCode, resp.Header)
	}
	if len(e.transcoder.Jobs()) != 1 {
		t.Fatalf("trabajos: %+v", e.transcoder.Jobs())
	}
	// La sesión que se quedó sin cupo no debe quedar abierta de más con su reserva.
	if used, _ := e.transcoder.Admission().Usage(); used["heavy"] != 1 {
		t.Fatalf("cupos en uso: %v", used)
	}
}

func TestDLNAPlanSinFFmpegEntregaTalCual(t *testing.T) {
	e, m := dlnaEnv(t, "transcode", 0, 0)
	file := m.addDLNAFile(m.item, "Prueba.mkv", "mkv_h264_aac.mkv", "mkv", 4023, h264aac)
	// Aunque alguien pida la URL de MPEG-TS, sin FFmpeg se sirve el original.
	rec := e.dlnaReq(http.MethodGet, "/dlna/ts/"+file+"/x.ts", "", "Range", "bytes=0-9")
	if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Type") != "video/x-matroska" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

var _ = context.Background
var _ = playback.DLNAAuto
