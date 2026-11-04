package ffmpeg

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildPipeCopiandoElVideo(t *testing.T) {
	path := abs(t, "Película; rm -rf ~ $(x) 'a\".mkv")
	args, err := BuildPipe(PipeParams{Input: Input{Path: path, Video: 0, Audio: 1}, Audio: Audio{Copy: true}, CopyVideo: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-hide_banner -nostdin -loglevel error", "-i file:" + path, "-map 0:0 -map 0:1", "-c:v copy", "-c:a copy",
		"-sn -dn", "-muxdelay 0", "-flush_packets 1", "-f mpegts pipe:1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("falta %q en %s", want, joined)
		}
	}
	// La salida estándar es el vídeo: nada más puede escribir en ella.
	for _, bad := range []string{"-progress", "-ss ", "-vf", "-filter_complex", "libx264", "-t "} {
		if strings.Contains(joined, bad) {
			t.Errorf("no debe contener %q: %s", bad, joined)
		}
	}
	if args[len(args)-1] != "pipe:1" {
		t.Errorf("la última es %q", args[len(args)-1])
	}
}

func TestBuildPipeRecodificando(t *testing.T) {
	path := abs(t, "a.mkv")
	args, err := BuildPipe(PipeParams{
		Input: Input{Path: path, StartMS: 90_500, Video: 0, Audio: 2}, Encoder: X264, Height: 720, VideoBitrate: 4_000_000, ToneMap: TransferPQ,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 90.500 -i file:" + path, "-map 0:0 -map 0:2", "scale=-2:720", "tonemap=tonemap=hable", "-c:v libx264 -preset veryfast",
		"-b:v 4000000 -maxrate 6000000 -bufsize 8000000", "-force_key_frames expr:gte(t,n_forced*2)",
		"-c:a aac -ac 2 -b:a 192000", "-f mpegts pipe:1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("falta %q en %s", want, joined)
		}
	}
	if strings.Contains(joined, "-c:v copy") || strings.Contains(joined, "-progress") {
		t.Errorf("%s", joined)
	}
}

func TestBuildPipeConSubtitulosGrabados(t *testing.T) {
	path := abs(t, "a.mkv")
	text, err := BuildPipe(PipeParams{
		Input: Input{Path: path, StartMS: 30_000, Video: 0, Audio: 1}, Encoder: NVENC, VideoBitrate: 2_000_000, Height: 480,
		Burn: Burn{Kind: BurnText, File: "sub.ass"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(text, " ")
	// Tras el salto los fotogramas empiezan en cero y el subtítulo no: se compensa.
	for _, want := range []string{"setpts=PTS+30.000/TB,subtitles=sub.ass,setpts=PTS-STARTPTS", "scale=-2:480", "-c:v h264_nvenc"} {
		if !strings.Contains(joined, want) {
			t.Errorf("falta %q en %s", want, joined)
		}
	}
	image, err := BuildPipe(PipeParams{
		Input: Input{Path: path, Video: 0, Audio: 1}, Encoder: X264, VideoBitrate: 2_000_000, Burn: Burn{Kind: BurnImage, Stream: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(image, " ")
	if !strings.Contains(joined, "-filter_complex [0:0]null[a];[a][0:3]overlay=format=auto[b];[b]null[v]") || !strings.Contains(joined, "-map [v] -map 0:1") {
		t.Errorf("%s", joined)
	}
}

func TestBuildPipeSinAudio(t *testing.T) {
	args, err := BuildPipe(PipeParams{Input: Input{Path: abs(t, "a.mkv"), Video: 0, Audio: -1}, CopyVideo: true})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "-c:a") || strings.Contains(strings.Join(args, " "), "-map 0:-1") {
		t.Errorf("%v", args)
	}
}

func TestBuildPipeValida(t *testing.T) {
	ok := Input{Path: abs(t, "a.mkv"), Video: 0, Audio: 1}
	for name, p := range map[string]PipeParams{
		"ruta relativa":                  {Input: Input{Path: "a.mkv", Video: 0}, CopyVideo: true},
		"sin ruta":                       {Input: Input{Video: 0}, CopyVideo: true},
		"sin vídeo":                      {Input: Input{Path: ok.Path, Video: -1}, CopyVideo: true},
		"inicio negativo":                {Input: Input{Path: ok.Path, Video: 0, StartMS: -1}, CopyVideo: true},
		"recodificar sin bitrate":        {Input: ok, Encoder: X264},
		"filtros al copiar: tone map":    {Input: ok, CopyVideo: true, ToneMap: TransferPQ},
		"filtros al copiar: subtítulos":  {Input: ok, CopyVideo: true, Burn: Burn{Kind: BurnText, File: "s.ass"}},
		"filtros al copiar: escala":      {Input: ok, CopyVideo: true, Height: 720},
		"transferencia desconocida":      {Input: ok, VideoBitrate: 1, ToneMap: "otra"},
		"subtítulo con nombre peligroso": {Input: ok, VideoBitrate: 1, Burn: Burn{Kind: BurnText, File: "a,b:c.ass"}},
		"pista de imagen inválida":       {Input: ok, VideoBitrate: 1, Burn: Burn{Kind: BurnImage, Stream: -1}},
	} {
		if args, err := BuildPipe(p); err == nil {
			t.Errorf("%s: debía fallar: %v", name, args)
		}
	}
}
