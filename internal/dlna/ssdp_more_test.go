package dlna

import (
	"net/netip"
	"testing"
	"time"
)

// La comprobación de que el origen es local no depende de que la red de la
// interfaz lo sea: una máscara absurda (/4) haría que la «red» de la interfaz
// incluyera direcciones públicas, y aun así no se responde a ellas.
func TestNoRespondeAUnaDireccionPublicaAunqueLaMascaraLaIncluya(t *testing.T) {
	odd := iface{Index: 9, Name: "odd0", IP: netip.MustParseAddr("10.0.0.5"), Prefix: netip.MustParsePrefix("0.0.0.0/4")}
	f := newFakeIO()
	s := newTestSSDP(f, odd)
	if _, ok := s.interfaceFor(netip.MustParseAddr("8.8.8.8")); !ok {
		t.Fatal("la prueba necesita una interfaz cuya red incluya la dirección pública")
	}
	s.handle(msearch("ssdp:all"), netip.MustParseAddrPort("8.8.8.8:1900"))
	time.Sleep(30 * time.Millisecond)
	if got := f.sentUnicast(); len(got) != 0 {
		t.Fatalf("se respondió a una dirección pública: %v", got)
	}
	// Y a una local de la misma «red», sí.
	s.handle(msearch("upnp:rootdevice"), netip.MustParseAddrPort("10.0.0.9:1900"))
	f.waitUnicast(t, 1)
}
