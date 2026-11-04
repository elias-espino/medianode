package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/api/middleware"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/media"
	"medianode/internal/playback"
	"medianode/internal/protocol"
	"medianode/internal/sessions"
)

// Playback atiende /playback.
type Playback struct {
	Sessions *sessions.Manager
	Progress *sessions.Progress
	Playback *playback.Service
	Network  *middleware.Network
	Log      zerolog.Logger
}

// CreateSession decide cómo reproducir y abre la sesión (§16.3).
func (h *Playback) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateSessionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if req.MediaItemID == "" {
		api.WriteError(w, r, core.ErrValidation("Falta media_item_id"))
		return
	}
	var profile protocol.ClientProfile
	declared := map[string]bool{}
	if len(req.ClientProfile) > 0 && string(req.ClientProfile) != "null" {
		var fields map[string]json.RawMessage
		dec := json.NewDecoder(bytes.NewReader(req.ClientProfile))
		dec.DisallowUnknownFields()
		if json.Unmarshal(req.ClientProfile, &fields) != nil || dec.Decode(&profile) != nil {
			api.WriteError(w, r, core.ErrValidation("client_profile no es válido"))
			return
		}
		for k := range fields {
			declared[k] = true
		}
	}
	resp, err := h.Playback.CreateSession(r.Context(), mustActor(r), playback.Request{
		ItemID: req.MediaItemID, FileID: req.MediaFileID, DeviceID: req.DeviceID,
		Profile: profile, Declared: declared, AudioStreamID: req.SelectedAudioStreamID,
		SubtitleID: req.SelectedSubtitleID, MaxBitrate: req.MaxBitrate, MaxResolution: req.MaxResolution,
		PreferredProtocol: req.PreferredProtocol, StartPositionMS: req.StartPositionMS,
		ClientIP: h.Network.ClientIP(r),
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, h.sessionResponse(resp))
}

// ChangeTracks cambia el audio o los subtítulos de una sesión viva y, en HLS,
// devuelve el stream_url de la nueva revisión.
func (h *Playback) ChangeTracks(w http.ResponseWriter, r *http.Request) {
	var req dto.ChangeTracksRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if req.SelectedAudioStreamID == nil && req.SelectedSubtitleID == nil {
		api.WriteError(w, r, core.ErrValidation("Indica selected_audio_stream_id o selected_subtitle_id"))
		return
	}
	resp, err := h.Playback.ChangeTracks(r.Context(), mustActor(r), chi.URLParam(r, "id"), playback.TrackChange{
		AudioStreamID: req.SelectedAudioStreamID, SubtitleID: req.SelectedSubtitleID, PositionMS: req.PositionMS,
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, h.sessionResponse(resp))
}

func (h *Playback) sessionResponse(resp *playback.Response) dto.CreateSessionResponse {
	d, s := resp.Decision, resp.Session
	out := dto.CreateSessionResponse{
		SessionID: s.ID, Status: string(s.Status), MediaFileID: resp.File.ID, PlaybackMode: string(d.Mode),
		Protocol: d.Protocol, Container: resp.Container, VideoAction: string(d.VideoAction),
		AudioAction: string(d.AudioAction), SubtitleMode: string(d.Subtitle),
		SelectedSubtitleID: resp.Tracks.SubtitleID(), StreamURL: resp.StreamURL,
		StartPositionMS: s.StartPositionMS, DurationMS: s.DurationMS,
		IdleTimeoutSeconds: int(h.Sessions.IdleTimeout().Seconds()), Reasons: []dto.PlaybackReason{},
	}
	if v := resp.Tracks.Video; v != nil {
		out.VideoCodec = v.Codec
	}
	if a := resp.Tracks.Audio; a != nil {
		out.AudioCodec, out.SelectedAudioStreamID = a.Codec, &a.ID
	}
	if resp.SubtitleURL != "" {
		out.SubtitleURL = &resp.SubtitleURL
	}
	for _, rr := range d.Reasons {
		out.Reasons = append(out.Reasons, dto.PlaybackReason{Code: rr.Code, Stream: rr.Stream})
	}
	return out
}

// GetSession devuelve una sesión propia, activa o terminada.
func (h *Playback) GetSession(w http.ResponseWriter, r *http.Request) {
	s, err := h.Sessions.Get(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, h.sessionView(s))
}

// Heartbeat actualiza estado y posición. Una sesión terminada responde
// SESSION_EXPIRED.
func (h *Playback) Heartbeat(w http.ResponseWriter, r *http.Request) {
	var req dto.HeartbeatRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if req.PositionMS == nil {
		api.WriteError(w, r, core.ErrValidation("Falta position_ms"))
		return
	}
	s, err := h.Sessions.Heartbeat(r.Context(), mustActor(r), chi.URLParam(r, "id"), sessions.Heartbeat{
		State: sessions.Status(req.State), PositionMS: *req.PositionMS,
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, h.sessionView(s))
}

// StopSession cierra la sesión. Cerrar una ya terminada no es un error.
func (h *Playback) StopSession(w http.ResponseWriter, r *http.Request) {
	if err := h.Sessions.StopSession(r.Context(), mustActor(r), chi.URLParam(r, "id")); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PutProgress guarda un progreso enviado por el cliente.
func (h *Playback) PutProgress(w http.ResponseWriter, r *http.Request) {
	var req dto.ProgressRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if req.MediaItemID == "" || req.PositionMS == nil {
		api.WriteError(w, r, core.ErrValidation("Faltan media_item_id o position_ms"))
		return
	}
	p := sessions.SetParams{
		ItemID: req.MediaItemID, PositionMS: *req.PositionMS, DurationMS: req.DurationMS, Completed: req.Completed,
	}
	if req.ClientTime != nil {
		t, err := time.Parse(time.RFC3339Nano, *req.ClientTime)
		if err != nil {
			api.WriteError(w, r, core.ErrValidation("client_time debe estar en ISO 8601"))
			return
		}
		p.ClientTime = &t
	}
	cur, err := h.Progress.Set(r.Context(), mustActor(r), p)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, dto.Progress{
		MediaItemID: cur.MediaItemID, PositionMS: cur.PositionMs, DurationMS: cur.DurationMs,
		Completed: cur.Completed != 0, UpdatedAt: cur.UpdatedAt,
	})
}

// ContinueWatching devuelve lo empezado y sin terminar.
func (h *Playback) ContinueWatching(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit")
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	rows, err := h.Progress.ContinueWatching(r.Context(), mustActor(r), limit)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.ContinueItem, len(rows))
	for i, c := range rows {
		out[i] = dto.ContinueItem{
			ID: c.ID, Type: c.Type, Title: c.Title, Year: c.Year, SeasonNumber: c.SeasonNumber,
			EpisodeNumber: c.EpisodeNumber, SeriesID: c.SeriesID, SeriesTitle: c.SeriesTitle,
			PositionMS: c.PositionMs, DurationMS: c.DurationMs, UpdatedAt: c.UpdatedAt,
		}
		if c.HasImage != 0 {
			t := media.ImagePoster
			if c.Type == string(media.ItemEpisode) {
				t = media.ImageThumbnail
			}
			out[i].ImageURL = imageURL(c.ID, t)
		}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// History devuelve el historial propio.
func (h *Playback) History(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit")
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	rows, next, err := h.Progress.History(r.Context(), mustActor(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := dto.HistoryPage{Items: make([]dto.HistoryEntry, len(rows))}
	for i, e := range rows {
		out.Items[i] = dto.HistoryEntry{
			ID: e.ID, MediaItemID: e.MediaItemID, DeviceID: e.DeviceID, Title: e.Title,
			PositionMS: e.PositionMs, Completed: e.Completed != 0, WatchedAt: e.WatchedAt,
		}
	}
	if next != "" {
		out.NextCursor = &next
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (h *Playback) sessionView(s *sessions.Session) dto.PlaybackSession {
	out := dto.PlaybackSession{
		ID: s.ID, Status: string(s.Status), MediaItemID: s.ItemID, MediaFileID: s.FileID,
		PlaybackMode: s.Mode, StartPositionMS: s.StartPositionMS, PositionMS: s.PositionMS,
		DurationMS: s.DurationMS, StartedAt: database.FormatTime(s.StartedAt),
		IdleTimeoutSeconds: int(h.Sessions.IdleTimeout().Seconds()),
	}
	if !s.LastActivity.IsZero() {
		t := database.FormatTime(s.LastActivity)
		out.LastActivityAt = &t
	}
	if s.EndedAt != nil {
		t := database.FormatTime(*s.EndedAt)
		out.EndedAt = &t
	}
	return out
}
