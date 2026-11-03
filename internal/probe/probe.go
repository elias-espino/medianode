// Package probe extrae con FFprobe la información técnica de un archivo.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"medianode/internal/media"
)

// maxOutput limita la salida JSON que se acepta de FFprobe. Es variable para
// poder probar el límite.
var maxOutput = 16 << 20

var (
	// ErrUnavailable indica que FFprobe no se pudo ejecutar. No es un fallo del
	// archivo: el escaneo debe detenerse en lugar de marcar todo como failed.
	ErrUnavailable = errors.New("ffprobe no disponible")
	// ErrInvalidMedia indica que FFprobe no pudo leer el archivo.
	ErrInvalidMedia = errors.New("archivo no analizable")
)

// Result es la información técnica de un archivo.
type Result struct {
	FormatName string
	DurationMS *int64
	Bitrate    *int64
	Streams    []Stream
	Chapters   int
}

// Stream es una pista del archivo.
type Stream struct {
	Index          int64
	Type           media.StreamType
	Codec          *string
	Profile        *string
	Level          *string
	Language       *string
	Channels       *int64
	Width          *int64
	Height         *int64
	FrameRate      *string
	Bitrate        *int64
	BitDepth       *int64
	ColorTransfer  *string
	ColorPrimaries *string
	HDRFormat      *string
	Default        bool
	Forced         bool
	MetadataJSON   *string
}

// Formatos HDR que se guardan en media_streams.hdr_format.
const (
	HDR10       = "hdr10"
	HLG         = "hlg"
	DolbyVision = "dolby_vision"
)

// Runner ejecuta FFprobe.
type Runner struct {
	Path    string
	Timeout time.Duration
}

// Locate busca FFprobe junto al ejecutable del servidor, donde lo dejan los
// paquetes, y después en el PATH.
func Locate() (string, error) {
	name := "ffprobe"
	if filepath.Separator == '\\' {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return path, nil
}

// Run analiza path, que debe ser absoluta. Se ejecuta sin shell y con el
// prefijo file:, para que un nombre como «concat:...» no se tome como
// protocolo de FFmpeg.
func (r *Runner) Run(ctx context.Context, path string) (*Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.Path,
		"-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-show_chapters",
		"file:"+path)
	stdout := &limitedBuffer{max: maxOutput}
	stderr := &limitedBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exitErr *exec.ExitError
	switch {
	case runCtx.Err() != nil:
		return nil, fmt.Errorf("%w: tiempo agotado tras %s", ErrInvalidMedia, r.Timeout)
	case errors.As(err, &exitErr):
		return nil, fmt.Errorf("%w: %s", ErrInvalidMedia, strings.TrimSpace(stderr.String()))
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	case stdout.overflow:
		return nil, fmt.Errorf("%w: salida demasiado grande", ErrInvalidMedia)
	}
	res, err := Parse(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMedia, err)
	}
	return res, nil
}

// limitedBuffer descarta lo que pase de max. No incrusta bytes.Buffer: su
// ReadFrom quedaría promocionado, io.Copy lo usaría y se saltaría el límite.
type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *limitedBuffer) String() string { return b.buf.String() }

type rawOutput struct {
	Streams  []rawStream      `json:"streams"`
	Chapters []map[string]any `json:"chapters"`
	Format   *struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

type rawStream struct {
	Index            int64             `json:"index"`
	CodecType        string            `json:"codec_type"`
	CodecName        string            `json:"codec_name"`
	Profile          string            `json:"profile"`
	Level            *int64            `json:"level"`
	Width            int64             `json:"width"`
	Height           int64             `json:"height"`
	PixFmt           string            `json:"pix_fmt"`
	BitsPerRawSample string            `json:"bits_per_raw_sample"`
	ColorTransfer    string            `json:"color_transfer"`
	ColorPrimaries   string            `json:"color_primaries"`
	AvgFrameRate     string            `json:"avg_frame_rate"`
	RFrameRate       string            `json:"r_frame_rate"`
	Channels         int64             `json:"channels"`
	ChannelLayout    string            `json:"channel_layout"`
	SampleRate       string            `json:"sample_rate"`
	BitRate          string            `json:"bit_rate"`
	Disposition      map[string]int    `json:"disposition"`
	Tags             map[string]string `json:"tags"`
	SideDataList     []struct {
		SideDataType string `json:"side_data_type"`
	} `json:"side_data_list"`
}

// Parse interpreta la salida JSON de FFprobe.
func Parse(data []byte) (*Result, error) {
	var raw rawOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("salida de ffprobe no válida: %w", err)
	}
	if raw.Format == nil {
		return nil, errors.New("la salida de ffprobe no incluye el formato")
	}
	res := &Result{
		FormatName: raw.Format.FormatName,
		DurationMS: seconds(raw.Format.Duration),
		Bitrate:    positiveInt(raw.Format.BitRate),
		Chapters:   len(raw.Chapters),
	}
	for _, s := range raw.Streams {
		res.Streams = append(res.Streams, parseStream(s))
	}
	return res, nil
}

func parseStream(s rawStream) Stream {
	out := Stream{
		Index:    s.Index,
		Type:     streamType(s),
		Codec:    text(s.CodecName),
		Profile:  text(s.Profile),
		Language: language(tag(s.Tags, "language")),
		Default:  s.Disposition["default"] == 1,
		Forced:   s.Disposition["forced"] == 1,
		Bitrate:  positiveInt(s.BitRate),
	}
	if out.Bitrate == nil {
		// Matroska guarda la tasa en una etiqueta.
		out.Bitrate = positiveInt(tag(s.Tags, "BPS"))
	}
	if s.Level != nil && *s.Level > 0 {
		out.Level = text(strconv.FormatInt(*s.Level, 10))
	}

	switch out.Type {
	case media.StreamVideo:
		out.Width = positive(s.Width)
		out.Height = positive(s.Height)
		out.FrameRate = frameRate(s.AvgFrameRate, s.RFrameRate)
		out.BitDepth = bitDepth(s.BitsPerRawSample, s.PixFmt)
		out.ColorTransfer = text(s.ColorTransfer)
		out.ColorPrimaries = text(s.ColorPrimaries)
		out.HDRFormat = hdrFormat(s)
	case media.StreamAudio:
		out.Channels = positive(s.Channels)
	}

	meta := map[string]any{}
	if title := tag(s.Tags, "title"); title != "" {
		meta["title"] = title
	}
	if s.ChannelLayout != "" {
		meta["channel_layout"] = s.ChannelLayout
	}
	if n := positiveInt(s.SampleRate); n != nil {
		meta["sample_rate"] = *n
	}
	if s.Disposition["hearing_impaired"] == 1 {
		meta["hearing_impaired"] = true
	}
	if len(meta) > 0 {
		if b, err := json.Marshal(meta); err == nil {
			out.MetadataJSON = text(string(b))
		}
	}
	return out
}

// streamType trata como adjunto la carátula que algunos contenedores guardan
// como pista de video: nunca debe elegirse para reproducir.
func streamType(s rawStream) media.StreamType {
	switch s.CodecType {
	case "video":
		if s.Disposition["attached_pic"] == 1 {
			return media.StreamAttachment
		}
		return media.StreamVideo
	case "audio":
		return media.StreamAudio
	case "subtitle":
		return media.StreamSubtitle
	case "attachment":
		return media.StreamAttachment
	}
	return media.StreamData
}

// hdrFormat detecta el formato por la función de transferencia y los datos
// laterales de la pista. HDR10+ solo se declara por fotograma, así que aquí se
// reconoce como HDR10, su capa base.
func hdrFormat(s rawStream) *string {
	for _, sd := range s.SideDataList {
		if strings.Contains(sd.SideDataType, "DOVI") {
			return text(DolbyVision)
		}
	}
	switch s.ColorTransfer {
	case "smpte2084":
		return text(HDR10)
	case "arib-std-b67":
		return text(HLG)
	}
	return nil
}

func bitDepth(raw, pixFmt string) *int64 {
	if n := positiveInt(raw); n != nil {
		return n
	}
	switch {
	case pixFmt == "":
		return nil
	// Se busca el sufijo de profundidad: «nv12» contiene un 12 y es de 8 bits.
	case strings.Contains(pixFmt, "p12"):
		return ptr(int64(12))
	case strings.Contains(pixFmt, "p10"), strings.HasPrefix(pixFmt, "p010"):
		return ptr(int64(10))
	}
	return ptr(int64(8))
}

func frameRate(avg, r string) *string {
	for _, v := range []string{avg, r} {
		if v != "" && v != "0/0" {
			return text(v)
		}
	}
	return nil
}

// language devuelve nil para «und», que significa idioma desconocido.
func language(v string) *string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || v == "und" {
		return nil
	}
	return &v
}

// tag busca una etiqueta sin distinguir mayúsculas: Matroska y MP4 no
// coinciden en cómo las escriben.
func tag(tags map[string]string, key string) string {
	if v, ok := tags[key]; ok {
		return v
	}
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func seconds(v string) *int64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) {
		return nil
	}
	return ptr(int64(math.Round(f * 1000)))
}

func positiveInt(v string) *int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

func positive(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

func text(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func ptr[T any](v T) *T { return &v }

// Container normaliza el nombre de formato de FFprobe. Algunos formatos
// agrupan varios contenedores, y la extensión decide entre ellos.
func Container(formatName, ext string) string {
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	switch {
	case strings.HasPrefix(formatName, "matroska"):
		if ext == "webm" {
			return "webm"
		}
		return "mkv"
	case strings.HasPrefix(formatName, "mov,mp4"):
		switch ext {
		case "mov", "m4v":
			return ext
		}
		return "mp4"
	case formatName == "mpegts":
		return "ts"
	}
	if first, _, _ := strings.Cut(formatName, ","); first != "" {
		return first
	}
	return ext
}
