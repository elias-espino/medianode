package handlers

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
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
	"medianode/internal/transcoding"
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
	// Transcoder es nil sin FFmpeg: solo hay Direct Play.
	Transcoder *transcoding.Manager
	Log        zerolog.Logger
}

const playlistType = "application/vnd.apple.mpegurl"

// hlsSession verifica la sesión y que tenga trabajo de FFmpeg. Una sesión de
// Direct Play no tiene playlists: 404.
func (h *Stream) hlsSession(w http.ResponseWriter, r *http.Request) *sessions.Session {
	s := h.session(w, r)
	if s == nil {
		return nil
	}
	if h.Transcoder == nil {
		api.WriteError(w, r, core.ErrNotFound())
		return nil
	}
	return s
}

// Master entrega el master.m3u8, que apunta a la playlist de medios.
func (h *Stream) Master(w http.ResponseWriter, r *http.Request) {
	s := h.hlsSession(w, r)
	if s == nil {
		return
	}
	data, err := h.Transcoder.Master(s.ID)
	h.writePlaylist(w, r, data, err)
}

// Media entrega la playlist de medios: la sintética VOD, que permite saltar
// a cualquier punto sin esperar a FFmpeg, o la EVENT si no hay índice.
func (h *Stream) Media(w http.ResponseWriter, r *http.Request) {
	s := h.hlsSession(w, r)
	if s == nil {
		return
	}
	data, err := h.Transcoder.Media(s.ID)
	h.writePlaylist(w, r, data, err)
}

func (h *Stream) writePlaylist(w http.ResponseWriter, r *http.Request, data []byte, err error) {
	if err != nil {
		h.failHLS(w, r, err)
		return
	}
	w.Header().Set("Content-Type", playlistType)
	// Una playlist EVENT crece: ningún proxy debe guardarla.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// Media entrega init.mp4 y los segmentos. El nombre se valida contra la
// lista de lo que el gestor produce; nada más llega al disco. Si el segmento
// aún no existe, espera a FFmpeg o lo reinicia en ese punto.
func (h *Stream) Segment(w http.ResponseWriter, r *http.Request) {
	s := h.hlsSession(w, r)
	if s == nil {
		return
	}
	name := chi.URLParam(r, "name")
	path, err := h.Transcoder.File(r.Context(), s.ID, name)
	if err != nil {
		h.failHLS(w, r, err)
		return
	}
	// La ruta la construye transcoding.File, y solo a partir de init.mp4 o de un
	// segmento que cumple reSegment dentro del directorio de la sesión.
	f, err := os.Open(path) //nolint:gosec // ruta validada por transcoding.File
	if err != nil {
		// Un segmento borrado por la cuota entre la espera y la apertura.
		api.WriteError(w, r, core.ErrTranscodeFailed())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	switch {
	case strings.HasSuffix(name, ".ts"):
		w.Header().Set("Content-Type", "video/mp2t")
	default:
		w.Header().Set("Content-Type", "video/mp4")
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// failHLS traduce los errores del gestor. Un segmento que no estuvo a tiempo
// responde 503 con Retry-After, no 500: el cliente puede reintentar.
func (h *Stream) failHLS(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, transcoding.ErrNoStream), errors.Is(err, transcoding.ErrBadName):
		api.WriteError(w, r, core.ErrNotFound())
	case errors.Is(err, transcoding.ErrSegmentTimeout), errors.Is(err, context.DeadlineExceeded):
		api.WriteError(w, r, core.ErrTranscodeFailed())
	case errors.Is(err, context.Canceled):
		// El cliente se fue: no hay a quién responder.
	default:
		h.Log.Error().Err(err).Str("session_id", chi.URLParam(r, "id")).Msg("error sirviendo HLS")
		api.WriteError(w, r, core.ErrTranscodeFailed())
	}
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
	// Una sesión que debía remuxearse o transcodificarse no puede pedir el
	// original: se saltaría el límite de bitrate o de resolución.
	if s.Mode != string(playback.DirectPlay) {
		api.WriteError(w, r, core.ErrNotFound())
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
	if ext != format && (ext != "vtt" || format != "srt") {
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
