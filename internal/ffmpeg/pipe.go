package ffmpeg

import (
	"errors"
	"path/filepath"
	"strconv"
)

// PipeParams es un trabajo que emite MPEG-TS progresivo por la salida estándar,
// sin cortes ni listas: lo que consume un renderer DLNA, que pide una única URL
// y no entiende HLS (§13.2).
type PipeParams struct {
	Input // StartMS es el salto pedido; LimitUS no se usa.
	Audio
	// CopyVideo copia el vídeo; si no, se recodifica con Encoder.
	CopyVideo     bool
	Encoder       Encoder
	Height        int // Altura de salida al recodificar; 0 conserva la del origen.
	VideoBitrate  int // bit/s al recodificar.
	ToneMap       string
	Burn          Burn
	KeyframeEvery int // Segundos entre keyframes al recodificar; 2 por defecto.
}

// BuildPipe devuelve los argumentos de un trabajo que escribe MPEG-TS en la
// salida estándar. A diferencia de los trabajos HLS no usa -progress: la salida
// estándar es el vídeo.
func BuildPipe(p PipeParams) ([]string, error) {
	if p.Path == "" || !filepath.IsAbs(p.Path) {
		return nil, errors.New("la ruta de entrada debe ser absoluta")
	}
	if p.Video < 0 {
		return nil, errors.New("el archivo no tiene vídeo")
	}
	if p.StartMS < 0 {
		return nil, errors.New("el inicio no puede ser negativo")
	}
	if !p.CopyVideo && p.VideoBitrate <= 0 {
		return nil, errors.New("parámetros de codificación incompletos")
	}
	if p.CopyVideo && (p.ToneMap != "" || p.Burn.Kind != BurnNone || p.Height > 0) {
		return nil, errors.New("no se puede filtrar el vídeo si se copia")
	}
	seek := ""
	if p.StartMS > 0 {
		seek = seconds(p.StartMS)
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	args = append(args, inputOnly(Input{Path: p.Path}, seek)...)
	if p.CopyVideo {
		args = append(args, mapArgs(p.Input, "")...)
		args = append(args, "-c:v", "copy")
	} else {
		enc := p.Encoder
		if enc == "" {
			enc = X264
		}
		graph, err := videoGraph(EncodeParams{Input: p.Input, Height: p.Height, ToneMap: p.ToneMap, Burn: p.Burn})
		if err != nil {
			return nil, err
		}
		switch {
		case graph.complex != "":
			args = append(args, "-filter_complex", graph.complex)
			args = append(args, mapArgs(p.Input, "[v]")...)
		default:
			args = append(args, mapArgs(p.Input, "")...)
			if graph.simple != "" {
				args = append(args, "-vf", graph.simple)
			}
		}
		args = append(args, encoderArgs(enc, p.VideoBitrate)...)
		every := p.KeyframeEvery
		if every <= 0 {
			every = 2
		}
		args = append(args, "-force_key_frames", "expr:gte(t,n_forced*"+strconv.Itoa(every)+")")
	}
	args = append(args, audioArgs(p.Audio, p.Input.Audio >= 0)...)
	// -muxdelay 0: el primer paquete sale sin esperar; flush_packets: cada
	// paquete llega al cliente en cuanto está listo.
	args = append(args, "-sn", "-dn", "-muxdelay", "0", "-flush_packets", "1", "-f", "mpegts", "pipe:1")
	return args, nil
}
