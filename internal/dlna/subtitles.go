package dlna

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"medianode/internal/database/sqlc"
	"medianode/internal/playback"
	"medianode/internal/protocol"
	"medianode/internal/storage"
)

// isSRT dice si un subtítulo externo es SRT, el único formato que los
// renderers piden por DLNA (Samsung, LG y Sony lo leen; ASS y WebVTT, no).
func isSRT(sub sqlc.Subtitle) bool {
	return sub.Format != nil && protocol.CodecName(*sub.Format) == "srt"
}

// pickSidecar elige el subtítulo SRT externo que se ofrece con un archivo: el
// del idioma preferido, si no el marcado por defecto y, si no, el primero. Un
// televisor no deja elegir pista, así que se anuncia una sola.
func (s *Server) pickSidecar(ctx context.Context, fileID string) *sqlc.Subtitle {
	subs, err := s.db.R.ListFileSubtitles(ctx, fileID)
	if err != nil {
		return nil
	}
	var srt []sqlc.Subtitle
	for _, sub := range subs {
		if isSRT(sub) {
			srt = append(srt, sub)
		}
	}
	for i := range srt {
		if s.opts.Language != "" && playback.SameLanguage(srt[i].Language, s.opts.Language) && srt[i].IsForced == 0 {
			return &srt[i]
		}
	}
	for i := range srt {
		if srt[i].IsDefault != 0 {
			return &srt[i]
		}
	}
	if len(srt) > 0 {
		return &srt[0]
	}
	return nil
}

// serveSubtitle entrega un subtítulo SRT externo de un archivo publicado.
func (s *Server) serveSubtitle(w http.ResponseWriter, r *http.Request) {
	id, ext, _ := strings.Cut(r.PathValue("name"), ".")
	if ext != "srt" || !reDBID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	sub, err := s.db.R.GetExternalSubtitle(r.Context(), id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.log.Error().Err(err).Msg("DLNA: no se pudo leer el subtítulo")
		}
		http.NotFound(w, r)
		return
	}
	// Mismo control de acceso que el vídeo: el archivo ha de estar publicado.
	if _, ok := s.exposed(r.Context(), sub.MediaFileID); !ok || sub.Format == nil || protocol.CodecName(*sub.Format) != "srt" {
		http.NotFound(w, r)
		return
	}
	f, err := storage.OpenMedia(sub.Root, sub.RelativePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() || info.Size() > maxSubtitleBytes {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/srt; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// maxSubtitleBytes: un SRT de una película pesa decenas de KiB.
const maxSubtitleBytes = 4 << 20
