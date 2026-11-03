// Package middleware reúne los filtros comunes de las peticiones HTTP.
package middleware

import (
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/core"
	"medianode/internal/monitoring"
)

// MaxBodyBytes limita el cuerpo de las peticiones JSON.
const MaxBodyBytes = 1 << 20

// AccessLog registra cada petición. Nunca incluye cabeceras ni la query, y
// oculta las rutas de streaming porque contienen el token de la sesión.
func AccessLog(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)

			status := ww.Status()
			event := log.Info()
			switch {
			case status >= 500:
				event = log.Error()
			case status >= 400:
				event = log.Warn()
			case r.URL.Path == "/health":
				event = log.Debug()
			}
			event.
				Str("request_id", monitoring.RequestID(r.Context())).
				Str("method", r.Method).
				Str("path", safePath(r.URL.Path)).
				Int("status", status).
				Int("bytes", ww.BytesWritten()).
				Dur("duration", time.Since(start)).
				Msg("petición")
		})
	}
}

func safePath(path string) string {
	if strings.HasPrefix(path, "/stream/") {
		return "/stream/[oculto]"
	}
	return path
}

// Recover convierte un panic en un 500 estructurado sin tumbar el proceso.
func Recover(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				log.Error().
					Str("request_id", monitoring.RequestID(r.Context())).
					Interface("panic", rec).
					Bytes("stack", debug.Stack()).
					Msg("panic en un handler")

				if ww, ok := w.(chimw.WrapResponseWriter); ok && ww.Status() != 0 {
					return // La respuesta ya empezó y no se puede sustituir.
				}
				api.WriteError(w, r, core.ErrInternal())
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// LimitBody rechaza los cuerpos mayores que limit al leerlos.
func LimitBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}
