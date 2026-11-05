package dlna

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/time/rate"

	"medianode/internal/remote"
)

// Constantes de UPnP Device Architecture 1.0 (§1, SSDP).
const (
	ssdpGroup     = "239.255.255.250"
	ssdpPort      = 1900
	ssdpMaxAge    = 1800 // Segundos que un anuncio sigue valiendo.
	maxPacket     = 2048 // Una petición M-SEARCH real cabe de sobra.
	maxMX         = 5    // Se acota MX: un valor enorme retendría memoria para nada.
	reannounce    = ssdpMaxAge / 2 * time.Second
	ifaceRefresh  = time.Minute
	searchBurst   = 10 // M-SEARCH admitidos de golpe por origen...
	searchEvery   = 2 * time.Second
	maxSearchers  = 1024
	deviceTypeURN = "urn:schemas-upnp-org:device:MediaServer:1"
	cdsTypeURN    = "urn:schemas-upnp-org:service:ContentDirectory:1"
	cmsTypeURN    = "urn:schemas-upnp-org:service:ConnectionManager:1"
)

// iface es una interfaz de red por la que se anuncia el servidor.
type iface struct {
	Index  int
	Name   string
	IP     netip.Addr
	Prefix netip.Prefix
}

// packetIO es el transporte UDP. Se sustituye en las pruebas.
type packetIO interface {
	read(b []byte) (int, netip.AddrPort, error)
	writeUnicast(b []byte, dst netip.AddrPort) error
	writeMulticast(b []byte, via iface) error
	join(via iface) error
	close() error
}

// target es lo que se anuncia o se busca: NT/ST con su USN.
type target struct{ nt, usn string }

// ssdp anuncia el servidor y responde a las búsquedas (§13.2). Solo atiende a
// direcciones de las redes a las que está conectado el equipo.
type ssdp struct {
	io       packetIO
	uuid     string
	httpPort int
	server   string
	netw     *remote.Network
	ifaces   func() []iface
	log      zerolog.Logger

	// delay devuelve cuánto esperar para responder a una búsqueda con ese MX.
	delay func(mx int) time.Duration
	now   func() time.Time

	mu      sync.Mutex
	current []iface
	closed  bool
	search  map[netip.Addr]*rate.Limiter

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newSSDP(io packetIO, uuid string, httpPort int, version string, netw *remote.Network, ifaces func() []iface, log zerolog.Logger) *ssdp {
	return &ssdp{
		io: io, uuid: uuid, httpPort: httpPort, netw: netw, ifaces: ifaces, log: log,
		server: fmt.Sprintf("%s/1.0 UPnP/1.0 MediaNode/%s", runtime.GOOS, sanitizeToken(version)),
		delay: func(mx int) time.Duration {
			// Jitter de una respuesta (UDA 1.0 §1.3.2), no un secreto.
			return time.Duration(rand.Int64N(int64(time.Duration(mx) * time.Second))) //nolint:gosec
		},
		now: time.Now, search: map[netip.Addr]*rate.Limiter{},
	}
}

// sanitizeToken deja solo lo que cabe en una cabecera HTTP sin comillas.
func sanitizeToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == '+':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "dev"
	}
	return b.String()
}

// targets son los cinco anuncios de un MediaServer:1 con dos servicios (UDA 1.0
// §1.2.2).
func (s *ssdp) targets() []target {
	u := "uuid:" + s.uuid
	return []target{
		{"upnp:rootdevice", u + "::upnp:rootdevice"},
		{u, u},
		{deviceTypeURN, u + "::" + deviceTypeURN},
		{cdsTypeURN, u + "::" + cdsTypeURN},
		{cmsTypeURN, u + "::" + cmsTypeURN},
	}
}

// matches devuelve los destinos que responden a una búsqueda ST.
func (s *ssdp) matches(st string) []target {
	if st == "ssdp:all" {
		return s.targets()
	}
	for _, t := range s.targets() {
		if t.nt == st {
			return []target{t}
		}
	}
	return nil
}

// search es una petición M-SEARCH válida.
type search struct {
	st string
	mx int
}

// parseSearch interpreta un datagrama. Devuelve false si no es un M-SEARCH
// bien formado: un NOTIFY de otro dispositivo, una respuesta o basura.
func parseSearch(pkt []byte) (search, bool) {
	if len(pkt) == 0 || len(pkt) > maxPacket {
		return search{}, false
	}
	head, _, _ := bytes.Cut(pkt, []byte("\r\n\r\n"))
	lines := strings.Split(string(head), "\r\n")
	if len(lines) == 0 || !strings.EqualFold(strings.TrimSpace(lines[0]), "M-SEARCH * HTTP/1.1") {
		return search{}, false
	}
	hdr := map[string]string{}
	for _, l := range lines[1:] {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		hdr[strings.ToUpper(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	if hdr["MAN"] != `"ssdp:discover"` || hdr["ST"] == "" {
		return search{}, false
	}
	mx, err := strconv.Atoi(hdr["MX"])
	if err != nil || mx < 1 {
		mx = 1
	}
	return search{st: hdr["ST"], mx: min(mx, maxMX)}, true
}

func (s *ssdp) location(ip netip.Addr) string {
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(s.httpPort)) + "/dlna/device.xml"
}

func (s *ssdp) searchResponse(t target, ip netip.Addr) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=%d\r\nDATE: %s\r\nEXT:\r\nLOCATION: %s\r\nSERVER: %s\r\nST: %s\r\nUSN: %s\r\n\r\n",
		ssdpMaxAge, s.now().UTC().Format(time.RFC1123), s.location(ip), s.server, t.nt, t.usn)
	return b.Bytes()
}

func (s *ssdp) notify(t target, nts string, ip netip.Addr) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "NOTIFY * HTTP/1.1\r\nHOST: %s:%d\r\n", ssdpGroup, ssdpPort)
	if nts == "ssdp:alive" {
		fmt.Fprintf(&b, "CACHE-CONTROL: max-age=%d\r\nLOCATION: %s\r\n", ssdpMaxAge, s.location(ip))
	}
	fmt.Fprintf(&b, "NT: %s\r\nNTS: %s\r\n", t.nt, nts)
	if nts == "ssdp:alive" {
		fmt.Fprintf(&b, "SERVER: %s\r\n", s.server)
	}
	fmt.Fprintf(&b, "USN: %s\r\n\r\n", t.usn)
	return b.Bytes()
}

// interfaceFor devuelve la interfaz cuya red contiene a src. Sin ella, la
// petición no viene de una red a la que el servidor esté conectado.
func (s *ssdp) interfaceFor(src netip.Addr) (iface, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range s.current {
		if i.Prefix.Contains(src) {
			return i, true
		}
	}
	return iface{}, false
}

// allow limita las búsquedas por origen: cada una cuesta un datagrama de
// respuesta, y UDP no se puede autenticar.
func (s *ssdp) allow(src netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.search) >= maxSearchers {
		clear(s.search)
	}
	l, ok := s.search[src]
	if !ok {
		l = rate.NewLimiter(rate.Every(searchEvery), searchBurst)
		s.search[src] = l
	}
	return l.Allow()
}

// handle atiende un datagrama recibido.
func (s *ssdp) handle(pkt []byte, from netip.AddrPort) {
	req, ok := parseSearch(pkt)
	if !ok {
		return
	}
	src := from.Addr().Unmap()
	// Solo la red local, y solo una red a la que este equipo está conectado: así
	// no se responde nunca a una dirección de fuera, ni siquiera si el puerto
	// UDP se reenvía por error.
	if !s.netw.IsLocalIP(src.String()) {
		return
	}
	via, ok := s.interfaceFor(src)
	if !ok || !s.allow(src) {
		return
	}
	matches := s.matches(req.st)
	if len(matches) == 0 {
		return
	}
	// La respuesta se retrasa al azar dentro de MX para que muchos servidores no
	// contesten a la vez (UDA 1.0 §1.3.2).
	wait := s.delay(req.mx)
	for _, t := range matches {
		msg := s.searchResponse(t, via.IP)
		time.AfterFunc(wait, func() {
			if s.isClosed() {
				return
			}
			if err := s.io.writeUnicast(msg, from); err != nil {
				s.log.Debug().Err(err).Str("destino", from.String()).Msg("no se pudo responder a M-SEARCH")
			}
		})
	}
}

func (s *ssdp) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// announce envía un anuncio por cada interfaz y destino.
func (s *ssdp) announce(nts string) {
	s.mu.Lock()
	ifaces := slicesClone(s.current)
	s.mu.Unlock()
	for _, via := range ifaces {
		for _, t := range s.targets() {
			if err := s.io.writeMulticast(s.notify(t, nts, via.IP), via); err != nil {
				s.log.Debug().Err(err).Str("interfaz", via.Name).Msg("no se pudo anunciar por SSDP")
				break
			}
		}
	}
}

func slicesClone(in []iface) []iface { return append([]iface(nil), in...) }

// refresh actualiza las interfaces y se une al grupo en las nuevas. Devuelve
// si cambió el conjunto.
func (s *ssdp) refresh() bool {
	now := s.ifaces()
	s.mu.Lock()
	prev := s.current
	changed := len(prev) != len(now)
	if !changed {
		for i := range now {
			if now[i] != prev[i] {
				changed = true
			}
		}
	}
	s.current = now
	s.mu.Unlock()
	if changed {
		names := make([]string, 0, len(now))
		for _, via := range now {
			names = append(names, via.Name+"="+via.IP.String())
		}
		s.log.Info().Strs("interfaces", names).Msg("DLNA: se anuncia por estas interfaces")
		for _, via := range now {
			if err := s.io.join(via); err != nil {
				s.log.Debug().Err(err).Str("interfaz", via.Name).Msg("no se pudo unir al grupo SSDP")
			}
		}
	}
	return changed
}

// start lanza la lectura y los anuncios periódicos.
func (s *ssdp) start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.refresh()
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		buf := make([]byte, maxPacket+1)
		for {
			n, from, err := s.io.read(buf)
			if err != nil {
				if ctx.Err() != nil || s.isClosed() {
					return
				}
				s.log.Debug().Err(err).Msg("error leyendo SSDP")
				time.Sleep(100 * time.Millisecond)
				continue
			}
			s.handle(append([]byte(nil), buf[:n]...), from)
		}
	}()
	go func() {
		defer s.wg.Done()
		// Dos anuncios seguidos al empezar: UDP puede perder uno (UDA 1.0 §1.2.2).
		s.announce("ssdp:alive")
		time.Sleep(200 * time.Millisecond)
		s.announce("ssdp:alive")
		refresh, again := time.NewTicker(ifaceRefresh), time.NewTicker(reannounce)
		defer refresh.Stop()
		defer again.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-refresh.C:
				if s.refresh() {
					s.announce("ssdp:alive")
				}
			case <-again.C:
				s.announce("ssdp:alive")
			}
		}
	}()
}

// stop avisa de que el servidor se va y cierra el socket.
func (s *ssdp) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.announce("ssdp:byebye")
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	_ = s.io.close()
	s.wg.Wait()
}

// localInterfaces son las interfaces por las que se anuncia: activas, con
// multidifusión, con una IPv4 privada. bind restringe a la interfaz de esa
// dirección si no es la de «todas».
func localInterfaces(bind string, netw *remote.Network) []iface {
	var out []iface
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	want, _ := netip.ParseAddr(bind)
	restrict := want.IsValid() && !want.IsUnspecified()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagMulticast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			ip = ip.Unmap()
			if !ok || !ip.Is4() || ip.IsLinkLocalUnicast() || !netw.IsLocalIP(ip.String()) {
				continue
			}
			if restrict && ip != want {
				continue
			}
			ones, _ := ipn.Mask.Size()
			out = append(out, iface{Index: ifc.Index, Name: ifc.Name, IP: ip, Prefix: netip.PrefixFrom(ip, ones).Masked()})
		}
	}
	return out
}
