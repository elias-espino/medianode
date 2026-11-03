package handlers

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/playback"
	"medianode/internal/protocol"
	"medianode/internal/sessions"
	"medianode/internal/storage"
	"medianode/internal/subtitles"
)

// touchEvery limita las llamadas a Touch mientras se lee el archivo.
const touchEvery = 5 * time.Second

// Tipos por contenedor. El tipo nunca sale de lo que pida el cliente.
var mimeTypes = map[string]string{
	"mp4":  "video/mp4",
	"m4v":  "video/mp4",
	"mov":  "video/quicktime",
	"mkv":  "video/x-matroska",
	"webm": "video/webm",
	"ts":   "video/mp2t",
	"avi":  "video/x-msvideo",
}

// Stream sirve las URL opacas de reproducción (§16.4). Van fuera de /api/v1:
// el cliente no las construye, y la credencial es el token de la URL.
type Stream struct {
	DB       *database.DB
	Sessions *sessions.Manager
	Playback *playback.Service
	Log      zerolog.Logger
}

// session verifica el token y que el dueño siga autorizado.
func (h *Stream) session(w http.ResponseWriter, r *http.Request) *sessions.Session {
	s, err := h.Sessions.VerifyToken(chi.URLParam(r, "id"), chi.URLParam(r, "token"))
	if err == nil {
		err = h.Playback.AuthorizeStream(r.Context(), s)
	}
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return nil
	}
	h.Sessions.Touch(s.ID)
	return s
}

// File entrega el archivo original con GET, HEAD y Range. Si el cliente se
// desconecta, la escritura falla y ServeContent termina.
func (h *Stream) File(w http.ResponseWriter, r *http.Request) {
	s := h.session(w, r)
	if s == nil {
		return
	}
	loc, err := h.DB.R.GetFileLocation(r.Context(), s.FileID)
	if errors.Is(err, sql.ErrNoRows) {
		api.WriteError(w, r, core.ErrFileUnavailable())
		return
	}
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if loc.MissingSince != nil {
		api.WriteError(w, r, core.ErrFileUnavailable())
		return
	}
	f, err := storage.OpenMedia(loc.Root, loc.RelativePath)
	if err != nil {
		// El mensaje no lleva la ruta: va a un cliente.
		h.Log.Warn().Err(err).Str("file_id", s.FileID).Msg("no se pudo abrir el archivo")
		api.WriteError(w, r, core.ErrFileUnavailable())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	ct := mimeTypes[deref(loc.Container)]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", info.ModTime(), &touchReader{ReadSeeker: f, touch: func() { h.Sessions.Touch(s.ID) }})
}

// Subtitle entrega un subtítulo externo del archivo de la sesión: tal cual, o
// convertido a WebVTT si se pide .vtt de un SRT.
func (h *Stream) Subtitle(w http.ResponseWriter, r *http.Request) {
	s := h.session(w, r)
	if s == nil {
		return
	}
	name := chi.URLParam(r, "name")
	id, ext := strings.TrimSuffix(name, path.Ext(name)), strings.TrimPrefix(path.Ext(name), ".")
	sub, err := h.DB.R.GetExternalSubtitle(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && sub.MediaFileID != s.FileID) {
		api.WriteError(w, r, core.ErrNotFound()) // Un subtítulo de otro archivo no existe para esta sesión.
		return
	}
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	format := protocol.CodecName(deref(sub.Format))
	if ext != format && !(ext == "vtt" && format == "srt") {
		api.WriteError(w, r, core.ErrNotFound())
		return
	}
	f, err := storage.OpenMedia(sub.Root, sub.RelativePath)
	if err != nil {
		h.Log.Warn().Err(err).Str("subtitle_id", id).Msg("no se pudo abrir el subtítulo")
		api.WriteError(w, r, core.ErrFileUnavailable())
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, subtitles.MaxBytes+1))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	if len(data) > subtitles.MaxBytes {
		api.WriteError(w, r, core.ErrFileUnavailable())
		return
	}
	switch {
	case ext == "vtt" && format == "srt":
		data = subtitles.SRTToWebVTT(data)
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	case format == "vtt":
		data = subtitles.ToUTF8(data)
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	default:
		data = subtitles.ToUTF8(data)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// touchReader registra actividad mientras se lee: un reproductor en pausa con
// el búfer lleno no lee, pero sigue enviando heartbeats.
type touchReader struct {
	io.ReadSeeker
	touch func()
	last  time.Time
}

func (t *touchReader) Read(p []byte) (int, error) {
	if now := time.Now(); now.Sub(t.last) >= touchEvery {
		t.last = now
		t.touch()
	}
	return t.ReadSeeker.Read(p)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
