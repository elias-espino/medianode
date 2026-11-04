// Package remote clasifica cada petición como local o remota (§14.4), aplica
// la política de acceso remoto (§14.2) y comprueba desde fuera de la red local
// que el servidor es alcanzable.
//
// La IP nunca es una identidad: solo decide qué superficie se expone. Quien
// llega con credenciales válidas sigue pasando por la autorización de siempre.
package remote

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Cabeceras de reenvío. Solo cuentan si la conexión llega de un proxy de
// confianza; si llegan de otro origen, la petición se trata como remota.
const (
	HeaderForwardedFor   = "X-Forwarded-For"
	HeaderForwardedProto = "X-Forwarded-Proto"
	headerForwarded      = "Forwarded"
	headerRealIP         = "X-Real-IP"
	headerForwardedHost  = "X-Forwarded-Host"
)

// forwardHeaders son las que delatan que la petición pasó por un proxy.
var forwardHeaders = []string{HeaderForwardedFor, HeaderForwardedProto, headerForwarded, headerRealIP, headerForwardedHost}

// Origin dice de dónde viene una petición.
type Origin int

// Orígenes. El valor cero es Remote a propósito: una petición sin clasificar
// nunca se trata como local.
const (
	OriginRemote Origin = iota
	OriginLocal
)

func (o Origin) String() string {
	if o == OriginLocal {
		return "local"
	}
	return "remote"
}

// Network interpreta la dirección de origen de una petición.
type Network struct {
	trusted []netip.Prefix
	local   []netip.Prefix
}

// NewNetwork acepta direcciones sueltas y rangos de proxies de confianza,
// ya validados por la configuración.
func NewNetwork(trusted []string) *Network {
	return &Network{trusted: parsePrefixes(trusted)}
}

// WithLocal añade rangos que cuentan como red local, además de las
// direcciones privadas, de enlace local y de bucle.
func (n *Network) WithLocal(nets []string) *Network {
	n.local = parsePrefixes(nets)
	return n
}

func parsePrefixes(list []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range list {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	return out
}

func (n *Network) isTrusted(a netip.Addr) bool {
	return slices.ContainsFunc(n.trusted, func(p netip.Prefix) bool { return p.Contains(a.Unmap()) })
}

func peerAddr(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

// ClientIP recorre X-Forwarded-For desde el final: lo que hay a la izquierda del
// último proxy de confianza lo escribe el cliente y puede ser falso.
func (n *Network) ClientIP(r *http.Request) string {
	if a, ok := n.clientAddr(r); ok {
		return a.String()
	}
	return r.RemoteAddr
}

// clientAddr es la dirección del cliente: la de la conexión, o, si viene de un
// proxy de confianza, la que este vio. Falla solo si la conexión no tiene una
// dirección legible.
func (n *Network) clientAddr(r *http.Request) (netip.Addr, bool) {
	peer, ok := peerAddr(r)
	if !ok || !n.isTrusted(peer) {
		return peer, ok
	}
	hops := strings.Split(r.Header.Get(HeaderForwardedFor), ",")
	for _, hop := range slices.Backward(hops) {
		a, err := netip.ParseAddr(strings.TrimSpace(hop))
		if err != nil {
			break
		}
		peer = a.Unmap()
		if !n.isTrusted(peer) {
			break
		}
	}
	return peer, true
}

// Secure indica si la conexión del cliente va cifrada.
func (n *Network) Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	peer, ok := peerAddr(r)
	return ok && n.isTrusted(peer) && r.Header.Get(HeaderForwardedProto) == "https"
}

// IsLocalIP dice si una dirección pertenece a la red local: bucle, privada
// (RFC 1918 y fc00::/7), de enlace local o de un rango de remote.local_networks.
// Lo que no se puede interpretar no es local.
func (n *Network) IsLocalIP(ip string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	return n.localAddr(a.Unmap())
}

func (n *Network) localAddr(a netip.Addr) bool {
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() {
		return true
	}
	return slices.ContainsFunc(n.local, func(p netip.Prefix) bool { return p.Contains(a) })
}

// Classify decide si una petición es local o remota (§14.4).
//
//   - Sin proxy: manda la dirección de la conexión, pero si trae cabeceras de
//     reenvío es remota. Un proxy que no esté en trusted_proxies no puede hacer
//     pasar por local el tráfico de fuera.
//   - Con un proxy de confianza: manda la última dirección de X-Forwarded-For.
//     Si falta o no se entiende, es remota: ante la duda, lo más restrictivo.
func (n *Network) Classify(r *http.Request) Origin {
	peer, ok := peerAddr(r)
	if !ok {
		return OriginRemote
	}
	if !n.isTrusted(peer) {
		for _, h := range forwardHeaders {
			if r.Header.Get(h) != "" {
				return OriginRemote
			}
		}
		return originOf(n.localAddr(peer))
	}
	hops := strings.Split(r.Header.Get(HeaderForwardedFor), ",")
	if _, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err != nil {
		return OriginRemote
	}
	ip, _ := n.clientAddr(r) // La conexión ya tiene dirección legible: se comprobó arriba.
	return originOf(n.localAddr(ip))
}

func originOf(local bool) Origin {
	if local {
		return OriginLocal
	}
	return OriginRemote
}
