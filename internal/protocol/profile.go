// Package protocol define los tipos que comparten servidor y clientes en la
// negociación de reproducción (§9.2).
package protocol

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Formas de entregar un subtítulo.
const (
	DeliverySidecar  = "sidecar"  // Archivo aparte, que pide el reproductor.
	DeliveryHLS      = "hls"      // Pista dentro de HLS (AVPlayer).
	DeliveryEmbedded = "embedded" // El reproductor dibuja la pista del contenedor.
)

// Protocolos de entrega.
const (
	ProtocolHTTP = "http"
	ProtocolHLS  = "hls"
)

// Identificadores conocidos. Lo que no está aquí se descarta al validar: un
// perfil no es una garantía (§9.2) y un nombre desconocido nunca casaría con
// lo que devuelve FFprobe.
var known = map[string][]string{
	"containers":        {"mp4", "mkv", "webm", "mov", "ts", "avi"},
	"video_codecs":      {"h264", "hevc", "vp8", "vp9", "av1", "mpeg4", "mpeg2", "vc1"},
	"audio_codecs":      {"aac", "mp3", "ac3", "eac3", "dts", "truehd", "opus", "vorbis", "flac", "alac", "pcm"},
	"subtitle_formats":  {"srt", "vtt", "ass", "ssa", "pgs", "vobsub", "dvbsub", "mov_text"},
	"subtitle_delivery": {DeliverySidecar, DeliveryHLS, DeliveryEmbedded},
	"protocols":         {ProtocolHTTP, ProtocolHLS},
	"hdr":               {"hdr10", "hlg", "dolby_vision"},
}

const maxListItems = 32

// ClientProfile son las capacidades que declara el cliente. Las listas usan
// los nombres cortos de known; MaxResolution es «ancho×alto».
type ClientProfile struct {
	Platform         string   `json:"platform"`
	Containers       []string `json:"containers"`
	VideoCodecs      []string `json:"video_codecs"`
	AudioCodecs      []string `json:"audio_codecs"`
	SubtitleFormats  []string `json:"subtitle_formats"`
	SubtitleDelivery []string `json:"subtitle_delivery"`
	Protocols        []string `json:"protocols"`
	HDR              []string `json:"hdr"`
	MaxResolution    string   `json:"max_resolution"`
	MaxBitDepth      int      `json:"max_bit_depth"`
	MaxAudioChannels int      `json:"max_audio_channels"`
}

// Has indica si la lista contiene el valor.
func Has(list []string, v string) bool { return slices.Contains(list, v) }

// Normalize completa con el perfil de la plataforma lo que el cliente no
// declara y descarta lo desconocido. Un campo enviado, aunque sea vacío,
// manda sobre el perfil predefinido.
func (c ClientProfile) Normalize(declared map[string]bool) (ClientProfile, error) {
	c.Platform = strings.ToLower(strings.TrimSpace(c.Platform))
	preset := PresetFor(c.Platform)
	pick := func(field string, got, def []string) []string {
		if !declared[field] {
			return def
		}
		return clean(field, got)
	}
	c.Containers = pick("containers", c.Containers, preset.Containers)
	c.VideoCodecs = pick("video_codecs", c.VideoCodecs, preset.VideoCodecs)
	c.AudioCodecs = pick("audio_codecs", c.AudioCodecs, preset.AudioCodecs)
	c.SubtitleFormats = pick("subtitle_formats", c.SubtitleFormats, preset.SubtitleFormats)
	c.SubtitleDelivery = pick("subtitle_delivery", c.SubtitleDelivery, preset.SubtitleDelivery)
	c.Protocols = pick("protocols", c.Protocols, preset.Protocols)
	c.HDR = pick("hdr", c.HDR, preset.HDR)
	if c.MaxResolution == "" {
		c.MaxResolution = preset.MaxResolution
	}
	if c.MaxBitDepth == 0 {
		c.MaxBitDepth = preset.MaxBitDepth
	}
	if c.MaxAudioChannels == 0 {
		c.MaxAudioChannels = preset.MaxAudioChannels
	}
	if c.MaxResolution != "" {
		if _, _, err := ParseResolution(c.MaxResolution); err != nil {
			return c, err
		}
	}
	if c.MaxBitDepth < 8 || c.MaxBitDepth > 16 {
		return c, fmt.Errorf("max_bit_depth fuera de rango: %d", c.MaxBitDepth)
	}
	if c.MaxAudioChannels < 1 || c.MaxAudioChannels > 16 {
		return c, fmt.Errorf("max_audio_channels fuera de rango: %d", c.MaxAudioChannels)
	}
	if len(c.Protocols) == 0 {
		return c, fmt.Errorf("el perfil no admite ningún protocolo conocido")
	}
	return c, nil
}

func clean(field string, in []string) []string {
	out := []string{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if Has(known[field], v) && !Has(out, v) {
			out = append(out, v)
		}
		if len(out) == maxListItems {
			break
		}
	}
	return out
}

// ParseResolution interpreta «1920x1080».
func ParseResolution(s string) (int64, int64, error) {
	w, h, ok := strings.Cut(strings.ToLower(s), "x")
	wi, err1 := strconv.ParseInt(w, 10, 64)
	hi, err2 := strconv.ParseInt(h, 10, 64)
	if !ok || err1 != nil || err2 != nil || wi < 1 || hi < 1 || wi > 16384 || hi > 16384 {
		return 0, 0, fmt.Errorf("resolución no válida: %q", s)
	}
	return wi, hi, nil
}

// PresetFor devuelve el perfil predefinido de una plataforma. Son
// estimaciones prudentes, pendientes de validar en cada dispositivo (§26.6):
// lo que no se sabe que funciona no se declara.
func PresetFor(platform string) ClientProfile {
	switch platform {
	case "web":
		// <video> en navegadores de escritorio: MP4 y WebM, WebVTT aparte.
		return ClientProfile{
			Containers: []string{"mp4", "webm"}, VideoCodecs: []string{"h264", "vp9"},
			AudioCodecs: []string{"aac", "mp3", "opus", "vorbis"}, SubtitleFormats: []string{"vtt"},
			SubtitleDelivery: []string{DeliverySidecar}, Protocols: []string{ProtocolHTTP},
			MaxBitDepth: 8, MaxAudioChannels: 2,
		}
	case "android", "androidtv":
		// Media3.
		return ClientProfile{
			Containers: []string{"mp4", "mkv", "webm", "ts"}, VideoCodecs: []string{"h264", "hevc", "vp9"},
			AudioCodecs:     []string{"aac", "mp3", "ac3", "eac3", "opus", "vorbis", "flac"},
			SubtitleFormats: []string{"srt", "vtt", "ass", "ssa", "pgs"}, SubtitleDelivery: []string{DeliverySidecar, DeliveryEmbedded, DeliveryHLS},
			Protocols: []string{ProtocolHTTP, ProtocolHLS}, MaxBitDepth: 10, MaxAudioChannels: 6,
		}
	case "ios", "tvos":
		// AVPlayer: subtítulos solo como pista WebVTT dentro de HLS (§11.2).
		return ClientProfile{
			Containers: []string{"mp4", "mov"}, VideoCodecs: []string{"h264", "hevc"},
			AudioCodecs: []string{"aac", "mp3", "ac3", "eac3", "alac", "flac"}, SubtitleFormats: []string{"vtt"},
			SubtitleDelivery: []string{DeliveryHLS}, Protocols: []string{ProtocolHTTP, ProtocolHLS},
			HDR: []string{"hdr10", "hlg", "dolby_vision"}, MaxBitDepth: 10, MaxAudioChannels: 6,
		}
	case "tizen", "webos":
		return ClientProfile{
			Containers: []string{"mp4", "mkv", "ts"}, VideoCodecs: []string{"h264", "hevc"},
			AudioCodecs: []string{"aac", "mp3", "ac3", "eac3"}, SubtitleFormats: []string{"srt", "vtt"},
			SubtitleDelivery: []string{DeliverySidecar}, Protocols: []string{ProtocolHTTP, ProtocolHLS},
			HDR: []string{"hdr10", "hlg"}, MaxBitDepth: 10, MaxAudioChannels: 6,
		}
	}
	// Desconocida: lo que reproduce casi cualquier cliente.
	return ClientProfile{
		Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac", "mp3"},
		SubtitleFormats: []string{"vtt"}, SubtitleDelivery: []string{DeliverySidecar}, Protocols: []string{ProtocolHTTP},
		MaxBitDepth: 8, MaxAudioChannels: 2,
	}
}

// CodecName traduce el nombre de FFprobe al de los perfiles: subrip → srt,
// pcm_s16le → pcm, hdmv_pgs_subtitle → pgs.
func CodecName(ffprobe string) string {
	c := strings.ToLower(ffprobe)
	switch {
	case strings.HasPrefix(c, "pcm_"):
		return "pcm"
	}
	switch c {
	case "mpeg2video":
		return "mpeg2"
	case "subrip":
		return "srt"
	case "webvtt":
		return "vtt"
	case "hdmv_pgs_subtitle":
		return "pgs"
	case "dvd_subtitle":
		return "vobsub"
	case "dvb_subtitle":
		return "dvbsub"
	case "m4v":
		return "mp4"
	}
	return c
}

// ImageSubtitle indica si el formato es de imagen: sin OCR no se convierte a
// texto (§11.1).
func ImageSubtitle(format string) bool {
	return format == "pgs" || format == "vobsub" || format == "dvbsub"
}

// HDRName traduce el hdr_format guardado por el escáner al de los perfiles.
func HDRName(stored string) string {
	switch strings.ToLower(stored) {
	case "hdr10", "hdr10+":
		return "hdr10"
	case "hlg":
		return "hlg"
	case "dolby vision", "dolby_vision", "dovi":
		return "dolby_vision"
	}
	return strings.ToLower(stored)
}
