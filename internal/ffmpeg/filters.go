package ffmpeg

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Funciones de transferencia HDR que se tonemapean a SDR.
const (
	TransferPQ  = "smpte2084"    // HDR10, HDR10+ y la capa base de Dolby Vision.
	TransferHLG = "arib-std-b67" // HLG.
)

// BurnKind es cómo se graba un subtítulo en la imagen.
type BurnKind int

// Tipos de grabado.
const (
	BurnNone  BurnKind = iota
	BurnText           // Texto (ASS, SRT, WebVTT) con el filtro subtitles.
	BurnImage          // Imagen (PGS, VobSub) con overlay.
)

// Burn describe el subtítulo que se graba en el vídeo.
type Burn struct {
	Kind BurnKind
	// File es el subtítulo de texto, en el directorio de trabajo del trabajo.
	File string
	// Stream es el índice de la pista de imagen en el archivo de origen.
	Stream int
}

// reFilterName limita los nombres que entran en una cadena de filtros: ahí
// los dos puntos, las comas y las comillas tienen significado propio.
var reFilterName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type graph struct {
	simple  string // para -vf
	complex string // para -filter_complex; etiqueta de salida [v]
}

// toneMapFilter lleva HDR a SDR BT.709 con zscale y tonemap. El origen se
// declara explícitamente (setparams y zscale): un archivo con las etiquetas
// de color incompletas no debe hacer fallar el filtro.
func toneMapFilter(transfer string) string {
	return "setparams=color_primaries=bt2020:color_trc=" + transfer + ":colorspace=bt2020nc," +
		"zscale=tin=" + transfer + ":min=bt2020nc:pin=bt2020:rin=tv:t=linear:npl=100," +
		"format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0," +
		"zscale=t=bt709:m=bt709:r=tv,format=yuv420p"
}

// videoGraph compone la cadena de vídeo en un orden fijo.
//
// Con subtítulos grabados: tone mapping, subtítulos y al final el escalado, de
// modo que los subtítulos no se tonemapean y se colocan sobre la resolución
// original.
//
// Sin ellos, el escalado va antes del tone mapping: este trabaja en coma
// flotante y cuesta por píxel, así que un 4K que se entrega en 1080p se
// tonemapea con la cuarta parte del trabajo.
func videoGraph(p EncodeParams) (graph, error) {
	var pre, post []string
	var tone string
	switch p.ToneMap {
	case "":
	case TransferPQ, TransferHLG:
		tone = toneMapFilter(p.ToneMap)
	default:
		return graph{}, fmt.Errorf("transferencia HDR desconocida: %q", p.ToneMap)
	}
	var scale string
	if p.Height > 0 {
		// -2 conserva la proporción con un ancho par, que exige yuv420p.
		scale = "scale=-2:" + strconv.Itoa(p.Height)
	}
	if p.Burn.Kind == BurnNone {
		var all []string
		if scale != "" {
			all = append(all, scale)
		}
		if tone != "" {
			all = append(all, tone)
		}
		return graph{simple: strings.Join(all, ",")}, nil
	}
	if tone != "" {
		pre = append(pre, tone)
	}
	if scale != "" {
		post = append(post, scale)
	}
	switch p.Burn.Kind {
	case BurnNone:
		return graph{simple: strings.Join(append(pre, post...), ",")}, nil
	case BurnText:
		if !reFilterName.MatchString(p.Burn.File) {
			return graph{}, errors.New("nombre de subtítulo no válido")
		}
		// Tras -ss los fotogramas empiezan en cero, pero el subtítulo usa la
		// línea de tiempo del archivo: se desplaza antes y se devuelve después.
		if p.StartMS > 0 {
			pre = append(pre, "setpts=PTS+"+seconds(p.StartMS)+"/TB")
		}
		pre = append(pre, "subtitles="+p.Burn.File)
		if p.StartMS > 0 {
			pre = append(pre, "setpts=PTS-STARTPTS")
		}
		return graph{simple: strings.Join(append(pre, post...), ",")}, nil
	case BurnImage:
		if p.Burn.Stream < 0 {
			return graph{}, errors.New("pista de subtítulo de imagen no válida")
		}
		chain := func(in, out string, f []string) string {
			if len(f) == 0 {
				f = []string{"null"}
			}
			return in + strings.Join(f, ",") + out
		}
		src := "[0:" + strconv.Itoa(p.Video) + "]"
		return graph{complex: strings.Join([]string{
			chain(src, "[a]", pre),
			"[a][0:" + strconv.Itoa(p.Burn.Stream) + "]overlay=format=auto[b]",
			chain("[b]", "[v]", post),
		}, ";")}, nil
	}
	return graph{}, errors.New("tipo de grabado desconocido")
}
