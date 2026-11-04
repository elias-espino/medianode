package transcoding

import (
	"strings"
	"testing"
)

func TestMasterConPistaDeSubtitulos(t *testing.T) {
	v := Variant{Bandwidth: 1_000_000, Width: 640, Height: 360, Codecs: "avc1.64001f,mp4a.40.2",
		Subtitle: &SubtitleRef{Name: "Español", Language: "spa", Forced: true}}
	got := string(MasterPlaylist(v))
	for _, want := range []string{
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Español",DEFAULT=YES,AUTOSELECT=YES,FORCED=YES,URI="subs.m3u8",LANGUAGE="spa"`,
		`#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=640x360,CODECS="avc1.64001f,mp4a.40.2",SUBTITLES="subs"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en:\n%s", want, got)
		}
	}
	// EXT-X-MEDIA va antes que la variante que lo usa.
	if strings.Index(got, "#EXT-X-MEDIA") > strings.Index(got, "#EXT-X-STREAM-INF") {
		t.Errorf("el orden es incorrecto:\n%s", got)
	}
	// Sin subtítulos, nada de lo anterior.
	if plain := string(MasterPlaylist(Variant{Bandwidth: 1})); strings.Contains(plain, "SUBTITLES") || strings.Contains(plain, "EXT-X-MEDIA") {
		t.Errorf("master sin subtítulos:\n%s", plain)
	}
}

func TestMasterSaneaLosAtributosDeLaPista(t *testing.T) {
	// Comillas, barras y saltos de línea romperían la playlist.
	hostile := &SubtitleRef{Name: "Mal\"nombre\\\n#EXT-X-ENDLIST", Language: "e\"s"}
	got := string(MasterPlaylist(Variant{Bandwidth: 1, Subtitle: hostile}))
	if strings.Count(got, "\n#EXT-X-ENDLIST") != 0 || strings.Count(got, "#EXT-X-MEDIA") != 1 {
		t.Fatalf("la pista inyectó líneas:\n%s", got)
	}
	if !strings.Contains(got, `NAME="Malnombre#EXT-X-ENDLIST"`) || !strings.Contains(got, `LANGUAGE="es"`) {
		t.Fatalf("atributos mal saneados:\n%s", got)
	}
	// Un nombre vacío cae a uno por defecto y un idioma vacío se omite.
	empty := string(MasterPlaylist(Variant{Bandwidth: 1, Subtitle: &SubtitleRef{Name: "  ", Language: ""}}))
	if !strings.Contains(empty, `NAME="Subtítulos"`) || strings.Contains(empty, "LANGUAGE") || !strings.Contains(empty, "FORCED=NO") {
		t.Fatalf("valores por defecto:\n%s", empty)
	}
}

func TestSubtitlePlaylist(t *testing.T) {
	got := string(SubtitlePlaylist(7_400_000))
	for _, want := range []string{"#EXT-X-TARGETDURATION:8", "#EXT-X-PLAYLIST-TYPE:VOD", "#EXTINF:7.400000,\nsubs.vtt\n", "#EXT-X-ENDLIST"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en:\n%s", want, got)
		}
	}
	// Una duración desconocida da un mínimo válido, no una playlist de 0 s.
	zero := string(SubtitlePlaylist(0))
	if !strings.Contains(zero, "#EXT-X-TARGETDURATION:1") || !strings.Contains(zero, "#EXTINF:1.000000,") {
		t.Errorf("duración 0:\n%s", zero)
	}
}
