package probe

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"medianode/internal/media"
)

func load(t *testing.T, name string) *Result {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func num(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestParseHDR10(t *testing.T) {
	res := load(t, "mkv_hdr10_hevc")
	if res.FormatName != "matroska,webm" || num(res.DurationMS) != 3023 {
		t.Fatalf("formato %q, duración %d", res.FormatName, num(res.DurationMS))
	}
	v := res.Streams[0]
	if v.Type != media.StreamVideo || str(v.Codec) != "hevc" || str(v.Profile) != "Main 10" {
		t.Fatalf("pista de video: %+v", v)
	}
	if num(v.BitDepth) != 10 || str(v.HDRFormat) != HDR10 || str(v.ColorTransfer) != "smpte2084" ||
		str(v.ColorPrimaries) != "bt2020" || num(v.Width) != 1280 || str(v.FrameRate) != "24/1" || str(v.Level) != "93" {
		t.Fatalf("color o geometría: %+v", v)
	}
	a := res.Streams[1]
	if a.Type != media.StreamAudio || num(a.Channels) != 1 || a.Width != nil || a.HDRFormat != nil {
		t.Fatalf("pista de audio: %+v", a)
	}
}

func TestParseIdiomasYDisposicion(t *testing.T) {
	res := load(t, "mkv_multi_audio")
	spa, eng := res.Streams[1], res.Streams[2]
	if str(spa.Language) != "spa" || !spa.Default || str(eng.Language) != "eng" || eng.Default {
		t.Fatalf("spa %+v eng %+v", spa, eng)
	}
	if res.Streams[0].HDRFormat != nil || num(res.Streams[0].BitDepth) != 8 {
		t.Fatalf("video SDR: %+v", res.Streams[0])
	}
	if num(res.Bitrate) != 1915165 {
		t.Fatalf("bitrate = %d", num(res.Bitrate))
	}
}

func TestParseUndEsIdiomaDesconocido(t *testing.T) {
	res := load(t, "mp4_h264_aac")
	for _, s := range res.Streams {
		if s.Language != nil {
			t.Fatalf("«und» debe quedar nil: %q", *s.Language)
		}
	}
	if num(res.Streams[1].Bitrate) != 96428 {
		t.Fatalf("bitrate de audio = %d", num(res.Streams[1].Bitrate))
	}
}

func TestParseSubtituloInterno(t *testing.T) {
	res := load(t, "mkv_srt_internal")
	s := res.Streams[2]
	if s.Type != media.StreamSubtitle || str(s.Codec) != "subrip" || str(s.Language) != "spa" {
		t.Fatalf("%+v", s)
	}
}

func TestParseCasosSinteticos(t *testing.T) {
	data := []byte(`{
	  "format": {"format_name": "matroska,webm", "duration": "N/A"},
	  "chapters": [{}, {}],
	  "streams": [
	    {"index": 0, "codec_type": "video", "codec_name": "hevc", "pix_fmt": "yuv420p10le",
	     "color_transfer": "smpte2084", "side_data_list": [{"side_data_type": "DOVI configuration record"}],
	     "tags": {"BPS": "8000000"}},
	    {"index": 1, "codec_type": "video", "codec_name": "mjpeg", "disposition": {"attached_pic": 1}},
	    {"index": 2, "codec_type": "video", "codec_name": "hevc", "color_transfer": "arib-std-b67", "pix_fmt": "nv12"},
	    {"index": 3, "codec_type": "audio", "codec_name": "eac3", "channels": 6, "level": -99,
	     "disposition": {"forced": 1, "hearing_impaired": 1}, "tags": {"TITLE": "Comentarios", "LANGUAGE": "SPA"}},
	    {"index": 4, "codec_type": "data", "codec_name": "bin_data"},
	    {"index": 5, "codec_type": "attachment", "codec_name": "ttf"}
	  ]
	}`)
	res, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if res.DurationMS != nil || res.Chapters != 2 {
		t.Fatalf("duración %v, capítulos %d", res.DurationMS, res.Chapters)
	}
	dv, cover, hlg, audio := res.Streams[0], res.Streams[1], res.Streams[2], res.Streams[3]
	if str(dv.HDRFormat) != DolbyVision || num(dv.Bitrate) != 8000000 || num(dv.BitDepth) != 10 {
		t.Fatalf("Dolby Vision: %+v", dv)
	}
	if cover.Type != media.StreamAttachment {
		t.Fatalf("la carátula no debe ser video: %s", cover.Type)
	}
	if str(hlg.HDRFormat) != HLG || num(hlg.BitDepth) != 8 {
		t.Fatalf("HLG: %+v", hlg)
	}
	if audio.Level != nil || !audio.Forced || str(audio.Language) != "spa" || num(audio.Channels) != 6 ||
		str(audio.MetadataJSON) != `{"hearing_impaired":true,"title":"Comentarios"}` {
		t.Fatalf("audio: %+v %s", audio, str(audio.MetadataJSON))
	}
	if res.Streams[4].Type != media.StreamData || res.Streams[5].Type != media.StreamAttachment {
		t.Fatal("tipos de datos y adjuntos")
	}
}

func TestParseRechazaSalidaInvalida(t *testing.T) {
	for _, in := range []string{"", "{", `{"streams": []}`} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Fatalf("%q: se esperaba un error", in)
		}
	}
}

func TestContainer(t *testing.T) {
	cases := []struct{ format, ext, want string }{
		{"matroska,webm", ".mkv", "mkv"},
		{"matroska,webm", ".webm", "webm"},
		{"mov,mp4,m4a,3gp,3g2,mj2", ".mp4", "mp4"},
		{"mov,mp4,m4a,3gp,3g2,mj2", ".M4V", "m4v"},
		{"mov,mp4,m4a,3gp,3g2,mj2", ".mov", "mov"},
		{"mpegts", ".m2ts", "ts"},
		{"avi", ".avi", "avi"},
		{"", ".flv", "flv"},
	}
	for _, c := range cases {
		if got := Container(c.format, c.ext); got != c.want {
			t.Fatalf("%s %s = %q", c.format, c.ext, got)
		}
	}
}

func runner(t *testing.T) *Runner {
	t.Helper()
	path, err := Locate()
	if err != nil {
		t.Skip("FFprobe no está instalado")
	}
	return &Runner{Path: path, Timeout: 30 * time.Second}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("falta el fixture; genéralo con scripts/gen-fixtures")
	}
	return p
}

func TestRunConFFprobeReal(t *testing.T) {
	res, err := runner(t).Run(context.Background(), fixture(t, "mkv_hdr10_hevc.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if str(res.Streams[0].HDRFormat) != HDR10 {
		t.Fatalf("%+v", res.Streams[0])
	}
}

func TestRunArchivoCorrupto(t *testing.T) {
	_, err := runner(t).Run(context.Background(), fixture(t, "corrupt_truncated.mp4"))
	if !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("error = %v", err)
	}
}

// Un nombre que FFmpeg tomaría por protocolo debe abrirse como archivo.
func TestRunNombreConAspectoDeProtocolo(t *testing.T) {
	r := runner(t)
	src, err := os.ReadFile(fixture(t, "mp4_h264_aac.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "concat_a.mp4"
	if filepath.Separator == '/' {
		name = "concat:a.mp4"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}

func TestRunSinBinarioEsNoDisponible(t *testing.T) {
	r := &Runner{Path: filepath.Join(t.TempDir(), "no-existe"), Timeout: time.Second}
	_, err := r.Run(context.Background(), "x.mkv")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCanceladoDevuelveElContexto(t *testing.T) {
	r := runner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Run(ctx, fixture(t, "mp4_h264_aac.mp4"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunTiempoAgotadoEsArchivoInvalido(t *testing.T) {
	r := runner(t)
	for _, timeout := range []time.Duration{time.Nanosecond, -time.Second} {
		r.Timeout = timeout
		_, err := r.Run(context.Background(), fixture(t, "mp4_h264_aac.mp4"))
		if !errors.Is(err, ErrInvalidMedia) {
			t.Fatalf("%s: error = %v; un plazo vencido no es FFprobe ausente", timeout, err)
		}
	}
}

func TestRunSalidaDemasiadoGrande(t *testing.T) {
	r := runner(t)
	old := maxOutput
	maxOutput = 64
	defer func() { maxOutput = old }()
	_, err := r.Run(context.Background(), fixture(t, "mp4_h264_aac.mp4"))
	if !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("error = %v", err)
	}
}

func TestLimitedBufferMarcaElDesborde(t *testing.T) {
	b := &limitedBuffer{max: 4}
	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("def"))
	if !b.overflow || b.String() != "abcd" {
		t.Fatalf("%q %v", b.String(), b.overflow)
	}
	// io.Copy no debe encontrar un ReadFrom que esquive el límite.
	if _, ok := any(b).(io.ReaderFrom); ok {
		t.Fatal("limitedBuffer no debe implementar io.ReaderFrom")
	}
}
