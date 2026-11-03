package handlers

import (
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/api/middleware"
	"medianode/internal/auth"
	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
)

// platformWeb marca al cliente que recibe los tokens en cookies (§15.4).
const platformWeb = "web"

// refreshCookiePath limita la cookie de refresh a las rutas que la usan.
const refreshCookiePath = "/api/v1/auth"

// Auth atiende /auth.
type Auth struct {
	Service *auth.Service
	Network *middleware.Network
	Log     zerolog.Logger
}

// Setup crea el Owner.
func (h *Auth) Setup(w http.ResponseWriter, r *http.Request) {
	var req dto.SetupRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	user, err := h.Service.Setup(r.Context(), req.Code, req.Username, req.Password)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, userView(user))
}

// Login inicia sesión.
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
	var req dto.LoginRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	pair, err := h.Service.Login(r.Context(), req.Username, req.Password, h.deviceInfo(r, req.Device))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.writeSession(w, r, pair, req.Platform == platformWeb)
}

// Refresh renueva los tokens. Una renovación por cookie necesita CSRF igual que
// cualquier otra escritura, y no pasa por Authenticate porque el access token
// puede haber caducado.
func (h *Auth) Refresh(w http.ResponseWriter, r *http.Request) {
	var req dto.RefreshRequest
	if r.ContentLength != 0 {
		if err := api.ReadJSON(r, &req); err != nil {
			api.WriteFailure(w, r, h.Log, err)
			return
		}
	}
	token, viaCookie := req.RefreshToken, false
	if token == "" {
		c, err := r.Cookie(middleware.CookieRefresh)
		if err != nil {
			api.WriteError(w, r, core.ErrAuthRequired())
			return
		}
		if !middleware.ValidCSRF(r) {
			api.WriteError(w, r, core.ErrCSRFInvalid())
			return
		}
		token, viaCookie = c.Value, true
	}
	pair, err := h.Service.Refresh(r.Context(), token)
	if err != nil {
		if viaCookie {
			h.clearCookies(w, r)
		}
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.writeSession(w, r, pair, viaCookie)
}

// Logout revoca el dispositivo actual.
func (h *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	actor := mustActor(r)
	if err := h.Service.Logout(r.Context(), actor.DeviceID); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.clearCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// Me devuelve el usuario actual.
func (h *Auth) Me(w http.ResponseWriter, r *http.Request) {
	user, err := h.Service.CurrentUser(r.Context(), mustActor(r))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, userView(user))
}

// ChangePassword cambia la contraseña propia.
func (h *Auth) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req dto.ChangePasswordRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if err := h.Service.ChangePassword(r.Context(), mustActor(r), req.CurrentPassword, req.NewPassword); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreatePairing genera el código que muestra un dispositivo sin sesión.
func (h *Auth) CreatePairing(w http.ResponseWriter, r *http.Request) {
	var req dto.PairingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	code, err := h.Service.CreatePairingCode(r.Context(), h.deviceInfo(r, req.Device))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	noStore(w)
	api.WriteJSON(w, http.StatusCreated, dto.PairingCode{
		Code: code.Code, PollToken: code.PollToken, ExpiresAt: database.FormatTime(code.ExpiresAt),
	})
}

// ClaimPairing reclama un código desde una sesión iniciada.
func (h *Auth) ClaimPairing(w http.ResponseWriter, r *http.Request) {
	var req dto.PairingClaimRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	claim, err := h.Service.ClaimPairingCode(r.Context(), mustActor(r), req.Code)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, dto.PairingClaim{DeviceName: claim.DeviceName, Platform: claim.Platform})
}

// PairingToken responde 202 hasta que el código se reclame y después entrega los
// tokens una sola vez.
func (h *Auth) PairingToken(w http.ResponseWriter, r *http.Request) {
	var req dto.PairingTokenRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	pair, err := h.Service.PollPairing(r.Context(), req.PollToken)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if pair == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	h.writeSession(w, r, pair, false)
}

func (h *Auth) deviceInfo(r *http.Request, d dto.Device) auth.DeviceInfo {
	return auth.DeviceInfo{
		Name:          d.Name,
		Platform:      d.Platform,
		ClientVersion: d.ClientVersion,
		Identifier:    d.Identifier,
		IP:            h.Network.ClientIP(r),
	}
}

func (h *Auth) writeSession(w http.ResponseWriter, r *http.Request, pair *auth.TokenPair, cookies bool) {
	session := dto.Session{
		ExpiresAt: database.FormatTime(pair.ExpiresAt),
		User:      userView(&pair.User),
		DeviceID:  pair.DeviceID,
	}
	if cookies {
		csrf, err := auth.NewCSRFToken()
		if err != nil {
			api.WriteFailure(w, r, h.Log, err)
			return
		}
		h.setCookie(w, r, middleware.CookieAccess, pair.AccessToken, "/", auth.AccessTTL, true)
		h.setCookie(w, r, middleware.CookieRefresh, pair.RefreshToken, refreshCookiePath, auth.RefreshTTL, true)
		h.setCookie(w, r, middleware.CookieCSRF, csrf, "/", auth.RefreshTTL, false)
	} else {
		session.AccessToken = pair.AccessToken
		session.RefreshToken = pair.RefreshToken
	}
	noStore(w)
	api.WriteJSON(w, http.StatusOK, session)
}

func (h *Auth) setCookie(w http.ResponseWriter, r *http.Request, name, value, path string, ttl time.Duration, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: httpOnly,
		Secure:   h.Network.Secure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *Auth) clearCookies(w http.ResponseWriter, r *http.Request) {
	h.setCookie(w, r, middleware.CookieAccess, "", "/", -time.Second, true)
	h.setCookie(w, r, middleware.CookieRefresh, "", refreshCookiePath, -time.Second, true)
	h.setCookie(w, r, middleware.CookieCSRF, "", "/", -time.Second, false)
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// mustActor solo se usa en rutas detrás de Authenticate. Sin actor sería un fallo
// de montaje de rutas, y Recover lo convierte en un 500.
func mustActor(r *http.Request) authorization.Actor {
	actor, ok := authorization.FromContext(r.Context())
	if !ok {
		panic("ruta protegida sin Authenticate")
	}
	return actor
}

func userView(u *sqlc.User) dto.User {
	return dto.User{
		ID:           u.ID,
		Username:     u.Username,
		Role:         u.Role,
		IsActive:     u.IsActive != 0,
		MaxAgeRating: u.MaxAgeRating,
		CreatedAt:    u.CreatedAt,
		LastLoginAt:  u.LastLoginAt,
	}
}
