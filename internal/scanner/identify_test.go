package scanner

import (
	"fmt"
	"slices"
	"testing"
)

func yr(y int64) *int64 { return &y }

func fmtYear(y *int64) string {
	if y == nil {
		return "-"
	}
	return fmt.Sprint(*y)
}

func TestParseMovie(t *testing.T) {
	cases := []struct {
		path  string
		title string
		year  *int64
	}{
		{"The Matrix (1999).mkv", "The Matrix", yr(1999)},
		{"The.Matrix.1999.1080p.BluRay.x264.mkv", "The Matrix", yr(1999)},
		{"The_Matrix_1999_720p.mp4", "The Matrix", yr(1999)},
		{"The Matrix [1999].avi", "The Matrix", yr(1999)},
		{"The Matrix 1999.mkv", "The Matrix", yr(1999)},
		{"The Matrix-1999.mkv", "The Matrix", yr(1999)},
		{"Matrix (1999)/Matrix (1999).mkv", "Matrix", yr(1999)},
		{"The Matrix (1999)/movie.mkv", "The Matrix", yr(1999)},
		{"The Matrix (1999)/VTS_01_1.vob", "The Matrix", yr(1999)},
		{"Peliculas/Matrix (1999)/Matrix.mkv", "Matrix", yr(1999)},
		{"1917 (2019).mkv", "1917", yr(2019)},
		{"1917.2019.2160p.UHD.mkv", "1917", yr(2019)},
		{"2001 A Space Odyssey (1968).mkv", "2001 A Space Odyssey", yr(1968)},
		{"2001.A.Space.Odyssey.1968.Remastered.mkv", "2001 A Space Odyssey", yr(1968)},
		{"Blade Runner 2049 (2017).mkv", "Blade Runner 2049", yr(2017)},
		{"Blade.Runner.2049.2017.1080p.mkv", "Blade Runner 2049", yr(2017)},
		{"Blade Runner 2049.mkv", "Blade Runner 2049", nil},
		{"2012 (2009).mkv", "2012", yr(2009)},
		{"2012.2009.720p.mkv", "2012", yr(2009)},
		{"1984 (1984).mkv", "1984", yr(1984)},
		{"Alien.1979.Directors.Cut.mkv", "Alien", yr(1979)},
		{"Aliens (1986) Special Edition.mkv", "Aliens", yr(1986)},
		{"El laberinto del fauno (2006).mkv", "El laberinto del fauno", yr(2006)},
		{"Amélie (2001).mkv", "Amélie", yr(2001)},
		{"Léon - The Professional (1994).mkv", "Léon - The Professional", yr(1994)},
		{"Crouching Tiger, Hidden Dragon (2000).mkv", "Crouching Tiger, Hidden Dragon", yr(2000)},
		{"Mission Impossible - Fallout (2018).mkv", "Mission Impossible - Fallout", yr(2018)},
		{"Spider-Man (2002).mkv", "Spider-Man", yr(2002)},
		{"Spider-Man.Into.the.Spider-Verse.2018.mkv", "Spider-Man Into the Spider-Verse", yr(2018)},
		{"WALL-E (2008).mkv", "WALL-E", yr(2008)},
		{"Se7en (1995).mkv", "Se7en", yr(1995)},
		{"Ocean's Eleven (2001).mkv", "Ocean's Eleven", yr(2001)},
		{"Dr. Strangelove (1964).mkv", "Dr. Strangelove", yr(1964)},
		{"Star Wars Episode IV - A New Hope (1977).mkv", "Star Wars Episode IV - A New Hope", yr(1977)},
		{"The Lord of the Rings The Fellowship of the Ring (2001) Extended.mkv", "The Lord of the Rings The Fellowship of the Ring", yr(2001)},
		{"Avatar.2009.EXTENDED.1080p.mkv", "Avatar", yr(2009)},
		{"Dune.Part.Two.2024.2160p.WEB-DL.mkv", "Dune Part Two", yr(2024)},
		{"Oppenheimer (2023) [2160p].mkv", "Oppenheimer", yr(2023)},
		{"Parasite.2019.KOREAN.1080p.mkv", "Parasite", yr(2019)},
		{"Coco (2017) - 4K.mkv", "Coco", yr(2017)},
		{"Coco (2017) - 1080p.mkv", "Coco", yr(2017)},
		{"Up.2009.mkv", "Up", yr(2009)},
		{"It (2017).mkv", "It", yr(2017)},
		{"It.Chapter.Two.2019.mkv", "It Chapter Two", yr(2019)},
		{"Her.2013.mkv", "Her", yr(2013)},
		{"Taxi Driver.mkv", "Taxi Driver", nil},
		{"Taxi.Driver.1080p.BluRay.mkv", "Taxi Driver", nil},
		{"Pulp_Fiction.mkv", "Pulp Fiction", nil},
		{"pulp fiction.MKV", "pulp fiction", nil},
		{"Casablanca.1942.mp4", "Casablanca", yr(1942)},
		{"Metropolis (1927).mkv", "Metropolis", yr(1927)},
		{"Nosferatu.1922.mkv", "Nosferatu", yr(1922)},
		{"Psycho (1960) (Remastered).mkv", "Psycho", yr(1960)},
		{"The.Thing.1982.REMASTERED.mkv", "The Thing", yr(1982)},
		{"Fight Club (1999) [1080p] [BluRay].mkv", "Fight Club", yr(1999)},
		{"Interstellar.(2014).mkv", "Interstellar", yr(2014)},
		{"Gravity 2013 3D.mkv", "Gravity", yr(2013)},
		{"Frozen II (2019).mkv", "Frozen II", yr(2019)},
		{"Toy Story 3 (2010).mkv", "Toy Story 3", yr(2010)},
		{"Rocky.IV.1985.mkv", "Rocky IV", yr(1985)},
		{"Terminator 2 Judgment Day (1991).mkv", "Terminator 2 Judgment Day", yr(1991)},
		{"Apollo 13 (1995).mkv", "Apollo 13", yr(1995)},
		{"District 9 (2009).mkv", "District 9", yr(2009)},
		{"Ocean's 8 (2018).mkv", "Ocean's 8", yr(2018)},
		{"(500) Days of Summer (2009).mkv", "(500) Days of Summer", yr(2009)},
		{"[REC] (2007).mkv", "[REC]", yr(2007)},
		{"Movie (2099).mkv", "Movie (2099)", nil},
		{"Space 1999.mkv", "Space", yr(1999)},
		{"Death Race 2000 (1975).mkv", "Death Race 2000", yr(1975)},
		{"Sin año/Pelicula casera.mp4", "Pelicula casera", nil},
		{"Vacaciones 2015/clip.mp4", "Vacaciones", yr(2015)},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			g := ParseMovie(c.path)
			if g.Title != c.title || fmtYear(g.Year) != fmtYear(c.year) {
				t.Fatalf("= %q (%s), se esperaba %q (%s)", g.Title, fmtYear(g.Year), c.title, fmtYear(c.year))
			}
		})
	}
	if len(cases) < 60 {
		t.Fatalf("la tabla de películas debe tener al menos 60 casos: %d", len(cases))
	}
}

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		path     string
		ok       bool
		series   string
		year     *int64
		season   int64
		episodes []int64
	}{
		{"Breaking Bad/Season 1/Breaking.Bad.S01E01.Pilot.mkv", true, "Breaking Bad", nil, 1, []int64{1}},
		{"Breaking Bad/Season 01/S01E02.mkv", true, "Breaking Bad", nil, 1, []int64{2}},
		{"Breaking Bad/Breaking Bad - S05E16 - Felina.mkv", true, "Breaking Bad", nil, 5, []int64{16}},
		{"Breaking.Bad.S02E03.720p.HDTV.x264.mkv", true, "Breaking Bad", nil, 2, []int64{3}},
		{"breaking.bad.s02e03.mkv", true, "breaking bad", nil, 2, []int64{3}},
		{"Breaking Bad 1x01.mkv", true, "Breaking Bad", nil, 1, []int64{1}},
		{"Breaking Bad/1x01 - Pilot.mkv", true, "Breaking Bad", nil, 1, []int64{1}},
		{"Breaking Bad/Temporada 2/2x05.mkv", true, "Breaking Bad", nil, 2, []int64{5}},
		{"Lost/Season 1/Lost.S01E01-E02.mkv", true, "Lost", nil, 1, []int64{1, 2}},
		{"Lost/Season 1/Lost.S01E01E02.mkv", true, "Lost", nil, 1, []int64{1, 2}},
		{"Lost/Season 1/Lost.S01E01-E03.mkv", true, "Lost", nil, 1, []int64{1, 2, 3}},
		{"Lost/Lost S01E05-E04.mkv", true, "Lost", nil, 1, []int64{5}},
		{"Lost/Lost S01E01-E99.mkv", true, "Lost", nil, 1, []int64{1}},
		{"Doctor Who (2005)/Season 1/Doctor.Who.2005.S01E01.mkv", true, "Doctor Who", yr(2005), 1, []int64{1}},
		{"Doctor Who (1963)/S01E01.mkv", true, "Doctor Who", yr(1963), 1, []int64{1}},
		{"Doctor.Who.2005.S01E01.mkv", true, "Doctor Who", yr(2005), 1, []int64{1}},
		{"The Office (US)/Season 2/The.Office.US.S02E01.mkv", true, "The Office (US)", nil, 2, []int64{1}},
		{"The.Office.US.S02E01.mkv", true, "The Office US", nil, 2, []int64{1}},
		{"Game of Thrones/Specials/Game.of.Thrones.S00E01.mkv", true, "Game of Thrones", nil, 0, []int64{1}},
		{"One Piece/One.Piece.S01E1015.mkv", true, "One Piece", nil, 1, []int64{1015}},
		{"Friends/S10E17-E18.mkv", true, "Friends", nil, 10, []int64{17, 18}},
		{"Friends/Friends S10 E17.mkv", true, "Friends", nil, 10, []int64{17}},
		{"Friends/Friends.S10.E17.mkv", true, "Friends", nil, 10, []int64{17}},
		{"Friends/Friends.S10-E17.mkv", true, "Friends", nil, 10, []int64{17}},
		{"La casa de papel/Temporada 1/La.casa.de.papel.S01E01.mkv", true, "La casa de papel", nil, 1, []int64{1}},
		{"Dark/Dark.S03E08.2160p.NF.WEB-DL.mkv", true, "Dark", nil, 3, []int64{8}},
		{"24/Season 1/24.S01E01.mkv", true, "24", nil, 1, []int64{1}},
		{"24.S01E01.mkv", true, "24", nil, 1, []int64{1}},
		{"1883/1883.S01E01.mkv", true, "1883", nil, 1, []int64{1}},
		{"9-1-1/9-1-1.S01E01.mkv", true, "9-1-1", nil, 1, []int64{1}},
		{"Season 1/Show.Name.S01E01.mkv", true, "Show Name", nil, 1, []int64{1}},
		{"Season 1/S01E01.mkv", false, "", nil, 0, nil},
		{"S01E01.mkv", false, "", nil, 0, nil},
		{"Show/Show - Episode 5.mkv", true, "Show", nil, 1, []int64{5}}, // Antes se descartaba; el número suelto ya cuenta.
		{"Show/Video 1080x720.mkv", false, "", nil, 0, nil},
		{"Show.1920x1080.mkv", false, "", nil, 0, nil},
		{"Show/Show.x264.mkv", false, "", nil, 0, nil},
		{"Show/Show - 3x07 - Title.mkv", true, "Show", nil, 3, []int64{7}},
		{"Show/show.03x07.mkv", true, "Show", nil, 3, []int64{7}},
		{"Show/Season 3/Show.3x07.x264.mkv", true, "Show", nil, 3, []int64{7}},
		{"Mr. Robot/Mr.Robot.S01E01.mkv", true, "Mr. Robot", nil, 1, []int64{1}},
		{"Mr.Robot.S01E01.mkv", true, "Mr Robot", nil, 1, []int64{1}},
		{"Star Trek - The Next Generation/S01E01.mkv", true, "Star Trek - The Next Generation", nil, 1, []int64{1}},
		{"House of Cards (2013)/Season 1/S01E01.mkv", true, "House of Cards", yr(2013), 1, []int64{1}},
		{"Shōgun (2024)/Shogun.2024.S01E01.mkv", true, "Shōgun", yr(2024), 1, []int64{1}},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			g, ok := ParseEpisode(c.path)
			if ok != c.ok {
				t.Fatalf("ok = %v (%+v)", ok, g)
			}
			if !ok {
				return
			}
			if g.Series != c.series || fmtYear(g.Year) != fmtYear(c.year) || g.Season != c.season || !slices.Equal(g.Episodes, c.episodes) {
				t.Fatalf("= %q (%s) T%d %v, se esperaba %q (%s) T%d %v",
					g.Series, fmtYear(g.Year), g.Season, g.Episodes, c.series, fmtYear(c.year), c.season, c.episodes)
			}
		})
	}
	if len(cases) < 40 {
		t.Fatalf("la tabla de episodios debe tener al menos 40 casos: %d", len(cases))
	}
}

func TestMatchKeyAgrupaVariantes(t *testing.T) {
	same := [][2]string{
		{"The Matrix", "the matrix"},
		{"Spider-Man", "Spider Man"},
		{"Ocean's Eleven", "Oceans Eleven"},
		{"  Coco ", "Coco"},
	}
	for _, p := range same {
		if MatchKey(p[0], yr(2000)) != MatchKey(p[1], yr(2000)) {
			t.Fatalf("%q y %q deberían agrupar", p[0], p[1])
		}
	}
	if MatchKey("Doctor Who", yr(1963)) == MatchKey("Doctor Who", yr(2005)) {
		t.Fatal("el año distingue elementos")
	}
	if MatchKey("Coco", nil) == MatchKey("Coco", yr(2017)) {
		t.Fatal("sin año no debe coincidir con un año concreto")
	}
}

func TestParseSubtitleName(t *testing.T) {
	cases := []struct {
		video, name   string
		ok            bool
		lang, format  string
		forced, deflt bool
	}{
		{"Movie (1999)", "Movie (1999).srt", true, "und", "srt", false, false},
		{"Movie (1999)", "Movie (1999).es.srt", true, "es", "srt", false, false},
		{"Movie (1999)", "Movie (1999).spa.forced.srt", true, "spa", "srt", true, false},
		{"Movie (1999)", "Movie (1999).en.default.ass", true, "en", "ass", false, true},
		{"Movie (1999)", "Movie (1999).pt-BR.sdh.vtt", true, "pt-br", "vtt", false, false},
		{"Movie (1999)", "Movie (1999).es.forzados.SRT", true, "es", "srt", true, false},
		{"Movie (1999)", "Movie (1999).Spanish.srt", true, "und", "srt", false, false},
		{"Movie", "Movie 2.srt", false, "", "", false, false},
		{"Movie", "Movie2.es.srt", false, "", "", false, false},
		{"Movie", "Other.es.srt", false, "", "", false, false},
	}
	for _, c := range cases {
		info, ok := parseSubtitleName(c.video, c.name)
		if ok != c.ok {
			t.Fatalf("%s: ok = %v", c.name, ok)
		}
		if ok && (info.language != c.lang || info.format != c.format || info.forced != c.forced || info.isDefault != c.deflt) {
			t.Fatalf("%s: %+v", c.name, info)
		}
	}
}

// Nombres sin SxxEyy: «Serie 01 - Título». Es lo habitual en anime y en series
// ripeadas a mano.
func TestParseEpisodioSuelto(t *testing.T) {
	cases := []struct {
		path    string
		ok      bool
		series  string
		season  int64
		episode int64
		title   string
	}{
		{"Samurai X 01 - El batusai Himura, el espadachin legendario.mp4", true, "Samurai X", 1, 1, "El batusai Himura, el espadachin legendario"},
		{"Samurai X 86 - Final.mp4", true, "Samurai X", 1, 86, "Final"},
		{"Samurai X - 03 - El hombre que mató el pasado.mkv", true, "Samurai X", 1, 3, "El hombre que mató el pasado"},
		{"One Piece 1000.mkv", true, "One Piece", 1, 100, ""}, // Cuatro cifras no es un episodio: ver abajo.
		{"Naruto Cap 12.mp4", true, "Naruto", 1, 12, ""},
		// Los separadores de la serie real: guion pegado, guion bajo y solo espacio.
		{"Samurai X 30 -La venganza del demonio.mp4", true, "Samurai X", 1, 30, "La venganza del demonio"},
		{"Samurai X 41_ Encuentro con el maestro.mp4", true, "Samurai X", 1, 41, "Encuentro con el maestro"},
		{"Samurai X 42 Aoshi y Shishio aliados.mp4", true, "Samurai X", 1, 42, "Aoshi y Shishio aliados"},
		{"Samurai X 3 Ninjas 05 - Título.mp4", true, "Samurai X 3 Ninjas", 1, 5, "Título"}, // Gana el número seguido de « - ».
		{"Naruto/42 Aoshi.mkv", true, "Naruto", 1, 42, "Aoshi"},
		{"Show 1080p.mkv", false, "", 0, 0, ""},
		{"Show 720p HDTV.mkv", false, "", 0, 0, ""},
		{"Naruto Ep.07 - Título.mp4", true, "Naruto", 1, 7, "Título"},
		{"Naruto/01 - Piloto.mkv", true, "Naruto", 1, 1, "Piloto"},
		{"Naruto/Temporada 2/05 - Quinto.mkv", true, "Naruto", 2, 5, "Quinto"},
		{"Naruto/Temporada 2/Naruto 05.mkv", true, "Naruto", 2, 5, ""},
		{"Serie 2011.mkv", false, "", 0, 0, ""},  // Un año no es un episodio.
		{"01 - Piloto.mkv", false, "", 0, 0, ""}, // Sin carpeta ni nombre no hay serie.
		{"Temporada 2/01 - Piloto.mkv", false, "", 0, 0, ""},
		{"Solo un nombre.mkv", false, "", 0, 0, ""},
	}
	for _, c := range cases {
		if c.series == "One Piece" {
			// Cuatro cifras no entran en el patrón de episodio suelto.
			if _, ok := ParseEpisode(c.path); ok {
				t.Errorf("%s: cuatro cifras no es un episodio suelto", c.path)
			}
			continue
		}
		g, ok := ParseEpisode(c.path)
		if ok != c.ok {
			t.Errorf("%s: ok = %v", c.path, ok)
			continue
		}
		if !ok {
			continue
		}
		if g.Series != c.series || g.Season != c.season || len(g.Episodes) != 1 || g.Episodes[0] != c.episode || g.Title != c.title {
			t.Errorf("%s: %+v", c.path, g)
		}
	}
	// El patrón clásico sigue mandando, y no deja título local.
	g, ok := ParseEpisode("Breaking Bad/Season 1/Breaking.Bad.S01E01.Pilot.mkv")
	if !ok || g.Season != 1 || g.Title != "" || g.Series != "Breaking Bad" {
		t.Fatalf("%+v", g)
	}
}
