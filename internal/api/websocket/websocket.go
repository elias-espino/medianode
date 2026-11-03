// Package websocket sirve /api/v1/events (§17).
package websocket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"medianode/internal/api/middleware"
	"medianode/internal/authorization"
	"medianode/internal/database"
	"medianode/internal/events"
)

// Tiempos de la conexión.
const (
	writeTimeout  = 10 * time.Second
	pingInterval  = 30 * time.Second
	recheckPeriod = time.Minute
)

// Handler atiende las conexiones de eventos. Va detrás de Authenticate.
type Handler struct {
	Bus  *events.Bus
	Libs events.LibraryChecker
	// Authenticate vuelve a validar el token: revocar el dispositivo,
	// desactivar al usuario o caducar el token cierra la conexión.
	Authenticate func(ctx context.Context, token string) (authorization.Actor, error)
	// Origins son los orígenes de cors_origins; el propio servidor siempre se
	// acepta. Un cliente nativo no envía Origin.
	Origins []string
	Log     zerolog.Logger

	// Intervalos; cero toma los de producción. Para pruebas.
	PingInterval  time.Duration
	RecheckPeriod time.Duration
}

type message struct {
	Event     string         `json:"event"`
	Timestamp string         `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

// ServeHTTP acepta la conexión y reenvía los eventos que el actor puede ver.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "sin sesión", http.StatusUnauthorized)
		return
	}
	token := middleware.AccessToken(r)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originHosts(h.Origins)})
	if err != nil {
		// Accept ya respondió, por ejemplo 403 a un Origin ajeno.
		h.Log.Debug().Err(err).Msg("conexión de eventos rechazada")
		return
	}
	defer conn.CloseNow()

	// La suscripción va antes de leer: así no se pierde lo que se publique
	// mientras arranca el bucle.
	sub := h.Bus.Subscribe()
	defer h.Bus.Unsubscribe(sub)

	// Los clientes no envían nada; CloseRead atiende el cierre y los ping.
	ctx := conn.CloseRead(context.WithoutCancel(r.Context()))

	ping := time.NewTicker(orDefault(h.PingInterval, pingInterval))
	defer ping.Stop()
	recheck := time.NewTicker(orDefault(h.RecheckPeriod, recheckPeriod))
	defer recheck.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Dropped:
			conn.Close(websocket.StatusTryAgainLater, "cliente demasiado lento")
			return
		case <-recheck.C:
			a, err := h.Authenticate(ctx, token)
			if err != nil {
				conn.Close(websocket.StatusPolicyViolation, "sesión no válida")
				return
			}
			actor = a
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case e := <-sub.C:
			if !events.CanReceive(ctx, h.Libs, actor, e) {
				continue
			}
			data, err := json.Marshal(message{Event: e.Name, Timestamp: database.FormatTime(e.Time), Data: e.Data})
			if err != nil {
				h.Log.Error().Err(err).Str("event", e.Name).Msg("evento no serializable")
				continue
			}
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err = conn.Write(wctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// originHosts convierte «https://panel.example:8443» en el patrón de host
// que espera coder/websocket. Los orígenes ya los validó la configuración.
func originHosts(origins []string) []string {
	out := make([]string, 0, len(origins))
	for _, o := range origins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			out = append(out, u.Host)
		}
	}
	return out
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
