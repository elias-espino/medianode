package routes

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (e *env) search(container, criteria string, start, count int, sort string) (*browsed, int) {
	e.t.Helper()
	args := fmt.Sprintf("<ContainerID>%s</ContainerID><SearchCriteria>%s</SearchCriteria><Filter>*</Filter><StartingIndex>%d</StartingIndex><RequestedCount>%d</RequestedCount><SortCriteria>%s</SortCriteria>",
		container, xmlEscape(criteria), start, count, sort)
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:Search xmlns:u="` + cdsNS + `">` + args + `</u:Search></s:Body></s:Envelope>`
	rec := e.dlnaReq(http.MethodPost, "/dlna/cds/control", body, "SOAPACTION", `"`+cdsNS+`#Search"`)
	if rec.Code != http.StatusOK {
		e.lastFault = rec.Body.String()
		return nil, rec.Code
	}
	var env struct {
		Body struct {
			Resp struct {
				Result   string
				Returned int `xml:"NumberReturned"`
				Total    int `xml:"TotalMatches"`
			} `xml:"SearchResponse"`
		}
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("respuesta inválida: %v\n%s", err, rec.Body)
	}
	b := &browsed{Result: env.Body.Resp.Result, Returned: env.Body.Resp.Returned, Total: env.Body.Resp.Total}
	var doc struct {
		Objects []didlObject `xml:",any"`
	}
	if err := xml.Unmarshal([]byte(b.Result), &doc); err != nil {
		e.t.Fatalf("DIDL-Lite inválido: %v", err)
	}
	b.items = doc.Objects
	return b, rec.Code
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func TestDLNABuscar(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	m.exec("DELETE FROM media_items WHERE id = ?", m.item)
	for _, title := range []string{"Alfa Uno", "Beta Dos", "Gamma Alfa", "Delta"} {
		id := m.addMovie(title)
		m.addDLNAFile(id, title+".mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	}
	m.addMovie("Sin archivo")

	// Una serie en otra biblioteca publicada.
	own := e.account(owner)
	tv := e.movie(own.token)
	tv.exec("UPDATE libraries SET type = 'series', name = 'Series', dlna_enabled = 1 WHERE id = ?", tv.lib)
	tv.exec("DELETE FROM media_items WHERE id = ?", tv.item)
	series, s1, s2 := tv.id(), tv.id(), tv.id()
	tv.exec("INSERT INTO media_items (id, library_id, type, title, sort_title, min_age) VALUES (?, ?, 'series', 'Alfa Serie', 'alfa serie', 0)", series, tv.lib)
	tv.exec("INSERT INTO media_items (id, library_id, parent_id, type, title, sort_title, season_number) VALUES (?, ?, ?, 'season', 'T1', 't1', 1)", s1, tv.lib, series)
	tv.exec("INSERT INTO media_items (id, library_id, parent_id, type, title, sort_title, season_number) VALUES (?, ?, ?, 'season', 'T2', 't2', 2)", s2, tv.lib, series)
	for season, n := range map[string]int{s1: 1, s2: 2} {
		ep := tv.id()
		tv.exec("INSERT INTO media_items (id, library_id, parent_id, type, title, sort_title, season_number, episode_number) VALUES (?, ?, ?, 'episode', ?, ?, ?, 1)",
			ep, tv.lib, season, fmt.Sprintf("Piloto %d", n), fmt.Sprintf("p%d", n), n)
		tv.addDLNAFile(ep, fmt.Sprintf("ep%d.mp4", n), "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	}
	// Una biblioteca sin publicar, con un título que coincidiría.
	hidden := e.movie(own.token)
	hidden.exec("UPDATE libraries SET name = 'Privada' WHERE id = ?", hidden.lib)
	hidden.exec("UPDATE media_items SET title = 'Alfa secreta' WHERE id = ?", hidden.item)
	hidden.addDLNAFile(hidden.item, "s.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)

	names := func(b *browsed) string { return strings.Join(titles(b), "|") }
	run := func(container, crit string) *browsed {
		t.Helper()
		b, code := e.search(container, crit, 0, 0, "")
		if code != http.StatusOK {
			t.Fatalf("%s %q: %d %s", container, crit, code, e.lastFault)
		}
		return b
	}

	if got := names(run("0", `*`)); got != "Alfa Uno|Beta Dos|Delta|Gamma Alfa|1x01 Piloto 1|2x01 Piloto 2" {
		t.Fatalf("todo: %s", got)
	}
	if got := names(run("0", `dc:title contains "alfa"`)); got != "Alfa Uno|Gamma Alfa" {
		t.Fatalf("por título (solo películas con archivo, no series ni la privada): %s", got)
	}
	if got := names(run("0", `upnp:class derivedfrom "object.item.videoItem.movie"`)); got != "Alfa Uno|Beta Dos|Delta|Gamma Alfa" {
		t.Fatalf("películas: %s", got)
	}
	if got := names(run("0", `upnp:class = "object.item.videoItem"`)); got != "1x01 Piloto 1|2x01 Piloto 2" {
		t.Fatalf("episodios: %s", got)
	}
	if got := names(run("0", `dc:title contains "1x01" or dc:title = "delta"`)); got != "Delta|1x01 Piloto 1" {
		t.Fatalf("o: %s", got)
	}
	// Por contenedor: la biblioteca, la serie y una temporada.
	if got := names(run("lib:"+m.lib, `*`)); got != "Alfa Uno|Beta Dos|Delta|Gamma Alfa" {
		t.Fatalf("en la biblioteca: %s", got)
	}
	if got := names(run("item:"+series, `*`)); got != "1x01 Piloto 1|2x01 Piloto 2" {
		t.Fatalf("en la serie: %s", got)
	}
	if got := names(run("item:"+s2, `*`)); got != "2x01 Piloto 2" {
		t.Fatalf("en la temporada: %s", got)
	}
	// Orden y paginación.
	if b, _ := e.search("lib:"+m.lib, `*`, 0, 0, "-dc:title"); names(b) != "Gamma Alfa|Delta|Beta Dos|Alfa Uno" {
		t.Fatalf("descendente: %s", names(b))
	}
	b, _ := e.search("lib:"+m.lib, `*`, 1, 2, "")
	if b.Total != 4 || b.Returned != 2 || names(b) != "Beta Dos|Delta" {
		t.Fatalf("página: %+v %s", b, names(b))
	}
	if b := run("0", `dc:title contains "no existe"`); b.Total != 0 || b.Returned != 0 {
		t.Fatalf("sin resultados: %+v", b)
	}
	// Cada resultado trae su <res>, como en Browse.
	for _, o := range run("0", `*`).items {
		if len(o.Res) == 0 {
			t.Fatalf("%s sin res", o.Title)
		}
	}

	// Errores: criterio inválido (708) y contenedores que no existen o no se publican (710).
	for _, bad := range []string{`dc:title`, `upnp:artist = "x"`, `dc:title = "x" and`, `dc:title = "sin cerrar`} {
		if _, code := e.search("0", bad, 0, 0, ""); code != http.StatusInternalServerError || !strings.Contains(e.lastFault, "<errorCode>708</errorCode>") {
			t.Errorf("%q: %d %s", bad, code, e.lastFault)
		}
	}
	for _, c := range []string{"lib:" + hidden.lib, "lib:no-existe", "item:no-existe", "item:" + hidden.item, "otra", "lib:a'b", "item:../x"} {
		if _, code := e.search(c, `*`, 0, 0, ""); code != http.StatusInternalServerError || !strings.Contains(e.lastFault, "<errorCode>710</errorCode>") {
			t.Errorf("contenedor %q: %d %s", c, code, e.lastFault)
		}
	}
	if _, code := e.search("0", `*`, -1, 0, ""); code != http.StatusInternalServerError || !strings.Contains(e.lastFault, "<errorCode>402</errorCode>") {
		t.Errorf("índice negativo: %d %s", code, e.lastFault)
	}
	// Quitar la marca de una biblioteca la saca de la búsqueda al momento.
	tv.exec("UPDATE libraries SET dlna_enabled = 0 WHERE id = ?", tv.lib)
	if got := names(run("0", `*`)); got != "Alfa Uno|Beta Dos|Delta|Gamma Alfa" {
		t.Fatalf("tras despublicar: %s", got)
	}
}

func TestDLNASubtitulosExternos(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	file := m.addDLNAFile(m.item, "Pelicula.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	srt := "1\n00:00:01,000 --> 00:00:02,000\nHola\n"
	for name, body := range map[string]string{"Pelicula.es.srt": srt, "Pelicula.en.srt": "1\n00:00:01,000 --> 00:00:02,000\nHello\n", "Pelicula.ass": "[Script Info]\n"} {
		if err := os.WriteFile(filepath.Join(m.root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	add := func(id, name, lang, format string, def, forced int) {
		m.exec("INSERT INTO subtitles (id, media_file_id, relative_path, language, format, is_default, is_forced) VALUES (?, ?, ?, ?, ?, ?, ?)", id, file, name, lang, format, def, forced)
	}
	add("sub-ass", "Pelicula.ass", "spa", "ass", 1, 0)
	add("sub-en", "Pelicula.en.srt", "eng", "srt", 0, 0)
	add("sub-es", "Pelicula.es.srt", "spa", "srt", 0, 0)

	b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
	if len(b.items) != 1 {
		t.Fatalf("%s", b.Result)
	}
	// Se anuncia el SRT del idioma preferido (español), no el ASS marcado por defecto.
	const url = "http://192.168.1.10:8420/dlna/sub/sub-es.srt"
	if !strings.Contains(b.Result, `<sec:CaptionInfoEx sec:type="srt">`+url+`</sec:CaptionInfoEx>`) || !strings.Contains(b.Result, `xmlns:sec="http://www.sec.co.kr/"`) {
		t.Fatalf("falta CaptionInfoEx:\n%s", b.Result)
	}
	if len(b.items[0].Res) != 2 || b.items[0].Res[1].URL != url || b.items[0].Res[1].Protocol != "http-get:*:text/srt:*" {
		t.Fatalf("res: %+v", b.items[0].Res)
	}
	rec := e.dlnaReq(http.MethodGet, "/dlna/sub/sub-es.srt", "")
	if rec.Code != 200 || rec.Body.String() != srt || rec.Header().Get("Content-Type") != "text/srt; charset=utf-8" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	// La cabecera de Samsung solo se manda a quien la pide.
	media := "/dlna/media/" + file + "/x.mp4"
	if h := e.dlnaReq(http.MethodGet, media, "", "Range", "bytes=0-9", "getCaptionInfo.sec", "1").Header().Get("CaptionInfo.sec"); h != url {
		t.Fatalf("CaptionInfo.sec = %q", h)
	}
	if h := e.dlnaReq(http.MethodGet, media, "", "Range", "bytes=0-9").Header().Get("CaptionInfo.sec"); h != "" {
		t.Fatalf("sin pedirla, CaptionInfo.sec = %q", h)
	}

	// Lo que no se entrega: ASS, un id inexistente o peligroso, y un archivo que se sale.
	for _, p := range []string{"/dlna/sub/sub-ass.srt", "/dlna/sub/sub-ass.ass", "/dlna/sub/sub-es.vtt", "/dlna/sub/sub-es", "/dlna/sub/no-existe.srt", "/dlna/sub/a'b.srt", "/dlna/sub/.srt"} {
		if rec := e.dlnaReq(http.MethodGet, p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	add("sub-fuera", "../secreto.srt", "spa", "srt", 0, 0)
	if rec := e.dlnaReq(http.MethodGet, "/dlna/sub/sub-fuera.srt", ""); rec.Code != http.StatusNotFound {
		t.Errorf("ruta que sale de la biblioteca: %d", rec.Code)
	}
	// Con la biblioteca sin publicar, tampoco.
	m.exec("UPDATE libraries SET dlna_enabled = 0 WHERE id = ?", m.lib)
	if rec := e.dlnaReq(http.MethodGet, "/dlna/sub/sub-es.srt", ""); rec.Code != http.StatusNotFound {
		t.Errorf("biblioteca sin publicar: %d", rec.Code)
	}
	// Desde fuera de la red local, nunca.
	m.exec("UPDATE libraries SET dlna_enabled = 1 WHERE id = ?", m.lib)
	if rec := e.dlnaFrom(outside, http.MethodGet, "/dlna/sub/sub-es.srt", ""); rec.Code != http.StatusNotFound {
		t.Errorf("desde fuera: %d", rec.Code)
	}
}

func TestDLNASubtitulosExternosSegunElIdioma(t *testing.T) {
	pick := func(prepare func(add func(id, lang string, def, forced int))) string {
		e, m := dlnaEnv(t, "auto", 0, 0)
		file := m.addDLNAFile(m.item, "P.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
		n := 0
		prepare(func(id, lang string, def, forced int) {
			n++
			m.exec("INSERT INTO subtitles (id, media_file_id, relative_path, language, format, is_default, is_forced) VALUES (?, ?, ?, ?, 'srt', ?, ?)",
				id, file, fmt.Sprintf("s%d.srt", n), lang, def, forced)
		})
		b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
		const marker = "/dlna/sub/"
		i := strings.Index(b.Result, marker)
		if i < 0 {
			return ""
		}
		rest := b.Result[i+len(marker):]
		return rest[:strings.Index(rest, ".srt")]
	}
	if got := pick(func(add func(string, string, int, int)) {}); got != "" {
		t.Errorf("sin subtítulos: %q", got)
	}
	if got := pick(func(add func(string, string, int, int)) { add("en", "eng", 1, 0); add("es", "spa", 0, 0) }); got != "es" {
		t.Errorf("el idioma preferido gana al marcado por defecto: %q", got)
	}
	if got := pick(func(add func(string, string, int, int)) { add("es-forzado", "spa", 0, 1); add("en", "eng", 1, 0) }); got != "en" {
		t.Errorf("un subtítulo forzado no es el que se ofrece como principal: %q", got)
	}
	if got := pick(func(add func(string, string, int, int)) { add("a", "eng", 0, 0); add("b", "fra", 1, 0) }); got != "b" {
		t.Errorf("sin el idioma preferido, el marcado por defecto: %q", got)
	}
	if got := pick(func(add func(string, string, int, int)) { add("a", "eng", 0, 0); add("b", "fra", 0, 0) }); got != "a" {
		t.Errorf("sin nada más, el primero: %q", got)
	}
}

func TestDLNAUnFlujoMPEGTSNoLlevaSubtituloExterno(t *testing.T) {
	// En MPEG-TS el subtítulo, si lo hay, va grabado: el externo no se anuncia.
	e, m := dlnaEnv(t, "auto", 1, 1)
	file := m.addDLNAFile(m.item, "P.mkv", "mkv_h264_aac.mkv", "mkv", 4000, h264aac)
	m.exec("INSERT INTO subtitles (id, media_file_id, relative_path, language, format) VALUES ('s', ?, 's.srt', 'spa', 'srt')", file)
	b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
	if !strings.Contains(b.items[0].Res[0].URL, "/dlna/ts/") || strings.Contains(b.Result, "CaptionInfoEx") || len(b.items[0].Res) != 1 {
		t.Fatalf("%s", b.Result)
	}
}

func TestDLNALaCaratulaNoDeclaraUnPerfilQueNoSeHaValidado(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	m.addDLNAFile(m.item, "a.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	if err := os.WriteFile(filepath.Join(e.imgDir, "p.jpg"), []byte("\xff\xd8\xff\xe0jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.exec("INSERT INTO media_images (id, media_item_id, type, local_path) VALUES (?, ?, 'poster', 'p.jpg')", m.id(), m.item)
	b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
	if !strings.Contains(b.Result, "<upnp:albumArtURI>") || strings.Contains(b.Result, "profileID") {
		t.Fatalf("un perfil de imagen equivocado (JPEG_TN es de 160x160) hace que un televisor la rechace:\n%s", b.Result)
	}
}
