// Package subtitles convierte subtítulos de texto para los reproductores
// (§11). Los internos y ASS llegan con FFmpeg, en la Fase 9.
package subtitles

import (
	"bytes"
	"regexp"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// MaxBytes limita lo que se lee de un subtítulo externo. Uno de varias horas
// ocupa unos cientos de KiB.
const MaxBytes = 5 << 20

// reTiming reconoce la línea de tiempos de SRT, que usa coma decimal.
var reTiming = regexp.MustCompile(`(?m)^(\d{1,2}:\d{2}:\d{2}),(\d{1,3})\s*-->\s*(\d{1,2}:\d{2}:\d{2}),(\d{1,3})`)

// SRTToWebVTT convierte SRT en WebVTT, el único formato que acepta un
// <video>. Un archivo que no es UTF-8 se interpreta como Windows-1252, lo
// habitual en subtítulos en español.
func SRTToWebVTT(srt []byte) []byte {
	srt = ToUTF8(srt)
	srt = bytes.ReplaceAll(srt, []byte("\r\n"), []byte("\n"))
	srt = bytes.ReplaceAll(srt, []byte("\r"), []byte("\n"))
	srt = reTiming.ReplaceAll(srt, []byte("$1.$2 --> $3.$4"))
	out := make([]byte, 0, len(srt)+8)
	out = append(out, "WEBVTT\n\n"...)
	return append(out, bytes.TrimLeft(srt, "\n")...)
}

// ToUTF8 quita la marca BOM y convierte desde Windows-1252 si el texto no es
// UTF-8 válido.
func ToUTF8(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if utf8.Valid(b) {
		return b
	}
	out, err := charmap.Windows1252.NewDecoder().Bytes(b)
	if err != nil {
		return b
	}
	return out
}
