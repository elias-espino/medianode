package playback

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/protocol"
	"medianode/internal/sessions"
	"medianode/internal/subtitles"
	"medianode/internal/transcoding"
)

// DefaultAuthTTL es cuánto vale una comprobación de autorización del
// streaming. Un reproductor pide muchos rangos por minuto; retirar un permiso
// corta la reproducción en este plazo como mucho.
const DefaultAuthTTL = 15 * time.Second

// Request es una petición de reproducción (§9.4).
type Request struct {
	ItemID            string
	FileID            string // Opcional: versión elegida.
	DeviceID          string // Opcional; si llega, debe ser el de la sesión.
	Profile           protocol.ClientProfile
	Declared          map[string]bool // Campos del perfil que envió el cliente.
	AudioStreamID     *string
	SubtitleID        *string // SubtitleNone desactiva los subtítulos.
	MaxBitrate        *int64
	MaxResolution     string
	PreferredProtocol string // Se usará con HLS, en la Fase 8.
	StartPositionMS   int64
}

// Response es lo que recibe el cliente (§9.5).
type Response struct {
	Session     *sessions.Session
	Decision    Decision
	File        sqlc.ListItemFilesRow
	Tracks      Tracks
	Container   string
	StreamURL   string
	SubtitleURL string
	// Al recodificar el vídeo.
	OutputHeight  int
	OutputBitrate int
}

// Service crea sesiones de reproducción y autoriza el streaming.
type Service struct {
	db       *database.DB
	authz    *authorization.Authorizer
	sessions *sessions.Manager
	log      zerolog.Logger

	// AuthTTL es la vigencia de una autorización del streaming.
	AuthTTL time.Duration
	// Transcoder e Indexer son nil sin FFmpeg: entonces solo hay Direct Play.
	Transcoder *transcoding.Manager
	Indexer    *transcoding.Indexer
	// Subtitles convierte pistas de texto; nil sin FFmpeg. Features dice qué
	// filtros tiene el FFmpeg instalado (burn-in y tone mapping).
	Subtitles *subtitles.Manager
	Features  Features

	mu       sync.Mutex
	verified map[string]time.Time // Sesión → última autorización correcta.
	states   map[string]*sessionState
}

// NewService crea el servicio.
func NewService(db *database.DB, authz *authorization.Authorizer, sm *sessions.Manager, log zerolog.Logger) *Service {
	return &Service{db: db, authz: authz, sessions: sm, log: log, AuthTTL: DefaultAuthTTL, verified: map[string]time.Time{}}
}

type option struct {
	file     sqlc.ListItemFilesRow
	tracks   Tracks
	decision Decision
}

// CreateSession sigue §9.4: autoriza, elige archivo y pistas, decide, crea la
// sesión y devuelve la URL con su token.
func (s *Service) CreateSession(ctx context.Context, actor authorization.Actor, req Request) (*Response, error) {
	if req.DeviceID != "" && req.DeviceID != actor.DeviceID {
		return nil, core.ErrValidation("El dispositivo no es el de la sesión iniciada")
	}
	if req.StartPositionMS < 0 {
		return nil, core.ErrValidation("La posición inicial no es válida")
	}
	if req.MaxResolution != "" {
		if _, _, err := protocol.ParseResolution(req.MaxResolution); err != nil {
			return nil, core.ErrValidation("max_resolution no es válida")
		}
	}
	if req.MaxBitrate != nil && *req.MaxBitrate <= 0 {
		return nil, core.ErrValidation("max_bitrate debe ser positivo")
	}
	profile, err := req.Profile.Normalize(req.Declared)
	if err != nil {
		return nil, core.ErrValidation("El perfil del cliente no es válido: " + err.Error())
	}
	if err := s.authz.CanItem(ctx, actor, req.ItemID); err != nil {
		return nil, err
	}
	item, err := s.db.R.GetMediaItem(ctx, req.ItemID)
	if err != nil {
		return nil, fmt.Errorf("leer elemento: %w", err)
	}
	if t := media.ItemType(item.Type); t != media.ItemMovie && t != media.ItemEpisode {
		return nil, core.ErrValidation("Solo se reproducen películas y episodios")
	}
	prefs, err := s.preferences(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}

	files, err := s.db.R.ListItemFiles(ctx, req.ItemID)
	if err != nil {
		return nil, fmt.Errorf("listar archivos: %w", err)
	}
	var options []option
	for _, f := range files {
		if req.FileID != "" && f.ID != req.FileID {
			continue
		}
		if f.MissingSince != nil || f.ProbeStatus != string(media.ProbeDone) {
			continue
		}
		o, err := s.evaluate(ctx, f, req, profile, prefs)
		if err != nil {
			return nil, err
		}
		options = append(options, o)
	}
	if len(options) == 0 {
		if req.FileID != "" && !hasFile(files, req.FileID) {
			return nil, core.ErrNotFound()
		}
		return nil, core.ErrFileUnavailable()
	}
	best := options[0]
	for _, o := range options[1:] {
		if better(o, best) {
			best = o
		}
	}

	if reason := s.unsupported(best); reason != "" {
		s.log.Info().Str("item_id", req.ItemID).Str("file_id", best.file.ID).Str("mode", string(best.decision.Mode)).
			Str("subtitle", string(best.decision.Subtitle)).Interface("reasons", best.decision.Reasons).
			Msg("reproducción rechazada")
		return nil, core.ErrPlaybackUnsupported(reason, map[string]any{
			"playback_mode": best.decision.Mode, "subtitle_mode": best.decision.Subtitle, "reasons": best.decision.Reasons,
		})
	}

	// El cupo se reserva antes de crear la sesión: sin cupo no hay sesión
	// (§9.3). El identificador se genera aquí para poder reservarlo.
	sessionID, err := database.NewID()
	if err != nil {
		return nil, err
	}
	var kind transcoding.JobKind
	if best.decision.Mode != DirectPlay {
		kind = transcoding.Light
		if best.decision.VideoAction == Recode {
			kind = transcoding.Heavy
		}
		if err := s.Transcoder.Admission().Reserve(kind, sessionID); err != nil {
			return nil, core.ErrCapacityExceeded()
		}
	}
	sess, err := s.sessions.Create(ctx, sessions.CreateParams{
		ID: sessionID, UserID: actor.UserID, DeviceID: actor.DeviceID, ItemID: req.ItemID, FileID: best.file.ID,
		Mode: string(best.decision.Mode), StartPositionMS: req.StartPositionMS,
		AudioStreamID: idOf(best.tracks.Audio), SubtitleID: best.tracks.SubtitleID(),
	})
	if err != nil {
		if kind != "" {
			s.Transcoder.Admission().Release(sessionID)
		}
		return nil, err
	}
	base := "/stream/session/" + sess.ID + "/" + s.sessions.StreamToken(sess.ID)
	resp := &Response{
		Session: sess, Decision: best.decision, File: best.file, Tracks: best.tracks,
		Container: deref(best.file.Container), StreamURL: base + "/file",
	}
	s.remember(sess.ID, &sessionState{req: req, profile: profile, mode: best.decision.Mode, kind: kind})
	if kind != "" {
		plan, err := s.plan(ctx, sess, best, kind, profile, req)
		if err == nil {
			err = s.Transcoder.Prepare(ctx, plan)
		}
		if err != nil {
			// Cerrar la sesión libera el cupo y borra lo que hubiera empezado.
			_ = s.sessions.Terminate(ctx, func(x sessions.Session) bool { return x.ID == sess.ID })
			s.log.Error().Err(err).Str("session_id", sess.ID).Msg("no se pudo arrancar FFmpeg")
			return nil, core.ErrTranscodeFailed()
		}
		resp.StreamURL = base + "/master.m3u8"
		resp.Container = "ts"
		if kind == transcoding.Heavy {
			resp.Container = "fmp4"
			resp.OutputHeight, resp.OutputBitrate = plan.Height, plan.VideoBitrate
		}
	}
	resp.SubtitleURL = s.subtitleURL(ctx, base, best)
	s.log.Info().Str("session_id", sess.ID).Str("item_id", req.ItemID).Str("file_id", best.file.ID).
		Str("platform", profile.Platform).Str("mode", string(best.decision.Mode)).
		Str("video", string(best.decision.VideoAction)).Str("audio", string(best.decision.AudioAction)).
		Str("subtitle", string(best.decision.Subtitle)).Interface("reasons", best.decision.Reasons).
		Msg("sesión de reproducción creada")
	return resp, nil
}

// plan traduce la decisión al trabajo de FFmpeg.
// indexWait es lo máximo que la creación de una sesión espera al índice de
// keyframes de un archivo que aún no lo tiene.
const indexWait = 8 * time.Second

func (s *Service) plan(ctx context.Context, sess *sessions.Session, o option, kind transcoding.JobKind,
	c protocol.ClientProfile, req Request) (transcoding.Plan, error) {
	loc, err := s.db.R.GetFileLocation(ctx, o.file.ID)
	if err != nil {
		return transcoding.Plan{}, fmt.Errorf("leer ubicación: %w", err)
	}
	file, err := s.db.R.GetMediaFile(ctx, o.file.ID)
	if err != nil {
		return transcoding.Plan{}, fmt.Errorf("leer archivo: %w", err)
	}
	if o.tracks.Video == nil {
		return transcoding.Plan{}, errors.New("el archivo no tiene vídeo")
	}
	p := transcoding.Plan{
		SessionID: sess.ID, FileID: o.file.ID,
		Path:       filepath.Join(loc.Root, filepath.FromSlash(loc.RelativePath)),
		DurationUS: deref64(o.file.DurationMs) * 1000, StartUS: req.StartPositionMS * 1000,
		Kind: kind, VideoIndex: int(o.tracks.Video.StreamIndex), AudioIndex: -1,
		AudioCopy: o.decision.AudioAction == Copy,
	}
	if a := o.tracks.Audio; a != nil {
		p.AudioIndex = int(a.StreamIndex)
	}
	codecs := []string{}
	if kind == transcoding.Heavy {
		p.Height, p.VideoBitrate = outputProfile(o.tracks.Video.Height, c.MaxResolution, req.MaxResolution, req.MaxBitrate)
		if needsToneMap(o.decision) {
			p.ToneMap = hdrTransfer(o.tracks.Video)
		}
		if err := s.burnPlan(ctx, o.file.ID, o, &p); err != nil {
			return transcoding.Plan{}, err
		}
		p.Variant = transcoding.Variant{Bandwidth: p.VideoBitrate + audioBitrate, Width: scaledWidth(o.tracks.Video, p.Height), Height: p.Height}
		codecs = append(codecs, encodedCodecs(p.Height))
	} else {
		size, mod := deref64(file.FileSize), deref(file.ModifiedAt)
		// Se espera un poco al índice de un archivo nuevo: sin él la sesión usa
		// una playlist EVENT, peor para el salto. Si no llega, la siguiente
		// sesión ya lo tendrá.
		p.Keyframes = s.Indexer.Wait(ctx, o.file.ID, p.Path, size, mod, indexWait)
		v := o.tracks.Video
		p.Variant = transcoding.Variant{Bandwidth: int(max(deref64(o.file.Bitrate), 1)), Width: int(deref64(v.Width)), Height: int(deref64(v.Height))}
		codecs = append(codecs, videoCodecs(v))
	}
	p.Variant.Subtitle = s.hlsSubtitle(ctx, o.file.ID, o)
	if ac := audioCodecs(o.tracks.Audio, p.AudioCopy); ac != "" || o.tracks.Audio == nil {
		// Un códec desconocido deja CODECS vacío entero: incompleto es peor.
		if codecs[0] != "" {
			if ac != "" {
				codecs = append(codecs, ac)
			}
			p.Variant.Codecs = strings.Join(codecs, ",")
		}
	}
	return p, nil
}

// unsupported devuelve por qué la decisión necesita algo que este servidor no
// tiene (FFmpeg, o un filtro de FFmpeg), o vacío. La entrega progresiva por
// HTTP sin HLS no está prevista.
func (s *Service) unsupported(o option) string {
	d := o.decision
	format, external := "", false
	switch {
	case o.tracks.Internal != nil:
		format = protocol.CodecName(deref(o.tracks.Internal.Codec))
	case o.tracks.External != nil:
		format, external = protocol.CodecName(deref(o.tracks.External.Format)), true
	}
	switch d.Subtitle {
	case SubBurnIn:
		switch {
		case s.Transcoder == nil:
			return "FFmpeg no está disponible en el servidor"
		case protocol.ImageSubtitle(format) && (external || !s.Features.BurnInImage):
			return "Este FFmpeg no puede grabar subtítulos de imagen; prueba otro o desactívalos"
		case !protocol.ImageSubtitle(format) && (!s.Features.BurnInText || s.Subtitles == nil):
			return "Este FFmpeg no puede grabar subtítulos de texto; prueba otro o desactívalos"
		}
	case SubHLS, SubConvert:
		switch {
		case !textFormats[format]:
			return "Este formato de subtítulo no se puede convertir; prueba otro o desactívalos"
		case d.Subtitle == SubConvert && external && (format == "srt" || format == "vtt"):
			// Se convierte sin FFmpeg.
		case s.Subtitles == nil:
			return "Convertir este subtítulo necesita FFmpeg, que no está disponible"
		}
	}
	switch {
	case d.Mode == DirectPlay:
		return ""
	case s.Transcoder == nil:
		return "FFmpeg no está disponible en el servidor"
	case d.Protocol != protocol.ProtocolHLS:
		return "Este archivo necesita remux o transcodificación, y el cliente no admite HLS"
	case needsToneMap(d) && !s.Features.ToneMapping:
		return "El vídeo es HDR y el cliente no, y este servidor no puede convertirlo a SDR (FFmpeg sin tone mapping, desactivado o demasiado lento: transcoding.tone_mapping)"
	}
	return ""
}

// evaluate elige las pistas de un archivo y decide cómo reproducirlo.
func (s *Service) evaluate(ctx context.Context, f sqlc.ListItemFilesRow, req Request, c protocol.ClientProfile, p Preferences) (option, error) {
	streams, err := s.db.R.ListFileStreams(ctx, f.ID)
	if err != nil {
		return option{}, fmt.Errorf("leer pistas: %w", err)
	}
	subs, err := s.db.R.ListFileSubtitles(ctx, f.ID)
	if err != nil {
		return option{}, fmt.Errorf("leer subtítulos: %w", err)
	}
	tracks, err := selectTracks(streams, subs, req.AudioStreamID, req.SubtitleID, p)
	if err != nil {
		return option{}, err
	}
	in := Input{
		Container: deref(f.Container), Bitrate: f.Bitrate, Client: c,
		MaxBitrate: req.MaxBitrate, MaxResolution: req.MaxResolution,
	}
	if v := tracks.Video; v != nil {
		in.Video = &Video{Codec: deref(v.Codec), Width: v.Width, Height: v.Height, BitDepth: v.BitDepth, HDR: v.HdrFormat}
	}
	if a := tracks.Audio; a != nil {
		in.Audio = &Audio{Codec: deref(a.Codec), Channels: a.Channels}
	}
	switch {
	case tracks.Internal != nil:
		in.Subtitle = &Subtitle{Format: deref(tracks.Internal.Codec)}
	case tracks.External != nil:
		in.Subtitle = &Subtitle{Format: deref(tracks.External.Format), External: true}
	}
	return option{file: f, tracks: tracks, decision: Decide(in)}, nil
}

// better prefiere el mejor modo y, a igualdad, la mayor calidad: más
// píxeles y después más bitrate.
func better(a, b option) bool {
	rank := map[Mode]int{DirectPlay: 3, DirectStream: 2, Transcode: 1}
	if ra, rb := rank[a.decision.Mode], rank[b.decision.Mode]; ra != rb {
		return ra > rb
	}
	if pa, pb := pixels(a.tracks.Video), pixels(b.tracks.Video); pa != pb {
		return pa > pb
	}
	return deref64(a.file.Bitrate) > deref64(b.file.Bitrate)
}

func pixels(v *sqlc.MediaStream) int64 {
	if v == nil || v.Width == nil || v.Height == nil {
		return 0
	}
	return *v.Width * *v.Height
}

func (s *Service) preferences(ctx context.Context, userID string) (Preferences, error) {
	row, err := s.db.R.GetPreferences(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return Preferences{SubtitleMode: media.SubtitleDefault}, nil
	}
	if err != nil {
		return Preferences{}, fmt.Errorf("leer preferencias: %w", err)
	}
	return Preferences{
		AudioLanguage: deref(row.AudioLanguage), SubtitleLanguage: deref(row.SubtitleLanguage),
		SubtitleMode: media.SubtitleMode(row.SubtitleMode),
	}, nil
}

// AuthorizeStream comprueba que el dueño de la sesión sigue pudiendo ver el
// elemento: usuario activo, dispositivo sin revocar y permiso vigente. Si no,
// cierra la sesión. El resultado positivo se recuerda AuthTTL.
func (s *Service) AuthorizeStream(ctx context.Context, sess *sessions.Session) error {
	if sess.ClientKind != sessions.KindApp {
		return nil
	}
	s.mu.Lock()
	at, ok := s.verified[sess.ID]
	s.mu.Unlock()
	if ok && time.Since(at) < s.AuthTTL {
		return nil
	}
	err := s.checkStream(ctx, sess)
	if core.CodeOf(err) != "" {
		s.mu.Lock()
		delete(s.verified, sess.ID)
		s.mu.Unlock()
		if terr := s.sessions.Terminate(ctx, func(x sessions.Session) bool { return x.ID == sess.ID }); terr != nil {
			s.log.Error().Err(terr).Str("session_id", sess.ID).Msg("no se pudo cerrar la sesión")
		}
		return core.ErrSessionExpired()
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.verified[sess.ID] = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *Service) checkStream(ctx context.Context, sess *sessions.Session) error {
	st, err := s.db.R.GetAuthState(ctx, sqlc.GetAuthStateParams{ID: sess.DeviceID, UserID: sess.UserID})
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound()
	}
	if err != nil {
		return fmt.Errorf("leer estado: %w", err)
	}
	if st.RevokedAt != nil || st.IsActive == 0 {
		return core.ErrForbidden()
	}
	actor := authorization.Actor{UserID: sess.UserID, DeviceID: sess.DeviceID, Role: media.Role(st.Role), MaxAgeRating: st.MaxAgeRating}
	return s.authz.CanItem(ctx, actor, sess.ItemID)
}

// OnSessionEnded implementa sessions.Listener: olvida su autorización.
func (s *Service) OnSessionEnded(sess sessions.Session) {
	s.mu.Lock()
	delete(s.verified, sess.ID)
	delete(s.states, sess.ID)
	s.mu.Unlock()
}

func hasFile(files []sqlc.ListItemFilesRow, id string) bool {
	for _, f := range files {
		if f.ID == id {
			return true
		}
	}
	return false
}

func idOf(s *sqlc.MediaStream) *string {
	if s == nil {
		return nil
	}
	return &s.ID
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func deref64(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}
