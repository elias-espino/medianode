package dlna

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/playback"
	"medianode/internal/sessions"
	"medianode/internal/storage"
	"medianode/internal/transcoding"
)

// maxFlows acota el mapa de pares (cliente, archivo) con sesión abierta.
const maxFlows = 1024

// exposed devuelve el elemento al que pertenece un archivo si está en una
// biblioteca publicada por DLNA. Es la única comprobación de acceso: sin ella,
// ningún archivo se entrega.
func (s *Server) exposed(ctx context.Context, fileID string) (sqlc.GetDLNAFileItemRow, bool) {
	if !reDBID.MatchString(fileID) {
		return sqlc.GetDLNAFileItemRow{}, false
	}
	row, err := s.db.R.GetDLNAFileItem(ctx, fileID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.log.Error().Err(err).Msg("DLNA: no se pudo comprobar el archivo")
		}
		return sqlc.GetDLNAFileItemRow{}, false
	}
	return row, true
}

// session devuelve la sesión del par (cliente, archivo), creándola si no existe
// o ya terminó. Un renderer pide el mismo archivo con varias conexiones y rangos;
// todas son una sola reproducción.
func (s *Server) session(ctx context.Context, r *http.Request, itemID, fileID, mode string) (string, error) {
	ip := s.net.ClientIP(r)
	key := ip + "|" + fileID
	s.mu.Lock()
	id := s.flows[key]
	s.mu.Unlock()
	if id != "" && s.sessions.Alive(id) {
		s.sessions.Touch(id)
		return id, nil
	}
	sess, err := s.sessions.Create(ctx, sessions.CreateParams{
		ClientKind: sessions.KindDLNA, ItemID: itemID, FileID: fileID, Mode: mode, ClientIP: ip,
	})
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if len(s.flows) >= maxFlows {
		for k, v := range s.flows {
			if !s.sessions.Alive(v) {
				delete(s.flows, k)
			}
		}
	}
	s.flows[key] = sess.ID
	s.mu.Unlock()
	return sess.ID, nil
}

// keepAlive renueva la actividad de la sesión mientras la respuesta sigue
// abierta, aunque el televisor no lea (pausa).
func (s *Server) keepAlive(ctx context.Context, id string) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(touchEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.sessions.Touch(id)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(done) }
}

// touchReader renueva la actividad de la sesión con cada lectura.
type touchReader struct {
	io.ReadSeeker
	touch func()
}

func (t *touchReader) Read(p []byte) (int, error) {
	t.touch()
	return t.ReadSeeker.Read(p)
}

// dlnaHeaders pone las cabeceras de DLNA que los televisores esperan.
func dlnaHeaders(w http.ResponseWriter, r *http.Request, contentFeatures string) {
	h := w.Header()
	mode := r.Header.Get("transferMode.dlna.org")
	if mode != "Streaming" && mode != "Interactive" && mode != "Background" {
		mode = "Streaming"
	}
	h.Set("transferMode.dlna.org", mode)
	h.Set("contentFeatures.dlna.org", contentFeatures)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
}

// serveDirect entrega el archivo original, con rangos.
func (s *Server) serveDirect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fileID := r.PathValue("file")
	fi, ok := s.exposed(ctx, fileID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	loc, err := s.db.R.GetFileLocation(ctx, fileID)
	if err != nil || loc.MissingSince != nil {
		http.NotFound(w, r)
		return
	}
	f, err := storage.OpenMedia(loc.Root, loc.RelativePath)
	if err != nil {
		s.log.Warn().Err(err).Str("file_id", fileID).Msg("DLNA: no se pudo abrir el archivo")
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	container := ""
	if loc.Container != nil {
		container = *loc.Container
	}
	w.Header().Set("Content-Type", playback.DLNAMIME(container))
	w.Header().Set("Accept-Ranges", "bytes")
	dlnaHeaders(w, r, strings.TrimPrefix(protocolInfo("x", "01", false), "http-get:*:x:"))
	var rs io.ReadSeeker = f
	// Un HEAD no es una reproducción: los televisores lo usan para sondear.
	if r.Method == http.MethodGet {
		id, err := s.session(ctx, r, fi.ItemID, fileID, string(playback.DirectPlay))
		if err != nil {
			s.log.Error().Err(err).Msg("DLNA: no se pudo abrir la sesión")
			http.Error(w, "error interno", http.StatusInternalServerError)
			return
		}
		_ = s.sessions.Activate(ctx, id)
		defer s.keepAlive(ctx, id)()
		rs = &touchReader{ReadSeeker: f, touch: func() { s.sessions.Touch(id) }}
	}
	http.ServeContent(w, r, "", info.ModTime(), rs)
}

// serveTS entrega el archivo recodificado (o remuxado) como MPEG-TS progresivo.
func (s *Server) serveTS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fileID := r.PathValue("file")
	fi, ok := s.exposed(ctx, fileID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	plan, err := s.playback.PlanDLNA(ctx, fi.ItemID, fileID, s.planOptions(false))
	if err != nil {
		s.log.Warn().Err(err).Str("file_id", fileID).Msg("DLNA: no se pudo preparar el flujo")
		http.NotFound(w, r)
		return
	}
	if plan.Direct || s.transcoder == nil {
		s.serveDirect(w, r) // Lo que no se puede recodificar se entrega tal cual.
		return
	}

	startMS := int64(0)
	durationMS := plan.DurationMS
	if h := r.Header.Get("TimeSeekRange.dlna.org"); h != "" {
		ms, ok := parseNPT(h)
		if !ok || (durationMS > 0 && ms >= durationMS) {
			http.Error(w, "intervalo no válido", http.StatusNotAcceptable)
			return
		}
		startMS = ms
		end := ""
		if durationMS > 0 {
			end = fmt.Sprintf("%.3f/%.3f", float64(durationMS)/1000, float64(durationMS)/1000)
		} else {
			end = "*"
		}
		w.Header().Set("TimeSeekRange.dlna.org", fmt.Sprintf("npt=%.3f-%s", float64(startMS)/1000, end))
	}
	w.Header().Set("Content-Type", "video/mpeg")
	w.Header().Set("Accept-Ranges", "none")
	dlnaHeaders(w, r, strings.TrimPrefix(protocolInfo("x", "10", true), "http-get:*:x:"))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	mode := string(playback.DirectStream)
	if plan.Pipe.Kind == transcoding.Heavy {
		mode = string(playback.Transcode)
	}
	id, err := s.session(ctx, r, fi.ItemID, fileID, mode)
	if err != nil {
		s.log.Error().Err(err).Msg("DLNA: no se pudo abrir la sesión")
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	_ = s.sessions.Activate(ctx, id)
	defer s.keepAlive(ctx, id)()

	pipe := plan.Pipe
	pipe.SessionID, pipe.StartMS = id, startMS
	// Si el servidor cierra la sesión (el administrador la cancela) con el televisor
	// en pausa, la escritura está bloqueada: un plazo vencido la libera.
	rc := http.NewResponseController(w)
	pipe.Abort = func() { _ = rc.SetWriteDeadline(time.Now()) }
	n, err := s.transcoder.StreamTS(ctx, pipe, w)
	switch {
	case errors.Is(err, transcoding.ErrCapacity) && n == 0:
		w.Header().Set("Retry-After", "10")
		http.Error(w, "sin cupo de transcodificación", http.StatusServiceUnavailable)
	case err != nil && n == 0 && ctx.Err() == nil:
		http.Error(w, "no se pudo iniciar la transcodificación", http.StatusInternalServerError)
	}
}

// parseNPT interpreta TimeSeekRange.dlna.org: «npt=90.5-» o «npt=0:01:30-» (el
// final y la duración, si vienen, se ignoran: el flujo sigue hasta el final).
func parseNPT(h string) (int64, bool) {
	v, ok := strings.CutPrefix(strings.TrimSpace(h), "npt=")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(v, "-")
	if !ok || start == "" {
		return 0, false
	}
	var secs float64
	if strings.Contains(start, ":") {
		parts := strings.Split(start, ":")
		if len(parts) != 3 {
			return 0, false
		}
		mult := 3600.0
		for _, p := range parts {
			n, err := strconv.ParseFloat(p, 64)
			if err != nil || n < 0 {
				return 0, false
			}
			secs += n * mult
			mult /= 60
		}
	} else {
		n, err := strconv.ParseFloat(start, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		secs = n
	}
	if secs > 1e7 { // Más de cien días: no es un salto.
		return 0, false
	}
	return int64(secs * 1000), true
}

// serveArt entrega la carátula de un elemento publicado.
func (s *Server) serveArt(w http.ResponseWriter, r *http.Request) {
	id, _, _ := strings.Cut(r.PathValue("name"), ".")
	if s.Images == nil || !reDBID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if _, err := s.db.R.GetDLNAItem(r.Context(), id); err != nil {
		http.NotFound(w, r)
		return
	}
	rel, err := s.db.R.GetItemImage(r.Context(), sqlc.GetItemImageParams{MediaItemID: id, Type: string(media.ImagePoster)})
	if err != nil || rel == nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.Images.Open(*rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, f.Info.Name(), f.Info.ModTime(), f)
}
