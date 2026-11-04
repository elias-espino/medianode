package ffmpeg

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func abs(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildEncode(t *testing.T) {
	path := abs(t, "Película; rm -rf ~ $(x) 'a\".mkv")
	args, err := BuildEncode(EncodeParams{
		Input:   Input{Path: path, StartMS: 60_000, Video: 0, Audio: 2},
		Encoder: X264, Height: 720, VideoBitrate: 4_000_000, SegmentSeconds: 6, StartSegment: 10,
		InitName: "init_3.mp4", Playlist: "ffmpeg.m3u8",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 60.000 -i file:" + path, "-map 0:0 -map 0:2", "-vf scale=-2:720", "-c:v libx264 -preset veryfast",
		"-b:v 4000000 -maxrate 6000000 -bufsize 8000000", "-force_key_frames expr:gte(t,n_forced*6)",
		"-c:a aac -ac 2 -b:a 192000", "-output_ts_offset 60.000", "-hls_time 6", "-hls_segment_type fmp4",
		"-hls_fmp4_init_filename init_3.mp4", "-hls_flags temp_file+independent_segments",
		"-hls_segment_options movflags=+frag_discont", "-start_number 10",
		"-hls_segment_filename seg_%05d.m4s ffmpeg.m3u8",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("falta %q en %s", want, joined)
		}
	}
	// El nombre con caracteres especiales es un único argumento.
	if !slices.Contains(args, "file:"+path) {
		t.Fatal("la ruta debe ir entera en un solo argumento")
	}
}

func TestBuildEncodeSinAudioYDesdeElPrincipio(t *testing.T) {
	args, err := BuildEncode(EncodeParams{
		Input: Input{Path: abs(t, "a.mkv"), Video: 0, Audio: -1}, Encoder: NVENC, VideoBitrate: 1_000_000,
		SegmentSeconds: 6, InitName: "init_0.mp4", Playlist: "ffmpeg.m3u8",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-ss ") || strings.Contains(joined, "-c:a") || strings.Contains(joined, "-vf") ||
		!strings.Contains(joined, "-c:v h264_nvenc -preset p4") {
		t.Fatal(joined)
	}
}

func TestBuildEncodeRechaza(t *testing.T) {
	ok := EncodeParams{Input: Input{Path: abs(t, "a.mkv"), Video: 0, Audio: 1}, Encoder: X264, VideoBitrate: 1,
		SegmentSeconds: 6, InitName: "init.mp4", Playlist: "p.m3u8"}
	for name, mut := range map[string]func(*EncodeParams){
		"ruta relativa":            func(p *EncodeParams) { p.Path = "a.mkv" },
		"sin vídeo":                func(p *EncodeParams) { p.Video = -1 },
		"inicio fuera de rejilla":  func(p *EncodeParams) { p.StartMS = 5000 },
		"init con ruta":            func(p *EncodeParams) { p.InitName = "../init.mp4" },
		"init es ..":               func(p *EncodeParams) { p.InitName = ".." },
		"lista es .":               func(p *EncodeParams) { p.Playlist = "." },
		"lista absoluta":           func(p *EncodeParams) { p.Playlist = abs(t, "x.m3u8") },
		"sin bitrate":              func(p *EncodeParams) { p.VideoBitrate = 0 },
		"segmento inicial erróneo": func(p *EncodeParams) { p.StartSegment = 2 },
	} {
		p := ok
		mut(&p)
		if _, err := BuildEncode(p); err == nil {
			t.Errorf("%s: debía fallar", name)
		}
	}
}

func TestBuildCopy(t *testing.T) {
	path := abs(t, "a.mkv")
	args, err := BuildCopy(CopyParams{
		Input: Input{Path: path, Video: 0, Audio: 1}, Audio: Audio{Copy: true},
		SeekUS: 36_520_500, OffsetUS: 36_520_000, CutsRelUS: []int64{7_479_000, 13_599_000, 18_479_000},
		StartSegment: 6, List: "list.csv",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 36.520500 -i file:" + path, "-c:v copy -c:a copy", "-output_ts_offset 36.520000 -muxdelay 0 -muxpreload 0",
		"-f segment -segment_format mpegts", "-segment_times 7.479000,13.599000,18.479000", "-segment_start_number 6",
		"-segment_list list.csv -segment_list_type csv seg_%05d.ts",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("falta %q en %s", want, joined)
		}
	}
	if _, err := BuildCopy(CopyParams{Input: Input{Path: path, Video: 0, Audio: -1},
		CutsRelUS: []int64{0}, List: "l.csv"}); err == nil {
		t.Fatal("un corte que no es posterior al inicio debía fallar")
	}
	args, _ = BuildCopy(CopyParams{Input: Input{Path: path, Video: 0, Audio: 1}, SegmentSeconds: 6, List: "l.csv"})
	if j := strings.Join(args, " "); strings.Contains(j, "-segment_times") || !strings.Contains(j, "-segment_time 6") ||
		strings.Contains(j, "-ss ") || !strings.Contains(j, "-c:a aac") {
		t.Fatalf("sin índice, cortes cada segmento; el audio por defecto se recodifica: %s", j)
	}
}

func TestParseEncodersYChoose(t *testing.T) {
	out := []byte(" V....D libx264              libx264 H.264\n V....D h264_nvenc           NVIDIA\n A....D aac   AAC\n V....D h264_qsv  QSV\n")
	got := parseEncoders(out)
	if !got[X264] || !got[NVENC] || !got[QSV] || got["aac"] {
		t.Fatalf("%v", got)
	}
	c := &Capabilities{Working: []Encoder{QSV, X264}}
	for setting, want := range map[string]Encoder{"auto": QSV, "none": X264, "qsv": QSV, "nvenc": X264, "": X264} {
		if got := c.Choose(setting); got != want {
			t.Errorf("%q = %s", setting, got)
		}
	}
	if (*Capabilities)(nil).Choose("auto") != X264 {
		t.Fatal("sin capacidades, CPU")
	}
}

// Con FFmpeg instalado: detecta la versión y que libx264 funciona.
func TestProbeReal(t *testing.T) {
	path, err := Locate()
	if err != nil {
		t.Skip("FFmpeg no está instalado")
	}
	c, err := Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version == "" || !slices.Contains(c.Working, X264) {
		t.Fatalf("%+v", c)
	}
	if err := TestEncoder(context.Background(), path, "codificador_inventado"); err == nil {
		t.Fatal("un codificador inexistente debía fallar")
	}
	t.Logf("FFmpeg %s, codificadores que funcionan: %v", c.Version, c.Working)
}
