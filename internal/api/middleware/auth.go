package middleware

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/auth"
	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/media"
	"medianode/internal/remote"
)

// Credenciales del cliente web de §15.4.
const (
	CookieAccess  = "mn_access"
	CookieRefresh = "mn_refresh"
	CookieCSRF    = "mn_csrf"
	HeaderCSRF    = "X-MediaNode-CSRF"

	headerForwardedFor   = remote.HeaderForwardedFor
	headerForwardedProto = remote.HeaderForwardedProto
	bearerPrefix         = "Bearer "
)

type cookieAuthKey struct{}

// Authenticate exige un access token, por Bearer o por cookie. Si llega la
// cabecera no se mira la cookie: una Bearer inválida no debe pasar por otra vía.
func Authenticate(s *auth.Service, log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, viaCookie := accessToken(r)
			if token == "" {
				api.WriteError(w, r, core.ErrAuthRequired())
				return
			}
			actor, err := s.Authenticate(r.Context(), token)
			if err != nil {
				api.WriteFailure(w, r, log, err)
				return
			}
			ctx := authorization.WithActor(r.Context(), actor)
			if viaCookie {
				ctx = context.WithValue(ctx, cookieAuthKey{}, true)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AccessToken devuelve el token de acceso de la petición, por Bearer o por
// cookie. El WebSocket lo usa para volver a validar la sesión mientras dura.
func AccessToken(r *http.Request) string {
	token, _ := accessToken(r)
	return token
}

func accessToken(r *http.Request) (string, bool) {
	if header := r.Header.Get("Authorization"); header != "" {
		return strings.TrimPrefix(header, bearerPrefix), false
	}
	if c, err := r.Cookie(CookieAccess); err == nil {
		return c.Value, true
	}
	return "", false
}

// RequireCSRF exige la cabecera de doble envío en las peticiones que modifican
// algo y se autenticaron por cookie. Debe ir después de Authenticate.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaCookie, _ := r.Context().Value(cookieAuthKey{}).(bool)
		if viaCookie && !safeMethod(r.Method) && !ValidCSRF(r) {
			api.WriteError(w, r, core.ErrCSRFInvalid())
			return
		}
		next.ServeHTTP(w, r)
	})
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// ValidCSRF compara la cookie con la cabecera. Un sitio ajeno puede provocar que
// se envíe la cookie, pero no leerla para copiarla en la cabecera.
func ValidCSRF(r *http.Request) bool {
	c, err := r.Cookie(CookieCSRF)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.Header.Get(HeaderCSRF))) == 1
}

// RequireRole exige un rol mínimo. Debe ir después de Authenticate.
func RequireRole(min media.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, ok := authorization.FromContext(r.Context())
			if !ok {
				api.WriteError(w, r, core.ErrAuthRequired())
				return
			}
			var apiErr *core.APIError
			if err := authorization.RequireRole(actor, min); errors.As(err, &apiErr) {
				api.WriteError(w, r, apiErr)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit limita los intentos por dirección de origen.
func RateLimit(l *auth.Limiter, network *Network) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(network.ClientIP(r)) {
				api.WriteError(w, r, core.ErrRateLimited())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CORS solo responde a los orígenes configurados. El cliente web servido por el
// propio servidor es del mismo origen y no lo necesita.
func CORS(origins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			w.Header().Add("Vary", "Origin")
			if origin == "" || !slices.Contains(origins, origin) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+HeaderCSRF)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Network interpreta el origen de una petición: las cabeceras de reenvío solo
// cuentan si la conexión llega desde un proxy de confianza (§14.4). La
// implementación está en el paquete remote.
type Network = remote.Network

// NewNetwork acepta direcciones sueltas y rangos, ya validados por la configuración.
func NewNetwork(trusted []string) *Network { return remote.NewNetwork(trusted) }
