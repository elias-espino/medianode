package routes

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"medianode/internal/ffmpeg"
)

// surround5_1 genera un MKV corto con audio AAC de seis canales.
func surround5_1(t *testing.T) []byte {
	t.Helper()
	ff, err := ffmpeg.Locate()
	if err != nil {
		t.Skip("FFmpeg no está instalado")
	}
	out := filepath.Join(t.TempDir(), "surround.mkv")
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "6", "-y", out}
	if b, err := exec.Command(ff, args...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func audioChannels(t *testing.T, seg []byte) int {
	t.Helper()
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("falta ffprobe")
	}
	p := filepath.Join(t.TempDir(), "s.ts")
	if err := os.WriteFile(p, seg, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(probe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=channels", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]))
	return n
}

// Un 5.1 se mezcla a estéreo cuando el cliente solo admite dos canales, y se
// copia tal cual cuando admite seis.
func TestAudioSurroundSeMezclaOSeCopia(t *testing.T) {
	e, own, m := phase9Env(t, 1, 2)
	layout := []streamRow{{typ: "video", codec: "h264", w: 320, h: 180, depth: 8}, {typ: "audio", codec: "aac", lang: "spa", channels: 6}}
	m.addFile("Surround.mkv", surround5_1(t), layout)

	stereo := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": profileWith(map[string]any{"max_audio_channels": 2})})
	if stereo["audio_action"] != "transcode" || stereo["video_action"] != "copy" || stereo["playback_mode"] != "transcode" {
		t.Fatalf("%v", stereo)
	}
	var reasons []string
	for _, r := range stereo["reasons"].([]any) {
		reasons = append(reasons, str(r.(map[string]any)["code"]))
	}
	if !contains(reasons, "audio_channels_unsupported") {
		t.Fatalf("falta audio_channels_unsupported: %v", reasons)
	}
	base := strings.TrimSuffix(str(stereo["stream_url"]), "master.m3u8")
	rec := e.get(base+"seg_00000.ts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("segmento: %d", rec.Code)
	}
	if n := audioChannels(t, rec.Body.Bytes()); n != 2 {
		t.Fatalf("canales tras la mezcla = %d, se esperaban 2", n)
	}
	e.closeSession(own, stereo)

	surround := e.session(own, map[string]any{"media_item_id": m.item, "client_profile": profileWith(map[string]any{"max_audio_channels": 6})})
	if surround["audio_action"] != "copy" {
		t.Fatalf("%v", surround)
	}
	base = strings.TrimSuffix(str(surround["stream_url"]), "master.m3u8")
	rec = e.get(base+"seg_00000.ts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("segmento: %d", rec.Code)
	}
	if n := audioChannels(t, rec.Body.Bytes()); n != 6 {
		t.Fatalf("canales copiados = %d, se esperaban 6", n)
	}
}
