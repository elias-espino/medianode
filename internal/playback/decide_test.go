package playback

import (
	"strings"
	"testing"

	"medianode/internal/protocol"
)

func i64(n int64) *int64   { return &n }
func str(s string) *string { return &s }

func h264(w, h int64) *Video {
	return &Video{Codec: "h264", Width: i64(w), Height: i64(h), BitDepth: i64(8)}
}
func aac() *Audio { return &Audio{Codec: "aac", Channels: i64(2)} }

func preset(platform string) protocol.ClientProfile { return protocol.PresetFor(platform) }

func with(c protocol.ClientProfile, fn func(*protocol.ClientProfile)) protocol.ClientProfile {
	fn(&c)
	return c
}

type want struct {
	mode     Mode
	protocol string
	video    Action
	audio    Action
	sub      SubtitleMode
	reasons  string // Códigos separados por coma, en orden.
}

func TestDecide(t *testing.T) {
	web, android, ios, tizen := preset("web"), preset("android"), preset("ios"), preset("tizen")
	hevc10 := &Video{Codec: "hevc", Width: i64(3840), Height: i64(2160), BitDepth: i64(10), HDR: str("hdr10")}
	dts := &Audio{Codec: "dts", Channels: i64(6)}
	ac3six := &Audio{Codec: "ac3", Channels: i64(6)}
	srtExt := &Subtitle{Format: "srt", External: true}
	srtInt := &Subtitle{Format: "subrip"}
	assExt := &Subtitle{Format: "ass", External: true}
	assInt := &Subtitle{Format: "ass"}
	vttExt := &Subtitle{Format: "vtt", External: true}
	pgs := &Subtitle{Format: "hdmv_pgs_subtitle"}
	vobsub := &Subtitle{Format: "dvd_subtitle"}
	movText := &Subtitle{Format: "mov_text"}
	hlsOnly := with(android, func(c *protocol.ClientProfile) { c.Protocols = []string{"hls"} })

	cases := []struct {
		name string
		in   Input
		want want
	}{
		// Direct Play.
		{"mp4 h264 aac en web", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Client: web},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"m4v cuenta como mp4", Input{Container: "m4v", Video: h264(1280, 720), Audio: aac(), Client: web},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"mkv en android", Input{Container: "mkv", Video: h264(1920, 1080), Audio: ac3six, Client: android},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"webm vp9 opus en web", Input{Container: "webm", Video: &Video{Codec: "vp9", BitDepth: i64(8)}, Audio: &Audio{Codec: "opus"}, Client: web},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"solo audio", Input{Container: "mp4", Audio: aac(), Client: web},
			want{DirectPlay, "http", NoTrack, Copy, SubNone, ""}},
		{"solo video", Input{Container: "mp4", Video: h264(640, 360), Client: web},
			want{DirectPlay, "http", Copy, NoTrack, SubNone, ""}},
		{"hevc hdr10 en ios", Input{Container: "mp4", Video: hevc10, Audio: aac(), Client: ios},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"srt externo en web se convierte", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Subtitle: srtExt, Client: web},
			want{DirectPlay, "http", Copy, Copy, SubConvert, ""}},
		{"vtt externo en web va aparte", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Subtitle: vttExt, Client: web},
			want{DirectPlay, "http", Copy, Copy, SubSidecar, ""}},
		{"srt externo en android va aparte", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Subtitle: srtExt, Client: android},
			want{DirectPlay, "http", Copy, Copy, SubSidecar, ""}},
		{"srt interno en android lo dibuja el reproductor", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Subtitle: srtInt, Client: android},
			want{DirectPlay, "http", Copy, Copy, SubEmbedded, ""}},
		{"pgs interno en android", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Subtitle: pgs, Client: android},
			want{DirectPlay, "http", Copy, Copy, SubEmbedded, ""}},
		{"ass externo en android", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Subtitle: assExt, Client: android},
			want{DirectPlay, "http", Copy, Copy, SubSidecar, ""}},
		{"srt externo en tizen", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Subtitle: srtExt, Client: tizen},
			want{DirectPlay, "http", Copy, Copy, SubSidecar, ""}},
		{"srt interno en tizen se extrae", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Subtitle: srtInt, Client: tizen},
			want{DirectPlay, "http", Copy, Copy, SubConvert, ""}},
		{"ass interno en web se convierte", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Subtitle: assInt, Client: web},
			want{DirectPlay, "http", Copy, Copy, SubConvert, ""}},
		{"mov_text en web se convierte", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Subtitle: movText, Client: web},
			want{DirectPlay, "http", Copy, Copy, SubConvert, ""}},
		{"dentro de los límites", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Bitrate: i64(5e6), MaxBitrate: i64(8e6), MaxResolution: "1920x1080", Client: web},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"sin dimensiones no hay límite que comprobar", Input{Container: "mp4", Video: &Video{Codec: "h264"}, Audio: aac(), MaxResolution: "640x360", Client: web},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"sin bitrate conocido no hay límite que comprobar", Input{Container: "mp4", Video: h264(640, 360), Audio: aac(), MaxBitrate: i64(1), Client: web},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},

		// Direct Stream: solo cambia el contenedor o la entrega.
		{"mkv en web", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Client: web},
			want{DirectStream, "http", Copy, Copy, SubNone, ReasonContainer}},
		{"mkv en ios va por hls", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Client: ios},
			want{DirectStream, "hls", Copy, Copy, SubNone, ReasonContainer}},
		{"srt externo en avplayer va por hls", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Subtitle: srtExt, Client: ios},
			want{DirectStream, "hls", Copy, Copy, SubHLS, ReasonSubtitleHLS}},
		{"avi en android", Input{Container: "avi", Video: h264(720, 480), Audio: &Audio{Codec: "mp3"}, Client: android},
			want{DirectStream, "hls", Copy, Copy, SubNone, ReasonContainer}},
		{"cliente sin http", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Client: hlsOnly},
			want{DirectStream, "hls", Copy, Copy, SubNone, ReasonNoHTTP}},
		{"srt interno en mkv para web: remux y conversión", Input{Container: "mkv", Video: h264(1920, 1080), Audio: aac(), Subtitle: srtInt, Client: web},
			want{DirectStream, "http", Copy, Copy, SubConvert, ReasonContainer}},
		// Sin el archivo original, la pista interna hay que extraerla.
		{"srt interno sin direct play deja de ir embebido", Input{Container: "avi", Video: h264(1920, 1080), Audio: aac(), Subtitle: srtInt, Client: android},
			want{DirectStream, "hls", Copy, Copy, SubConvert, ReasonContainer}},

		// Transcodificación por pista.
		{"dts en web: audio sí, video no", Input{Container: "mp4", Video: h264(1920, 1080), Audio: dts, Client: web},
			want{Transcode, "http", Copy, Recode, SubNone, ReasonAudioCodec}},
		{"dts en android sin dts", Input{Container: "mkv", Video: h264(1920, 1080), Audio: dts, Client: android},
			want{Transcode, "hls", Copy, Recode, SubNone, ReasonAudioCodec}},
		{"5.1 en web de dos canales", Input{Container: "mp4", Video: h264(1920, 1080), Audio: &Audio{Codec: "aac", Channels: i64(6)}, Client: web},
			want{Transcode, "http", Copy, Recode, SubNone, ReasonAudioChannels}},
		{"hevc en web", Input{Container: "mp4", Video: &Video{Codec: "hevc", BitDepth: i64(8)}, Audio: aac(), Client: web},
			want{Transcode, "http", Recode, Copy, SubNone, ReasonVideoCodec}},
		{"h264 de 10 bits en web", Input{Container: "mp4", Video: &Video{Codec: "h264", BitDepth: i64(10)}, Audio: aac(), Client: web},
			want{Transcode, "http", Recode, Copy, SubNone, ReasonBitDepth}},
		// El HDR no debe quedar oculto por otro motivo: sin el aviso, se recodificaría
		// sin tone mapping y el color saldría lavado.
		{"hevc hdr10 en web: ni códec ni HDR", Input{Container: "mp4", Video: hevc10, Audio: aac(), Client: web},
			want{Transcode, "http", Recode, Copy, SubNone, ReasonVideoCodec + "," + ReasonHDR}},
		{"h264 hlg de 10 bits en web: ni profundidad ni HDR", Input{Container: "mp4", Video: &Video{Codec: "h264", BitDepth: i64(10), HDR: str("hlg")}, Audio: aac(), Client: web},
			want{Transcode, "http", Recode, Copy, SubNone, ReasonBitDepth + "," + ReasonHDR}},
		{"hevc hdr10 de 4K en android sin HDR: solo el HDR", Input{Container: "mkv", Video: hevc10, Audio: aac(), Client: android},
			want{Transcode, "hls", Recode, Copy, SubNone, ReasonHDR}},
		{"sdr de 10 bits en web: sin HDR que convertir", Input{Container: "mp4", Video: &Video{Codec: "h264", BitDepth: i64(10)}, Audio: aac(), Client: web},
			want{Transcode, "http", Recode, Copy, SubNone, ReasonBitDepth}},
		{"hdr10 en tizen sí", Input{Container: "mkv", Video: hevc10, Audio: aac(), Client: tizen},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"hdr10 en android sin hdr", Input{Container: "mkv", Video: hevc10, Audio: aac(), Client: android},
			want{Transcode, "hls", Recode, Copy, SubNone, ReasonHDR}},
		{"dolby vision en tizen", Input{Container: "mkv", Video: &Video{Codec: "hevc", BitDepth: i64(10), HDR: str("dolby_vision")}, Audio: aac(), Client: tizen},
			want{Transcode, "hls", Recode, Copy, SubNone, ReasonHDR}},
		{"pgs en web se quema", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Subtitle: pgs, Client: web},
			want{Transcode, "http", Recode, Copy, SubBurnIn, ReasonSubtitleBurnIn}},
		// Android acepta pistas internas, pero no VobSub.
		{"vobsub interno en android se quema", Input{Container: "mkv", Video: h264(720, 480), Audio: aac(), Subtitle: vobsub, Client: android},
			want{Transcode, "hls", Recode, Copy, SubBurnIn, ReasonSubtitleBurnIn}},
		{"vobsub en ios se quema", Input{Container: "mp4", Video: h264(720, 480), Audio: aac(), Subtitle: vobsub, Client: ios},
			want{Transcode, "hls", Recode, Copy, SubBurnIn, ReasonSubtitleBurnIn}},
		{"pgs en avi para android se quema", Input{Container: "avi", Video: h264(1920, 1080), Audio: aac(), Subtitle: pgs, Client: android},
			want{Transcode, "hls", Recode, Copy, SubBurnIn, ReasonSubtitleBurnIn + "," + ReasonContainer}},
		{"4k sobre límite 1080p", Input{Container: "mp4", Video: h264(3840, 2160), Audio: aac(), MaxResolution: "1920x1080", Client: web},
			want{Transcode, "http", Recode, Copy, SubNone, ReasonResolution}},
		{"límite del perfil", Input{Container: "mp4", Video: h264(3840, 2160), Audio: aac(),
			Client: with(web, func(c *protocol.ClientProfile) { c.MaxResolution = "1280x720" })},
			want{Transcode, "http", Recode, Copy, SubNone, ReasonResolution}},
		{"bitrate sobre el máximo", Input{Container: "mp4", Video: h264(1920, 1080), Audio: aac(), Bitrate: i64(20e6), MaxBitrate: i64(8e6), Client: web},
			want{Transcode, "http", Recode, Copy, SubNone, ReasonBitrate}},
		{"todo incompatible", Input{Container: "avi", Video: &Video{Codec: "mpeg4"}, Audio: dts, Client: web},
			want{Transcode, "http", Recode, Recode, SubNone, ReasonVideoCodec + "," + ReasonAudioCodec + "," + ReasonContainer}},
		{"mpeg2video se traduce y no está en web", Input{Container: "ts", Video: &Video{Codec: "mpeg2video"}, Audio: &Audio{Codec: "ac3"}, Client: web},
			want{Transcode, "http", Recode, Recode, SubNone, ReasonVideoCodec + "," + ReasonAudioCodec + "," + ReasonContainer}},
		{"pcm se traduce", Input{Container: "mkv", Video: h264(1920, 1080), Audio: &Audio{Codec: "pcm_s16le"},
			Client: with(android, func(c *protocol.ClientProfile) { c.AudioCodecs = append(c.AudioCodecs, "pcm") })},
			want{DirectPlay, "http", Copy, Copy, SubNone, ""}},
		{"perfil vacío: solo http", Input{Container: "mp4", Video: h264(640, 360), Audio: aac(), Client: protocol.ClientProfile{Protocols: []string{"http"}, MaxBitDepth: 8, MaxAudioChannels: 2}},
			want{Transcode, "http", Recode, Recode, SubNone, ReasonVideoCodec + "," + ReasonAudioCodec + "," + ReasonContainer}},
		{"subtítulo de texto sin ninguna entrega se quema", Input{Container: "mp4", Video: h264(640, 360), Audio: aac(), Subtitle: srtExt,
			Client: with(web, func(c *protocol.ClientProfile) { c.SubtitleDelivery = nil })},
			want{Transcode, "http", Recode, Copy, SubBurnIn, ReasonSubtitleBurnIn}},
		// Restricciones del contenedor HLS de salida.
		{"vp9 en webm para android va por hls y no cabe en ts", Input{Container: "webm", Video: &Video{Codec: "vp9", BitDepth: i64(8)}, Audio: &Audio{Codec: "opus"},
			Client: with(android, func(c *protocol.ClientProfile) { c.Containers = []string{"mp4"} })},
			want{Transcode, "hls", Recode, Copy, SubNone, ReasonContainer + "," + ReasonVideoContainer}},
		{"opus copiado no cabe en ts", Input{Container: "mkv", Video: h264(1920, 1080), Audio: &Audio{Codec: "opus"}, Client: ios},
			want{Transcode, "hls", Copy, Recode, SubNone, ReasonAudioCodec + "," + ReasonContainer}},
		{"opus en ts sí se recodifica aunque el cliente lo admita", Input{Container: "avi", Video: h264(1920, 1080), Audio: &Audio{Codec: "opus"}, Client: android},
			want{Transcode, "hls", Copy, Recode, SubNone, ReasonContainer + "," + ReasonAudioContainer}},
		{"flac con vídeo recodificado va en fmp4", Input{Container: "mkv", Video: &Video{Codec: "mpeg4"}, Audio: &Audio{Codec: "flac"}, Client: android},
			want{Transcode, "hls", Recode, Copy, SubNone, ReasonVideoCodec}},
	}
	if len(cases) < 40 {
		t.Fatalf("solo hay %d casos", len(cases))
	}
	for _, c := range cases {
		d := Decide(c.in)
		var codes []string
		for _, r := range d.Reasons {
			codes = append(codes, r.Code)
		}
		got := want{d.Mode, d.Protocol, d.VideoAction, d.AudioAction, d.Subtitle, strings.Join(codes, ",")}
		if got != c.want {
			t.Errorf("%s:\n got  %+v\n want %+v", c.name, got, c.want)
		}
		if (d.Mode == DirectPlay) != (len(d.Reasons) == 0) {
			t.Errorf("%s: Direct Play sin motivos y el resto con ellos: %+v", c.name, d)
		}
	}
}
