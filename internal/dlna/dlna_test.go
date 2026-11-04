package dlna

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"medianode/internal/remote"
	"medianode/internal/testutil"
)

func TestDescripcionDelDispositivo(t *testing.T) {
	data := deviceDescription(`Cine <"de"> & casa`, "1.2.3", testUUID)
	var root struct {
		XMLName xml.Name `xml:"root"`
		Device  struct {
			Type     string `xml:"deviceType"`
			Name     string `xml:"friendlyName"`
			UDN      string `xml:"UDN"`
			Model    string `xml:"modelNumber"`
			Services []struct {
				Type, ID, SCPD, Control, Event string
			} `xml:"serviceList>service"`
		} `xml:"device"`
	}
	if err := xml.Unmarshal(data, &root); err != nil {
		t.Fatalf("XML inválido: %v\n%s", err, data)
	}
	d := root.Device
	if d.Type != deviceTypeURN || d.Name != `Cine <"de"> & casa` || d.UDN != "uuid:"+testUUID || d.Model != "1.2.3" {
		t.Fatalf("%+v", d)
	}
	if len(d.Services) != 2 {
		t.Fatalf("servicios: %+v", d.Services)
	}
}

func TestDescripcionDeLosServicios(t *testing.T) {
	for name, tc := range map[string]struct {
		doc     []byte
		actions []string
	}{
		"cds": {cdsSCPD, []string{"GetSearchCapabilities", "GetSortCapabilities", "GetSystemUpdateID", "Browse"}},
		"cms": {cmsSCPD, []string{"GetProtocolInfo", "GetCurrentConnectionIDs", "GetCurrentConnectionInfo"}},
	} {
		var doc struct {
			Actions []struct {
				Name string `xml:"name"`
				Args []struct {
					Name      string `xml:"name"`
					Direction string `xml:"direction"`
					Related   string `xml:"relatedStateVariable"`
				} `xml:"argumentList>argument"`
			} `xml:"actionList>action"`
			Vars []struct {
				Name string `xml:"name"`
			} `xml:"serviceStateTable>stateVariable"`
		}
		if err := xml.Unmarshal(tc.doc, &doc); err != nil {
			t.Fatalf("%s: XML inválido: %v", name, err)
		}
		var got []string
		vars := map[string]bool{}
		for _, v := range doc.Vars {
			vars[v.Name] = true
		}
		for _, a := range doc.Actions {
			got = append(got, a.Name)
		}
		if strings.Join(got, ",") != strings.Join(tc.actions, ",") {
			t.Errorf("%s: acciones %v", name, got)
		}
		// Toda variable relacionada con un argumento debe existir en la tabla.
		for _, s := range []string{string(tc.doc)} {
			for _, v := range []string{"A_ARG_TYPE_ObjectID", "SystemUpdateID", "SourceProtocolInfo"} {
				if strings.Contains(s, "<relatedStateVariable>"+v+"<") && !vars[v] {
					t.Errorf("%s: falta la variable %s", name, v)
				}
			}
		}
	}
}

func TestParseSOAP(t *testing.T) {
	const ns = "urn:schemas-upnp-org:service:ContentDirectory:1"
	wrap := func(body string) string {
		return `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` + body + `</s:Body></s:Envelope>`
	}
	cases := []struct {
		name   string
		in     string
		action string
		args   map[string]string
		err    bool
	}{
		{"Browse", wrap(`<u:Browse xmlns:u="` + ns + `"><ObjectID>0</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag><Filter>*</Filter><StartingIndex>0</StartingIndex><RequestedCount>10</RequestedCount><SortCriteria></SortCriteria></u:Browse>`),
			"Browse", map[string]string{"ObjectID": "0", "BrowseFlag": "BrowseDirectChildren", "Filter": "*", "StartingIndex": "0", "RequestedCount": "10", "SortCriteria": ""}, false},
		{"argumento vacío en una sola etiqueta", wrap(`<u:Browse xmlns:u="` + ns + `"><SortCriteria/></u:Browse>`), "Browse", map[string]string{"SortCriteria": ""}, false},
		{"sin argumentos", wrap(`<u:GetSystemUpdateID xmlns:u="` + ns + `"/>`), "GetSystemUpdateID", map[string]string{}, false},
		{"entidades escapadas", wrap(`<u:Browse xmlns:u="` + ns + `"><ObjectID>lib:a&amp;b&lt;</ObjectID></u:Browse>`), "Browse", map[string]string{"ObjectID": "lib:a&b<"}, false},
		{"hijos anidados no se mezclan", wrap(`<u:Browse xmlns:u="` + ns + `"><ObjectID>0<x>nope</x>1</ObjectID><Filter>*</Filter></u:Browse>`), "Browse", map[string]string{"ObjectID": "01", "Filter": "*"}, false},
		{"cabecera antes del cuerpo", `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Header><h>x</h></s:Header><s:Body><u:GetSortCapabilities xmlns:u="` + ns + `"/></s:Body></s:Envelope>`, "GetSortCapabilities", map[string]string{}, false},
		{"sin cuerpo", `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"></s:Envelope>`, "", nil, true},
		{"cuerpo vacío", wrap(``), "", nil, true},
		{"XML roto", `<s:Envelope><s:Body><u:Browse`, "", nil, true},
		{"vacío", ``, "", nil, true},
		{"entidad externa no se expande", `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>` + wrap(`<u:Browse xmlns:u="`+ns+`"><ObjectID>&e;</ObjectID></u:Browse>`), "", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSOAP(strings.NewReader(c.in))
			if (err != nil) != c.err {
				t.Fatalf("err = %v", err)
			}
			if c.err {
				return
			}
			if got.action != c.action || got.service != ns {
				t.Fatalf("%+v", got)
			}
			if len(got.args) != len(c.args) {
				t.Fatalf("args = %v", got.args)
			}
			for k, v := range c.args {
				if got.args[k] != v {
					t.Errorf("%s = %q, se esperaba %q", k, got.args[k], v)
				}
			}
		})
	}
}

func TestParseSOAPAcotaElTamano(t *testing.T) {
	body := `<s:Envelope xmlns:s="x"><s:Body><u:Browse xmlns:u="y"><ObjectID>` + strings.Repeat("a", maxSOAPBody*2) + `</ObjectID></u:Browse></s:Body></s:Envelope>`
	if _, err := parseSOAP(strings.NewReader(body)); err == nil {
		t.Fatal("un cuerpo que supera el límite debe fallar, no leerse entero")
	}
}

func TestRespuestasSOAP(t *testing.T) {
	resp := soapResponse(cdsTypeURN, "Browse", []pair{{"Result", `<a b="c">&</a>`}, {"NumberReturned", "1"}})
	var env struct {
		Body struct {
			Resp struct {
				XMLName xml.Name
				Result  string
				Number  string `xml:"NumberReturned"`
			} `xml:",any"`
		}
	}
	if err := xml.Unmarshal(resp, &env); err != nil {
		t.Fatalf("XML inválido: %v\n%s", err, resp)
	}
	if env.Body.Resp.XMLName.Local != "BrowseResponse" || env.Body.Resp.XMLName.Space != cdsTypeURN || env.Body.Resp.Result != `<a b="c">&</a>` || env.Body.Resp.Number != "1" {
		t.Fatalf("%+v", env.Body.Resp)
	}

	fault := string(soapFault(&upnpError{errNoSuchObject, `No <such> object`}))
	for _, want := range []string{"<errorCode>701</errorCode>", "<errorDescription>No &lt;such&gt; object</errorDescription>", "s:Fault", "UPnPError"} {
		if !strings.Contains(fault, want) {
			t.Errorf("falta %q en %s", want, fault)
		}
	}
	if err := xml.Unmarshal([]byte(fault), new(struct{})); err != nil {
		t.Errorf("fallo mal formado: %v", err)
	}
	if s, a := soapActionName(`"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`); s != cdsTypeURN || a != "Browse" {
		t.Errorf("soapActionName = %q %q", s, a)
	}
	if s, a := soapActionName(""); s != "" || a != "" {
		t.Errorf("sin cabecera: %q %q", s, a)
	}
}

func TestDIDL(t *testing.T) {
	out := didl([]object{
		{id: "lib:1", parent: "0", title: `Cine & "series"`, container: true, childCount: 3, class: classContainer},
		{id: "item:2", parent: "lib:1", title: "Peli <1>", class: classMovie, date: "2020-01-01", desc: "a&b", art: "http://h/dlna/art/2.jpg?x=1&y=2",
			res: []resource{{url: "http://h/dlna/media/f/Peli.mp4", protocolInfo: protocolInfo("video/mp4", "01", false), size: 1234, durationMS: 3723004, width: 1920, height: 1080, bitrate: 125000, channels: 2}}},
	})
	var doc struct {
		XMLName    xml.Name `xml:"DIDL-Lite"`
		Containers []struct {
			ID         string `xml:"id,attr"`
			ParentID   string `xml:"parentID,attr"`
			Restricted string `xml:"restricted,attr"`
			ChildCount string `xml:"childCount,attr"`
			Title      string `xml:"title"`
		} `xml:"container"`
		Items []struct {
			ID    string `xml:"id,attr"`
			Title string `xml:"title"`
			Class string `xml:"class"`
			Date  string `xml:"date"`
			Desc  string `xml:"description"`
			Art   string `xml:"albumArtURI"`
			Res   []struct {
				Size       string `xml:"size,attr"`
				Duration   string `xml:"duration,attr"`
				Resolution string `xml:"resolution,attr"`
				Bitrate    string `xml:"bitrate,attr"`
				Channels   string `xml:"nrAudioChannels,attr"`
				Protocol   string `xml:"protocolInfo,attr"`
				URL        string `xml:",chardata"`
			} `xml:"res"`
		} `xml:"item"`
	}
	if err := xml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("DIDL-Lite inválido: %v\n%s", err, out)
	}
	if len(doc.Containers) != 1 || doc.Containers[0].Title != `Cine & "series"` || doc.Containers[0].ChildCount != "3" {
		t.Fatalf("contenedores: %+v", doc.Containers)
	}
	it := doc.Items[0]
	if it.Title != "Peli <1>" || it.Class != classMovie || it.Date != "2020-01-01" || it.Desc != "a&b" || it.Art != "http://h/dlna/art/2.jpg?x=1&y=2" {
		t.Fatalf("elemento: %+v", it)
	}
	r := it.Res[0]
	if r.URL != "http://h/dlna/media/f/Peli.mp4" {
		t.Errorf("res: %+v", r)
	}
	for _, want := range []string{`size="1234"`, `duration="1:02:03.004"`, `resolution="1920x1080"`, `bitrate="125000"`, `nrAudioChannels="2"`, `protocolInfo="http-get:*:video/mp4:DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"`} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %s", want)
		}
	}
	// Los atributos desconocidos no se escriben vacíos.
	empty := didl([]object{{id: "i", parent: "p", title: "t", class: classEpisode, res: []resource{{url: "u", protocolInfo: "p"}}}})
	for _, bad := range []string{`size=`, `duration=`, `resolution=`, `bitrate=`, `nrAudioChannels=`, `dc:date`, `albumArtURI`} {
		if strings.Contains(empty, bad) {
			t.Errorf("no debe aparecer %s: %s", bad, empty)
		}
	}
}

func TestProtocolInfoYDuracion(t *testing.T) {
	if got := protocolInfo("video/mpeg", "10", true); got != "http-get:*:video/mpeg:DLNA.ORG_OP=10;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=01700000000000000000000000000000" {
		t.Errorf("protocolInfo = %q", got)
	}
	if strings.Contains(protocolInfo("video/mp4", "01", false), "DLNA.ORG_PN") {
		t.Error("no se debe anunciar un perfil que no se ha validado")
	}
	for ms, want := range map[int64]string{0: "0:00:00.000", 1: "0:00:00.001", 61000: "0:01:01.000", 3723004: "1:02:03.004", 36000000: "10:00:00.000", -5: "0:00:00.000"} {
		if got := formatDuration(ms); got != want {
			t.Errorf("formatDuration(%d) = %q, se esperaba %q", ms, got, want)
		}
	}
}

func TestFileSlug(t *testing.T) {
	for _, c := range []struct{ title, ext, want string }{
		{"Mi Película (2020)", "mp4", "Mi_Pelcula_2020.mp4"},
		{"../../etc/passwd", "mkv", "etcpasswd.mkv"},
		{"a b  c.d", "ts", "a_b_c_d.ts"},
		{"", "mp4", "video.mp4"},
		{"日本語", "mp4", "video.mp4"},
		{"x\r\nSet-Cookie: a=b", "mp4", "xSet-Cookie_ab.mp4"},
		{strings.Repeat("a", 100), "mp4", strings.Repeat("a", 60) + ".mp4"},
		{"  --  ", "ts", "--.ts"},
	} {
		got := fileSlug(c.title, c.ext)
		if got != c.want {
			t.Errorf("fileSlug(%q) = %q, se esperaba %q", c.title, got, c.want)
		}
		if strings.ContainsAny(got, `/\:?#%"<> `) {
			t.Errorf("fileSlug(%q) = %q: caracteres peligrosos", c.title, got)
		}
	}
}

func TestParseNPT(t *testing.T) {
	for in, want := range map[string]int64{
		"npt=0-": 0, "npt=90.5-": 90500, "npt=0:01:30-": 90000, "npt=1:00:00.5-": 3600500, "npt=10-20": 10000,
		" npt=5- ": 5000, "npt=0.001-": 1,
	} {
		got, ok := parseNPT(in)
		if !ok || got != want {
			t.Errorf("parseNPT(%q) = %d %v, se esperaba %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "npt=", "npt=-", "npt=abc-", "npt=-5-", "npt=1:2-", "npt=1:2:3:4-", "bytes=0-", "npt=1:x:3-", "npt=99999999-", "npt=1e9-", "npt=-1:00:00-", "npt=5"} {
		if got, ok := parseNPT(in); ok {
			t.Errorf("parseNPT(%q) = %d: debía rechazarse", in, got)
		}
	}
}

func TestParseSortYPaginacion(t *testing.T) {
	for in, want := range map[string]bool{"": false, "+dc:title": false, "dc:title": false, "-dc:title": true, "-dc:date,+dc:title": false,
		"+upnp:class,-dc:title": true, "-upnp:originalTrackNumber": false} {
		if got := parseSort(in); got != want {
			t.Errorf("parseSort(%q) = %v", in, got)
		}
	}
	all := []int{0, 1, 2, 3, 4}
	for _, c := range []struct {
		start, count int64
		want         []int
	}{
		{0, 0, all}, {0, 2, []int{0, 1}}, {3, 0, []int{3, 4}}, {3, 10, []int{3, 4}}, {5, 1, nil}, {9, 1, nil}, {1, 3, []int{1, 2, 3}},
	} {
		got := window(all, c.start, c.count)
		if len(got) != len(c.want) {
			t.Errorf("window(%d,%d) = %v", c.start, c.count, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("window(%d,%d) = %v", c.start, c.count, got)
			}
		}
	}
	for _, c := range []struct {
		in   string
		def  int64
		want int64
		ok   bool
	}{{"", 7, 7, true}, {" 12 ", 0, 12, true}, {"0", 5, 0, true}, {"-1", 0, 0, false}, {"abc", 0, 0, false}, {"1.5", 0, 0, false}, {"99999999999", 0, 0, false}} {
		got, ok := parseIndex(c.in, c.def)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseIndex(%q) = %d %v", c.in, got, ok)
		}
	}
	if pageSize(0) != maxPage || pageSize(-1) != maxPage || pageSize(10) != 10 || pageSize(maxPage+1) != maxPage {
		t.Error("pageSize")
	}
}

func TestIdentificadoresDeBaseDeDatos(t *testing.T) {
	for _, ok := range []string{"a", "abc123", "A-b_c", strings.Repeat("x", 64)} {
		if !reDBID.MatchString(ok) {
			t.Errorf("%q debe valer", ok)
		}
	}
	for _, bad := range []string{"", "a b", "a/b", "../x", "a;b", "a'b", "a%20b", "a\nb", strings.Repeat("x", 65), "ñ"} {
		if reDBID.MatchString(bad) {
			t.Errorf("%q no debe valer", bad)
		}
	}
}

// --- HTTP, sobre una base de datos real ---

func newServer(t *testing.T, name string) (*Server, http.Handler) {
	t.Helper()
	db := testutil.NewDB(t)
	s := New(Options{Name: name, Version: "1.2.3", HTTPPort: 8420}, db, nil, nil, nil, remote.NewNetwork(nil), zerolog.Nop())
	if err := s.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, s.Handler()
}

func doReq(h http.Handler, method, path, body string, remoteAddr string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = remoteAddr
	r.Host = "192.168.1.10:8420"
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const lanAddr = "192.168.1.50:4000"

func TestLaIdentidadDelDispositivoEsEstable(t *testing.T) {
	db := testutil.NewDB(t)
	mk := func() *Server {
		s := New(Options{Name: "x", HTTPPort: 1}, db, nil, nil, nil, remote.NewNetwork(nil), zerolog.Nop())
		if err := s.Prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := mk().UUID(), mk().UUID()
	if a == "" || a != b {
		t.Fatalf("el UUID debe persistir entre arranques: %q %q", a, b)
	}
	// Un valor corrupto no se usa: se genera otro y se guarda.
	if _, err := db.Write.Exec("UPDATE server_settings SET value = 'no-es-un-uuid' WHERE key = ?", settingUUID); err != nil {
		t.Fatal(err)
	}
	c := mk().UUID()
	if c == "no-es-un-uuid" || c == a || mk().UUID() != c {
		t.Fatalf("UUID corrupto: %q", c)
	}
}

func TestSoloResponderALaRedLocal(t *testing.T) {
	_, h := newServer(t, "Cine")
	paths := []struct{ method, path string }{
		{"GET", "/dlna/device.xml"}, {"GET", "/dlna/cds.xml"}, {"GET", "/dlna/cms.xml"},
		{"POST", "/dlna/cds/control"}, {"GET", "/dlna/media/x/y.mp4"}, {"GET", "/dlna/ts/x/y.ts"}, {"GET", "/dlna/art/x.jpg"},
		{"SUBSCRIBE", "/dlna/cds/event"},
	}
	for _, p := range paths {
		if w := doReq(h, p.method, p.path, "", "203.0.113.9:1"); w.Code != http.StatusNotFound {
			t.Errorf("%s %s desde fuera: %d", p.method, p.path, w.Code)
		}
		// Un proxy no declarado tampoco convierte lo de fuera en local.
		if w := doReq(h, p.method, p.path, "", "192.168.1.9:1", "X-Forwarded-For", "203.0.113.9"); w.Code != http.StatusNotFound {
			t.Errorf("%s %s con reenvío desde un proxy no declarado: %d", p.method, p.path, w.Code)
		}
	}
	if w := doReq(h, "GET", "/dlna/device.xml", "", lanAddr); w.Code != 200 {
		t.Errorf("desde la red local: %d", w.Code)
	}
	if w := doReq(h, "GET", "/dlna/device.xml", "", "127.0.0.1:1"); w.Code != 200 {
		t.Errorf("desde la propia máquina: %d", w.Code)
	}
}

func TestEndpointsDescriptivos(t *testing.T) {
	s, h := newServer(t, `Salón <1>`)
	w := doReq(h, "GET", "/dlna/device.xml", "", lanAddr)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/xml") || !strings.Contains(w.Body.String(), "uuid:"+s.UUID()) ||
		!strings.Contains(w.Body.String(), "Salón &lt;1&gt;") {
		t.Fatalf("%d %v\n%s", w.Code, w.Header(), w.Body)
	}
	for _, p := range []string{"/dlna/cds.xml", "/dlna/cms.xml"} {
		if w := doReq(h, "GET", p, "", lanAddr); w.Code != 200 || !strings.Contains(w.Body.String(), "<scpd") {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	// Métodos que no corresponden.
	if w := doReq(h, "POST", "/dlna/device.xml", "", lanAddr); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST device.xml: %d", w.Code)
	}
	if w := doReq(h, "GET", "/dlna/cds/control", "", lanAddr); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET control: %d", w.Code)
	}
	if w := doReq(h, "GET", "/dlna/otra", "", lanAddr); w.Code != http.StatusNotFound {
		t.Errorf("ruta desconocida: %d", w.Code)
	}
}

func soap(service, action, args string) string {
	return `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` +
		`<u:` + action + ` xmlns:u="` + service + `">` + args + `</u:` + action + `></s:Body></s:Envelope>`
}

func control(h http.Handler, path, service, action, args string) *httptest.ResponseRecorder {
	return doReq(h, "POST", path, soap(service, action, args), lanAddr, "SOAPACTION", `"`+service+`#`+action+`"`, "Content-Type", `text/xml; charset="utf-8"`)
}

func TestConnectionManager(t *testing.T) {
	_, h := newServer(t, "x")
	w := control(h, "/dlna/cms/control", cmsTypeURN, "GetProtocolInfo", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "http-get:*:video/mp4:*") || !strings.Contains(w.Body.String(), "<Sink></Sink>") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := control(h, "/dlna/cms/control", cmsTypeURN, "GetCurrentConnectionIDs", ""); !strings.Contains(w.Body.String(), "<ConnectionIDs>0</ConnectionIDs>") {
		t.Fatalf("%s", w.Body)
	}
	w = control(h, "/dlna/cms/control", cmsTypeURN, "GetCurrentConnectionInfo", "<ConnectionID>0</ConnectionID>")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<Direction>Output</Direction>") || !strings.Contains(w.Body.String(), "<Status>OK</Status>") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = control(h, "/dlna/cms/control", cmsTypeURN, "GetCurrentConnectionInfo", "<ConnectionID>7</ConnectionID>")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "<errorCode>706</errorCode>") {
		t.Fatalf("conexión inexistente: %d %s", w.Code, w.Body)
	}
}

func TestControlRechazaLoQueNoEntiende(t *testing.T) {
	_, h := newServer(t, "x")
	cases := []struct {
		name  string
		path  string
		body  string
		hdr   []string
		code  int
		fault string
	}{
		{"XML roto", "/dlna/cds/control", "<no", nil, 400, "402"},
		{"acción desconocida", "/dlna/cds/control", soap(cdsTypeURN, "Destroy", ""), nil, 500, "401"},
		{"servicio equivocado en el cuerpo", "/dlna/cds/control", soap(cmsTypeURN, "Browse", ""), nil, 500, "401"},
		{"acción de otro servicio", "/dlna/cds/control", soap(cdsTypeURN, "GetProtocolInfo", ""), nil, 500, "401"},
		{"SOAPACTION de otro servicio", "/dlna/cds/control", soap(cdsTypeURN, "GetSortCapabilities", ""), []string{"SOAPACTION", `"` + cmsTypeURN + `#GetSortCapabilities"`}, 500, "401"},
		{"SOAPACTION no coincide con el cuerpo", "/dlna/cds/control", soap(cdsTypeURN, "GetSortCapabilities", ""), []string{"SOAPACTION", `"` + cdsTypeURN + `#Browse"`}, 500, "401"},
		{"Browse con flag inválido", "/dlna/cds/control", soap(cdsTypeURN, "Browse", "<ObjectID>0</ObjectID><BrowseFlag>Otra</BrowseFlag>"), nil, 500, "402"},
		{"Browse sin flag", "/dlna/cds/control", soap(cdsTypeURN, "Browse", "<ObjectID>0</ObjectID>"), nil, 500, "402"},
		{"índice negativo", "/dlna/cds/control", soap(cdsTypeURN, "Browse", "<ObjectID>0</ObjectID><BrowseFlag>BrowseMetadata</BrowseFlag><StartingIndex>-1</StartingIndex>"), nil, 500, "402"},
		{"cuenta no numérica", "/dlna/cds/control", soap(cdsTypeURN, "Browse", "<ObjectID>0</ObjectID><BrowseFlag>BrowseMetadata</BrowseFlag><RequestedCount>x</RequestedCount>"), nil, 500, "402"},
		{"objeto inexistente", "/dlna/cds/control", soap(cdsTypeURN, "Browse", "<ObjectID>zzz</ObjectID><BrowseFlag>BrowseMetadata</BrowseFlag>"), nil, 500, "701"},
		{"biblioteca con id peligroso", "/dlna/cds/control", soap(cdsTypeURN, "Browse", "<ObjectID>lib:a' OR 1=1--</ObjectID><BrowseFlag>BrowseMetadata</BrowseFlag>"), nil, 500, "701"},
		{"elemento con id peligroso", "/dlna/cds/control", soap(cdsTypeURN, "Browse", "<ObjectID>item:../../x</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag>"), nil, 500, "701"},
		{"biblioteca inexistente", "/dlna/cds/control", soap(cdsTypeURN, "Browse", "<ObjectID>lib:nada</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag>"), nil, 500, "701"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doReq(h, "POST", c.path, c.body, lanAddr, c.hdr...)
			if w.Code != c.code || !strings.Contains(w.Body.String(), "<errorCode>"+c.fault+"</errorCode>") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/xml") {
				t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
			}
		})
	}
	// Un cuerpo enorme se corta.
	big := soap(cdsTypeURN, "Browse", "<ObjectID>"+strings.Repeat("a", maxSOAPBody*2)+"</ObjectID>")
	if w := doReq(h, "POST", "/dlna/cds/control", big, lanAddr); w.Code != 400 {
		t.Errorf("cuerpo enorme: %d", w.Code)
	}
}

func TestCapacidadesDeContentDirectory(t *testing.T) {
	_, h := newServer(t, "x")
	if w := control(h, "/dlna/cds/control", cdsTypeURN, "GetSortCapabilities", ""); !strings.Contains(w.Body.String(), "<SortCaps>dc:title</SortCaps>") {
		t.Errorf("%s", w.Body)
	}
	if w := control(h, "/dlna/cds/control", cdsTypeURN, "GetSearchCapabilities", ""); !strings.Contains(w.Body.String(), "<SearchCaps></SearchCaps>") {
		t.Errorf("%s", w.Body)
	}
	if w := control(h, "/dlna/cds/control", cdsTypeURN, "GetSystemUpdateID", ""); !strings.Contains(w.Body.String(), "<Id>") {
		t.Errorf("%s", w.Body)
	}
}

func TestSuscripcionesNoLlamanANadie(t *testing.T) {
	_, h := newServer(t, "x")
	w := doReq(h, "SUBSCRIBE", "/dlna/cds/event", "", lanAddr, "CALLBACK", "<http://169.254.169.254/latest>", "NT", "upnp:event", "TIMEOUT", "Second-300")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("SID"), "uuid:") || w.Header().Get("TIMEOUT") != "Second-1800" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	// Renovar.
	if w2 := doReq(h, "SUBSCRIBE", "/dlna/cds/event", "", lanAddr, "SID", w.Header().Get("SID")); w2.Code != 200 || w2.Header().Get("SID") != w.Header().Get("SID") {
		t.Fatalf("renovar: %d %v", w2.Code, w2.Header())
	}
	if w := doReq(h, "SUBSCRIBE", "/dlna/cms/event", "", lanAddr); w.Code != http.StatusPreconditionFailed {
		t.Errorf("sin CALLBACK ni SID: %d", w.Code)
	}
	if w := doReq(h, "UNSUBSCRIBE", "/dlna/cms/event", "", lanAddr, "SID", "uuid:x"); w.Code != 200 {
		t.Errorf("UNSUBSCRIBE: %d", w.Code)
	}
	if w := doReq(h, "UNSUBSCRIBE", "/dlna/cms/event", "", lanAddr); w.Code != http.StatusPreconditionFailed {
		t.Errorf("UNSUBSCRIBE sin SID: %d", w.Code)
	}
}

func TestLaCabeceraHostNoDevuelveTextoAjeno(t *testing.T) {
	s, _ := newServer(t, "x")
	for host, want := range map[string]string{
		"192.168.1.10:8420": "192.168.1.10:8420", "media.local": "media.local", "[fe80::1]:8420": "[fe80::1]:8420",
		"a\"><script>": ":8420", "evil.example/x": ":8420", "": ":8420", "a b": ":8420",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host, r.RemoteAddr = host, lanAddr
		if got := s.hostOf(r); got != want {
			t.Errorf("hostOf(%q) = %q, se esperaba %q", host, got, want)
		}
	}
}
