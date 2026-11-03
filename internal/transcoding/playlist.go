package transcoding

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
)

// Segment es un segmento de la playlist: inicio y duración en microsegundos.
type Segment struct {
	StartUS    int64
	DurationUS int64
}

// EncodeSegments son los segmentos de un trabajo que recodifica el vídeo:
// una rejilla fija, porque los keyframes se fuerzan en cada frontera.
func EncodeSegments(durationUS int64, segSeconds int) []Segment {
	step := int64(segSeconds) * 1_000_000
	if durationUS <= 0 || step <= 0 {
		return nil
	}
	n := int((durationUS + step - 1) / step)
	out := make([]Segment, n)
	for i := range out {
		start := int64(i) * step
		out[i] = Segment{StartUS: start, DurationUS: min(step, durationUS-start)}
	}
	return out
}

// CopySegments son los segmentos de un trabajo que copia el vídeo: solo se
// puede cortar en keyframes. Cada segmento empieza en el primer keyframe
// desde el múltiplo siguiente de segSeconds, como hace FFmpeg en una pasada
// completa. Al copiar se pasan estos cortes de forma explícita, así que la
// regla solo tiene que ser estable.
func CopySegments(keyframesUS []int64, durationUS int64, segSeconds int) []Segment {
	step := int64(segSeconds) * 1_000_000
	if len(keyframesUS) == 0 || durationUS <= keyframesUS[0] || step <= 0 {
		return nil
	}
	starts := []int64{keyframesUS[0]}
	next := (keyframesUS[0]/step + 1) * step
	for _, kf := range keyframesUS[1:] {
		if kf >= durationUS {
			break
		}
		if kf >= next {
			starts = append(starts, kf)
			next = (kf/step + 1) * step
		}
	}
	out := make([]Segment, len(starts))
	for i, s := range starts {
		end := durationUS
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		out[i] = Segment{StartUS: s, DurationUS: end - s}
	}
	return out
}

func targetDuration(segs []Segment) int {
	longest := int64(0)
	for _, s := range segs {
		longest = max(longest, s.DurationUS)
	}
	return int(math.Ceil(float64(longest) / 1e6))
}

// MediaPlaylist es la playlist VOD sintética: se genera sin esperar a FFmpeg,
// y permite saltar a cualquier punto. initName vacío indica segmentos
// MPEG-TS, que no llevan EXT-X-MAP.
func MediaPlaylist(segs []Segment, ext, initName string) []byte {
	var b bytes.Buffer
	version := 3
	if initName != "" {
		version = 7
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-TARGETDURATION:%d\n", version, targetDuration(segs))
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	if initName != "" {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", initName)
	}
	for i, s := range segs {
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", float64(s.DurationUS)/1e6, SegmentName(i, ext))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.Bytes()
}

// EventPlaylist lista lo ya generado cuando no hay índice de keyframes: solo
// se puede saltar dentro de esa zona (ADR 0004). Lleva EXT-X-ENDLIST cuando
// FFmpeg terminó.
func EventPlaylist(segs []Segment, ext string, done bool) []byte {
	var b bytes.Buffer
	target := max(targetDuration(segs), 1)
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n", target)
	b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for i, s := range segs {
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", float64(s.DurationUS)/1e6, SegmentName(i, ext))
	}
	if done {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.Bytes()
}

// Variant describe la única variante del master.
type Variant struct {
	Bandwidth int
	Width     int
	Height    int
	Codecs    string // Vacío si no se conoce: el reproductor lo deduce.
}

// MasterPlaylist apunta a la playlist de medios.
func MasterPlaylist(v Variant) []byte {
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	attrs := "BANDWIDTH=" + strconv.Itoa(max(v.Bandwidth, 1))
	if v.Width > 0 && v.Height > 0 {
		attrs += fmt.Sprintf(",RESOLUTION=%dx%d", v.Width, v.Height)
	}
	if v.Codecs != "" {
		attrs += fmt.Sprintf(",CODECS=%q", v.Codecs)
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:%s\nmedia.m3u8\n", attrs)
	return b.Bytes()
}

// SegmentName es el nombre del segmento n.
func SegmentName(n int, ext string) string {
	return fmt.Sprintf("seg_%05d.%s", n, ext)
}
