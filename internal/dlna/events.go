package dlna

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Límites de las suscripciones (UDA 1.0 §4): un control point legítimo suscribe
// dos servicios, y cada callback es una conexión saliente.
const (
	maxSubscriptions = 64
	maxPerClient     = 8
	maxCallbacks     = 4
	minSubTimeout    = 300 * time.Second
	maxSubTimeout    = ssdpMaxAge * time.Second
	notifyTimeout    = 5 * time.Second
	maxCallbackFails = 3
	eventPoll        = 5 * time.Second
	maxCallbackURL   = 512
)

type subscription struct {
	sid       string
	service   string // "cds" o "cms".
	client    netip.Addr
	callbacks []string
	fails     int
	seq       uint32
	expires   time.Time
}

// hub lleva las suscripciones y entrega los eventos.
type hub struct {
	mu     sync.Mutex
	subs   map[string]*subscription
	last   uint32 // Último SystemUpdateID notificado.
	client *http.Client
	now    func() time.Time
}

func newHub() *hub {
	return &hub{
		subs: map[string]*subscription{}, now: time.Now,
		client: &http.Client{
			Timeout: notifyTimeout,
			// Un callback que redirige no es el callback que se aceptó.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// parseCallbacks interpreta «<http://ip:puerto/ruta><...>». Solo se aceptan URL
// http con una IP literal que sea la de quien suscribe: el servidor nunca
// conecta a otro equipo por orden de un cliente.
func parseCallbacks(header string, from netip.Addr) []string {
	var out []string
	for rest := strings.TrimSpace(header); strings.HasPrefix(rest, "<"); {
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			break
		}
		raw := rest[1:end]
		rest = strings.TrimSpace(rest[end+1:])
		if len(raw) > maxCallbackURL {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" {
			continue
		}
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || ip.Unmap() != from.Unmap() {
			continue
		}
		if port := u.Port(); port != "" {
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
				continue
			}
		}
		out = append(out, raw)
		if len(out) == maxCallbacks {
			break
		}
	}
	return out
}

// parseTimeout lee «Second-N» o «infinite» y lo acota.
func parseTimeout(h string) time.Duration {
	h = strings.TrimSpace(h)
	d := maxSubTimeout
	if n, ok := strings.CutPrefix(h, "Second-"); ok {
		if secs, err := strconv.Atoi(n); err == nil && secs > 0 {
			d = time.Duration(secs) * time.Second
		}
	}
	return min(max(d, minSubTimeout), maxSubTimeout)
}

// subscribe atiende SUBSCRIBE: una suscripción nueva (CALLBACK y NT) o una
// renovación (SID).
func (s *Server) subscribe(service string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := s.hub
		sid, cb, nt := r.Header.Get("SID"), r.Header.Get("CALLBACK"), r.Header.Get("NT")
		switch {
		case sid != "" && (cb != "" || nt != ""):
			http.Error(w, "SID no se combina con CALLBACK ni NT", http.StatusBadRequest)
			return
		case sid != "":
			h.mu.Lock()
			sub, ok := h.subs[sid]
			if !ok || sub.service != service || !h.now().Before(sub.expires) {
				h.mu.Unlock()
				http.Error(w, "suscripción desconocida", http.StatusPreconditionFailed)
				return
			}
			sub.expires = h.now().Add(parseTimeout(r.Header.Get("TIMEOUT")))
			timeout := sub.expires.Sub(h.now())
			h.mu.Unlock()
			s.subscribed(w, sid, timeout)
			return
		case nt != "upnp:event" || cb == "":
			http.Error(w, "falta CALLBACK o NT no es upnp:event", http.StatusPreconditionFailed)
			return
		}
		from, err := netip.ParseAddr(s.net.ClientIP(r))
		if err != nil {
			http.Error(w, "origen ilegible", http.StatusPreconditionFailed)
			return
		}
		callbacks := parseCallbacks(cb, from)
		if len(callbacks) == 0 {
			s.log.Warn().Str("origen", from.String()).Str("callback", cb).Msg("DLNA: suscripción rechazada: el CALLBACK no es una URL http hacia el origen")
			http.Error(w, "CALLBACK no válido: solo http hacia la dirección de quien suscribe", http.StatusPreconditionFailed)
			return
		}
		timeout := parseTimeout(r.Header.Get("TIMEOUT"))
		h.mu.Lock()
		h.prune()
		perClient := 0
		for _, o := range h.subs {
			if o.client == from {
				perClient++
			}
		}
		if len(h.subs) >= maxSubscriptions || perClient >= maxPerClient {
			h.mu.Unlock()
			http.Error(w, "demasiadas suscripciones", http.StatusServiceUnavailable)
			return
		}
		sub := &subscription{sid: "uuid:" + uuid.NewString(), service: service, client: from, callbacks: callbacks, expires: h.now().Add(timeout)}
		h.subs[sub.sid] = sub
		h.mu.Unlock()
		s.subscribed(w, sub.sid, timeout)
		bg := context.WithoutCancel(r.Context()) // La respuesta ya habrá terminado cuando se envíe.
		// El estado inicial se envía tras la respuesta (UDA 1.0 §4.1.1).
		time.AfterFunc(100*time.Millisecond, func() { s.deliver(bg, sub, s.stateVars(bg, service)) })
	}
}

func (s *Server) subscribed(w http.ResponseWriter, sid string, timeout time.Duration) {
	w.Header().Set("SID", sid)
	w.Header().Set("TIMEOUT", "Second-"+strconv.Itoa(int(timeout/time.Second)))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("SID")
	if sid == "" {
		http.Error(w, "falta SID", http.StatusPreconditionFailed)
		return
	}
	s.hub.mu.Lock()
	_, ok := s.hub.subs[sid]
	delete(s.hub.subs, sid)
	s.hub.mu.Unlock()
	if !ok {
		http.Error(w, "suscripción desconocida", http.StatusPreconditionFailed)
		return
	}
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// prune borra las suscripciones caducadas. Hay que tener h.mu.
func (h *hub) prune() {
	for sid, sub := range h.subs {
		if !h.now().Before(sub.expires) {
			delete(h.subs, sid)
		}
	}
}

// stateVars son las variables con evento de un servicio.
func (s *Server) stateVars(ctx context.Context, service string) []pair {
	if service == "cds" {
		return []pair{{"SystemUpdateID", strconv.FormatUint(uint64(s.updateID(context.WithoutCancel(ctx))), 10)}}
	}
	return []pair{{"SourceProtocolInfo", strings.Join(sourceProtocols, ",")}, {"SinkProtocolInfo", ""}, {"CurrentConnectionIDs", "0"}}
}

func propertySet(vars []pair) []byte {
	var b strings.Builder
	b.WriteString(xmlHeader + `<e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0">`)
	for _, v := range vars {
		b.WriteString(`<e:property><` + v.name + `>` + esc(v.value) + `</` + v.name + `></e:property>`)
	}
	b.WriteString(`</e:propertyset>`)
	return []byte(b.String())
}

// deliver envía un evento a una suscripción: al primer callback que responda.
// Tras maxCallbackFails fallos seguidos, la suscripción se da por muerta.
func (s *Server) deliver(ctx context.Context, sub *subscription, vars []pair) {
	h := s.hub
	h.mu.Lock()
	if _, ok := h.subs[sub.sid]; !ok {
		h.mu.Unlock()
		return
	}
	seq := sub.seq
	sub.seq++
	if sub.seq == 0 {
		sub.seq = 1 // El contador da la vuelta a 1, no a 0 (UDA 1.0 §4.2).
	}
	callbacks := append([]string(nil), sub.callbacks...)
	h.mu.Unlock()

	body := propertySet(vars)
	delivered := false
	for _, cb := range callbacks {
		req, err := http.NewRequestWithContext(ctx, "NOTIFY", cb, bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
		req.Header.Set("NT", "upnp:event")
		req.Header.Set("NTS", "upnp:propchange")
		req.Header.Set("SID", sub.sid)
		req.Header.Set("SEQ", strconv.FormatUint(uint64(seq), 10))
		resp, err := h.client.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			delivered = true
			break
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if delivered {
		sub.fails = 0
		return
	}
	if sub.fails++; sub.fails >= maxCallbackFails {
		delete(h.subs, sub.sid)
		s.log.Debug().Str("sid", sub.sid).Msg("DLNA: suscripción descartada: el callback no responde")
	}
}

// pollEvents mira si el catálogo cambió y avisa a los suscritos a ContentDirectory.
func (s *Server) pollEvents(ctx context.Context) {
	h := s.hub
	id := s.updateID(ctx)
	h.mu.Lock()
	h.prune()
	changed := id != h.last
	h.last = id
	var subs []*subscription
	if changed {
		for _, sub := range h.subs {
			if sub.service == "cds" {
				subs = append(subs, sub)
			}
		}
	}
	h.mu.Unlock()
	for _, sub := range subs {
		go s.deliver(ctx, sub, []pair{{"SystemUpdateID", strconv.FormatUint(uint64(id), 10)}})
	}
}

// eventLoop llama a pollEvents hasta que se cancele ctx.
func (s *Server) eventLoop(ctx context.Context) {
	s.pollEvents(ctx) // Fija el valor de partida: no avisa de lo que ya había.
	t := time.NewTicker(eventPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pollEvents(ctx)
		}
	}
}
