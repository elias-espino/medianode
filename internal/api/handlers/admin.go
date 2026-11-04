package handlers

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/admin"
	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/sessions"
	"medianode/internal/transcoding"
)

// Admin atiende /admin/*. Va detrás de RequireRole(admin).
type Admin struct {
	Transcoder *transcoding.Manager
	Sessions   *sessions.Manager
	Service    *admin.Service
	Log        zerolog.Logger
}

// System devuelve el resumen del servidor y del equipo.
func (h *Admin) System(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.Service.System(r.Context()))
}

// Metrics devuelve las métricas de §22.2.
func (h *Admin) Metrics(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.Service.MetricsNow(r.Context()))
}

// ActiveSessions lista las sesiones de reproducción activas de todos los
// usuarios.
func (h *Admin) ActiveSessions(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.Service.SessionList(r.Context()))
}

// Network devuelve la configuración de red y las direcciones del servidor.
func (h *Admin) Network(w http.ResponseWriter, _ *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.Service.Network())
}

// Storage devuelve el uso de disco.
func (h *Admin) Storage(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.Service.StorageNow(r.Context()))
}

// Logs devuelve las últimas líneas del log, filtradas.
func (h *Admin) Logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lines := 200
	if v := q.Get("lines"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > admin.MaxLogLines {
			api.WriteError(w, r, core.ErrValidation("lines debe estar entre 1 y "+strconv.Itoa(admin.MaxLogLines)))
			return
		}
		lines = n
	}
	level := q.Get("level")
	if level != "" && !admin.ValidLevel(level) {
		api.WriteError(w, r, core.ErrValidation("level no es un nivel de log"))
		return
	}
	text := q.Get("q")
	if len(text) > 200 {
		api.WriteError(w, r, core.ErrValidation("q es demasiado largo"))
		return
	}
	entries, err := h.Service.Logs(lines, level, text)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, entries)
}

// Settings devuelve la configuración efectiva, sin secretos.
func (h *Admin) Settings(w http.ResponseWriter, r *http.Request) {
	s, err := h.Service.Settings()
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// SystemCheck ejecuta el diagnóstico de §19.1.
func (h *Admin) SystemCheck(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.Service.SystemCheck(r.Context()))
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
