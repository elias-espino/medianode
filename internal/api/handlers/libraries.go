package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/database/sqlc"
	"medianode/internal/libraries"
	"medianode/internal/media"
)

// Libraries atiende /libraries y los permisos por biblioteca.
type Libraries struct {
	Service *libraries.Service
	Log     zerolog.Logger
	// PathsChanged avisa al vigilante de que las rutas cambiaron.
	PathsChanged func()
}

// List devuelve las bibliotecas visibles.
func (h *Libraries) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Service.List(r.Context(), mustActor(r))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.Library, len(rows))
	for i := range rows {
		out[i] = libraryView(&rows[i])
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Create crea una biblioteca.
func (h *Libraries) Create(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateLibraryRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	lib, err := h.Service.Create(r.Context(), mustActor(r), libraries.CreateParams{
		Name: req.Name, Type: media.LibraryType(req.Type), DLNAEnabled: req.DLNAEnabled, Paths: req.Paths,
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.changed()
	api.WriteJSON(w, http.StatusCreated, libraryView(lib))
}

// Get devuelve una biblioteca.
func (h *Libraries) Get(w http.ResponseWriter, r *http.Request) {
	lib, err := h.Service.Get(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, libraryView(lib))
}

// Update modifica una biblioteca.
func (h *Libraries) Update(w http.ResponseWriter, r *http.Request) {
	var req dto.UpdateLibraryRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	lib, err := h.Service.Update(r.Context(), mustActor(r), chi.URLParam(r, "id"), libraries.UpdateParams{
		Name: req.Name, Enabled: req.Enabled, DLNAEnabled: req.DLNAEnabled,
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.changed()
	api.WriteJSON(w, http.StatusOK, libraryView(lib))
}

// Delete elimina una biblioteca.
func (h *Libraries) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.Service.Delete(r.Context(), mustActor(r), chi.URLParam(r, "id")); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.changed()
	w.WriteHeader(http.StatusNoContent)
}

// AddPath añade una raíz.
func (h *Libraries) AddPath(w http.ResponseWriter, r *http.Request) {
	var req dto.AddPathRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	lp, err := h.Service.AddPath(r.Context(), mustActor(r), chi.URLParam(r, "id"), req.Path)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.changed()
	api.WriteJSON(w, http.StatusCreated, pathView(lp))
}

// RemovePath quita una raíz.
func (h *Libraries) RemovePath(w http.ResponseWriter, r *http.Request) {
	err := h.Service.RemovePath(r.Context(), mustActor(r), chi.URLParam(r, "id"), chi.URLParam(r, "pathId"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.changed()
	w.WriteHeader(http.StatusNoContent)
}

// StartScan inicia un escaneo. Responde 202 también si ya había uno en curso.
func (h *Libraries) StartScan(w http.ResponseWriter, r *http.Request) {
	job, err := h.Service.StartScan(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, scanView(job))
}

// ScanStatus devuelve el último escaneo, o 204 si nunca se escaneó.
func (h *Libraries) ScanStatus(w http.ResponseWriter, r *http.Request) {
	job, err := h.Service.ScanStatus(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	api.WriteJSON(w, http.StatusOK, scanView(job))
}

// GetPermissions devuelve los permisos de un usuario.
func (h *Libraries) GetPermissions(w http.ResponseWriter, r *http.Request) {
	grants, err := h.Service.Permissions(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, permissionsView(grants))
}

// PutPermissions sustituye los permisos de un usuario.
func (h *Libraries) PutPermissions(w http.ResponseWriter, r *http.Request) {
	var req dto.PermissionsRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	grants := make([]libraries.Grant, len(req.Libraries))
	for i, g := range req.Libraries {
		grants[i].LibraryID = g.LibraryID
		for _, p := range g.Permissions {
			grants[i].Permissions = append(grants[i].Permissions, media.Permission(p))
		}
	}
	out, err := h.Service.SetPermissions(r.Context(), mustActor(r), chi.URLParam(r, "id"), grants)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, permissionsView(out))
}

func (h *Libraries) changed() {
	if h.PathsChanged != nil {
		h.PathsChanged()
	}
}

func libraryView(l *libraries.Library) dto.Library {
	out := dto.Library{
		ID: l.ID, Name: l.Name, Type: l.Type, Enabled: l.Enabled != 0, DLNAEnabled: l.DlnaEnabled != 0,
		CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
	for i := range l.Paths {
		out.Paths = append(out.Paths, pathView(&l.Paths[i]))
	}
	return out
}

func pathView(p *sqlc.LibraryPath) dto.LibraryPath {
	return dto.LibraryPath{ID: p.ID, Path: p.Path, IsAvailable: p.IsAvailable != 0, LastScanAt: p.LastScanAt}
}

func scanView(j *sqlc.ScanJob) dto.ScanJob {
	return dto.ScanJob{
		ID: j.ID, LibraryID: j.LibraryID, Trigger: j.Trigger, Status: j.Status,
		StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, FilesDiscovered: j.FilesDiscovered,
		FilesProcessed: j.FilesProcessed, ErrorMessage: j.ErrorMessage,
	}
}

func permissionsView(grants []libraries.Grant) dto.Permissions {
	out := dto.Permissions{Libraries: []dto.LibraryGrant{}}
	for _, g := range grants {
		dg := dto.LibraryGrant{LibraryID: g.LibraryID, Permissions: []string{}}
		for _, p := range g.Permissions {
			dg.Permissions = append(dg.Permissions, string(p))
		}
		out.Libraries = append(out.Libraries, dg)
	}
	return out
}
