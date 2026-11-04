package playback

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"medianode/internal/core"
	"medianode/internal/database/sqlc"
	"medianode/internal/ffmpeg"
	"medianode/internal/media"
	"medianode/internal/protocol"
	"medianode/internal/storage"
	"medianode/internal/subtitles"
	"medianode/internal/transcoding"
)

// Features es lo que el FFmpeg instalado puede hacer además de copiar y
// recodificar: grabar subtítulos y pasar HDR a SDR (§10.8, §11).
type Features struct {
	BurnInText  bool
	BurnInImage bool
	ToneMapping bool
}

// FeaturesOf toma las capacidades de ffmpeg.Probe.
func FeaturesOf(c *ffmpeg.Capabilities) Features {
	if c == nil {
		return Features{}
	}
	return Features{BurnInText: c.BurnInText, BurnInImage: c.BurnInImage, ToneMapping: c.ToneMapping}
}

// subtitleWait es lo máximo que una petición espera a que termine la
// extracción de un subtítulo. Si no llega, el cliente puede reintentar: la
// extracción sigue en segundo plano.
const subtitleWait = 25 * time.Second

// textFormats son los formatos de texto que FFmpeg convierte a WebVTT o ASS.
var textFormats = map[string]bool{
	"srt": true, "ass": true, "ssa": true, "vtt": true, "mov_text": true, "text": true, "subviewer": true, "microdvd": true, "sami": true,
}

// IsTextSubtitle indica si el formato (nombre de FFprobe o extensión) es de
// texto y, por tanto, se puede convertir.
func IsTextSubtitle(format string) bool { return textFormats[protocol.CodecName(format)] }

// subtitleTrack es una pista de subtítulos del archivo de una sesión, lista
// para convertir.
type subtitleTrack struct {
	track    subtitles.Track
	format   string // Normalizado: srt, ass, pgs…
	external bool
	language string
	forced   bool
	name     string
}

// trackFor busca el subtítulo id entre las pistas internas del archivo y sus
// subtítulos externos. Uno de otro archivo no existe para esta sesión.
func (s *Service) trackFor(ctx context.Context, fileID, id string) (subtitleTrack, error) {
	streams, err := s.db.R.ListFileStreams(ctx, fileID)
	if err != nil {
		return subtitleTrack{}, fmt.Errorf("leer pistas: %w", err)
	}
	for _, st := range streams {
		if st.ID != id || media.StreamType(st.StreamType) != media.StreamSubtitle {
			continue
		}
		loc, err := s.db.R.GetFileLocation(ctx, fileID)
		if err != nil {
			return subtitleTrack{}, fmt.Errorf("leer ubicación: %w", err)
		}
		file, err := s.db.R.GetMediaFile(ctx, fileID)
		if err != nil {
			return subtitleTrack{}, fmt.Errorf("leer archivo: %w", err)
		}
		in := filepath.Join(loc.Root, filepath.FromSlash(loc.RelativePath))
		return subtitleTrack{
			track: subtitles.Track{FileID: fileID, TrackID: id, Input: in, Stream: int(st.StreamIndex),
				Size: deref64(file.FileSize), ModifiedAt: deref(file.ModifiedAt)},
			format: protocol.CodecName(deref(st.Codec)), language: deref(st.Language), forced: st.IsForced != 0,
			name: trackName(deref(st.Language), st.IsForced != 0),
		}, nil
	}
	ex, err := s.db.R.GetExternalSubtitle(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ex.MediaFileID != fileID) {
		return subtitleTrack{}, core.ErrNotFound()
	}
	if err != nil {
		return subtitleTrack{}, fmt.Errorf("leer subtítulo: %w", err)
	}
	inside, err := storage.IsWithinRoot(ex.Root, filepath.Join(ex.Root, filepath.FromSlash(ex.RelativePath)))
	if err != nil || !inside {
		return subtitleTrack{}, core.ErrFileUnavailable()
	}
	in := filepath.Join(ex.Root, filepath.FromSlash(ex.RelativePath))
	info, err := os.Stat(in)
	if err != nil {
		return subtitleTrack{}, core.ErrFileUnavailable()
	}
	lang, forced := "", false
	if subs, err := s.db.R.ListFileSubtitles(ctx, fileID); err == nil {
		for _, sub := range subs {
			if sub.ID == id {
				lang, forced = sub.Language, sub.IsForced != 0
			}
		}
	}
	return subtitleTrack{
		track: subtitles.Track{FileID: fileID, TrackID: id, Input: in, Stream: 0,
			Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano)},
		format: protocol.CodecName(deref(ex.Format)), external: true, language: lang, forced: forced, name: trackName(lang, forced),
	}, nil
}

func trackName(language string, forced bool) string {
	name := "Subtítulos"
	if language != "" && language != "und" {
		name += " (" + language + ")"
	}
	if forced {
		name += " forzados"
	}
	return name
}

// SubtitleVTT devuelve la ruta de la pista id del archivo, convertida a
// WebVTT. Solo pistas de texto: las de imagen no se convierten sin OCR.
func (s *Service) SubtitleVTT(ctx context.Context, fileID, id string) (string, error) {
	t, err := s.trackFor(ctx, fileID, id)
	if err != nil {
		return "", err
	}
	if !textFormats[t.format] {
		return "", core.ErrNotFound()
	}
	if s.Subtitles == nil {
		return "", subtitles.ErrNoExtractor
	}
	ctx, cancel := context.WithTimeout(ctx, subtitleWait)
	defer cancel()
	return s.Subtitles.Get(ctx, t.track, ffmpeg.FormatWebVTT)
}

// hdrTransfer devuelve la transferencia que hay que tonemapear, o "".
func hdrTransfer(v *sqlc.MediaStream) string {
	if v == nil || v.HdrFormat == nil {
		return ""
	}
	if protocol.HDRName(*v.HdrFormat) == "hlg" {
		return ffmpeg.TransferHLG
	}
	// HDR10, HDR10+ y la capa base de Dolby Vision (perfiles con base HDR10).
	return ffmpeg.TransferPQ
}

func needsToneMap(d Decision) bool {
	for _, r := range d.Reasons {
		if r.Code == ReasonHDR {
			return true
		}
	}
	return false
}

// burnPlan prepara lo que FFmpeg necesita para grabar el subtítulo elegido:
// una pista de imagen se superpone desde el propio archivo; una de texto se
// convierte antes a ASS, que conserva los estilos.
func (s *Service) burnPlan(ctx context.Context, fileID string, o option, p *transcoding.Plan) error {
	if o.decision.Subtitle != SubBurnIn {
		return nil
	}
	id := o.tracks.SubtitleID()
	if id == nil {
		return nil
	}
	t, err := s.trackFor(ctx, fileID, *id)
	if err != nil {
		return err
	}
	if protocol.ImageSubtitle(t.format) {
		if t.external {
			return core.ErrValidation("Los subtítulos de imagen externos no se pueden grabar")
		}
		p.Burn, p.BurnStream = ffmpeg.BurnImage, t.track.Stream
		return nil
	}
	if s.Subtitles == nil {
		return subtitles.ErrNoExtractor
	}
	ctx, cancel := context.WithTimeout(ctx, subtitleWait)
	defer cancel()
	path, err := s.Subtitles.Get(ctx, t.track, ffmpeg.FormatASS)
	if err != nil {
		return err
	}
	p.Burn, p.BurnFile = ffmpeg.BurnText, path
	return nil
}

// hlsSubtitle describe la pista que viaja dentro de HLS, si la hay.
func (s *Service) hlsSubtitle(ctx context.Context, fileID string, o option) *transcoding.SubtitleRef {
	if o.decision.Subtitle != SubHLS {
		return nil
	}
	id := o.tracks.SubtitleID()
	if id == nil {
		return nil
	}
	t, err := s.trackFor(ctx, fileID, *id)
	if err != nil {
		return nil
	}
	lang := t.language
	if lang == "und" {
		lang = ""
	}
	return &transcoding.SubtitleRef{ID: *id, Name: t.name, Language: lang, Forced: t.forced}
}

// subtitleURL es la URL de la pista elegida para los clientes que la piden
// aparte, y arranca su extracción para que esté lista cuando el reproductor
// la pida. Vacía si el subtítulo va grabado, dentro de HLS o dentro del archivo.
func (s *Service) subtitleURL(ctx context.Context, base string, o option) string {
	d := o.decision
	if d.Subtitle != SubSidecar && d.Subtitle != SubConvert {
		return ""
	}
	id := o.tracks.SubtitleID()
	if id == nil {
		return ""
	}
	if ex := o.tracks.External; ex != nil {
		format := protocol.CodecName(deref(ex.Format))
		switch {
		case d.Subtitle == SubSidecar:
			return base + "/subtitles/" + ex.ID + "." + format
		case format == "srt" || format == "vtt":
			return base + "/subtitles/" + ex.ID + ".vtt"
		}
	}
	// Interno, o externo que hay que convertir.
	if s.Subtitles != nil {
		if t, err := s.trackFor(ctx, o.file.ID, *id); err == nil && textFormats[t.format] {
			s.Subtitles.Prefetch(t.track, ffmpeg.FormatWebVTT)
		}
	}
	return base + "/subtitles/" + *id + ".vtt"
}
