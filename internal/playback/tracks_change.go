package playback

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/protocol"
	"medianode/internal/sessions"
	"medianode/internal/transcoding"
)

// sessionState es lo que hace falta recordar de una sesión para poder
// cambiarle las pistas sin que el cliente repita su petición. Vive en
// memoria: si el servidor se reinicia, la sesión se cierra de todos modos
// (§12.5) y el cliente crea otra.
type sessionState struct {
	mu       sync.Mutex // Un cambio a la vez por sesión.
	req      Request
	profile  protocol.ClientProfile
	mode     Mode
	kind     transcoding.JobKind // Vacío en Direct Play.
	revision int
}

func (s *Service) remember(id string, st *sessionState) {
	s.mu.Lock()
	if s.states == nil {
		s.states = map[string]*sessionState{}
	}
	s.states[id] = st
	s.mu.Unlock()
}

func (s *Service) state(id string) *sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[id]
}

// TrackChange es lo que el cliente quiere cambiar. Un campo nil no cambia.
type TrackChange struct {
	AudioStreamID *string
	SubtitleID    *string // SubtitleNone los desactiva.
	PositionMS    *int64  // Por defecto, la última posición de la sesión.
}

// ChangeTracks cambia la pista de audio o de subtítulos de una sesión viva,
// en la posición actual (§12, Fase 9).
//
// En una sesión HLS reinicia FFmpeg con las pistas nuevas y devuelve un
// stream_url con otra revisión: el cliente recarga la lista desde esa
// posición. El cupo no se toca. En una de Direct Play el archivo llega entero
// y el reproductor elige la pista de audio él mismo, así que solo cambia la
// selección guardada y la URL del subtítulo; si el cambio exigiera remux o
// transcodificar, hay que crear otra sesión.
func (s *Service) ChangeTracks(ctx context.Context, actor authorization.Actor, sessionID string, ch TrackChange) (*Response, error) {
	sess, err := s.sessions.Get(ctx, actor, sessionID)
	if err != nil {
		return nil, err
	}
	st := s.state(sessionID)
	if sess.EndedAt != nil || st == nil {
		return nil, core.ErrSessionExpired()
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	req := st.req
	if ch.AudioStreamID != nil {
		req.AudioStreamID = ch.AudioStreamID
	}
	if ch.SubtitleID != nil {
		req.SubtitleID = ch.SubtitleID
	}
	req.StartPositionMS = sess.PositionMS
	if ch.PositionMS != nil {
		if *ch.PositionMS < 0 {
			return nil, core.ErrValidation("La posición no es válida")
		}
		req.StartPositionMS = *ch.PositionMS
	}

	files, err := s.db.R.ListItemFiles(ctx, sess.ItemID)
	if err != nil {
		return nil, fmt.Errorf("listar archivos: %w", err)
	}
	var best *option
	prefs, err := s.preferences(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.ID != sess.FileID {
			continue
		}
		o, err := s.evaluate(ctx, f, req, st.profile, prefs)
		if err != nil {
			return nil, err
		}
		best = &o
	}
	if best == nil {
		return nil, core.ErrFileUnavailable()
	}
	if reason := s.unsupported(*best); reason != "" {
		return nil, core.ErrPlaybackUnsupported(reason, map[string]any{
			"playback_mode": best.decision.Mode, "subtitle_mode": best.decision.Subtitle, "reasons": best.decision.Reasons,
		})
	}

	base := "/stream/session/" + sess.ID + "/" + s.sessions.StreamToken(sess.ID)
	resp := &Response{
		Session: sess, Decision: best.decision, File: best.file, Tracks: best.tracks,
		Container: deref(best.file.Container), StreamURL: base + "/file",
	}

	if st.mode == DirectPlay {
		if best.decision.Mode != DirectPlay {
			return nil, core.ErrPlaybackUnsupported(
				"Este cambio necesita remux o transcodificación: crea una sesión nueva con esas pistas",
				map[string]any{"playback_mode": best.decision.Mode, "reasons": best.decision.Reasons})
		}
	} else {
		kind := transcoding.Light
		if best.decision.VideoAction == Recode {
			kind = transcoding.Heavy
		}
		if kind != st.kind {
			// Reserve libera el cupo anterior antes de mirar el nuevo: si no hay
			// sitio, se devuelve el que tenía.
			if err := s.Transcoder.Admission().Reserve(kind, sess.ID); err != nil {
				_ = s.Transcoder.Admission().Reserve(st.kind, sess.ID)
				return nil, core.ErrCapacityExceeded()
			}
		}
		plan, err := s.plan(ctx, sess, *best, kind, st.profile, req)
		if err == nil {
			err = s.Transcoder.Restream(ctx, plan)
		}
		if err != nil {
			_ = s.sessions.Terminate(ctx, func(x sessions.Session) bool { return x.ID == sess.ID })
			s.log.Error().Err(err).Str("session_id", sess.ID).Msg("no se pudo cambiar de pista")
			return nil, core.ErrTranscodeFailed()
		}
		st.kind = kind
		st.revision++
		resp.StreamURL = base + "/master.m3u8?rev=" + strconv.Itoa(st.revision)
		resp.Container = "ts"
		if kind == transcoding.Heavy {
			resp.Container = "fmp4"
			resp.OutputHeight, resp.OutputBitrate = plan.Height, plan.VideoBitrate
		}
	}

	if err := s.sessions.SetTracks(ctx, sess.ID, idOf(best.tracks.Audio), best.tracks.SubtitleID()); err != nil {
		return nil, err
	}
	// Lo que pidió el cliente pasa a ser la selección de la sesión.
	st.req.AudioStreamID, st.req.SubtitleID = idOf(best.tracks.Audio), best.tracks.SubtitleID()
	if st.req.SubtitleID == nil {
		none := SubtitleNone
		st.req.SubtitleID = &none
	}
	resp.SubtitleURL = s.subtitleURL(ctx, base, *best)
	s.log.Info().Str("session_id", sess.ID).Str("mode", string(best.decision.Mode)).
		Str("audio", string(best.decision.AudioAction)).Str("subtitle", string(best.decision.Subtitle)).
		Int64("position_ms", req.StartPositionMS).Msg("pistas de la sesión cambiadas")
	return resp, nil
}
