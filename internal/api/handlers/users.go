package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/media"
	"medianode/internal/users"
)

// Users atiende /admin/users y /users/me.
type Users struct {
	Service *users.Service
	Log     zerolog.Logger
}

// List devuelve todos los usuarios.
func (h *Users) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Service.List(r.Context(), mustActor(r))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.User, len(rows))
	for i := range rows {
		out[i] = userView(&rows[i])
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Create crea un usuario.
func (h *Users) Create(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateUserRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	user, err := h.Service.Create(r.Context(), mustActor(r), users.CreateParams{
		Username:     req.Username,
		Password:     req.Password,
		Role:         media.Role(req.Role),
		MaxAgeRating: req.MaxAgeRating,
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, userView(user))
}

// Get devuelve un usuario.
func (h *Users) Get(w http.ResponseWriter, r *http.Request) {
	user, err := h.Service.Get(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, userView(user))
}

// Update modifica un usuario.
func (h *Users) Update(w http.ResponseWriter, r *http.Request) {
	var req dto.UpdateUserRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	params := users.UpdateParams{
		Username:       req.Username,
		Password:       req.Password,
		IsActive:       req.IsActive,
		MaxAgeRating:   req.MaxAgeRating.Value,
		ClearAgeRating: req.MaxAgeRating.Set && req.MaxAgeRating.Value == nil,
	}
	if req.Role != nil {
		role := media.Role(*req.Role)
		params.Role = &role
	}
	user, err := h.Service.Update(r.Context(), mustActor(r), chi.URLParam(r, "id"), params)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, userView(user))
}

// Delete elimina un usuario.
func (h *Users) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.Service.Delete(r.Context(), mustActor(r), chi.URLParam(r, "id")); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetPreferences devuelve las preferencias propias.
func (h *Users) GetPreferences(w http.ResponseWriter, r *http.Request) {
	p, err := h.Service.Preferences(r.Context(), mustActor(r).UserID)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, dto.Preferences{
		AudioLanguage:    p.AudioLanguage,
		SubtitleLanguage: p.SubtitleLanguage,
		SubtitleMode:     string(p.SubtitleMode),
		MaxBitrateRemote: p.MaxBitrateRemote,
	})
}

// PutPreferences sustituye las preferencias propias.
func (h *Users) PutPreferences(w http.ResponseWriter, r *http.Request) {
	var req dto.Preferences
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	err := h.Service.SetPreferences(r.Context(), mustActor(r).UserID, users.Preferences{
		AudioLanguage:    req.AudioLanguage,
		SubtitleLanguage: req.SubtitleLanguage,
		SubtitleMode:     media.SubtitleMode(req.SubtitleMode),
		MaxBitrateRemote: req.MaxBitrateRemote,
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, req)
}
