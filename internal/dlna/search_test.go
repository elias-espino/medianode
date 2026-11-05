package dlna

import (
	"strings"
	"testing"
)

func TestTokenizar(t *testing.T) {
	toks, err := tokenize(`(dc:title contains "a \"b\" \\ c") and upnp:class = "x"`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range toks {
		got = append(got, k.kind+":"+k.text)
	}
	want := `(: word:dc:title word:contains str:a "b" \ c ): word:and word:upnp:class word:= str:x`
	if strings.Join(got, " ") != want {
		t.Fatalf("\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	for _, bad := range []string{`dc:title = "sin cerrar`, `x = "a\`, `"`} {
		if _, err := tokenize(bad); err == nil {
			t.Errorf("%q debía fallar", bad)
		}
	}
}

func TestCriteriosDeBusqueda(t *testing.T) {
	movie := subject{title: "El Señor de los Anillos", class: classMovie}
	episode := subject{title: "1x02 Piloto", class: classEpisode}
	cases := []struct {
		crit        string
		movie, epis bool
	}{
		{`*`, true, true},
		{` * `, true, true},
		{`dc:title contains "anillos"`, true, false},
		{`dc:title contains "ANILLOS"`, true, false},
		{`dc:title doesNotContain "anillos"`, false, true},
		{`dc:title = "1x02 piloto"`, false, true},
		{`dc:title != "1x02 Piloto"`, true, false},
		{`dc:title < "b"`, false, true},
		{`dc:title <= "1x02 piloto"`, false, true},
		{`dc:title > "b"`, true, false},
		{`dc:title >= "el señor de los anillos"`, true, false},
		{`upnp:class derivedfrom "object.item.videoItem"`, true, true},
		{`upnp:class derivedfrom "object.item.videoItem.movie"`, true, false},
		{`upnp:class derivedfrom "object.item.video"`, false, false}, // Una clase no deriva de un prefijo que no termina en punto.
		{`upnp:class = "object.item.videoItem.movie"`, true, false},
		{`upnp:class derivedfrom "object.item.videoItem" and dc:title contains "piloto"`, false, true},
		{`dc:title contains "piloto" or dc:title contains "anillos"`, true, true},
		{`dc:title contains "zzz" or dc:title contains "yyy" and upnp:class = "object.item.videoItem"`, false, false},
		{`(dc:title contains "anillos" or dc:title contains "piloto") and upnp:class = "object.item.videoItem"`, false, true},
		{`dc:title exists true`, true, true},
		{`dc:title exists false`, false, false},
		{`upnp:class EXISTS true`, true, true},
		{`DC:TITLE`, false, false}, // Se prueba más abajo como error.
	}
	for _, c := range cases {
		crit, err := parseCriteria(c.crit)
		if c.crit == `DC:TITLE` {
			if err == nil {
				t.Errorf("%q debía fallar", c.crit)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.crit, err)
			continue
		}
		if got := crit.match(movie); got != c.movie {
			t.Errorf("%q sobre la película = %v", c.crit, got)
		}
		if got := crit.match(episode); got != c.epis {
			t.Errorf("%q sobre el episodio = %v", c.crit, got)
		}
	}
}

func TestCriteriosInvalidos(t *testing.T) {
	deep := strings.Repeat("(", maxDepth+1) + `dc:title = "x"` + strings.Repeat(")", maxDepth+1)
	for _, bad := range []string{
		``, `   `, `dc:title`, `dc:title =`, `dc:title = x`, `dc:title ~ "x"`, `dc:title contains`,
		`upnp:artist = "x"`, `dc:creator contains "x"`, `dc:title derivedfrom "x"`, `upnp:class exists maybe`,
		`dc:title = "x" and`, `and dc:title = "x"`, `(dc:title = "x"`, `dc:title = "x")`, `dc:title = "x" dc:title = "y"`,
		`dc:title = "sin cerrar`, `* and dc:title = "x"`, deep, `dc:title = "x"; DROP TABLE media_items`,
	} {
		if _, err := parseCriteria(bad); err == nil {
			t.Errorf("%q debía rechazarse", bad)
		}
	}
	// Un nivel por debajo del límite sí vale.
	ok := strings.Repeat("(", maxDepth) + `dc:title = "x"` + strings.Repeat(")", maxDepth)
	if _, err := parseCriteria(ok); err != nil {
		t.Errorf("anidamiento permitido: %v", err)
	}
}

func TestLaBusquedaAnuncia(t *testing.T) {
	if searchCaps != "dc:title,upnp:class" {
		t.Fatal(searchCaps)
	}
	if classOf("movie") != classMovie || classOf("episode") != classEpisode {
		t.Fatal("classOf")
	}
}
