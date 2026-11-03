package playback

import (
	"fmt"
	"strconv"
	"strings"

	"medianode/internal/database/sqlc"
	"medianode/internal/protocol"
)

// audioBitrate es el del AAC estéreo que sale al recodificar el audio.
const audioBitrate = 192_000

// MaxEncodeHeight limita la salida recodificada. Con libx264, 4K en tiempo
// real no es viable en un equipo doméstico (§10.3).
const MaxEncodeHeight = 1080

// ladder son los perfiles de salida de §10.3: altura y bitrate de vídeo.
var ladder = []struct{ height, bitrate int }{
	{1080, 8_000_000}, {720, 4_000_000}, {480, 2_000_000}, {360, 1_000_000}, {240, 500_000},
}

// outputProfile elige altura y bitrate al recodificar: el peldaño más alto que
// no supera el origen, la resolución máxima del perfil y de la petición, ni
// el bitrate máximo pedido.
func outputProfile(srcHeight *int64, clientMax, reqMax string, maxBitrate *int64) (height, bitrate int) {
	limit := MaxEncodeHeight
	if srcHeight != nil && *srcHeight > 0 {
		limit = min(limit, int(*srcHeight))
	}
	for _, res := range []string{clientMax, reqMax} {
		if res == "" {
			continue
		}
		if _, h, err := protocol.ParseResolution(res); err == nil {
			limit = min(limit, int(h))
		}
	}
	budget := 0
	if maxBitrate != nil {
		budget = int(*maxBitrate) - audioBitrate
	}
	for _, r := range ladder {
		if r.height <= limit && (budget <= 0 || r.bitrate <= budget) {
			return r.height, r.bitrate
		}
	}
	// Por debajo del último peldaño: la altura del origen, par, y lo que quepa.
	last := ladder[len(ladder)-1]
	height, bitrate = max(limit&^1, 2), last.bitrate
	if budget > 0 {
		bitrate = max(min(bitrate, budget), 150_000)
	}
	return height, bitrate
}

// scaledWidth es el ancho de salida al escalar a height, par como pide
// yuv420p.
func scaledWidth(v *sqlc.MediaStream, height int) int {
	if v == nil || v.Width == nil || v.Height == nil || *v.Height == 0 {
		return 0
	}
	return int(*v.Width*int64(height)/(*v.Height)) &^ 1
}

// h264Profiles son los profile_idc del atributo CODECS de HLS (RFC 6381).
var h264Profiles = map[string]string{
	"baseline": "42", "constrained baseline": "42", "main": "4d", "high": "64", "high 10": "6e",
}

// videoCodecs devuelve el CODECS de un H.264 copiado, o vacío si no se sabe:
// un valor incorrecto es peor que ninguno.
func videoCodecs(v *sqlc.MediaStream) string {
	if v == nil || protocol.CodecName(deref(v.Codec)) != "h264" {
		return ""
	}
	prof, ok := h264Profiles[strings.ToLower(deref(v.Profile))]
	level, err := strconv.Atoi(deref(v.Level))
	if !ok || err != nil || level <= 0 || level > 62 {
		return ""
	}
	constraint := "00"
	if strings.EqualFold(deref(v.Profile), "constrained baseline") {
		constraint = "40"
	}
	return fmt.Sprintf("avc1.%s%s%02x", prof, constraint, level)
}

// encodedCodecs es el CODECS de la salida de libx264, perfil High.
func encodedCodecs(height int) string {
	if height <= 720 {
		return "avc1.64001f" // Nivel 3.1.
	}
	return "avc1.640028" // Nivel 4.0.
}

// audioCodecs es el CODECS del audio de salida.
func audioCodecs(a *sqlc.MediaStream, copied bool) string {
	if a == nil {
		return ""
	}
	if !copied {
		return "mp4a.40.2"
	}
	switch protocol.CodecName(deref(a.Codec)) {
	case "aac":
		return "mp4a.40.2"
	case "mp3":
		return "mp4a.40.34"
	case "ac3":
		return "ac-3"
	case "eac3":
		return "ec-3"
	}
	return ""
}
