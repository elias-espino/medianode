// Package api contiene el servidor HTTP y las utilidades de respuesta.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rs/zerolog"

	"medianode/internal/core"
	"medianode/internal/monitoring"
)

// retryAfterSeconds coincide con el minuto de los límites de intentos.
const retryAfterSeconds = "60"

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// WriteJSON serializa v como respuesta JSON UTF-8.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // Tras WriteHeader ya no se puede informar del fallo.
}

// ReadJSON decodifica el cuerpo y rechaza los campos desconocidos.
func ReadJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return core.ErrValidation("El cuerpo de la petición no es válido")
	}
	return nil
}

// WriteFailure responde a un error de un módulo. Uno inesperado se registra y
// llega al cliente como genérico, para no revelar detalles internos.
func WriteFailure(w http.ResponseWriter, r *http.Request, log zerolog.Logger, err error) {
	var apiErr *core.APIError
	if errors.As(err, &apiErr) {
		WriteError(w, r, apiErr)
		return
	}
	log.Error().Err(err).Str("request_id", monitoring.RequestID(r.Context())).Msg("error en la petición")
	WriteError(w, r, core.ErrInternal())
}

// WriteError responde con el formato de error estable de la API.
func WriteError(w http.ResponseWriter, r *http.Request, err *core.APIError) {
	switch {
	case err.Status == http.StatusTooManyRequests:
		w.Header().Set("Retry-After", retryAfterSeconds)
	case err.Code == core.CodeCapacityExceeded, err.Code == core.CodeTranscodeFailed:
		// Un cupo o un segmento pueden quedar libres o listos enseguida.
		w.Header().Set("Retry-After", "5")
	}
	WriteJSON(w, err.Status, errorEnvelope{Error: errorBody{
		Code:      err.Code,
		Message:   err.Message,
		Details:   err.Details,
		RequestID: monitoring.RequestID(r.Context()),
	}})
}
