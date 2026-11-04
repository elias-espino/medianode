package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func encodeParams(t *testing.T, mut func(*EncodeParams)) EncodeParams {
	t.Helper()
	p := EncodeParams{Input: Input{Path: abs(t, "a.mkv"), Video: 0, Audio: 1}, Encoder: X264, VideoBitrate: 1000,
		SegmentSeconds: 6, InitName: "init.mp4", Playlist: "p.m3u8"}
	if mut != nil {
		mut(&p)
	}
	return p
}

func TestVideoGraph(t *testing.T) {
	cases := []struct {
		name       string
		mut        func(*EncodeParams)
		simple     string
		complexHas []string
	}{
		{"sin filtros", nil, "", nil},
		{"solo escalado", func(p *EncodeParams) { p.Height = 720 }, "scale=-2:720", nil},
		{"escalar antes del tone mapping, que cuesta por píxel", func(p *EncodeParams) { p.ToneMap = TransferPQ; p.Height = 1080 },
			"scale=-2:1080," + toneMapFilter(TransferPQ), nil},
		{"tone mapping sin escalar", func(p *EncodeParams) { p.ToneMap = TransferHLG }, toneMapFilter(TransferHLG), nil},
		{"texto desde el principio", func(p *EncodeParams) { p.Burn = Burn{Kind: BurnText, File: "sub.ass"}; p.Height = 480 },
			"subtitles=sub.ass,scale=-2:480", nil},
		{"texto tras un salto desplaza el tiempo", func(p *EncodeParams) {
			p.Burn = Burn{Kind: BurnText, File: "sub.ass"}
			p.StartMS, p.StartSegment = 12000, 2
		}, "setpts=PTS+12.000/TB,subtitles=sub.ass,setpts=PTS-STARTPTS", nil},
		{"tone mapping y texto: primero el color", func(p *EncodeParams) { p.ToneMap = TransferHLG; p.Burn = Burn{Kind: BurnText, File: "s.ass"} },
			toneMapFilter(TransferHLG) + ",subtitles=s.ass", nil},
		{"imagen", func(p *EncodeParams) { p.Burn = Burn{Kind: BurnImage, Stream: 3}; p.Height = 720 }, "",
			[]string{"[0:0]null[a]", "[a][0:3]overlay=format=auto[b]", "[b]scale=-2:720[v]"}},
		{"texto con tone mapping y escalado: el color primero, el escalado al final", func(p *EncodeParams) {
			p.Burn = Burn{Kind: BurnText, File: "s.ass"}
			p.ToneMap, p.Height = TransferPQ, 720
		}, toneMapFilter(TransferPQ) + ",subtitles=s.ass,scale=-2:720", nil},
		{"imagen con tone mapping", func(p *EncodeParams) { p.Burn = Burn{Kind: BurnImage, Stream: 2}; p.ToneMap = TransferPQ }, "",
			[]string{"[0:0]setparams=", "[a][0:2]overlay", "[b]null[v]"}},
	}
	for _, c := range cases {
		g, err := videoGraph(encodeParams(t, c.mut))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if g.simple != c.simple {
			t.Errorf("%s: simple = %q, se esperaba %q", c.name, g.simple, c.simple)
		}
		for _, want := range c.complexHas {
			if !strings.Contains(g.complex, want) {
				t.Errorf("%s: complex = %q, falta %q", c.name, g.complex, want)
			}
		}
		if (len(c.complexHas) == 0) != (g.complex == "") {
			t.Errorf("%s: complex = %q", c.name, g.complex)
		}
	}
}

func TestVideoGraphRechaza(t *testing.T) {
	for name, mut := range map[string]func(*EncodeParams){
		"transferencia desconocida": func(p *EncodeParams) { p.ToneMap = "bt709" },
		"nombre con dos puntos":     func(p *EncodeParams) { p.Burn = Burn{Kind: BurnText, File: "c:sub.ass"} },
		"nombre con coma":           func(p *EncodeParams) { p.Burn = Burn{Kind: BurnText, File: "a,b.ass"} },
		"nombre con comilla":        func(p *EncodeParams) { p.Burn = Burn{Kind: BurnText, File: "a'b.ass"} },
		"nombre con ruta":           func(p *EncodeParams) { p.Burn = Burn{Kind: BurnText, File: "../x.ass"} },
		"nombre vacío":              func(p *EncodeParams) { p.Burn = Burn{Kind: BurnText} },
		"pista de imagen negativa":  func(p *EncodeParams) { p.Burn = Burn{Kind: BurnImage, Stream: -1} },
		"tipo desconocido":          func(p *EncodeParams) { p.Burn = Burn{Kind: BurnKind(9)} },
	} {
		if _, err := BuildEncode(encodeParams(t, mut)); err == nil {
			t.Errorf("%s: debía fallar", name)
		}
	}
}

func TestBuildEncodeConFiltros(t *testing.T) {
	args, err := BuildEncode(encodeParams(t, func(p *EncodeParams) { p.Height = 720; p.ToneMap = TransferPQ }))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-vf scale=-2:720,setparams=color_primaries=bt2020:color_trc=smpte2084") || strings.Contains(joined, "filter_complex") {
		t.Fatalf("%s", joined)
	}
	args, err = BuildEncode(encodeParams(t, func(p *EncodeParams) { p.Burn = Burn{Kind: BurnImage, Stream: 2} }))
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(args, " ")
	// El vídeo sale del grafo; no se mapea también directamente.
	if !strings.Contains(joined, "-map [v]") || strings.Contains(joined, "-map 0:0") || !strings.Contains(joined, "-map 0:1") ||
		strings.Contains(joined, " -vf ") {
		t.Fatalf("%s", joined)
	}
	if strings.Index(joined, "-map [v]") > strings.Index(joined, "-map 0:1") {
		t.Fatalf("el vídeo debe ir antes que el audio: %s", joined)
	}
}

func TestParseFilters(t *testing.T) {
	out := []byte(` T. = Timeline support
 ... subtitles         V->V       Render text subtitles
 TS overlay           VV->V      Overlay a video source
 .S zscale            V->V       Apply resizing
 ... anullsrc         |->A       Null audio source
`)
	got := parseFilters(out)
	for _, name := range []string{"subtitles", "overlay", "zscale", "anullsrc"} {
		if !got[name] {
			t.Errorf("falta %s: %v", name, got)
		}
	}
	// La leyenda («T. = Timeline support») no son filtros.
	if got["Timeline"] || got["T."] || got["="] || len(got) != 4 {
		t.Errorf("la leyenda no es un filtro: %v", got)
	}
}

func needFFmpeg(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("necesita FFmpeg")
	}
	path, err := Locate()
	if err != nil {
		t.Skip("FFmpeg no está instalado")
	}
	return path
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

func TestExtractSubtitleReal(t *testing.T) {
	ff := needFFmpeg(t)
	dir := t.TempDir()
	vtt := filepath.Join(dir, "a", "t.vtt")
	if err := ExtractSubtitle(context.Background(), ff, fixture(t, "mkv_srt_internal.mkv"), 2, FormatWebVTT, vtt); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(vtt)
	if !strings.HasPrefix(string(data), "WEBVTT") || !strings.Contains(string(data), "-->") {
		t.Fatalf("no es WebVTT:\n%s", data)
	}
	ass := filepath.Join(dir, "t.ass")
	if err := ExtractSubtitle(context.Background(), ff, fixture(t, "mkv_ass_internal.mkv"), 2, FormatASS, ass); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(ass)
	if !strings.Contains(string(data), "[Events]") {
		t.Fatalf("no es ASS:\n%s", data)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("no deben quedar temporales: %v", entries)
	}
	// Una pista que no existe falla y no deja archivo.
	bad := filepath.Join(dir, "x.vtt")
	if err := ExtractSubtitle(context.Background(), ff, fixture(t, "mkv_srt_internal.mkv"), 9, FormatWebVTT, bad); err == nil {
		t.Fatal("una pista inexistente debía fallar")
	}
	for _, p := range []string{bad, bad + ".tmp"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("quedó %s", p)
		}
	}
}

func TestExtractSubtitleRechaza(t *testing.T) {
	ctx := context.Background()
	if err := ExtractSubtitle(ctx, "ffmpeg", "relativa.mkv", 0, FormatWebVTT, abs(t, "o.vtt")); err == nil {
		t.Error("entrada relativa")
	}
	if err := ExtractSubtitle(ctx, "ffmpeg", abs(t, "a.mkv"), 0, FormatWebVTT, "o.vtt"); err == nil {
		t.Error("salida relativa")
	}
	// Los parámetros se validan antes de lanzar FFmpeg: con un ejecutable que no
	// existe, el error tiene que ser el de validación, no el de arrancarlo.
	if err := ExtractSubtitle(ctx, "no-existe-ffmpeg", abs(t, "a.mkv"), -1, FormatWebVTT, abs(t, "o.vtt")); err == nil || !strings.Contains(err.Error(), "no válidos") {
		t.Errorf("pista negativa: %v", err)
	}
	if err := ExtractSubtitle(ctx, "no-existe-ffmpeg", abs(t, "a.mkv"), 0, "srt", abs(t, "o.vtt")); err == nil || !strings.Contains(err.Error(), "no válidos") {
		t.Errorf("formato no admitido: %v", err)
	}
}

// El tone mapping produce un SDR de verdad: BT.709 y 8 bits. Se codifica un
// segundo del fixture HDR10 con la misma cadena que usa el servidor.
func TestToneMapReal(t *testing.T) {
	ff := needFFmpeg(t)
	if err := TestToneMap(context.Background(), ff); err != nil {
		t.Skipf("este FFmpeg no tiene tone mapping: %v", err)
	}
	out := filepath.Join(t.TempDir(), "sdr.mp4")
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", "file:" + fixture(t, "mkv_hdr10_hevc.mkv"),
		"-t", "1", "-vf", toneMapFilter(TransferPQ), "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-an", out}
	if b, err := exec.Command(ff, args...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	probe, _ := exec.LookPath("ffprobe")
	if probe == "" {
		return
	}
	b, err := exec.Command(probe, "-v", "error", "-select_streams", "v:0", "-show_entries",
		"stream=pix_fmt,color_transfer,color_primaries", "-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "yuv420p,bt709,bt709" && got != "yuv420p,bt709,bt709," {
		t.Fatalf("metadatos de color de la salida: %q", got)
	}
}
