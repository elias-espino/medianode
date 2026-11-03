package subtitles

import (
	"strings"
	"testing"
)

func TestSRTToWebVTT(t *testing.T) {
	srt := "\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:02,500\r\n¡Hola!\r\n\r\n2\r\n01:02:03,04 --> 1:02:04,5\r\nAdiós, 00:00:01,000 no es una línea de tiempos\r\n"
	got := string(SRTToWebVTT([]byte(srt)))
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\n¡Hola!\n\n2\n01:02:03.04 --> 1:02:04.5\nAdiós, 00:00:01,000 no es una línea de tiempos\n"
	if got != want {
		t.Fatalf("%q", got)
	}
}

// Un SRT en Windows-1252, habitual en español, llega como UTF-8.
func TestWindows1252(t *testing.T) {
	srt := []byte("1\n00:00:01,000 --> 00:00:02,000\nAcci\xf3n, ni\xf1o\n")
	got := string(SRTToWebVTT(srt))
	if !strings.Contains(got, "Acción, niño") {
		t.Fatalf("%q", got)
	}
	if utf := ToUTF8([]byte("ya es UTF-8: ñ")); string(utf) != "ya es UTF-8: ñ" {
		t.Fatalf("%q", utf)
	}
}
