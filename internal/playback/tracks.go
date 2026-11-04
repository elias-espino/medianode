package playback

import (
	"strings"

	"medianode/internal/core"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
)

// SubtitleNone en selected_subtitle_id desactiva los subtítulos aunque las
// preferencias digan otra cosa.
const SubtitleNone = "none"

// Tracks son las pistas elegidas de un archivo.
type Tracks struct {
	Video    *sqlc.MediaStream
	Audio    *sqlc.MediaStream
	Internal *sqlc.MediaStream // Subtítulo interno.
	External *sqlc.Subtitle    // Subtítulo externo.
}

// SubtitleID es el identificador del subtítulo elegido, interno o externo.
func (t Tracks) SubtitleID() *string {
	switch {
	case t.Internal != nil:
		return &t.Internal.ID
	case t.External != nil:
		return &t.External.ID
	}
	return nil
}

// Preferences son las preferencias de reproducción que influyen en la
// elección de pistas.
type Preferences struct {
	AudioLanguage    string
	SubtitleLanguage string
	SubtitleMode     media.SubtitleMode
	// MaxBitrateRemote limita el bitrate fuera de la red local; nil no limita.
	MaxBitrateRemote *int64
}

// selectTracks elige vídeo, audio y subtítulo de un archivo. Una pista pedida
// debe pertenecer a ese archivo (§9.4); si no se pide, mandan las
// preferencias.
func selectTracks(streams []sqlc.MediaStream, subs []sqlc.Subtitle, audioID, subtitleID *string, p Preferences) (Tracks, error) {
	var t Tracks
	var audios, internals []sqlc.MediaStream
	for i := range streams {
		s := &streams[i]
		switch media.StreamType(s.StreamType) {
		case media.StreamVideo:
			if t.Video == nil {
				t.Video = s
			}
		case media.StreamAudio:
			audios = append(audios, *s)
		case media.StreamSubtitle:
			internals = append(internals, *s)
		}
	}

	if audioID != nil {
		for i := range audios {
			if audios[i].ID == *audioID {
				t.Audio = &audios[i]
			}
		}
		if t.Audio == nil {
			return t, core.ErrValidation("La pista de audio no pertenece al archivo")
		}
	} else {
		t.Audio = pickAudio(audios, p.AudioLanguage)
	}

	switch {
	case subtitleID != nil && *subtitleID == SubtitleNone:
	case subtitleID != nil:
		for i := range internals {
			if internals[i].ID == *subtitleID {
				t.Internal = &internals[i]
			}
		}
		for i := range subs {
			if subs[i].ID == *subtitleID {
				t.External = &subs[i]
			}
		}
		if t.Internal == nil && t.External == nil {
			return t, core.ErrValidation("El subtítulo no pertenece al archivo")
		}
	default:
		audioLang := ""
		if t.Audio != nil && t.Audio.Language != nil {
			audioLang = *t.Audio.Language
		}
		t.Internal, t.External = pickSubtitle(internals, subs, p, audioLang)
	}
	return t, nil
}

func pickAudio(audios []sqlc.MediaStream, lang string) *sqlc.MediaStream {
	if len(audios) == 0 {
		return nil
	}
	if lang != "" {
		for i := range audios {
			if audios[i].Language != nil && SameLanguage(*audios[i].Language, lang) {
				return &audios[i]
			}
		}
	}
	for i := range audios {
		if audios[i].IsDefault != 0 {
			return &audios[i]
		}
	}
	return &audios[0]
}

// candidate es un subtítulo interno o externo, para compararlos igual.
type candidate struct {
	internal *sqlc.MediaStream
	external *sqlc.Subtitle
	lang     string
	forced   bool
	def      bool
}

// pickSubtitle aplica subtitle_mode: off nunca; always, en el idioma
// preferido; forced, los forzados en el idioma del audio; default, la pista
// marcada por defecto.
func pickSubtitle(internals []sqlc.MediaStream, subs []sqlc.Subtitle, p Preferences, audioLang string) (*sqlc.MediaStream, *sqlc.Subtitle) {
	var all []candidate
	for i := range internals {
		s := &internals[i]
		c := candidate{internal: s, forced: s.IsForced != 0, def: s.IsDefault != 0}
		if s.Language != nil {
			c.lang = *s.Language
		}
		all = append(all, c)
	}
	for i := range subs {
		s := &subs[i]
		all = append(all, candidate{external: s, lang: s.Language, forced: s.IsForced != 0, def: s.IsDefault != 0})
	}
	pick := func(ok func(candidate) bool) (*sqlc.MediaStream, *sqlc.Subtitle) {
		for _, c := range all {
			if ok(c) {
				return c.internal, c.external
			}
		}
		return nil, nil
	}
	lang := p.SubtitleLanguage
	if lang == "" {
		lang = p.AudioLanguage
	}
	switch p.SubtitleMode {
	case media.SubtitleOff:
		return nil, nil
	case media.SubtitleAlways:
		if in, ex := pick(func(c candidate) bool { return SameLanguage(c.lang, lang) && !c.forced }); in != nil || ex != nil {
			return in, ex
		}
		return pick(func(c candidate) bool { return SameLanguage(c.lang, lang) })
	case media.SubtitleForced:
		target := audioLang
		if target == "" {
			target = lang
		}
		return pick(func(c candidate) bool { return c.forced && (target == "" || SameLanguage(c.lang, target)) })
	default:
		return pick(func(c candidate) bool { return c.def })
	}
}

// Códigos ISO 639-1 y sus variantes 639-2: FFprobe da «spa» y un nombre de
// archivo suele dar «es».
var languages = map[string]string{
	"spa": "es", "eng": "en", "fre": "fr", "fra": "fr", "ger": "de", "deu": "de", "ita": "it",
	"por": "pt", "jpn": "ja", "kor": "ko", "chi": "zh", "zho": "zh", "rus": "ru", "cat": "ca",
	"glg": "gl", "baq": "eu", "eus": "eu", "dut": "nl", "nld": "nl", "pol": "pl", "swe": "sv",
	"nor": "no", "dan": "da", "fin": "fi", "tur": "tr", "ara": "ar", "hin": "hi",
}

// SameLanguage compara idiomas sin distinguir 639-1, 639-2 ni región:
// «spa», «es» y «es-ES» son el mismo. Un idioma vacío o «und» no coincide
// con nada.
func SameLanguage(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s, _, _ = strings.Cut(s, "-")
		s, _, _ = strings.Cut(s, "_")
		if v, ok := languages[s]; ok {
			return v
		}
		return s
	}
	na, nb := norm(a), norm(b)
	return na != "" && na != "und" && na == nb
}
