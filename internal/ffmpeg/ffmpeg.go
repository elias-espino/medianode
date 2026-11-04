// Package ffmpeg construye los comandos de FFmpeg y detecta sus capacidades
// (§10). Los argumentos son listas tipadas; nunca pasan por un shell.
package ffmpeg

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrUnavailable indica que FFmpeg no está instalado.
var ErrUnavailable = errors.New("FFmpeg no está disponible")

// Locate busca FFmpeg junto al ejecutable del servidor y después en el PATH,
// igual que probe.Locate.
func Locate() (string, error) {
	name := "ffmpeg"
	if filepath.Separator == '\\' {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return path, nil
}

// Encoder es un codificador de vídeo H.264.
type Encoder string

// Codificadores conocidos. libx264 es el de CPU y siempre es el respaldo.
const (
	X264         Encoder = "libx264"
	NVENC        Encoder = "h264_nvenc"
	QSV          Encoder = "h264_qsv"
	AMF          Encoder = "h264_amf"
	VAAPI        Encoder = "h264_vaapi"
	VideoToolbox Encoder = "h264_videotoolbox"
)

// Hardware es el orden en que se prueban los codificadores por hardware, y el
// nombre de cada uno en transcoding.hardware_acceleration.
var Hardware = []struct {
	Name    string
	Encoder Encoder
}{
	{"nvenc", NVENC}, {"qsv", QSV}, {"amf", AMF}, {"vaapi", VAAPI}, {"videotoolbox", VideoToolbox},
}

// Input es el archivo de origen y desde dónde leer.
type Input struct {
	Path    string // Absoluta.
	StartMS int64  // Inicio de un trabajo que recodifica; al copiar manda CopyParams.SeekUS.
	Video   int    // Índice de la pista de vídeo en el archivo, o -1.
	Audio   int    // Índice de la pista de audio, o -1.
	// LimitUS, si no es cero, para FFmpeg tras leer ese tiempo desde el
	// inicio: así no reescribe segmentos que ya existen y se están sirviendo.
	LimitUS int64
}

// Audio es lo que se hace con el audio.
type Audio struct {
	Copy     bool
	Channels int // Al recodificar; 2 por defecto.
	Bitrate  int // bit/s; 192 000 por defecto.
}

// EncodeParams es un trabajo que recodifica el vídeo: fMP4 con el muxer HLS
// y keyframes forzados en una rejilla fija (ADR 0003).
type EncodeParams struct {
	Input
	Audio
	Encoder        Encoder
	Width, Height  int // Altura de salida; 0 conserva la del origen.
	VideoBitrate   int // bit/s
	SegmentSeconds int
	StartSegment   int    // floor(start / segmento): StartMS debe ser StartSegment × segmento.
	InitName       string // init de este trabajo; cambia en cada reinicio.
	Playlist       string // Lista interna de FFmpeg; no se entrega al cliente.
}

// CopyParams es un trabajo que copia el vídeo: MPEG-TS con el muxer segment y
// cortes explícitos sacados del índice de keyframes. El muxer HLS no sirve,
// porque su rejilla cambia en cada reinicio. Los tiempos van en
// microsegundos, la precisión de FFprobe: redondear a milisegundos puede
// dejar el salto antes del keyframe y arrancar en el anterior.
type CopyParams struct {
	Input // StartMS no se usa: manda SeekUS.
	Audio
	SeekUS         int64   // -ss: algo después del keyframe de inicio.
	OffsetUS       int64   // -output_ts_offset: el keyframe de inicio.
	CutsRelUS      []int64 // Cortes relativos al salto, algo antes de cada keyframe.
	StartSegment   int
	SegmentSeconds int    // Solo sin cortes: duración aproximada de cada segmento.
	List           string // CSV donde FFmpeg anota cada segmento terminado.
}

func seconds(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

func secondsUS(us int64) string {
	return strconv.FormatFloat(float64(us)/1e6, 'f', 6, 64)
}

func common(in Input) []string {
	return []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-progress", "pipe:1", "-nostats"}
}

// file: evita que un nombre como «concat:...» se tome como protocolo.
func inputArgs(in Input, seek string) []string {
	args := []string{}
	if seek != "" {
		args = append(args, "-ss", seek)
	}
	if in.LimitUS > 0 {
		args = append(args, "-t", secondsUS(in.LimitUS))
	}
	args = append(args, "-i", "file:"+in.Path)
	if in.Video >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(in.Video))
	}
	if in.Audio >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(in.Audio))
	}
	return args
}

func audioArgs(a Audio, hasAudio bool) []string {
	if !hasAudio {
		return nil
	}
	if a.Copy {
		return []string{"-c:a", "copy"}
	}
	ch, br := a.Channels, a.Bitrate
	if ch <= 0 {
		ch = 2
	}
	if br <= 0 {
		br = 192_000
	}
	return []string{"-c:a", "aac", "-ac", strconv.Itoa(ch), "-b:a", strconv.Itoa(br)}
}

// encoderArgs son las opciones de cada codificador. Los de hardware usan su
// control de tasa y un preset rápido.
func encoderArgs(e Encoder, bitrate int) []string {
	br := strconv.Itoa(bitrate)
	maxrate, bufsize := strconv.Itoa(bitrate*3/2), strconv.Itoa(bitrate*2)
	args := []string{"-c:v", string(e)}
	switch e {
	case X264:
		args = append(args, "-preset", "veryfast", "-profile:v", "high", "-pix_fmt", "yuv420p")
	case NVENC:
		args = append(args, "-preset", "p4", "-profile:v", "high", "-pix_fmt", "yuv420p")
	case QSV:
		args = append(args, "-preset", "veryfast", "-profile:v", "high")
	case AMF:
		args = append(args, "-quality", "speed", "-profile:v", "high")
	case VAAPI:
		args = append(args, "-profile:v", "high")
	case VideoToolbox:
		args = append(args, "-profile:v", "high", "-realtime", "1")
	}
	return append(args, "-b:v", br, "-maxrate", maxrate, "-bufsize", bufsize)
}

// BuildEncode devuelve los argumentos de un trabajo con vídeo recodificado.
func BuildEncode(p EncodeParams) ([]string, error) {
	if p.Path == "" || !filepath.IsAbs(p.Path) {
		return nil, errors.New("la ruta de entrada debe ser absoluta")
	}
	if p.Video < 0 || p.SegmentSeconds <= 0 || p.VideoBitrate <= 0 || p.StartSegment < 0 {
		return nil, errors.New("parámetros de codificación incompletos")
	}
	if p.StartMS != int64(p.StartSegment)*int64(p.SegmentSeconds)*1000 {
		return nil, errors.New("el inicio debe coincidir con un segmento")
	}
	if !safeName(p.InitName) || !safeName(p.Playlist) {
		return nil, errors.New("nombre de salida no válido")
	}
	seg := strconv.Itoa(p.SegmentSeconds)
	seek := ""
	if p.StartMS > 0 {
		seek = seconds(p.StartMS)
	}
	args := append(common(p.Input), inputArgs(p.Input, seek)...)
	if p.Height > 0 {
		// -2 conserva la proporción con un ancho par, que exige yuv420p.
		args = append(args, "-vf", "scale=-2:"+strconv.Itoa(p.Height))
	}
	args = append(args, encoderArgs(p.Encoder, p.VideoBitrate)...)
	args = append(args, "-force_key_frames", "expr:gte(t,n_forced*"+seg+")")
	args = append(args, audioArgs(p.Audio, p.Input.Audio >= 0)...)
	args = append(args,
		"-output_ts_offset", seconds(p.StartMS),
		"-f", "hls", "-hls_time", seg, "-hls_playlist_type", "event",
		"-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", p.InitName,
		"-hls_flags", "temp_file+independent_segments",
		// Sin frag_discont, cada trabajo empieza tfdt en cero y guarda el
		// desfase en la lista de edición de su init: un reproductor que use el
		// init del primer trabajo, el único que declara la playlist, colocaría
		// los segmentos de un reinicio al principio. Con él, tfdt es absoluto
		// y el init sale idéntico en cada reinicio (ADR 0006).
		"-hls_segment_options", "movflags=+frag_discont",
		"-start_number", strconv.Itoa(p.StartSegment),
		"-hls_segment_filename", "seg_%05d.m4s",
		p.Playlist,
	)
	return args, nil
}

// BuildCopy devuelve los argumentos de un trabajo que copia el vídeo. Los
// cortes se pasan relativos al inicio: así, un reinicio produce los mismos
// segmentos que la pasada completa.
func BuildCopy(p CopyParams) ([]string, error) {
	if p.Path == "" || !filepath.IsAbs(p.Path) {
		return nil, errors.New("la ruta de entrada debe ser absoluta")
	}
	if p.Video < 0 || p.StartSegment < 0 {
		return nil, errors.New("parámetros de copia incompletos")
	}
	if !safeName(p.List) {
		return nil, errors.New("nombre de salida no válido")
	}
	if p.SeekUS < 0 || p.OffsetUS < 0 {
		return nil, errors.New("tiempos de copia negativos")
	}
	seek := ""
	if p.SeekUS > 0 {
		seek = secondsUS(p.SeekUS)
	}
	args := append(common(p.Input), inputArgs(p.Input, seek)...)
	args = append(args, "-c:v", "copy")
	args = append(args, audioArgs(p.Audio, p.Input.Audio >= 0)...)
	args = append(args, "-output_ts_offset", secondsUS(p.OffsetUS), "-muxdelay", "0", "-muxpreload", "0",
		"-f", "segment", "-segment_format", "mpegts")
	if len(p.CutsRelUS) == 0 {
		// Sin índice de keyframes: cortes cada SegmentSeconds, en el primer
		// keyframe disponible, y una playlist EVENT con lo ya generado.
		args = append(args, "-segment_time", strconv.Itoa(max(p.SegmentSeconds, 1)))
	} else {
		rel := make([]string, 0, len(p.CutsRelUS))
		for _, c := range p.CutsRelUS {
			if c <= 0 {
				return nil, errors.New("un corte no es posterior al inicio")
			}
			rel = append(rel, secondsUS(c))
		}
		args = append(args, "-segment_times", strings.Join(rel, ","))
	}
	args = append(args,
		"-segment_start_number", strconv.Itoa(p.StartSegment),
		"-segment_list", p.List, "-segment_list_type", "csv",
		"seg_%05d.ts",
	)
	return args, nil
}

// safeName exige un nombre de archivo simple dentro del directorio de trabajo.
func safeName(s string) bool {
	return s != "" && s != "." && filepath.Base(s) == s && filepath.IsLocal(s) && !strings.ContainsAny(s, `/\:`)
}
