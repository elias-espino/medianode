package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database/sqlc"
	"medianode/internal/devices"
)

const scopeAll = "all"

// Devices atiende /devices.
type Devices struct {
	Service *devices.Service
	Log     zerolog.Logger
}

// List devuelve los dispositivos propios o, con ?scope=all, todos.
func (h *Devices) List(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope != "" && scope != scopeAll {
		api.WriteError(w, r, core.ErrValidation("El valor de scope no es válido"))
		return
	}
	actor := mustActor(r)
	rows, err := h.Service.List(r.Context(), actor, scope == scopeAll)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.DeviceView, len(rows))
	for i := range rows {
		out[i] = deviceView(&rows[i], actor)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Get devuelve un dispositivo.
func (h *Devices) Get(w http.ResponseWriter, r *http.Request) {
	actor := mustActor(r)
	dev, err := h.Service.Get(r.Context(), actor, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, deviceView(dev, actor))
}

// Update cambia el nombre de un dispositivo.
func (h *Devices) Update(w http.ResponseWriter, r *http.Request) {
	var req dto.RenameDeviceRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	actor := mustActor(r)
	dev, err := h.Service.Rename(r.Context(), actor, chi.URLParam(r, "id"), req.Name)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, deviceView(dev, actor))
}

// Revoke retira el acceso de un dispositivo.
func (h *Devices) Revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.Service.Revoke(r.Context(), mustActor(r), chi.URLParam(r, "id")); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func deviceView(d *sqlc.Device, actor authorization.Actor) dto.DeviceView {
	return dto.DeviceView{
		ID:            d.ID,
		UserID:        d.UserID,
		Name:          d.DeviceName,
		Platform:      d.Platform,
		ClientVersion: d.ClientVersion,
		CreatedAt:     d.CreatedAt,
		LastSeenAt:    d.LastSeenAt,
		RevokedAt:     d.RevokedAt,
		Current:       d.ID == actor.DeviceID,
	}
}
