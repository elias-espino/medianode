package routes

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"medianode/internal/sessions"
)

// Un elemento con dos versiones, una sin analizar: se ofrece la analizada, aunque
// la otra salga antes en el orden de los archivos.
func TestDLNAOfreceLaVersionReproducibleDeUnElemento(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	pending := m.addDLNAFile(m.item, "a-primera.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	m.exec("UPDATE media_files SET probe_status = 'pending' WHERE id = ?", pending)
	good := m.addDLNAFile(m.item, "b-segunda.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)

	b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
	if len(b.items) != 1 || len(b.items[0].Res) != 1 {
		t.Fatalf("%s", b.Result)
	}
	if !strings.Contains(b.items[0].Res[0].URL, "/dlna/media/"+good+"/") {
		t.Fatalf("debía ofrecer %s, no la versión sin analizar: %s", good, b.items[0].Res[0].URL)
	}
}

func TestDLNAFechaDeEstreno(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	m.addDLNAFile(m.item, "a.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	date := func() string {
		b := e.mustBrowse("lib:"+m.lib, "BrowseDirectChildren", 0, 0)
		const open, closing = "<dc:date>", "</dc:date>"
		i := strings.Index(b.Result, open)
		if i < 0 {
			return ""
		}
		rest := b.Result[i+len(open):]
		return rest[:strings.Index(rest, closing)]
	}
	// Sin fecha ni año, no hay dc:date.
	if got := date(); got != "" {
		t.Fatalf("sin datos: %q", got)
	}
	m.exec("UPDATE media_items SET year = 1999 WHERE id = ?", m.item)
	if got := date(); got != "1999-01-01" {
		t.Fatalf("solo el año: %q", got)
	}
	// Una fecha de diez caracteres exactos se usa entera.
	m.exec("UPDATE media_items SET release_date = '2020-05-17' WHERE id = ?", m.item)
	if got := date(); got != "2020-05-17" {
		t.Fatalf("fecha de 10 caracteres: %q", got)
	}
	// Con hora, se recorta al día.
	m.exec("UPDATE media_items SET release_date = '2021-07-04T00:00:00.000Z' WHERE id = ?", m.item)
	if got := date(); got != "2021-07-04" {
		t.Fatalf("fecha con hora: %q", got)
	}
	// Una fecha demasiado corta no se usa: se cae al año.
	m.exec("UPDATE media_items SET release_date = '2020' WHERE id = ?", m.item)
	if got := date(); got != "1999-01-01" {
		t.Fatalf("fecha corta: %q", got)
	}
}

// Si el administrador cierra la sesión de un televisor, su siguiente petición
// abre otra: no sigue «reproduciendo» sobre una sesión que ya no existe.
func TestDLNAUnaSesionCerradaNoSeReutiliza(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 0, 0)
	file := m.addDLNAFile(m.item, "a.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	url := "/dlna/media/" + file + "/x.mp4"
	if rec := e.dlnaReq(http.MethodGet, url, "", "Range", "bytes=0-9"); rec.Code != http.StatusPartialContent {
		t.Fatalf("%d", rec.Code)
	}
	var first string
	if err := e.db.Read.QueryRow("SELECT id FROM playback_sessions WHERE client_kind = 'dlna'").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := e.sessions.Terminate(context.Background(), func(s sessions.Session) bool { return s.ID == first }); err != nil {
		t.Fatal(err)
	}
	if rec := e.dlnaReq(http.MethodGet, url, "", "Range", "bytes=0-9"); rec.Code != http.StatusPartialContent {
		t.Fatalf("%d", rec.Code)
	}
	var n, alive int
	_ = e.db.Read.QueryRow("SELECT COUNT(*) FROM playback_sessions WHERE client_kind = 'dlna'").Scan(&n)
	for _, s := range e.sessions.Snapshots() {
		if s.ClientKind == sessions.KindDLNA {
			alive++
			if s.ID == first {
				t.Fatal("se reutilizó la sesión cerrada")
			}
		}
	}
	if n != 2 || alive != 1 {
		t.Fatalf("sesiones guardadas = %d, vivas = %d", n, alive)
	}
}
