// gen-fixtures genera con FFmpeg los archivos de prueba de la matriz §23.3.
//
// Los archivos son diminutos y no se versionan. PGS y VobSub no se generan
// porque FFmpeg no puede crearlos desde lavfi: necesitan muestras reales.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const srtBody = `1
00:00:00,500 --> 00:00:02,000
Subtítulo de prueba con acentos: áéíóú

2
00:00:02,500 --> 00:00:04,000
Segunda línea
`

const assBody = `[Script Info]
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, Alignment
Style: Default,Arial,24,&H00FFFFFF,2

[Events]
Format: Layer, Start, End, Style, Text
Dialogue: 0,0:00:00.50,0:00:04.00,Default,Subtítulo ASS de prueba
`

type fixture struct {
	name string
	args []string
}

// source devuelve las entradas de video y audio sintéticos.
func source(size string, seconds int) []string {
	return []string{
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%s:rate=24:duration=%d", size, seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d", seconds),
	}
}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func fixtures(srt, ass string) []fixture {
	h264 := []string{"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p"}
	aac := []string{"-c:a", "aac", "-b:a", "96k"}

	return []fixture{
		{"mp4_h264_aac.mp4", join(source("640x360", 4), h264, aac)},
		{"mkv_h264_aac.mkv", join(source("640x360", 4), h264, aac)},
		{"mkv_hevc_aac.mkv", join(source("640x360", 4),
			[]string{"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error"}, aac)},
		{"mkv_multi_audio.mkv", join(source("640x360", 4),
			[]string{"-f", "lavfi", "-i", "sine=frequency=880:duration=4"},
			[]string{"-map", "0:v", "-map", "1:a", "-map", "2:a"}, h264, aac,
			[]string{"-metadata:s:a:0", "language=spa", "-metadata:s:a:1", "language=eng"})},
		{"mkv_srt_internal.mkv", join(source("640x360", 4), []string{"-i", srt},
			[]string{"-map", "0:v", "-map", "1:a", "-map", "2:s"}, h264, aac,
			[]string{"-c:s", "srt", "-metadata:s:s:0", "language=spa"})},
		{"mkv_ass_internal.mkv", join(source("640x360", 4), []string{"-i", ass},
			[]string{"-map", "0:v", "-map", "1:a", "-map", "2:s"}, h264, aac,
			[]string{"-c:s", "ass"})},
		{"mkv_h264_ac3.mkv", join(source("640x360", 4), h264,
			[]string{"-c:a", "ac3", "-b:a", "192k"})},
		{"mkv_h264_dts.mkv", join(source("640x360", 4), h264,
			[]string{"-c:a", "dca", "-strict", "-2", "-ar", "48000", "-ac", "2"})},
		{"mkv_4k_h264.mkv", join(source("3840x2160", 2), h264, aac)},
		{"mkv_hdr10_hevc.mkv", join(source("1280x720", 3),
			[]string{"-vf", "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,format=yuv420p10le",
				"-c:v", "libx265", "-preset", "ultrafast",
				"-x265-params", "log-level=error:hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc"}, aac)},
		{"seek_120s_h264.mkv", join(source("640x360", 120), h264,
			[]string{"-g", "240"}, aac)},
	}
}

func run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// truncate crea un archivo corrupto a partir de la primera mitad de otro.
func truncate(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data[:len(data)/2], 0o644)
}

func main() {
	out := flag.String("out", "testdata/fixtures", "directorio de salida")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	srt := filepath.Join(*out, "mp4_h264_aac.es.srt")
	ass := filepath.Join(*out, "sample.ass")
	for path, body := range map[string]string{srt: srtBody, ass: assBody} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	ctx := context.Background()
	failed := 0
	for _, f := range fixtures(srt, ass) {
		path := filepath.Join(*out, f.name)
		if err := run(ctx, append(f.args, path)...); err != nil {
			fmt.Printf("FALLA %s: %v\n", f.name, err)
			failed++
			continue
		}
		fmt.Println("OK   ", f.name)
	}

	if err := truncate(filepath.Join(*out, "mp4_h264_aac.mp4"), filepath.Join(*out, "corrupt_truncated.mp4")); err != nil {
		fmt.Println("FALLA corrupt_truncated.mp4:", err)
		failed++
	} else {
		fmt.Println("OK    corrupt_truncated.mp4")
	}

	if failed > 0 {
		os.Exit(1)
	}
}
