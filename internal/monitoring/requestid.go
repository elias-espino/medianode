package monitoring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// HeaderRequestID es la cabecera con la que el cliente recibe el identificador.
const HeaderRequestID = "X-Request-ID"

type requestIDKey struct{}

// WithRequestID asigna un identificador a cada petición. Se genera siempre en
// el servidor para que un cliente no pueda inyectar valores en los logs.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set(HeaderRequestID, id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestID devuelve el identificador de la petición, o "" si no tiene.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand no falla en los sistemas soportados.
	return hex.EncodeToString(b[:])
}
