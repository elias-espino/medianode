package dto

import (
	"bytes"
	"encoding/json"
)

// Optional distingue un campo ausente de uno enviado como null en un PATCH.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON solo se invoca si el campo está presente.
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(data, []byte("null")) {
		o.Value = nil
		return nil
	}
	return json.Unmarshal(data, &o.Value)
}

// Device identifica al cliente en login y vinculación. Identifier debe ser
// estable: permite reutilizar la fila del dispositivo al volver a entrar.
type Device struct {
	Name          string `json:"device_name"`
	Platform      string `json:"platform"`
	ClientVersion string `json:"client_version,omitempty"`
	Identifier    string `json:"device_identifier,omitempty"`
}

// SetupRequest crea el Owner.
type SetupRequest struct {
	Code     string `json:"code"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginRequest inicia sesión.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Device
}

// RefreshRequest renueva tokens. La web lo deja vacío y usa la cookie.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Session es la respuesta de login, refresh y vinculación. La web recibe los
// tokens solo en cookies, sin exponerlos a JavaScript.
type Session struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    string `json:"expires_at"`
	User         User   `json:"user"`
	DeviceID     string `json:"device_id"`
}

// PairingRequest pide un código de vinculación.
type PairingRequest struct {
	Device
}

// PairingCode se muestra en el dispositivo que se vincula.
type PairingCode struct {
	Code      string `json:"code"`
	PollToken string `json:"poll_token"`
	ExpiresAt string `json:"expires_at"`
}

// PairingClaimRequest reclama un código desde una sesión iniciada.
type PairingClaimRequest struct {
	Code string `json:"code"`
}

// PairingClaim muestra qué dispositivo se ha vinculado.
type PairingClaim struct {
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
}

// PairingTokenRequest consulta si el código ya se reclamó.
type PairingTokenRequest struct {
	PollToken string `json:"poll_token"`
}

// User es la vista pública de una cuenta. Nunca incluye el hash.
type User struct {
	ID           string  `json:"id"`
	Username     string  `json:"username"`
	Role         string  `json:"role"`
	IsActive     bool    `json:"is_active"`
	MaxAgeRating *int64  `json:"max_age_rating"`
	CreatedAt    string  `json:"created_at"`
	LastLoginAt  *string `json:"last_login_at"`
}

// CreateUserRequest crea un usuario.
type CreateUserRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	Role         string `json:"role"`
	MaxAgeRating *int64 `json:"max_age_rating"`
}

// UpdateUserRequest modifica los campos presentes.
type UpdateUserRequest struct {
	Username     *string         `json:"username"`
	Password     *string         `json:"password"`
	Role         *string         `json:"role"`
	IsActive     *bool           `json:"is_active"`
	MaxAgeRating Optional[int64] `json:"max_age_rating"`
}

// ChangePasswordRequest cambia la contraseña propia.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// Preferences son las preferencias de reproducción.
type Preferences struct {
	AudioLanguage    *string `json:"audio_language"`
	SubtitleLanguage *string `json:"subtitle_language"`
	SubtitleMode     string  `json:"subtitle_mode"`
	MaxBitrateRemote *int64  `json:"max_bitrate_remote"`
}

// DeviceView es la vista de un dispositivo vinculado.
type DeviceView struct {
	ID            string  `json:"id"`
	UserID        string  `json:"user_id"`
	Name          string  `json:"device_name"`
	Platform      string  `json:"platform"`
	ClientVersion *string `json:"client_version"`
	CreatedAt     string  `json:"created_at"`
	LastSeenAt    *string `json:"last_seen_at"`
	RevokedAt     *string `json:"revoked_at"`
	Current       bool    `json:"current"`
}

// RenameDeviceRequest cambia el nombre de un dispositivo.
type RenameDeviceRequest struct {
	Name string `json:"device_name"`
}
