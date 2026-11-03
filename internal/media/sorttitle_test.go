package media

import "testing"

func TestSortTitle(t *testing.T) {
	cases := []struct{ title, lang, want string }{
		{"The Matrix", "", "matrix"},
		{"The Matrix", "es-ES", "matrix"}, // El inglés se quita siempre.
		{"El Padrino", "es-ES", "padrino"},
		{"El Padrino", "", "padrino"},
		{"El Padrino", "fr-FR", "el padrino"}, // Con idioma conocido, solo ese y el inglés.
		{"Los Otros", "es", "otros"},
		{"La La Land", "", "la land"},
		{"Amélie", "", "amelie"},
		{"Ñandú", "", "nandu"},
		{"¿Qué hacemos con Maisie?", "es-ES", "que hacemos con maisie?"},
		{"L'Odyssée", "fr-FR", "odyssee"},
		{"L’Avventura", "it", "avventura"},
		{"Theater Camp", "", "theater camp"}, // «the» exige espacio detrás.
		{"An American Werewolf", "", "american werewolf"},
		{"A", "", "a"},       // Un título que solo es un artículo se conserva.
		{"The", "en", "the"}, // Igual.
		{"  The   Thing  ", "", "thing"},
		{"Der Untergang", "de-DE", "untergang"},
		{"Uno", "es", "uno"},
		{"Unos días", "es", "dias"},
		{"2001: A Space Odyssey", "", "2001: a space odyssey"},
		{"...And Justice for All", "", "and justice for all"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := SortTitle(c.title, c.lang); got != c.want {
			t.Errorf("SortTitle(%q, %q) = %q, se esperaba %q", c.title, c.lang, got, c.want)
		}
	}
}
