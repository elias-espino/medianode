package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var reCaption = regexp.MustCompile(`<sec:CaptionInfoEx sec:type="srt">([^<]+)</sec:CaptionInfoEx>`)

// searchAndSubtitles comprueba Search y los subtítulos externos.
func (c *checker) searchAndSubtitles(root string,
	browse func(id, flag string) (int, string),
	soap func(path, service, action, args string) (int, string),
	get func(path string, headers map[string]string) (int, http.Header, []byte),
	lib, listing string) {
	const cds = "urn:schemas-upnp-org:service:ContentDirectory:1"
	search := func(container, criteria string) (int, string) {
		return soap("/dlna/cds/control", cds, "Search", "<ContainerID>"+container+"</ContainerID><SearchCriteria>"+criteria+
			"</SearchCriteria><Filter>*</Filter><StartingIndex>0</StartingIndex><RequestedCount>0</RequestedCount><SortCriteria></SortCriteria>")
	}
	st, body := search("0", "dc:title contains &quot;pelicula&quot;")
	c.check("D18", "Search por título encuentra las dos películas", st == 200 && strings.Contains(body, "<TotalMatches>2</TotalMatches>"), st)
	st, body = search("lib:"+lib, "upnp:class derivedfrom &quot;object.item.videoItem&quot; and dc:title contains &quot;MKV&quot;")
	c.check("D19", "Search combina clase y título dentro de una biblioteca", st == 200 && strings.Contains(body, "<TotalMatches>1</TotalMatches>"), st)
	st, body = search("0", "dc:title")
	c.check("D20", "un criterio inválido es el error UPnP 708", st == 500 && strings.Contains(body, "<errorCode>708</errorCode>"), st)
	st, body = soap("/dlna/cds/control", cds, "GetSearchCapabilities", "")
	c.check("D21", "GetSearchCapabilities anuncia dc:title y upnp:class", st == 200 && strings.Contains(body, "dc:title,upnp:class"), st)

	m := reCaption.FindStringSubmatch(listing)
	if m == nil {
		c.check("D22", "la película con subtítulo .es.srt anuncia CaptionInfoEx", false, "sin CaptionInfoEx")
		return
	}
	c.check("D22", "la película con subtítulo .es.srt anuncia CaptionInfoEx", true, m[1])
	u, err := url.Parse(m[1])
	if err != nil {
		c.check("D23", "el subtítulo se entrega", false, err)
		return
	}
	st, h, data := get(u.Path, nil)
	c.check("D23", "el subtítulo se entrega como text/srt", st == 200 && strings.HasPrefix(h.Get("Content-Type"), "text/srt") && len(data) > 0, st)
	_ = browse
}

// eventSink es un receptor de eventos UPnP: lo que hace un control point.
type eventSink struct {
	srv *http.Server
	mu  sync.Mutex
	seq []int
	sid string
	url string
	all []string
}

func (e *eventSink) close() {
	if e != nil && e.srv != nil {
		_ = e.srv.Close()
	}
}

func (e *eventSink) seqs() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.seq...)
}

// subscribe abre un receptor y se suscribe a ContentDirectory. host es la
// dirección por la que se llegó al servidor: la misma será la del callback, que
// es lo único que el servidor acepta.
func (c *checker) subscribe(root, host string) *eventSink {
	e := &eventSink{}
	// La dirección que verá el servidor es la de origen de la conexión, que no siempre
	// es la de destino (con una interfaz sin portadora, por ejemplo): se mide.
	if u, err := url.Parse(root); err == nil {
		if conn, err := net.DialTimeout("tcp", u.Host, 3*time.Second); err == nil {
			if ta, ok := conn.LocalAddr().(*net.TCPAddr); ok {
				host = ta.IP.String()
			}
			_ = conn.Close()
		}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		c.skip("D24", "eventos UPnP", "no se pudo abrir un receptor en "+host+": "+err.Error())
		return e
	}
	e.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.Header.Get("SEQ"))
		buf := make([]byte, 4096)
		k, _ := r.Body.Read(buf)
		e.mu.Lock()
		e.seq = append(e.seq, n)
		e.sid = r.Header.Get("SID")
		e.all = append(e.all, string(buf[:k]))
		e.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = e.srv.Serve(ln) }()
	e.url = "http://" + ln.Addr().String() + "/events"

	req, _ := http.NewRequest("SUBSCRIBE", root+"/dlna/cds/event", nil)
	req.Header.Set("CALLBACK", "<"+e.url+">")
	req.Header.Set("NT", "upnp:event")
	req.Header.Set("TIMEOUT", "Second-600")
	// La suscripción sale desde la dirección del receptor (se enlaza el socket a ella):
	// es la única forma de que el servidor vea esa misma dirección como origen.
	from := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext:       (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(host)}}).DialContext,
	}}
	resp, err := from.Do(req)
	sid, status := "", 0
	if err == nil {
		sid, status = resp.Header.Get("SID"), resp.StatusCode
		_ = resp.Body.Close()
	}
	c.check("D24", "SUBSCRIBE a ContentDirectory devuelve un SID", err == nil && status == 200 && strings.HasPrefix(sid, "uuid:"), status, err, " callback "+e.url)
	ok := e.wait(1, 5*time.Second)
	e.mu.Lock()
	first := ""
	if len(e.all) > 0 {
		first = e.all[0]
	}
	e.mu.Unlock()
	c.check("D25", "llega el estado inicial (SEQ 0) con SystemUpdateID", ok && e.seqs()[0] == 0 && strings.Contains(first, "<SystemUpdateID>"), e.seqs())

	// Un callback hacia otro equipo no se acepta: el servidor no conecta adonde le digan.
	bad, _ := http.NewRequest("SUBSCRIBE", root+"/dlna/cds/event", nil)
	bad.Header.Set("CALLBACK", "<http://169.254.169.254/latest>")
	bad.Header.Set("NT", "upnp:event")
	if r2, err := from.Do(bad); err == nil {
		_ = r2.Body.Close()
		c.check("D26", "un CALLBACK hacia otra dirección es un 412", r2.StatusCode == http.StatusPreconditionFailed, r2.StatusCode)
	}
	return e
}

func (e *eventSink) wait(n int, d time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for ctx.Err() == nil {
		if len(e.seqs()) >= n {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// expectChange comprueba que un cambio del catálogo (quitar la marca DLNA de la
// biblioteca) llegó como un segundo aviso.
func (e *eventSink) expectChange(c *checker) {
	if e == nil || e.srv == nil {
		return
	}
	ok := e.wait(2, 20*time.Second)
	s := e.seqs()
	c.check("D27", "un cambio del catálogo llega como aviso (SEQ 1)", ok && s[1] == 1, s)
}
