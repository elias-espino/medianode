package dlna

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"golang.org/x/net/ipv4"
)

// udpIO es el transporte real: un socket UDP en el puerto SSDP, unido al grupo
// de multidifusión en cada interfaz.
type udpIO struct {
	pc *ipv4.PacketConn
	// mu hace atómico «elegir interfaz de salida y escribir»: la interfaz es
	// estado del socket.
	mu    sync.Mutex
	group *net.UDPAddr
}

// newUDPIO abre el puerto. Se comparte con otros programas UPnP del equipo
// (SO_REUSEADDR): la búsqueda llega por multidifusión a todos.
func newUDPIO(ctx context.Context, port int) (*udpIO, error) {
	lc := net.ListenConfig{Control: reuseAddr}
	conn, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	pc := ipv4.NewPacketConn(conn)
	_ = pc.SetMulticastTTL(2) // Una red local: UDA 1.0 recomienda 2.
	// Con la vuelta activada, un cliente en este mismo equipo también nos ve.
	_ = pc.SetMulticastLoopback(true)
	return &udpIO{pc: pc, group: &net.UDPAddr{IP: net.ParseIP(ssdpGroup), Port: ssdpPort}}, nil
}

func (u *udpIO) read(b []byte) (int, netip.AddrPort, error) {
	n, _, src, err := u.pc.ReadFrom(b)
	if err != nil {
		return 0, netip.AddrPort{}, err
	}
	ua, ok := src.(*net.UDPAddr)
	if !ok {
		return n, netip.AddrPort{}, nil
	}
	return n, ua.AddrPort(), nil
}

func (u *udpIO) writeUnicast(b []byte, dst netip.AddrPort) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, err := u.pc.WriteTo(b, nil, net.UDPAddrFromAddrPort(dst))
	return err
}

func (u *udpIO) writeMulticast(b []byte, via iface) error {
	ifc, err := net.InterfaceByIndex(via.Index)
	if err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.pc.SetMulticastInterface(ifc); err != nil {
		return err
	}
	_, err = u.pc.WriteTo(b, nil, u.group)
	return err
}

func (u *udpIO) join(via iface) error {
	ifc, err := net.InterfaceByIndex(via.Index)
	if err != nil {
		return err
	}
	return u.pc.JoinGroup(ifc, u.group)
}

func (u *udpIO) close() error { return u.pc.Close() }

// localAddr es la dirección real del socket (útil con el puerto 0 de las pruebas).
func (u *udpIO) localAddr() netip.AddrPort {
	if ua, ok := u.pc.LocalAddr().(*net.UDPAddr); ok {
		return ua.AddrPort()
	}
	return netip.AddrPort{}
}
