package remote

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/core"
)

// Options es la política de acceso remoto, tomada de la configuración.
type Options struct {
	Enabled      bool // remote.enabled
	Direct       bool // remote.direct_enabled
	AdminEnabled bool // remote.admin_enabled
	PublicURL    string

	HTTPSEnabled   bool
	TLSCertFile    string
	TrustedProxies []string
	LocalNetworks  []string
	BindAddress    string
}

// Remote aplica la política de acceso remoto y comprueba que sea alcanzable.
type Remote struct {
	Net  *Network
	Opts Options
	Log  zerolog.Logger

	probe probeState
	env   environment
	deny  denials
}

// denials limita el aviso de conexiones rechazadas a uno por minuto y origen:
// un análisis de puertos no debe llenar el registro.
type denials struct {
	mu   sync.Mutex
	last map[string]time.Time
}

const (
	denyLogEvery = time.Minute
	maxDenied    = 256
)

// New crea el módulo de acceso remoto.
func New(opts Options, log zerolog.Logger) *Remote {
	r := &Remote{
		Net:  NewNetwork(opts.TrustedProxies).WithLocal(opts.LocalNetworks),
		Opts: opts, Log: log,
	}
	r.env = defaultEnvironment()
	return r
}

// Classify dice si la petición es local o remota (§14.4).
func (r *Remote) Classify(req *http.Request) Origin { return r.Net.Classify(req) }

// Allowed dice si se aceptan las peticiones que no son de la red local.
func (r *Remote) Allowed() bool { return r.Opts.Enabled && r.Opts.Direct }

// AdminAllowed dice si la administración es accesible para esa petición.
func (r *Remote) AdminAllowed(req *http.Request) bool {
	return r.Classify(req) == OriginLocal || (r.Allowed() && r.Opts.AdminEnabled)
}

// Gate rechaza lo que no viene de la red local mientras el acceso remoto no
// esté activo, y añade HSTS a las respuestas remotas por una conexión segura.
// Va el primero: una petición rechazada no llega a ninguna ruta.
func (r *Remote) Gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.Classify(req) == OriginLocal {
			next.ServeHTTP(w, req)
			return
		}
		if !r.Allowed() {
			r.noteDenied(req)
			api.WriteError(w, req, core.ErrRemoteDisabled())
			return
		}
		if r.Net.Secure(req) {
			// Un año, sin includeSubDomains: el dominio puede tener otros servicios.
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, req)
	})
}

// noteDenied avisa en el registro de una conexión remota rechazada, para que quien
// puso un reverse proxy sin declararlo sepa por qué recibe un 403.
func (r *Remote) noteDenied(req *http.Request) {
	ip := r.Net.ClientIP(req)
	now := r.env.now()
	r.deny.mu.Lock()
	if r.deny.last == nil || len(r.deny.last) >= maxDenied {
		r.deny.last = map[string]time.Time{}
	}
	if at, ok := r.deny.last[ip]; ok && now.Sub(at) < denyLogEvery {
		r.deny.mu.Unlock()
		return
	}
	r.deny.last[ip] = now
	r.deny.mu.Unlock()
	ev := r.Log.Warn().Str("ip", ip).Str("ruta", req.URL.Path)
	for _, h := range forwardHeaders {
		if req.Header.Get(h) != "" {
			ev = ev.Bool("reenviada", true)
			break
		}
	}
	ev.Msg("conexión remota rechazada: remote.enabled y remote.direct_enabled están desactivados; si es tu reverse proxy, declara su dirección en server.trusted_proxies")
}

// RequireLocalAdmin cierra a las peticiones remotas las rutas de
// administración, salvo que remote.admin_enabled lo permita. Se aplica antes de
// la autenticación: el aviso es el mismo con o sin sesión, y un atacante no
// averigua nada de las credenciales.
func (r *Remote) RequireLocalAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !r.AdminAllowed(req) {
			api.WriteError(w, req, core.ErrRemoteAdminDisabled())
			return
		}
		next.ServeHTTP(w, req)
	})
}

// adminPanelPaths son las pantallas del panel que solo sirven a un
// administrador. El panel se enruta en el navegador; esto evita que se
// descarguen desde fuera, y la API ya está cerrada de todos modos.
var adminPanelPaths = []string{"/admin", "/users", "/metadata"}

// GuardPanel sirve el panel, pero no sus pantallas de administración a quien
// no puede usarlas.
func (r *Remote) GuardPanel(panel http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !r.AdminAllowed(req) {
			for _, p := range adminPanelPaths {
				if req.URL.Path == p || strings.HasPrefix(req.URL.Path, p+"/") {
					http.Error(w, "La administración no está disponible desde fuera de la red local", http.StatusForbidden)
					return
				}
			}
		}
		panel.ServeHTTP(w, req)
	})
}
