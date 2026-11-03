package protocol

import (
	"slices"
	"testing"
)

func TestNormalizeCompletaConLaPlataforma(t *testing.T) {
	got, err := ClientProfile{Platform: " WEB "}.Normalize(nil)
	if err != nil {
		t.Fatal(err)
	}
	web := PresetFor("web")
	if got.Platform != "web" || !slices.Equal(got.Containers, web.Containers) || got.MaxBitDepth != 8 {
		t.Fatalf("%+v", got)
	}
}

func TestNormalizeRespetaLoDeclarado(t *testing.T) {
	in := ClientProfile{
		Platform: "web", VideoCodecs: []string{"HEVC", "h264", "h264", "inventado"}, Containers: []string{},
		MaxResolution: "1280x720",
	}
	got, err := in.Normalize(map[string]bool{"video_codecs": true, "containers": true})
	if err != nil {
		t.Fatal(err)
	}
	// Lo declarado manda, en minúsculas, sin repetidos ni desconocidos;
	// declarar una lista vacía es no admitir nada.
	if !slices.Equal(got.VideoCodecs, []string{"hevc", "h264"}) || len(got.Containers) != 0 || got.MaxResolution != "1280x720" {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(got.AudioCodecs, PresetFor("web").AudioCodecs) {
		t.Fatal("lo no declarado viene de la plataforma")
	}
}

func TestNormalizeRechaza(t *testing.T) {
	for _, c := range []ClientProfile{
		{MaxResolution: "grande"},
		{MaxResolution: "0x1080"},
		{MaxBitDepth: 4},
		{MaxBitDepth: 32},
		{MaxAudioChannels: 99},
	} {
		if _, err := c.Normalize(nil); err == nil {
			t.Errorf("%+v debía fallar", c)
		}
	}
	if _, err := (ClientProfile{Protocols: []string{"rtsp"}}).Normalize(map[string]bool{"protocols": true}); err == nil {
		t.Error("sin ningún protocolo conocido debía fallar")
	}
}

func TestPlataformaDesconocida(t *testing.T) {
	p := PresetFor("nevera")
	if !slices.Equal(p.VideoCodecs, []string{"h264"}) || !slices.Equal(p.Protocols, []string{"http"}) {
		t.Fatalf("%+v", p)
	}
}

func TestCodecName(t *testing.T) {
	for in, want := range map[string]string{
		"subrip": "srt", "webvtt": "vtt", "hdmv_pgs_subtitle": "pgs", "dvd_subtitle": "vobsub",
		"dvb_subtitle": "dvbsub", "mpeg2video": "mpeg2", "pcm_s24le": "pcm", "H264": "h264", "m4v": "mp4", "ass": "ass",
	} {
		if got := CodecName(in); got != want {
			t.Errorf("%s = %s", in, got)
		}
	}
	for in, want := range map[string]string{"hdr10": "hdr10", "HDR10+": "hdr10", "hlg": "hlg", "dolby_vision": "dolby_vision"} {
		if got := HDRName(in); got != want {
			t.Errorf("HDR %s = %s", in, got)
		}
	}
}
