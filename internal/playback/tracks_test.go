package playback

import (
	"testing"

	"medianode/internal/core"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
)

func stream(id, kind, lang string, def, forced bool) sqlc.MediaStream {
	s := sqlc.MediaStream{ID: id, StreamType: kind}
	if lang != "" {
		s.Language = &lang
	}
	if def {
		s.IsDefault = 1
	}
	if forced {
		s.IsForced = 1
	}
	return s
}

func external(id, lang string, forced bool) sqlc.Subtitle {
	s := sqlc.Subtitle{ID: id, Language: lang}
	if forced {
		s.IsForced = 1
	}
	return s
}

func id(t Tracks) string {
	if s := t.SubtitleID(); s != nil {
		return *s
	}
	return ""
}

func TestElegirAudio(t *testing.T) {
	streams := []sqlc.MediaStream{
		stream("v", "video", "", false, false),
		stream("en", "audio", "eng", true, false),
		stream("es", "audio", "spa", false, false),
	}
	cases := []struct {
		lang string
		want string
	}{{"es", "es"}, {"spa", "es"}, {"es-ES", "es"}, {"fr", "en"}, {"", "en"}}
	for _, c := range cases {
		tr, err := selectTracks(streams, nil, nil, nil, Preferences{AudioLanguage: c.lang})
		if err != nil || tr.Audio.ID != c.want || tr.Video.ID != "v" {
			t.Errorf("%q: %v %+v", c.lang, err, tr.Audio)
		}
	}
	tr, _ := selectTracks(streams[:1], nil, nil, nil, Preferences{})
	if tr.Audio != nil {
		t.Fatal("sin pistas de audio no se elige ninguna")
	}
}

// Una pista pedida debe ser de este archivo (§9.4).
func TestPistaDeOtroArchivo(t *testing.T) {
	streams := []sqlc.MediaStream{stream("a1", "audio", "spa", false, false), stream("s1", "subtitle", "spa", false, false)}
	subs := []sqlc.Subtitle{external("x1", "es", false)}
	for _, c := range []struct{ audio, sub *string }{
		{str("de-otro"), nil}, {str("s1"), nil}, {nil, str("a1")}, {nil, str("de-otro")},
	} {
		_, err := selectTracks(streams, subs, c.audio, c.sub, Preferences{})
		if core.CodeOf(err) != core.CodeValidation {
			t.Errorf("%v %v: %v", c.audio, c.sub, err)
		}
	}
	tr, err := selectTracks(streams, subs, str("a1"), str("x1"), Preferences{})
	if err != nil || tr.Audio.ID != "a1" || id(tr) != "x1" {
		t.Fatalf("%v %+v", err, tr)
	}
	tr, _ = selectTracks(streams, subs, nil, str("s1"), Preferences{})
	if tr.Internal == nil || tr.External != nil {
		t.Fatal("un subtítulo interno se pide por el id de su pista")
	}
	tr, err = selectTracks(streams, subs, nil, str(SubtitleNone), Preferences{SubtitleMode: media.SubtitleAlways, SubtitleLanguage: "es"})
	if err != nil || id(tr) != "" {
		t.Fatal("none desactiva los subtítulos")
	}
}

func TestPreferenciasDeSubtitulos(t *testing.T) {
	streams := []sqlc.MediaStream{
		stream("audio-en", "audio", "eng", true, false),
		stream("int-en-forzado", "subtitle", "eng", false, true),
		stream("int-es", "subtitle", "spa", true, false),
	}
	subs := []sqlc.Subtitle{external("ext-es-forzado", "es", true), external("ext-fr", "fr", false)}
	cases := []struct {
		name string
		p    Preferences
		want string
	}{
		{"off", Preferences{SubtitleMode: media.SubtitleOff, SubtitleLanguage: "es"}, ""},
		{"default usa la marcada", Preferences{SubtitleMode: media.SubtitleDefault}, "int-es"},
		{"always en español evita el forzado", Preferences{SubtitleMode: media.SubtitleAlways, SubtitleLanguage: "es"}, "int-es"},
		{"always en francés", Preferences{SubtitleMode: media.SubtitleAlways, SubtitleLanguage: "fr"}, "ext-fr"},
		{"always sin idioma usa el del audio preferido", Preferences{SubtitleMode: media.SubtitleAlways, AudioLanguage: "fr"}, "ext-fr"},
		{"always sin coincidencia", Preferences{SubtitleMode: media.SubtitleAlways, SubtitleLanguage: "de"}, ""},
		{"forced sigue al audio", Preferences{SubtitleMode: media.SubtitleForced}, "int-en-forzado"},
	}
	for _, c := range cases {
		tr, err := selectTracks(streams, subs, nil, nil, c.p)
		if err != nil || id(tr) != c.want {
			t.Errorf("%s: %v %q", c.name, err, id(tr))
		}
	}
	// Con el audio en español, el forzado es el externo en español.
	tr, _ := selectTracks(append(streams, stream("audio-es", "audio", "spa", false, false)), subs, str("audio-es"), nil,
		Preferences{SubtitleMode: media.SubtitleForced})
	if id(tr) != "ext-es-forzado" {
		t.Fatalf("%q", id(tr))
	}
}

func TestSameLanguage(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"spa", "es", true}, {"es-ES", "spa", true}, {"pt_BR", "por", true}, {"fre", "fra", true},
		{"eng", "es", false}, {"und", "und", false}, {"", "", false}, {"xx", "xx", true},
	} {
		if got := SameLanguage(c.a, c.b); got != c.want {
			t.Errorf("%q %q = %v", c.a, c.b, got)
		}
	}
}
