package database

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	reQueryName = regexp.MustCompile(`(?m)^-- name: (\w+)`)
	reGenerated = regexp.MustCompile("(?s)-- name: (\\w+) :\\w+\n(.*?)`")
	// sqlc sustituye cada sqlc.arg(nombre) por ?N; ambos se comparan como «?».
	reParam = regexp.MustCompile(`sqlc\.n?arg\(\w+\)|\?\d+`)
)

// sqlc calcula mal los desplazamientos si un comentario de queries/ tiene
// caracteres no ASCII, y genera la consulta truncada: «LIMIT 1» quedó en
// «LIMIT». Esta prueba exige que cada consulta generada termine como su
// fuente.
func TestConsultasGeneradasCompletas(t *testing.T) {
	sources, err := filepath.Glob(filepath.Join("..", "..", "queries", "*.sql"))
	if err != nil || len(sources) == 0 {
		t.Fatalf("sin consultas: %v", err)
	}
	generated, err := filepath.Glob(filepath.Join("sqlc", "*.sql.go"))
	if err != nil {
		t.Fatal(err)
	}
	// Cada consulta se compara con su propia constante generada: una línea
	// como «LIMIT 1» puede aparecer en otra consulta y ocultar el truncado.
	gen := map[string]string{}
	for _, g := range generated {
		data, err := os.ReadFile(g)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range reGenerated.FindAllStringSubmatch(strings.ReplaceAll(string(data), "\r\n", "\n"), -1) {
			gen[m[1]] = strings.TrimSpace(m[2])
		}
	}

	checked := 0
	for _, src := range sources {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "--") && strings.ContainsFunc(line, func(r rune) bool { return r > 127 }) {
				t.Errorf("%s: comentario con caracteres no ASCII: %q", filepath.Base(src), line)
			}
			// sqlc numera el parámetro de un BETWEEN pero no lo pone en la
			// estructura generada, y los valores se enlazan descolocados.
			if !strings.HasPrefix(strings.TrimSpace(line), "--") && strings.Contains(strings.ToUpper(line), "BETWEEN") &&
				reParam.MatchString(line) {
				t.Errorf("%s: BETWEEN con parámetros: %q", filepath.Base(src), line)
			}
		}
		idx := reQueryName.FindAllStringSubmatchIndex(text, -1)
		for i, m := range idx {
			end := len(text)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			var last string
			for _, l := range strings.Split(text[m[1]:end], "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "--") {
					last = strings.TrimSuffix(l, ";")
				}
			}
			if strings.Contains(last, "SELECT *") {
				last = last[strings.Index(last, "FROM"):] // sqlc expande el asterisco
			}
			name := text[m[2]:m[3]]
			got := reParam.ReplaceAllString(strings.TrimSuffix(gen[name], ";"), "?")
			if !strings.HasSuffix(got, reParam.ReplaceAllString(last, "?")) {
				t.Errorf("%s: la consulta %s generada no termina en %q", filepath.Base(src), name, last)
			}
			checked++
		}
	}
	if checked < 50 {
		t.Fatalf("solo se revisaron %d consultas", checked)
	}
}
