// Package handlers traduce HTTP a llamadas de los módulos, sin lógica de negocio.
package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/authorization"
	"medianode/internal/database"
	"medianode/internal/ffmpeg"
	"medianode/internal/media"
	"medianode/internal/metadata"
	"medianode/internal/readmodel"
	"medianode/internal/transcoding"
)

// ProtocolVersion es la versión del contrato /api/v1.
const ProtocolVersion = 1

// Server atiende los endpoints de información del servidor.
type Server struct {
	Name             string
	Version          string
	Log              zerolog.Logger
	SetupRequired    func(context.Context) (bool, error)
	Started          time.Time
	FFprobeAvailable bool
	Catalog          *readmodel.Catalog
	Enricher         *metadata.Enricher
	Transcoder       *transcoding.Manager
	FFmpeg           *ffmpeg.Capabilities
}

// Health responde a la supervisión. No requiere autenticación.
func (s *Server) Health(w http.ResponseWriter, _ *http.Request) {
	api.WriteJSON(w, http.StatusOK, dto.Health{Status: "ok"})
}

// Info devuelve la información pública. SetupRequired indica al cliente que debe
// mostrar la creación del Owner; no incluye datos sensibles.
func (s *Server) Info(w http.ResponseWriter, r *http.Request) {
	setup, err := s.SetupRequired(r.Context())
	if err != nil {
		api.WriteFailure(w, r, s.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, dto.ServerInfo{
		Name:            s.Name,
		Version:         s.Version,
		ProtocolVersion: ProtocolVersion,
		SetupRequired:   setup,
	})
}

// ServerVersion devuelve la versión instalada.
func (s *Server) ServerVersion(w http.ResponseWriter, _ *http.Request) {
	api.WriteJSON(w, http.StatusOK, dto.ServerVersion{
		Version:         s.Version,
		ProtocolVersion: ProtocolVersion,
	})
}

// Status devuelve el estado. Requiere sesión; el detalle es para
// administradores.
func (s *Server) Status(w http.ResponseWriter, r *http.Request) {
	out := dto.ServerStatus{
		Status: "ok", Version: s.Version, UptimeSeconds: int64(time.Since(s.Started).Seconds()),
	}
	if authorization.IsAdmin(mustActor(r)) {
		counts, err := s.Catalog.Counts(r.Context())
		if err != nil {
			api.WriteFailure(w, r, s.Log, err)
			return
		}
		st := s.Enricher.Status()
		md := dto.MetadataStatus{Configured: st.Provider != "", Pending: counts.PendingMetadata}
		if st.Provider != "" {
			md.Provider = &st.Provider
		}
		if !st.PausedUntil.IsZero() {
			t := database.FormatTime(st.PausedUntil)
			md.PausedUntil = &t
		}
		if st.LastError != "" {
			md.LastError = &st.LastError
		}
		out.Admin = &dto.AdminStatus{
			FFprobeAvailable: s.FFprobeAvailable, Libraries: counts.Libraries,
			ScansRunning: counts.ScansRunning, Metadata: md,
		}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Capabilities describe qué ofrece el servidor. Lo que llega en fases
// posteriores figura como false hasta entonces.
func (s *Server) Capabilities(w http.ResponseWriter, _ *http.Request) {
	out := dto.ServerCapabilities{
		ProtocolVersion: ProtocolVersion,
		ImageTypes: []string{string(media.ImagePoster), string(media.ImageBackdrop),
			string(media.ImageThumbnail), string(media.ImageLogo)},
		Search:     true,
		DirectPlay: true,
	}
	if s.Transcoder != nil {
		out.Transcoding, out.HLS = true, true
		enc := string(s.Transcoder.Encoder())
		out.VideoEncoder = &enc
	}
	if p := s.Enricher.Status().Provider; p != "" {
		out.MetadataProvider = &p
	}
	api.WriteJSON(w, http.StatusOK, out)
}
