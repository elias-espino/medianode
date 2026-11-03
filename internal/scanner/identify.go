package scanner

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// MovieGuess es la identificación local de una película.
type MovieGuess struct {
	Title string
	Year  *int64
}

// EpisodeGuess es la identificación local de uno o varios episodios.
type EpisodeGuess struct {
	Series   string
	Year     *int64
	Season   int64
	Episodes []int64
	// Title es el título del episodio que lleva el nombre del archivo, si lo
	// hay: «Serie 01 - Título». Solo con un episodio por archivo.
	Title string
}

var (
	reSxxEyy     = regexp.MustCompile(`(?i)S(\d{1,2})[ ._-]?E(\d{1,4})(?:-?E(\d{1,4}))?`)
	reNxNN       = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])(\d{1,2})x(\d{2,3})(?:[^0-9]|$)`)
	reSeasonDir  = regexp.MustCompile(`(?i)^(?:Season|Temporada|Staffel|Saison|S)[ ._-]?(\d{1,3})$`)
	reYearBounds = regexp.MustCompile(`(?:19|20)\d{2}`)
	// «Serie 01 - Título», «Serie - 01», «Serie Cap 03» o «Serie 12»: el
	// número va tras el nombre y, si lo sigue algo, es un guion. Tres cifras
	// como mucho, para no confundir un año con un episodio.
	reBareNamed = regexp.MustCompile(`(?i)^(.*?\S)[ ._]+(?:-[ ._]+)?(?:episodio|episode|cap|ep|e)?[ ._]?(\d{1,3})(?:[ ._]*-[ ._]+(.+)|[ ._]*)$`)
	// Respaldo del anterior, cuando el separador no es « - »: «Serie 30 -Título»,
	// «Serie 41_ Título» o «Serie 42 Título». Va después, para que un número
	// seguido de « - » gane a otro que solo va seguido de espacio.
	reBareLoose = regexp.MustCompile(`(?i)^(.*?\S)[ ._]+(?:episodio|episode|cap|ep|e)?[ ._]?(\d{1,3})[ ._:-]+(.+)$`)
	// «01 - Título» dentro de la carpeta de la serie, con los mismos separadores.
	reBareLeading = regexp.MustCompile(`(?i)^(?:episodio|episode|cap|ep|e)?[ ._]?(\d{1,3})(?:[ ._:-]+(.+))?$`)
)

// Etiquetas técnicas que marcan el final del título cuando no hay año.
var releaseTags = map[string]bool{
	"480p": true, "576p": true, "720p": true, "1080p": true, "1080i": true, "2160p": true,
	"4k": true, "uhd": true, "bluray": true, "blu-ray": true, "bdrip": true, "brrip": true,
	"webrip": true, "web-dl": true, "webdl": true, "web": true, "hdtv": true, "dvdrip": true,
	"dvd": true, "remux": true, "x264": true, "x265": true, "h264": true, "h265": true,
	"hevc": true, "avc": true, "hdr": true, "hdr10": true, "dv": true, "10bit": true,
	"proper": true, "repack": true, "extended": true, "unrated": true, "remastered": true,
	"multi": true, "dual": true, "aac": true, "ac3": true, "dts": true, "atmos": true,
}

// ParseMovie identifica una película por el nombre del archivo y, si este no
// lo resuelve, por la carpeta que lo contiene. relPath usa «/».
func ParseMovie(relPath string) MovieGuess {
	file := stem(path.Base(relPath))
	dir := path.Base(path.Dir(relPath))

	title, year := titleYear(file)
	if year == nil && dir != "." && dir != "/" {
		if dirTitle, dirYear := titleYear(dir); dirYear != nil {
			return MovieGuess{Title: dirTitle, Year: dirYear}
		}
	}
	if title == "" && dir != "." {
		title, year = titleYear(dir)
	}
	if title == "" {
		title = clean(file)
	}
	return MovieGuess{Title: title, Year: year}
}

// ParseEpisode identifica episodios por SxxEyy, NxNN o, si no hay ninguno, por
// un número suelto tras el nombre («Serie 01 - Título», temporada 1 o la de su
// carpeta). La serie sale de la primera carpeta bajo la raíz o, si el archivo
// está en la raíz o en una carpeta de temporada, de lo que precede al patrón
// en el nombre.
func ParseEpisode(relPath string) (EpisodeGuess, bool) {
	file := stem(path.Base(relPath))
	g := EpisodeGuess{}
	var prefix string

	if m := reSxxEyy.FindStringSubmatchIndex(file); m != nil {
		g.Season = atoi(file[m[2]:m[3]])
		first := atoi(file[m[4]:m[5]])
		last := first
		if m[6] >= 0 {
			last = atoi(file[m[6]:m[7]])
		}
		g.Episodes = episodeRange(first, last)
		prefix = file[:m[0]]
	} else if m := reNxNN.FindStringSubmatchIndex(file); m != nil {
		g.Season = atoi(file[m[2]:m[3]])
		g.Episodes = []int64{atoi(file[m[4]:m[5]])}
		prefix = file[:m[2]]
	} else if m := reBareNamed.FindStringSubmatch(file); m != nil {
		g.Season, g.Episodes = seasonFromDirs(relPath), []int64{atoi(m[2])}
		g.Title, prefix = clean(m[3]), m[1]
	} else if m := reBareLoose.FindStringSubmatch(file); m != nil {
		g.Season, g.Episodes = seasonFromDirs(relPath), []int64{atoi(m[2])}
		g.Title, prefix = clean(m[3]), m[1]
	} else if m := reBareLeading.FindStringSubmatch(file); m != nil && hasSeriesDir(relPath) {
		g.Season, g.Episodes = seasonFromDirs(relPath), []int64{atoi(m[1])}
		g.Title = clean(m[2])
	} else {
		return EpisodeGuess{}, false
	}
	if len(g.Episodes) != 1 || g.Episodes[0] < 1 {
		g.Title = ""
	}

	parts := strings.Split(path.Dir(relPath), "/")
	if len(parts) > 0 && parts[0] != "." && parts[0] != "" && !reSeasonDir.MatchString(parts[0]) {
		g.Series, g.Year = titleYear(parts[0])
	}
	if g.Series == "" {
		g.Series, g.Year = titleYear(prefix)
	}
	if g.Series == "" {
		return EpisodeGuess{}, false
	}
	return g, true
}

// seasonFromDirs toma la temporada de una carpeta «Temporada 2»; sin ella, 1.
func seasonFromDirs(relPath string) int64 {
	for _, d := range strings.Split(path.Dir(relPath), "/") {
		if m := reSeasonDir.FindStringSubmatch(d); m != nil {
			return atoi(m[1])
		}
	}
	return 1
}

// hasSeriesDir indica si el archivo está bajo una carpeta que no es de
// temporada: sin ella no hay de dónde sacar el nombre de la serie.
func hasSeriesDir(relPath string) bool {
	first := strings.Split(path.Dir(relPath), "/")[0]
	return first != "." && first != "" && !reSeasonDir.MatchString(first)
}

// episodeRange expande S01E01-E03. Un rango invertido o desmesurado se reduce
// al primer episodio: suele ser un número que no forma parte del patrón.
func episodeRange(first, last int64) []int64 {
	if last <= first || last-first > 20 {
		return []int64{first}
	}
	out := make([]int64, 0, last-first+1)
	for n := first; n <= last; n++ {
		out = append(out, n)
	}
	return out
}

// titleYear separa título y año. Se toma el último año plausible delimitado:
// en «Blade.Runner.2049.2017» el año es 2017, y en «Blade Runner 2049» no hay
// año porque 2049 aún no ha llegado.
func titleYear(s string) (string, *int64) {
	maxYear := int64(time.Now().Year() + 1)
	cut, year := -1, int64(0)
	for _, m := range reYearBounds.FindAllStringIndex(s, -1) {
		if m[0] == 0 || !isDelim(s[m[0]-1]) || (m[1] < len(s) && !isDelim(s[m[1]])) {
			continue
		}
		y := atoi(s[m[0]:m[1]])
		if y > maxYear {
			continue
		}
		if title := clean(s[:m[0]-1]); title != "" {
			cut, year = m[0]-1, y
		}
	}
	if cut < 0 {
		return clean(cutAtTags(s)), nil
	}
	return clean(s[:cut]), &year
}

func isDelim(c byte) bool {
	switch c {
	case ' ', '.', '_', '-', '(', ')', '[', ']':
		return true
	}
	return false
}

// cutAtTags corta el nombre en la primera etiqueta técnica.
func cutAtTags(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '.' || r == '_' })
	for i, w := range words {
		if releaseTags[strings.ToLower(strings.Trim(w, "()[]"))] && i > 0 {
			return strings.Join(words[:i], " ")
		}
	}
	return s
}

// clean normaliza separadores y quita los restos que deja el patrón, como el
// paréntesis abierto de «Matrix (». Los puntos solo separan palabras en
// nombres sin espacios: en «Mr. Robot» son parte del título.
func clean(s string) string {
	if !strings.Contains(s, " ") {
		s = strings.ReplaceAll(s, ".", " ")
	}
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, " -,")
	return strings.TrimRightFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("-([{,.", r)
	})
}

// MatchKey es la clave de agrupación local: título sin mayúsculas, signos ni
// espacios sobrantes, y el año si se conoce.
func MatchKey(title string, year *int64) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(title) {
		if r == '\'' || r == '’' {
			continue // «Ocean's» y «Oceans» agrupan
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		} else {
			space = true
		}
	}
	if year != nil {
		b.WriteString("|")
		b.WriteString(strconv.FormatInt(*year, 10))
	}
	return b.String()
}

func stem(name string) string {
	return strings.TrimSuffix(name, path.Ext(name))
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
