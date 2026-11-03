// Package core define los tipos compartidos por todo el servidor.
package core

import (
	"errors"
	"net/http"
)

// Códigos estables de §22.3 que ya produce el servidor. El resto entra con la
// fase que los produce.
const (
	CodeInternal         = "INTERNAL_ERROR"
	CodeNotFound         = "NOT_FOUND"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodeValidation       = "VALIDATION_ERROR"
	CodeRateLimited      = "RATE_LIMITED"
	CodeForbidden        = "FORBIDDEN"
	CodeCSRFInvalid      = "CSRF_INVALID"

	CodeAuthRequired       = "AUTH_REQUIRED"
	CodeAuthTokenExpired   = "AUTH_TOKEN_EXPIRED"
	CodeInvalidCredentials = "AUTH_INVALID_CREDENTIALS"
	CodeDeviceRevoked      = "AUTH_DEVICE_REVOKED"
	CodeSetupCompleted     = "AUTH_SETUP_COMPLETED"
	CodePairingInvalid     = "AUTH_PAIRING_INVALID"
	CodeUsernameTaken      = "USERNAME_TAKEN"

	CodeLibraryDenied   = "PERMISSION_LIBRARY_DENIED"
	CodePathUnavailable = "LIBRARY_PATH_UNAVAILABLE"
	CodeScanFailed      = "SCAN_FAILED"

	CodeMetadataUnavailable = "METADATA_PROVIDER_UNAVAILABLE"
	CodeMetadataLocked      = "METADATA_LOCKED"

	CodeSessionExpired = "SESSION_EXPIRED"

	CodePlaybackUnsupported = "PLAYBACK_UNSUPPORTED"
	CodeFileUnavailable     = "MEDIA_FILE_UNAVAILABLE"
	CodeCapacityExceeded    = "TRANSCODE_CAPACITY_EXCEEDED"
	CodeTranscodeFailed     = "TRANSCODE_INITIALIZATION_FAILED"
)

// APIError es un error con forma estable para los clientes. Message no debe
// contener rutas internas ni detalles sensibles.
type APIError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *APIError) Error() string {
	return e.Code + ": " + e.Message
}

// CodeOf devuelve el código estable de err, o vacío si no es un APIError.
func CodeOf(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// ErrInternal es la respuesta genérica ante un fallo inesperado.
func ErrInternal() *APIError {
	return &APIError{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "Error interno del servidor"}
}

// ErrNotFound indica que la ruta o el recurso no existe.
func ErrNotFound() *APIError {
	return &APIError{Status: http.StatusNotFound, Code: CodeNotFound, Message: "Recurso no encontrado"}
}

// ErrMethodNotAllowed indica que la ruta no admite el método.
func ErrMethodNotAllowed() *APIError {
	return &APIError{Status: http.StatusMethodNotAllowed, Code: CodeMethodNotAllowed, Message: "Método no permitido"}
}

// ErrValidation indica que la petición no es válida.
func ErrValidation(message string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: CodeValidation, Message: message}
}

// ErrRateLimited indica que se superó un límite de intentos.
func ErrRateLimited() *APIError {
	return &APIError{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: "Demasiados intentos, espera un momento"}
}

// ErrForbidden indica que el actor no tiene permiso.
func ErrForbidden() *APIError {
	return &APIError{Status: http.StatusForbidden, Code: CodeForbidden, Message: "No tienes permiso para esta acción"}
}

// ErrCSRFInvalid indica que falta o no coincide la cabecera CSRF.
func ErrCSRFInvalid() *APIError {
	return &APIError{Status: http.StatusForbidden, Code: CodeCSRFInvalid, Message: "Protección CSRF no superada"}
}

// ErrAuthRequired indica que falta una credencial válida.
func ErrAuthRequired() *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeAuthRequired, Message: "Debes iniciar sesión"}
}

// ErrAuthTokenExpired indica que el cliente debe renovar su access token.
func ErrAuthTokenExpired() *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeAuthTokenExpired, Message: "La sesión debe renovarse"}
}

// ErrInvalidCredentials no distingue entre usuario inexistente, cuenta
// desactivada y contraseña errónea.
func ErrInvalidCredentials() *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeInvalidCredentials, Message: "Credenciales no válidas"}
}

// ErrDeviceRevoked indica que el dispositivo ya no tiene acceso.
func ErrDeviceRevoked() *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: CodeDeviceRevoked, Message: "Este dispositivo ya no tiene acceso"}
}

// ErrSetupCompleted indica que el Owner ya existe.
func ErrSetupCompleted() *APIError {
	return &APIError{Status: http.StatusConflict, Code: CodeSetupCompleted, Message: "El servidor ya está configurado"}
}

// ErrPairingInvalid indica un código de vinculación erróneo, caducado o usado.
func ErrPairingInvalid() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: CodePairingInvalid, Message: "Código de vinculación no válido"}
}

// ErrUsernameTaken indica que el nombre de usuario ya existe.
func ErrUsernameTaken() *APIError {
	return &APIError{Status: http.StatusConflict, Code: CodeUsernameTaken, Message: "Ese nombre de usuario ya existe"}
}

// ErrLibraryDenied indica que el actor ve la biblioteca pero no tiene el
// permiso que pide la operación. Si no la ve, se responde 404.
func ErrLibraryDenied() *APIError {
	return &APIError{Status: http.StatusForbidden, Code: CodeLibraryDenied, Message: "No tienes permiso sobre esta biblioteca"}
}

// ErrPathUnavailable indica que una ruta no sirve como raíz de biblioteca. El
// mensaje explica el motivo sin repetir la ruta.
func ErrPathUnavailable(reason string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: CodePathUnavailable, Message: reason}
}

// ErrMetadataUnavailable indica que no hay proveedor configurado o que no
// responde. El catálogo sigue funcionando con los datos de los archivos.
func ErrMetadataUnavailable(reason string) *APIError {
	return &APIError{Status: http.StatusServiceUnavailable, Code: CodeMetadataUnavailable, Message: reason}
}

// ErrMetadataLocked indica que el elemento tiene los metadatos bloqueados y
// la operación los sobrescribiría con los del proveedor.
func ErrMetadataLocked() *APIError {
	return &APIError{
		Status: http.StatusConflict, Code: CodeMetadataLocked,
		Message: "Los metadatos están bloqueados; desbloquéalos antes de actualizarlos",
	}
}

// ErrSessionExpired indica que la sesión de reproducción terminó. El cliente
// crea otra desde la última posición conocida (§12.4).
func ErrSessionExpired() *APIError {
	return &APIError{Status: http.StatusGone, Code: CodeSessionExpired, Message: "La sesión de reproducción ha terminado"}
}

// ErrPlaybackUnsupported indica que el servidor no puede servir el archivo a
// ese cliente. details explica la decisión y sus motivos.
func ErrPlaybackUnsupported(message string, details map[string]any) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: CodePlaybackUnsupported, Message: message, Details: details}
}

// ErrFileUnavailable indica que el elemento no tiene ningún archivo
// reproducible: falta en disco o su análisis falló.
func ErrFileUnavailable() *APIError {
	return &APIError{Status: http.StatusConflict, Code: CodeFileUnavailable, Message: "El archivo no está disponible"}
}

// ErrCapacityExceeded indica que no hay cupo para otro trabajo de FFmpeg. Va
// con Retry-After: el cliente puede reintentar o elegir otra calidad.
func ErrCapacityExceeded() *APIError {
	return &APIError{Status: http.StatusServiceUnavailable, Code: CodeCapacityExceeded,
		Message: "El servidor está transcodificando al máximo; inténtalo en unos segundos"}
}

// ErrTranscodeFailed indica que FFmpeg no arrancó o no produjo el recurso.
func ErrTranscodeFailed() *APIError {
	return &APIError{Status: http.StatusServiceUnavailable, Code: CodeTranscodeFailed,
		Message: "No se pudo preparar la reproducción"}
}

// ErrLibraryBusy indica que la biblioteca está cambiando sus rutas o
// eliminándose y no admite un escaneo en este momento.
func ErrLibraryBusy() *APIError {
	return &APIError{Status: http.StatusConflict, Code: CodeScanFailed, Message: "La biblioteca está ocupada, inténtalo de nuevo"}
}
