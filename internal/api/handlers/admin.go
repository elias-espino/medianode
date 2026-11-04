package handlers

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/sessions"
	"medianode/internal/transcoding"
)

// Admin atiende /admin/transcodes. Va detrás de RequireRole(admin).
type Admin struct {
	Transcoder *transcoding.Manager
	Sessions   *sessions.Manager
	Log        zerolog.Logger
}

// Transcodes lista los trabajos de FFmpeg activos y el uso de cupos.
func (h *Admin) Transcodes(w http.ResponseWriter, r *http.Request) {
	out := dto.Transcodes{Jobs: []dto.TranscodeJob{}, Capacity: map[string]dto.Capacity{}}
	if h.Transcoder == nil {
		api.WriteJSON(w, http.StatusOK, out)
		return
	}
	jobs := h.Transcoder.Jobs()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.Before(jobs[j].StartedAt) })
	for _, j := range jobs {
		out.Jobs = append(out.Jobs, dto.TranscodeJob{
			ID: j.ID, SessionID: j.SessionID, Kind: string(j.Kind), Encoder: optional(j.Encoder),
			StartSegment: j.StartSegment, Segment: j.Segment, PositionMS: j.PositionUS / 1000,
			StartedAt: database.FormatTime(j.StartedAt),
		})
	}
	used, limits := h.Transcoder.Admission().Usage()
	for k, limit := range limits {
		out.Capacity[string(k)] = dto.Capacity{Used: used[k], Limit: limit}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// CancelTranscode cierra la sesión del trabajo, que para FFmpeg, borra su
// directorio y libera el cupo. Cancelar solo el proceso lo reiniciaría en la
// siguiente petición de segmento.
func (h *Admin) CancelTranscode(w http.ResponseWriter, r *http.Request) {
	if h.Transcoder == nil {
		api.WriteError(w, r, core.ErrNotFound())
		return
	}
	sessionID, ok := h.Transcoder.SessionOfJob(chi.URLParam(r, "id"))
	if !ok {
		api.WriteError(w, r, core.ErrNotFound())
		return
	}
	err := h.Sessions.Terminate(r.Context(), func(s sessions.Session) bool { return s.ID == sessionID })
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
