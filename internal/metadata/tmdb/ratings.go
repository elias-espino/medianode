package tmdb

import (
	"strconv"
	"strings"
)

// Edad mínima de cada clasificación (§15.1). Las que no son un número se
// traducen aquí; las numéricas («12», «16+») se leen tal cual. PG y TV-PG no
// prohíben nada, pero recomiendan supervisión: se tratan como 10 años, igual
// que hacen otros servidores multimedia, para que un perfil infantil no las vea.
var certifications = map[string]map[string]int64{
	"US": {
		"G": 0, "PG": 10, "PG-13": 13, "R": 17, "NC-17": 18,
		"TV-Y": 0, "TV-Y7": 7, "TV-G": 0, "TV-PG": 10, "TV-14": 14, "TV-MA": 17,
	},
	"ES": {"A": 0, "APTA": 0, "TP": 0, "7I": 7, "X": 18},
	"GB": {"U": 0, "PG": 8, "12A": 12, "R18": 18},
	"FR": {"U": 0, "TP": 0},
	"DE": {},
}

// minAge normaliza una clasificación de un país, o devuelve false si no la
// reconoce. Una clasificación desconocida deja la edad sin definir y el
// elemento queda oculto a los usuarios restringidos.
func minAge(country, cert string) (int64, bool) {
	c := strings.ToUpper(strings.TrimSpace(cert))
	if n, ok := certifications[country][c]; ok {
		return n, true
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(c, "+"), 10, 64)
	if err != nil || n < 0 || n > 21 {
		return 0, false
	}
	return n, true
}

// pickRating elige la clasificación de la región configurada y, si no la hay o
// no se reconoce, la de Estados Unidos, la más completa en TMDB.
func pickRating(ratings map[string]string, region string) (*string, *int64) {
	for _, country := range []string{region, "US"} {
		cert := strings.TrimSpace(ratings[country])
		if country == "" || cert == "" {
			continue
		}
		if age, ok := minAge(country, cert); ok {
			label := country + ":" + cert
			return &label, &age
		}
	}
	return nil, nil
}
