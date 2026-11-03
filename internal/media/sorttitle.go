package media

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Artículos iniciales por idioma. Los ingleses se quitan siempre, porque muchos
// títulos sin traducir conservan el original; sin idioma conocido se añaden los
// del español, el de la primera versión.
var articles = map[string][]string{
	"en": {"the", "a", "an"},
	"es": {"el", "la", "los", "las", "un", "una", "unos", "unas"},
	"fr": {"le", "la", "les", "l'", "l’", "un", "une"},
	"de": {"der", "die", "das", "ein", "eine"},
	"it": {"il", "lo", "la", "i", "gli", "le", "l'", "l’", "un", "uno", "una"},
	"pt": {"o", "a", "os", "as", "um", "uma"},
}

var defaultArticles = append(append([]string{}, articles["en"]...), articles["es"]...)

// SortTitle genera el título ordenable de §6.3: sin artículo inicial, sin
// mayúsculas ni acentos y sin signos al principio. lang es una etiqueta BCP 47
// como «es-ES», o vacía si se desconoce. Un título que solo es un artículo se
// conserva.
func SortTitle(title, lang string) string {
	s := strings.ToLower(strings.Join(strings.Fields(title), " "))
	s = strings.TrimLeftFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })

	list := defaultArticles
	if primary, _, _ := strings.Cut(strings.ToLower(lang), "-"); articles[primary] != nil {
		list = append(append([]string{}, articles[primary]...), articles["en"]...)
	}
	for _, a := range list {
		rest, ok := strings.CutPrefix(s, a)
		if !ok {
			continue
		}
		// «l'» va pegado a la palabra; el resto exige un espacio detrás.
		if !strings.HasSuffix(a, "'") && !strings.HasSuffix(a, "’") {
			if rest, ok = strings.CutPrefix(rest, " "); !ok {
				continue
			}
		}
		if rest != "" {
			s = rest
		}
		break
	}
	return foldDiacritics(s)
}

// foldDiacritics quita los acentos: descompone cada carácter y descarta las
// marcas. «ñ» queda como «n», igual que hace la búsqueda.
func foldDiacritics(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}
