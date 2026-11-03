package transcoding

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func starts(segs []Segment) []int64 {
	out := make([]int64, len(segs))
	for i, s := range segs {
		out[i] = s.StartUS
	}
	return out
}

func TestEncodeSegments(t *testing.T) {
	segs := EncodeSegments(20_500_000, 6)
	if len(segs) != 4 || segs[3].StartUS != 18_000_000 || segs[3].DurationUS != 2_500_000 || segs[0].DurationUS != 6_000_000 {
		t.Fatalf("%+v", segs)
	}
	if EncodeSegments(0, 6) != nil {
		t.Fatal("sin duración no hay segmentos")
	}
}

// Los cortes coinciden con los que FFmpeg produjo en una pasada completa
// sobre el vídeo de keyframes irregulares medido al diseñar la fase.
func TestCopySegmentsComoFFmpeg(t *testing.T) {
	kf := []int64{0, 2_520_000, 4_000_000, 9_000_000, 11_200_000, 13_000_000, 19_520_000, 20_000_000, 24_000_000,
		31_000_000, 32_000_000, 36_520_000, 44_000_000, 45_000_000, 50_120_000, 55_000_000, 58_000_000}
	segs := CopySegments(kf, 60_000_000, 6)
	want := []int64{0, 9_000_000, 13_000_000, 19_520_000, 24_000_000, 31_000_000, 36_520_000, 44_000_000, 50_120_000, 55_000_000}
	if !slices.Equal(starts(segs), want) {
		t.Fatalf("%v", starts(segs))
	}
	if last := segs[len(segs)-1]; last.DurationUS != 5_000_000 {
		t.Fatalf("el último llega a la duración: %+v", last)
	}
	if CopySegments(nil, 10, 6) != nil || CopySegments([]int64{0}, 0, 6) != nil {
		t.Fatal("sin keyframes o sin duración no hay segmentos")
	}
	// Un keyframe más allá de la duración no abre segmento.
	if got := CopySegments([]int64{0, 7_000_000, 12_000_000}, 10_000_000, 6); len(got) != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestPlaylists(t *testing.T) {
	segs := []Segment{{0, 6_000_000}, {6_000_000, 6_500_000}, {12_500_000, 1_000_000}}
	vod := string(MediaPlaylist(segs, "m4s", "init.mp4"))
	for _, want := range []string{"#EXT-X-VERSION:7", "#EXT-X-TARGETDURATION:7", "#EXT-X-PLAYLIST-TYPE:VOD",
		`#EXT-X-MAP:URI="init.mp4"`, "#EXTINF:6.500000,\nseg_00001.m4s", "#EXT-X-ENDLIST"} {
		if !strings.Contains(vod, want) {
			t.Errorf("falta %q:\n%s", want, vod)
		}
	}
	ts := string(MediaPlaylist(segs, "ts", ""))
	if strings.Contains(ts, "EXT-X-MAP") || !strings.Contains(ts, "#EXT-X-VERSION:3") || !strings.Contains(ts, "seg_00002.ts") {
		t.Fatal(ts)
	}
	ev := string(EventPlaylist(segs[:1], "ts", false))
	if !strings.Contains(ev, "PLAYLIST-TYPE:EVENT") || strings.Contains(ev, "ENDLIST") {
		t.Fatal(ev)
	}
	if !strings.Contains(string(EventPlaylist(segs, "ts", true)), "#EXT-X-ENDLIST") {
		t.Fatal("terminado lleva ENDLIST")
	}
	master := string(MasterPlaylist(Variant{Bandwidth: 4_192_000, Width: 1280, Height: 720, Codecs: "avc1.64001f,mp4a.40.2"}))
	if !strings.Contains(master, `#EXT-X-STREAM-INF:BANDWIDTH=4192000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2"`+"\nmedia.m3u8") {
		t.Fatal(master)
	}
	if strings.Contains(string(MasterPlaylist(Variant{})), "RESOLUTION") {
		t.Fatal("sin dimensiones no hay RESOLUTION")
	}
}

func TestAdmission(t *testing.T) {
	a := NewAdmission(1, 2)
	if err := a.Reserve(Heavy, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Reserve(Heavy, "s1"); err != nil {
		t.Fatal("reservar otra vez para la misma sesión no consume cupo")
	}
	if err := a.Reserve(Heavy, "s2"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("%v", err)
	}
	// Cambiar de clase libera la anterior: un cambio de calidad no pierde el cupo.
	if err := a.Reserve(Light, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Reserve(Heavy, "s2"); err != nil {
		t.Fatal("el cupo pesado quedó libre")
	}
	a.Release("s1")
	a.Release("s1")
	used, limits := a.Usage()
	if used[Heavy] != 1 || used[Light] != 0 || limits[Light] != 2 {
		t.Fatalf("%v %v", used, limits)
	}
}

func TestParsePackets(t *testing.T) {
	out := []byte("0.000000,K__\n0.040000,___\nN/A,K__\n2.520000,K_\n2.520000,K_\n1.000000,K__\n60.023000\n")
	kf, dur := parsePackets(out)
	if !slices.Equal(kf, []int64{0, 1_000_000, 2_520_000}) || dur != 60_023_000 {
		t.Fatalf("%v %d", kf, dur)
	}
	for in, want := range map[string]int64{"36.520000": 36_520_000, "1.5": 1_500_000, "33.366667": 33_366_667, "7": 7_000_000} {
		if got, ok := toUS(in); !ok || got != want {
			t.Errorf("%s = %d", in, got)
		}
	}
	for _, bad := range []string{"-1.0", "x", ""} {
		if _, ok := toUS(bad); ok {
			t.Errorf("%q debía fallar", bad)
		}
	}
}
