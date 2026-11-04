package playback

import (
	"testing"

	"medianode/internal/database/sqlc"
	"medianode/internal/ffmpeg"
)

func ptr[T any](v T) *T { return &v }

func TestHDRTransfer(t *testing.T) {
	cases := []struct {
		name string
		v    *sqlc.MediaStream
		want string
	}{
		{"sin pista", nil, ""},
		{"SDR", &sqlc.MediaStream{}, ""},
		{"HDR10", &sqlc.MediaStream{HdrFormat: ptr("hdr10")}, ffmpeg.TransferPQ},
		{"HDR10+", &sqlc.MediaStream{HdrFormat: ptr("hdr10+")}, ffmpeg.TransferPQ},
		{"HLG", &sqlc.MediaStream{HdrFormat: ptr("hlg")}, ffmpeg.TransferHLG},
		{"HLG en mayúsculas", &sqlc.MediaStream{HdrFormat: ptr("HLG")}, ffmpeg.TransferHLG},
		{"Dolby Vision usa su capa base HDR10", &sqlc.MediaStream{HdrFormat: ptr("dolby_vision")}, ffmpeg.TransferPQ},
	}
	for _, c := range cases {
		if got := hdrTransfer(c.v); got != c.want {
			t.Errorf("%s: %q, se esperaba %q", c.name, got, c.want)
		}
	}
}

func TestNeedsToneMap(t *testing.T) {
	if needsToneMap(Decision{}) {
		t.Error("sin motivos no hay tone mapping")
	}
	if needsToneMap(Decision{Reasons: []Reason{{Code: ReasonBitrate}, {Code: ReasonVideoCodec}}}) {
		t.Error("otros motivos no son HDR")
	}
	if !needsToneMap(Decision{Reasons: []Reason{{Code: ReasonBitrate}, {Code: ReasonHDR}}}) {
		t.Error("el motivo HDR pide tone mapping")
	}
}

func TestIsTextSubtitle(t *testing.T) {
	for _, f := range []string{"subrip", "srt", "ass", "ssa", "webvtt", "vtt", "mov_text", "SRT", "text"} {
		if !IsTextSubtitle(f) {
			t.Errorf("%s es de texto", f)
		}
	}
	for _, f := range []string{"hdmv_pgs_subtitle", "pgs", "dvd_subtitle", "vobsub", "dvb_subtitle", "dvbsub", "", "eia_608", "invento"} {
		if IsTextSubtitle(f) {
			t.Errorf("%q no se convierte", f)
		}
	}
}

func TestTrackName(t *testing.T) {
	cases := []struct {
		lang   string
		forced bool
		want   string
	}{
		{"", false, "Subtítulos"},
		{"und", false, "Subtítulos"},
		{"spa", false, "Subtítulos (spa)"},
		{"spa", true, "Subtítulos (spa) forzados"},
		{"", true, "Subtítulos forzados"},
	}
	for _, c := range cases {
		if got := trackName(c.lang, c.forced); got != c.want {
			t.Errorf("trackName(%q, %v) = %q, se esperaba %q", c.lang, c.forced, got, c.want)
		}
	}
}

func TestFeaturesOf(t *testing.T) {
	if got := FeaturesOf(nil); got != (Features{}) {
		t.Errorf("sin FFmpeg: %+v", got)
	}
	got := FeaturesOf(&ffmpeg.Capabilities{BurnInText: true, ToneMapping: true})
	if !got.BurnInText || got.BurnInImage || !got.ToneMapping {
		t.Errorf("%+v", got)
	}
}
