package playback

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"medianode/internal/core"
	"medianode/internal/database/sqlc"
	"medianode/internal/ffmpeg"
	"medianode/internal/media"
	"medianode/internal/protocol"
	"medianode/internal/transcoding"
)

// Modos de dlna.mode.
const (
	DLNAAuto      = "auto"      // El original si el perfil genérico lo admite; si no, MPEG-TS.
	DLNADirect    = "direct"    // Siempre el original.
	DLNATranscode = "transcode" // Siempre MPEG-TS (copiando lo que se pueda).
)

// DLNAOptions ajusta cómo se ofrece un archivo a un renderer.
type DLNAOptions struct {
	Mode string // DLNAAuto, DLNADirect o DLNATranscode.
	// BurnSubtitles graba un subtítulo en la imagen al recodificar. Un renderer
	// no puede elegir pistas: se escoge una por el idioma de los metadatos.
	BurnSubtitles bool
	// Language es el idioma preferido, de metadata.language.
	Language string
	// DecideOnly decide cómo se entregaría el archivo sin preparar el flujo:
	// no convierte subtítulos ni abre nada. Es lo que necesita un listado.
	DecideOnly bool
}

// DLNAPlan es cómo se entrega un archivo a un renderer.
type DLNAPlan struct {
	ItemID, FileID string
	File           sqlc.ListItemFilesRow
	Container      string
	Path           string // Absoluta; solo para el flujo recodificado.
	DurationMS     int64
	// Direct entrega el archivo original, con rangos. Si no, Pipe describe el
	// MPEG-TS progresivo (sin SessionID: lo pone quien lo ejecuta).
	Direct   bool
	Pipe     transcoding.PipePlan
	Decision Decision
	Video    *sqlc.MediaStream
	Audio    *sqlc.MediaStream
	// BurnedSubtitle indica que el flujo lleva un subtítulo grabado.
	BurnedSubtitle bool
	OutputHeight   int
}

// dlnaProfile es el cliente genérico con el que se decide para DLNA.
func dlnaProfile() protocol.ClientProfile {
	p := protocol.PresetFor("dlna")
	p.Platform = "dlna"
	return p
}

// PlanDLNA decide cómo entregar un archivo a un renderer DLNA. No comprueba que
// el archivo esté expuesto: eso es de quien lo llama, que conoce la política de
// bibliotecas.
//
// Sin FFmpeg, o si lo que haría falta no se puede hacer con él, un archivo se
// entrega tal cual: un televisor que no lo entienda dará error, pero es mejor
// que no ofrecerlo, porque muchos formatos sí se reproducen.
func (s *Service) PlanDLNA(ctx context.Context, itemID, fileID string, o DLNAOptions) (*DLNAPlan, error) {
	files, err := s.db.R.ListItemFiles(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("listar archivos: %w", err)
	}
	var file *sqlc.ListItemFilesRow
	for i := range files {
		if files[i].ID == fileID {
			file = &files[i]
		}
	}
	if file == nil {
		return nil, core.ErrNotFound()
	}
	if file.MissingSince != nil || file.ProbeStatus != string(media.ProbeDone) {
		return nil, core.ErrFileUnavailable()
	}

	client := dlnaProfile()
	prefs := Preferences{AudioLanguage: o.Language, SubtitleLanguage: o.Language, SubtitleMode: media.SubtitleOff}
	plan := &DLNAPlan{ItemID: itemID, FileID: fileID, File: *file, Container: deref(file.Container), DurationMS: deref64(file.DurationMs)}

	opt, err := s.dlnaOption(ctx, *file, client, prefs, o)
	if err != nil {
		return nil, err
	}
	// Un subtítulo que este FFmpeg no puede grabar no impide reproducir.
	if reason := s.unsupportedSubtitle(opt); reason != "" {
		o.BurnSubtitles = false
		if opt, err = s.dlnaOption(ctx, *file, client, prefs, o); err != nil {
			return nil, err
		}
	}
	plan.Decision, plan.Video, plan.Audio = opt.decision, opt.tracks.Video, opt.tracks.Audio

	switch {
	case o.Mode == DLNADirect:
		plan.Direct = true
	case o.Mode == DLNAAuto && opt.decision.Mode == DirectPlay:
		plan.Direct = true
	case !s.canPipe(opt):
		plan.Direct = true // No se puede hacer más que entregarlo.
		plan.Decision.Subtitle = SubNone
	}
	if plan.Direct || o.DecideOnly {
		return plan, nil
	}
	return plan, s.dlnaPipe(ctx, plan, opt, client)
}

// canPipe dice si este servidor puede producir el flujo MPEG-TS de la opción.
func (s *Service) canPipe(o option) bool {
	switch {
	case s.Transcoder == nil || o.tracks.Video == nil:
		return false
	case s.unsupportedSubtitle(o) != "":
		return false
	case needsToneMap(o.decision) && !s.Features.ToneMapping:
		return false
	}
	return true
}

// dlnaOption evalúa el archivo con el cliente DLNA y la política de subtítulos.
func (s *Service) dlnaOption(ctx context.Context, f sqlc.ListItemFilesRow, c protocol.ClientProfile, p Preferences, o DLNAOptions) (option, error) {
	req := Request{MaxResolution: c.MaxResolution}
	none := SubtitleNone
	req.SubtitleID = &none
	if o.BurnSubtitles {
		// Solo los forzados, que traducen lo que se habla en otro idioma; si el
		// audio no es el preferido, también los subtítulos de ese idioma.
		p.SubtitleMode = media.SubtitleForced
		req.SubtitleID = nil
		opt, err := s.evaluate(ctx, f, req, c, p)
		if err != nil {
			return option{}, err
		}
		if lang := audioLanguage(opt.tracks); o.Language != "" && lang != "" && !SameLanguage(lang, o.Language) {
			p.SubtitleMode = media.SubtitleAlways
			return s.evaluate(ctx, f, req, c, p)
		}
		return opt, nil
	}
	return s.evaluate(ctx, f, req, c, p)
}

func audioLanguage(t Tracks) string {
	if t.Audio != nil && t.Audio.Language != nil {
		return *t.Audio.Language
	}
	return ""
}

// dlnaPipe completa plan.Pipe con lo que FFmpeg necesita.
func (s *Service) dlnaPipe(ctx context.Context, plan *DLNAPlan, o option, c protocol.ClientProfile) error {
	loc, err := s.db.R.GetFileLocation(ctx, o.file.ID)
	if err != nil {
		return fmt.Errorf("leer ubicación: %w", err)
	}
	plan.Path = filepath.Join(loc.Root, filepath.FromSlash(loc.RelativePath))
	kind := transcoding.Light
	if o.decision.VideoAction == Recode {
		kind = transcoding.Heavy
	}
	p := transcoding.PipePlan{
		FileID: o.file.ID, Path: plan.Path, DurationUS: plan.DurationMS * 1000, Kind: kind,
		VideoIndex: int(o.tracks.Video.StreamIndex), AudioIndex: -1, AudioCopy: o.decision.AudioAction == Copy,
	}
	if a := o.tracks.Audio; a != nil {
		p.AudioIndex = int(a.StreamIndex)
	}
	if kind == transcoding.Heavy {
		p.Height, p.VideoBitrate = outputProfile(o.tracks.Video.Height, c.MaxResolution, "", nil)
		plan.OutputHeight = p.Height
		if needsToneMap(o.decision) {
			p.ToneMap = hdrTransfer(o.tracks.Video)
		}
		var burn transcoding.Plan
		if err := s.burnPlan(ctx, o.file.ID, o, &burn); err != nil {
			return err
		}
		p.Burn, p.BurnFile, p.BurnStream = burn.Burn, burn.BurnFile, burn.BurnStream
		plan.BurnedSubtitle = burn.Burn != ffmpeg.BurnNone
	}
	plan.Pipe = p
	return nil
}

// DLNAMIME es el tipo MIME con el que se anuncia un contenedor por DLNA.
func DLNAMIME(container string) string {
	switch strings.ToLower(container) {
	case "mp4", "m4v":
		return "video/mp4"
	case "mkv":
		return "video/x-matroska"
	case "webm":
		return "video/webm"
	case "ts":
		return "video/mpeg"
	case "mov":
		return "video/quicktime"
	case "avi":
		return "video/x-msvideo"
	}
	return "video/mpeg"
}
