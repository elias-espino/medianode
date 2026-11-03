// Package playback decide cómo reproducir un archivo en un cliente (§9) y
// crea las sesiones de reproducción.
package playback

import (
	"medianode/internal/protocol"
)

// Mode es el modo de reproducción de la API (§9.1).
type Mode string

// Modos.
const (
	DirectPlay   Mode = "direct_play"
	DirectStream Mode = "direct_stream"
	Transcode    Mode = "transcode"
)

// Action es lo que se hace con una pista.
type Action string

// Acciones por pista.
const (
	Copy    Action = "copy"
	Recode  Action = "transcode"
	NoTrack Action = "none" // El archivo no tiene esa pista.
)

// SubtitleMode es la estrategia de subtítulos (§11.2).
type SubtitleMode string

// Estrategias. embedded no está en el plan: en Direct Play el archivo llega
// entero, y un reproductor como Media3 dibuja la pista interna él mismo.
const (
	SubNone     SubtitleMode = "none"
	SubEmbedded SubtitleMode = "embedded"
	SubSidecar  SubtitleMode = "sidecar"
	SubConvert  SubtitleMode = "convert"
	SubHLS      SubtitleMode = "hls"
	SubBurnIn   SubtitleMode = "burn_in"
)

// Códigos de motivo: por qué no es Direct Play.
const (
	ReasonContainer      = "container_unsupported"
	ReasonVideoCodec     = "video_codec_unsupported"
	ReasonBitDepth       = "video_bit_depth_unsupported"
	ReasonHDR            = "hdr_unsupported"
	ReasonResolution     = "resolution_limit"
	ReasonBitrate        = "bitrate_limit"
	ReasonAudioCodec     = "audio_codec_unsupported"
	ReasonAudioChannels  = "audio_channels_unsupported"
	ReasonSubtitleBurnIn = "subtitle_burn_in"
	ReasonSubtitleHLS    = "subtitle_requires_hls"
	ReasonNoHTTP         = "http_unsupported"
	// El cliente admite el códec, pero no el contenedor de salida HLS.
	ReasonVideoContainer = "video_codec_unsupported_in_hls"
	ReasonAudioContainer = "audio_codec_unsupported_in_hls"
)

// Reason explica una parte de la decisión.
type Reason struct {
	Code   string `json:"code"`
	Stream string `json:"stream"` // container, video, audio, subtitle o limits.
}

// Video es la pista de video elegida.
type Video struct {
	Codec    string // Nombre de FFprobe.
	Width    *int64
	Height   *int64
	BitDepth *int64
	HDR      *string
}

// Audio es la pista de audio elegida.
type Audio struct {
	Codec    string
	Channels *int64
}

// Subtitle es el subtítulo elegido.
type Subtitle struct {
	Format   string // Nombre de FFprobe (interno) o extensión (externo).
	External bool
}

// Input es todo lo que decide. Sin base de datos ni red.
type Input struct {
	Container string // De probe.Container: mkv, mp4, m4v…
	Bitrate   *int64
	Video     *Video
	Audio     *Audio
	Subtitle  *Subtitle
	Client    protocol.ClientProfile
	// Límites de la petición, además de los del perfil.
	MaxBitrate    *int64
	MaxResolution string
}

// Decision es el resultado. Reasons está vacío en Direct Play.
type Decision struct {
	Mode        Mode
	Protocol    string
	VideoAction Action
	AudioAction Action
	Subtitle    SubtitleMode
	Reasons     []Reason
}

// Decide aplica el orden de §9.3: subtítulos, límites, pistas por separado y
// contenedor. Solo se recodifica lo incompatible.
func Decide(in Input) Decision {
	d := decide(in, true)
	// Una pista interna «embedded» solo viaja con el archivo original. Si no
	// hay Direct Play, el subtítulo necesita otra estrategia.
	if d.Subtitle == SubEmbedded && d.Mode != DirectPlay {
		d = decide(in, false)
	}
	return d
}

func decide(in Input, allowEmbedded bool) Decision {
	c := in.Client
	d := Decision{VideoAction: NoTrack, AudioAction: NoTrack}
	reason := func(code, stream string) { d.Reasons = append(d.Reasons, Reason{Code: code, Stream: stream}) }

	d.Subtitle = resolveSubtitle(in.Subtitle, c, allowEmbedded)
	if in.Video != nil {
		d.VideoAction = Copy
		if d.Subtitle == SubBurnIn {
			d.VideoAction = Recode
			reason(ReasonSubtitleBurnIn, "subtitle")
		}
		if code := withinLimits(in); code != "" {
			d.VideoAction = Recode
			reason(code, "limits")
		}
		if code := videoCompatible(*in.Video, c); code != "" {
			d.VideoAction = Recode
			reason(code, "video")
		}
	}
	if in.Audio != nil {
		d.AudioAction = Copy
		if code := audioCompatible(*in.Audio, c); code != "" {
			d.AudioAction = Recode
			reason(code, "audio")
		}
	}

	containerOK := protocol.Has(c.Containers, protocol.CodecName(in.Container))
	httpOK := protocol.Has(c.Protocols, protocol.ProtocolHTTP)
	recode := d.VideoAction == Recode || d.AudioAction == Recode
	switch {
	case !recode && containerOK && httpOK && d.Subtitle != SubHLS:
		d.Mode, d.Protocol = DirectPlay, protocol.ProtocolHTTP
		return d
	case !recode:
		d.Mode = DirectStream
	default:
		d.Mode = Transcode
	}
	if !containerOK {
		reason(ReasonContainer, "container")
	}
	if !httpOK {
		reason(ReasonNoHTTP, "container")
	}
	if d.Subtitle == SubHLS {
		reason(ReasonSubtitleHLS, "subtitle")
	}
	d.Protocol = protocol.ProtocolHTTP
	if protocol.Has(c.Protocols, protocol.ProtocolHLS) {
		d.Protocol = protocol.ProtocolHLS
		hlsContainers(in, &d, reason)
	}
	return d
}

// Códecs que admite cada contenedor de salida HLS. Copiando el vídeo, los
// segmentos son MPEG-TS; recodificándolo, fMP4 (ADR 0005 de la Fase 8).
var (
	tsVideo   = []string{"h264", "hevc"}
	tsAudio   = []string{"aac", "ac3", "eac3", "mp3"}
	fmp4Audio = []string{"aac", "ac3", "eac3", "mp3", "opus", "flac", "alac"}
)

// hlsContainers recodifica lo que el contenedor de salida no puede llevar,
// aunque el cliente lo admita: VP9 no viaja en MPEG-TS.
func hlsContainers(in Input, d *Decision, reason func(code, stream string)) {
	if d.VideoAction == Copy && !protocol.Has(tsVideo, protocol.CodecName(in.Video.Codec)) {
		d.VideoAction, d.Mode = Recode, Transcode
		reason(ReasonVideoContainer, "video")
	}
	audioOK := tsAudio
	if d.VideoAction == Recode {
		audioOK = fmp4Audio
	}
	if d.AudioAction == Copy && !protocol.Has(audioOK, protocol.CodecName(in.Audio.Codec)) {
		d.AudioAction, d.Mode = Recode, Transcode
		reason(ReasonAudioContainer, "audio")
	}
}

// resolveSubtitle elige la estrategia. Las de imagen no se convierten a
// texto: o las dibuja el reproductor desde el contenedor, o se queman.
func resolveSubtitle(s *Subtitle, c protocol.ClientProfile, allowEmbedded bool) SubtitleMode {
	if s == nil {
		return SubNone
	}
	f := protocol.CodecName(s.Format)
	delivers := func(d string) bool { return protocol.Has(c.SubtitleDelivery, d) }
	supports := func(f string) bool { return protocol.Has(c.SubtitleFormats, f) }

	if !s.External && allowEmbedded && delivers(protocol.DeliveryEmbedded) && supports(f) {
		return SubEmbedded
	}
	if protocol.ImageSubtitle(f) {
		return SubBurnIn
	}
	if s.External && delivers(protocol.DeliverySidecar) && supports(f) {
		return SubSidecar
	}
	if delivers(protocol.DeliverySidecar) && (supports("vtt") || supports("srt")) {
		return SubConvert
	}
	if delivers(protocol.DeliveryHLS) {
		return SubHLS
	}
	return SubBurnIn
}

// withinLimits compara con la resolución máxima del perfil y de la petición,
// y con el bitrate pedido. Devuelve el motivo o vacío.
func withinLimits(in Input) string {
	for _, res := range []string{in.Client.MaxResolution, in.MaxResolution} {
		if res == "" || in.Video.Width == nil || in.Video.Height == nil {
			continue
		}
		w, h, err := protocol.ParseResolution(res)
		if err == nil && (*in.Video.Width > w || *in.Video.Height > h) {
			return ReasonResolution
		}
	}
	if in.MaxBitrate != nil && in.Bitrate != nil && *in.Bitrate > *in.MaxBitrate {
		return ReasonBitrate
	}
	return ""
}

func videoCompatible(v Video, c protocol.ClientProfile) string {
	switch {
	case !protocol.Has(c.VideoCodecs, protocol.CodecName(v.Codec)):
		return ReasonVideoCodec
	case v.BitDepth != nil && *v.BitDepth > int64(c.MaxBitDepth):
		return ReasonBitDepth
	case v.HDR != nil && !protocol.Has(c.HDR, protocol.HDRName(*v.HDR)):
		return ReasonHDR
	}
	return ""
}

func audioCompatible(a Audio, c protocol.ClientProfile) string {
	switch {
	case !protocol.Has(c.AudioCodecs, protocol.CodecName(a.Codec)):
		return ReasonAudioCodec
	case a.Channels != nil && *a.Channels > int64(c.MaxAudioChannels):
		return ReasonAudioChannels
	}
	return ""
}
