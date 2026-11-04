package dlna

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/remote"
)

const testUUID = "11111111-2222-3333-4444-555555555555"

var (
	eth0 = iface{Index: 2, Name: "eth0", IP: netip.MustParseAddr("192.168.1.10"), Prefix: netip.MustParsePrefix("192.168.1.0/24")}
	eth1 = iface{Index: 3, Name: "eth1", IP: netip.MustParseAddr("10.0.0.5"), Prefix: netip.MustParsePrefix("10.0.0.0/16")}
)

type sentUnicast struct {
	dst netip.AddrPort
	msg string
}

type sentMulticast struct {
	via iface
	msg string
}

type fakeIO struct {
	mu        sync.Mutex
	unicast   []sentUnicast
	multicast []sentMulticast
	joined    []iface
	closed    bool
	got       chan struct{}
	done      chan struct{}
}

func newFakeIO() *fakeIO {
	return &fakeIO{got: make(chan struct{}, 64), done: make(chan struct{})}
}

func (f *fakeIO) read([]byte) (int, netip.AddrPort, error) {
	<-f.done
	return 0, netip.AddrPort{}, net.ErrClosed
}

func (f *fakeIO) writeUnicast(b []byte, dst netip.AddrPort) error {
	f.mu.Lock()
	f.unicast = append(f.unicast, sentUnicast{dst, string(b)})
	f.mu.Unlock()
	f.got <- struct{}{}
	return nil
}

func (f *fakeIO) writeMulticast(b []byte, via iface) error {
	f.mu.Lock()
	f.multicast = append(f.multicast, sentMulticast{via, string(b)})
	f.mu.Unlock()
	return nil
}

func (f *fakeIO) join(via iface) error {
	f.mu.Lock()
	f.joined = append(f.joined, via)
	f.mu.Unlock()
	return nil
}

func (f *fakeIO) close() error {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		close(f.done)
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeIO) sentUnicast() []sentUnicast {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentUnicast(nil), f.unicast...)
}

func (f *fakeIO) waitUnicast(t *testing.T, n int) []sentUnicast {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for len(f.sentUnicast()) < n {
		select {
		case <-f.got:
		case <-deadline:
			t.Fatalf("se esperaban %d respuestas y llegaron %d", n, len(f.sentUnicast()))
		}
	}
	return f.sentUnicast()
}

func newTestSSDP(io packetIO, ifaces ...iface) *ssdp {
	s := newSSDP(io, testUUID, 8420, "1.2.3", remote.NewNetwork(nil), func() []iface { return ifaces }, zerolog.Nop())
	s.delay = func(int) time.Duration { return 0 }
	s.refresh()
	return s
}

func msearch(st string, extra ...string) []byte {
	lines := []string{"M-SEARCH * HTTP/1.1", "HOST: 239.255.255.250:1900", `MAN: "ssdp:discover"`, "MX: 2", "ST: " + st}
	lines = append(lines, extra...)
	return []byte(strings.Join(lines, "\r\n") + "\r\n\r\n")
}

func TestParseSearch(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
		ok   bool
		st   string
		mx   int
	}{
		{"búsqueda válida", msearch("ssdp:all"), true, "ssdp:all", 2},
		{"sin MX se asume 1", []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: upnp:rootdevice\r\n\r\n"), true, "upnp:rootdevice", 1},
		{"MX enorme se acota", []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nMX: 9999\r\nST: ssdp:all\r\n\r\n"), true, "ssdp:all", maxMX},
		{"MX inválido", []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nMX: -3\r\nST: ssdp:all\r\n\r\n"), true, "ssdp:all", 1},
		{"MX cero: con el retraso al azar dentro de MX, cero daría pánico", []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nMX: 0\r\nST: ssdp:all\r\n\r\n"), true, "ssdp:all", 1},
		{"MX no numérico", []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nMX: abc\r\nST: ssdp:all\r\n\r\n"), true, "ssdp:all", 1},
		{"cabeceras en minúsculas", []byte("m-search * http/1.1\r\nman: \"ssdp:discover\"\r\nmx: 3\r\nst: ssdp:all\r\n\r\n"), true, "ssdp:all", 3},
		{"un NOTIFY no es búsqueda", []byte("NOTIFY * HTTP/1.1\r\nNT: upnp:rootdevice\r\nNTS: ssdp:alive\r\n\r\n"), false, "", 0},
		{"una respuesta no es búsqueda", []byte("HTTP/1.1 200 OK\r\nST: ssdp:all\r\n\r\n"), false, "", 0},
		{"sin MAN", []byte("M-SEARCH * HTTP/1.1\r\nMX: 2\r\nST: ssdp:all\r\n\r\n"), false, "", 0},
		{"MAN sin comillas", []byte("M-SEARCH * HTTP/1.1\r\nMAN: ssdp:discover\r\nST: ssdp:all\r\n\r\n"), false, "", 0},
		{"sin ST", []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\n\r\n"), false, "", 0},
		{"vacío", nil, false, "", 0},
		{"basura", []byte("\x00\x01\x02"), false, "", 0},
		{"demasiado grande", append(msearch("ssdp:all"), make([]byte, maxPacket)...), false, "", 0},
		{"otra ruta", []byte("M-SEARCH /x HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\n\r\n"), false, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseSearch(c.pkt)
			if ok != c.ok || (ok && (got.st != c.st || got.mx != c.mx)) {
				t.Fatalf("parseSearch = %+v %v", got, ok)
			}
		})
	}
}

func TestResponderALaBusquedaDeUnaRedLocal(t *testing.T) {
	f := newFakeIO()
	s := newTestSSDP(f, eth0, eth1)
	from := netip.MustParseAddrPort("192.168.1.77:50000")

	s.handle(msearch("ssdp:all"), from)
	got := f.waitUnicast(t, 5)
	want := map[string]string{
		"upnp:rootdevice":  "uuid:" + testUUID + "::upnp:rootdevice",
		"uuid:" + testUUID: "uuid:" + testUUID,
		deviceTypeURN:      "uuid:" + testUUID + "::" + deviceTypeURN,
		cdsTypeURN:         "uuid:" + testUUID + "::" + cdsTypeURN,
		cmsTypeURN:         "uuid:" + testUUID + "::" + cmsTypeURN,
	}
	seen := map[string]bool{}
	for _, m := range got {
		if m.dst != from {
			t.Errorf("respuesta a %v, no a quien preguntó", m.dst)
		}
		// Con ssdp:all, cada respuesta lleva su propio destino en ST (UDA 1.0 §1.3.3).
		st := header(m.msg, "ST")
		if want[st] == "" || header(m.msg, "USN") != want[st] {
			t.Errorf("ST %q USN %q", st, header(m.msg, "USN"))
		}
		seen[st] = true
		// La dirección es la de la interfaz de la red del cliente, no otra.
		if loc := header(m.msg, "LOCATION"); loc != "http://192.168.1.10:8420/dlna/device.xml" {
			t.Errorf("LOCATION = %q", loc)
		}
		for _, h := range []string{"CACHE-CONTROL", "DATE", "EXT", "SERVER"} {
			if !hasHeader(m.msg, h) {
				t.Errorf("falta %s: %q", h, m.msg)
			}
		}
		if header(m.msg, "CACHE-CONTROL") != "max-age=1800" {
			t.Errorf("CACHE-CONTROL = %q", header(m.msg, "CACHE-CONTROL"))
		}
		if !strings.HasPrefix(m.msg, "HTTP/1.1 200 OK\r\n") || !strings.HasSuffix(m.msg, "\r\n\r\n") {
			t.Errorf("formato: %q", m.msg)
		}
		if !strings.Contains(header(m.msg, "SERVER"), "UPnP/1.0 MediaNode/1.2.3") {
			t.Errorf("SERVER = %q", header(m.msg, "SERVER"))
		}
	}
	if len(seen) != 5 {
		t.Errorf("destinos distintos: %v", seen)
	}

	// Desde la otra red, la dirección es la de la otra interfaz.
	f2 := newFakeIO()
	s2 := newTestSSDP(f2, eth0, eth1)
	s2.handle(msearch(deviceTypeURN), netip.MustParseAddrPort("10.0.3.4:1"))
	if loc := header(f2.waitUnicast(t, 1)[0].msg, "LOCATION"); loc != "http://10.0.0.5:8420/dlna/device.xml" {
		t.Errorf("LOCATION desde la otra red = %q", loc)
	}
}

func TestBusquedasEspecificas(t *testing.T) {
	for _, c := range []struct {
		st   string
		want int
	}{
		{"upnp:rootdevice", 1}, {"uuid:" + testUUID, 1}, {deviceTypeURN, 1}, {cdsTypeURN, 1}, {cmsTypeURN, 1},
		{"uuid:otro", 0}, {"urn:schemas-upnp-org:device:MediaRenderer:1", 0}, {"ssdp:ALL", 0}, {"", 0},
	} {
		f := newFakeIO()
		s := newTestSSDP(f, eth0)
		s.handle(msearch(c.st), netip.MustParseAddrPort("192.168.1.77:1"))
		time.Sleep(20 * time.Millisecond)
		if got := len(f.sentUnicast()); got != c.want {
			t.Errorf("ST %q: %d respuestas, se esperaban %d", c.st, got, c.want)
		}
	}
}

func TestNoRespondeAQuienNoEsDeLaRedLocal(t *testing.T) {
	for _, src := range []string{
		"203.0.113.9:1900", // IP pública.
		"8.8.8.8:1",        // Idem.
		"192.168.7.7:1",    // Privada, pero de una red a la que este equipo no está conectado.
		"[2001:db8::1]:1",  // IPv6 pública.
		"100.64.1.1:1",     // CGNAT sin declarar.
		"169.254.1.1:1",    // Enlace local: no hay interfaz anunciada.
	} {
		f := newFakeIO()
		s := newTestSSDP(f, eth0, eth1)
		s.handle(msearch("ssdp:all"), netip.MustParseAddrPort(src))
		time.Sleep(20 * time.Millisecond)
		if got := f.sentUnicast(); len(got) != 0 {
			t.Errorf("desde %s se respondió: %v", src, got)
		}
	}
}

func TestLaRespuestaTardaSegunMX(t *testing.T) {
	f := newFakeIO()
	s := newTestSSDP(f, eth0)
	var asked int
	s.delay = func(mx int) time.Duration { asked = mx; return 80 * time.Millisecond }
	start := time.Now()
	s.handle(msearch("upnp:rootdevice"), netip.MustParseAddrPort("192.168.1.77:1"))
	if len(f.sentUnicast()) != 0 {
		t.Fatal("la respuesta debe retrasarse")
	}
	f.waitUnicast(t, 1)
	if asked != 2 || time.Since(start) < 70*time.Millisecond {
		t.Fatalf("MX pedido %d, esperó %v", asked, time.Since(start))
	}
}

func TestLimitePorOrigen(t *testing.T) {
	f := newFakeIO()
	s := newTestSSDP(f, eth0)
	from := netip.MustParseAddrPort("192.168.1.77:1")
	for range searchBurst + 5 {
		s.handle(msearch("upnp:rootdevice"), from)
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(f.sentUnicast()); got != searchBurst {
		t.Fatalf("respuestas = %d, se esperaban %d", got, searchBurst)
	}
	// Otro origen tiene su propio cupo.
	s.handle(msearch("upnp:rootdevice"), netip.MustParseAddrPort("192.168.1.78:1"))
	f.waitUnicast(t, searchBurst+1)
}

func TestIgnoraLoQueNoEsUnaBusqueda(t *testing.T) {
	f := newFakeIO()
	s := newTestSSDP(f, eth0)
	from := netip.MustParseAddrPort("192.168.1.77:1")
	s.handle([]byte("NOTIFY * HTTP/1.1\r\nNT: upnp:rootdevice\r\nNTS: ssdp:alive\r\n\r\n"), from)
	s.handle([]byte("HTTP/1.1 200 OK\r\nST: ssdp:all\r\n\r\n"), from)
	s.handle(nil, from)
	s.handle([]byte("\xff\xfe"), from)
	time.Sleep(20 * time.Millisecond)
	if len(f.sentUnicast()) != 0 {
		t.Fatal("respondió a algo que no era una búsqueda")
	}
}

func TestNoRespondeTrasCerrar(t *testing.T) {
	f := newFakeIO()
	s := newTestSSDP(f, eth0)
	s.delay = func(int) time.Duration { return 50 * time.Millisecond }
	s.handle(msearch("upnp:rootdevice"), netip.MustParseAddrPort("192.168.1.77:1"))
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	if len(f.sentUnicast()) != 0 {
		t.Fatal("respondió con el servidor cerrado")
	}
}

func TestAnuncios(t *testing.T) {
	f := newFakeIO()
	s := newTestSSDP(f, eth0, eth1)
	s.announce("ssdp:alive")
	if len(f.multicast) != 10 {
		t.Fatalf("%d anuncios, se esperaban 5 por interfaz", len(f.multicast))
	}
	perIface := map[string]int{}
	for _, m := range f.multicast {
		perIface[m.via.Name]++
		if !strings.HasPrefix(m.msg, "NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\n") {
			t.Errorf("formato: %q", m.msg)
		}
		if header(m.msg, "NTS") != "ssdp:alive" || header(m.msg, "CACHE-CONTROL") != "max-age=1800" || !hasHeader(m.msg, "SERVER") {
			t.Errorf("alive: %q", m.msg)
		}
		want := "http://" + m.via.IP.String() + ":8420/dlna/device.xml"
		if header(m.msg, "LOCATION") != want {
			t.Errorf("LOCATION = %q, se esperaba %q", header(m.msg, "LOCATION"), want)
		}
		nt := header(m.msg, "NT")
		if nt == "uuid:"+testUUID {
			if header(m.msg, "USN") != nt {
				t.Errorf("USN de la raíz: %q", m.msg)
			}
		} else if !strings.HasPrefix(header(m.msg, "USN"), "uuid:"+testUUID+"::"+nt) {
			t.Errorf("USN = %q para NT %q", header(m.msg, "USN"), nt)
		}
	}
	if perIface["eth0"] != 5 || perIface["eth1"] != 5 {
		t.Errorf("por interfaz: %v", perIface)
	}

	f2 := newFakeIO()
	s2 := newTestSSDP(f2, eth0)
	s2.announce("ssdp:byebye")
	for _, m := range f2.multicast {
		if header(m.msg, "NTS") != "ssdp:byebye" || hasHeader(m.msg, "LOCATION") || hasHeader(m.msg, "CACHE-CONTROL") || !hasHeader(m.msg, "USN") {
			t.Errorf("byebye: %q", m.msg)
		}
	}
	if len(f2.multicast) != 5 {
		t.Errorf("byebye: %d", len(f2.multicast))
	}
}

func TestRefreshSoloSeUneALasInterfacesNuevas(t *testing.T) {
	f := newFakeIO()
	list := []iface{eth0}
	s := newSSDP(f, testUUID, 8420, "1", remote.NewNetwork(nil), func() []iface { return list }, zerolog.Nop())
	if !s.refresh() || len(f.joined) != 1 {
		t.Fatalf("primera vez: %v", f.joined)
	}
	if s.refresh() || len(f.joined) != 1 {
		t.Fatalf("sin cambios no se une de nuevo: %v", f.joined)
	}
	list = []iface{eth0, eth1}
	if !s.refresh() {
		t.Fatal("cambió el conjunto")
	}
	if _, ok := s.interfaceFor(netip.MustParseAddr("10.0.0.9")); !ok {
		t.Fatal("la interfaz nueva no está activa")
	}
	list = nil
	if !s.refresh() {
		t.Fatal("se quedó sin interfaces")
	}
	if _, ok := s.interfaceFor(netip.MustParseAddr("192.168.1.9")); ok {
		t.Fatal("sin interfaces no se puede responder a nadie")
	}
}

func TestStopAvisaYCierra(t *testing.T) {
	f := newFakeIO()
	s := newTestSSDP(f, eth0)
	s.start(context.Background())
	time.Sleep(50 * time.Millisecond)
	s.stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	byebyes := 0
	for _, m := range f.multicast {
		if header(m.msg, "NTS") == "ssdp:byebye" {
			byebyes++
		}
	}
	if byebyes != 5 || !f.closed {
		t.Fatalf("byebye=%d cerrado=%v", byebyes, f.closed)
	}
}

func TestSocketRealPorLoopback(t *testing.T) {
	lo := iface{Index: 1, Name: "lo", IP: netip.MustParseAddr("127.0.0.1"), Prefix: netip.MustParsePrefix("127.0.0.0/8")}
	io, err := newUDPIO(context.Background(), 0)
	if err != nil {
		t.Skipf("no se puede abrir un socket UDP: %v", err)
	}
	s := newSSDP(io, testUUID, 8420, "1", remote.NewNetwork(nil), func() []iface { return []iface{lo} }, zerolog.Nop())
	s.delay = func(int) time.Duration { return 0 }
	s.start(context.Background())
	defer s.stop()

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	defer client.Close()
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(io.localAddr().Port())}
	if _, err := client.WriteToUDP(msearch(deviceTypeURN), target); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := client.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("sin respuesta por el socket real: %v", err)
	}
	msg := string(buf[:n])
	if header(msg, "ST") != deviceTypeURN || header(msg, "LOCATION") != "http://127.0.0.1:8420/dlna/device.xml" {
		t.Fatalf("respuesta: %q", msg)
	}
}

func TestSanitizeToken(t *testing.T) {
	for in, want := range map[string]string{"1.2.3": "1.2.3", "dev": "dev", "": "dev", "1.0\r\nX: y": "1.0Xy", "a b/c": "abc", "v1.0+build-7_x": "v1.0+build-7_x"} {
		if got := sanitizeToken(in); got != want {
			t.Errorf("sanitizeToken(%q) = %q, se esperaba %q", in, got, want)
		}
	}
}

// header devuelve el valor de una cabecera de un mensaje SSDP, sin distinguir
// mayúsculas.
func header(msg, name string) string {
	for _, l := range strings.Split(msg, "\r\n") {
		k, v, ok := strings.Cut(l, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func hasHeader(msg, name string) bool {
	for _, l := range strings.Split(msg, "\r\n") {
		k, _, ok := strings.Cut(l, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return true
		}
	}
	return false
}
