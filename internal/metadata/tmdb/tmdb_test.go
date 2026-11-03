package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"medianode/internal/media"
	"medianode/internal/metadata"
)

// Las respuestas de testdata siguen la forma documentada de la API v3. No
// están capturadas del servicio real: falta una clave para hacerlo.
const testKey = "clave-secreta-v3"

type fake struct {
	t        *testing.T
	mu       sync.Mutex
	requests []*http.Request
	status   int // Si no es cero, todas las respuestas usan este código.
	routes   map[string]string
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{t: t, routes: map[string]string{
		"/search/movie":        "search_movie.json",
		"/search/tv":           "search_tv.json",
		"/movie/603":           "movie_603.json",
		"/tv/1396":             "tv_1396.json",
		"/tv/1396/season/1":    "tv_1396_season_1.json",
		"/search/movie?empty":  "",
		"/tv/1396/season/9999": "",
	}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(testKey, "es-ES")
	c.BaseURL = srv.URL
	c.ImageURL = "https://img.test/t/p"
	c.limiter = nil
	return f, c
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	status := f.status
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	name, ok := f.routes[r.URL.Path]
	if !ok || name == "" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"status_code":34}`))
		return
	}
	// Una búsqueda con un año que no existe devuelve vacío, para probar el
	// reintento sin año.
	if r.URL.Query().Get("year") == "1800" {
		_, _ = w.Write([]byte(`{"page":1,"results":[]}`))
		return
	}
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		f.t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (f *fake) setStatus(s int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = s
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fake) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func i64(n int64) *int64 { return &n }

func TestSearchPelicula(t *testing.T) {
	f, c := newFake(t)
	ms, err := c.Search(context.Background(), metadata.Query{Kind: media.ItemMovie, Title: "The Matrix", Year: i64(1999)})
	if err != nil {
		t.Fatal(err)
	}
	q := f.last().URL.Query()
	if q.Get("query") != "The Matrix" || q.Get("year") != "1999" || q.Get("language") != "es-ES" ||
		q.Get("api_key") != testKey || q.Get("include_adult") != "false" {
		t.Fatalf("parámetros: %v", q)
	}
	if len(ms) != 2 || ms[0].ExternalID != "603" || ms[0].Title != "Matrix" || ms[0].OriginalTitle != "The Matrix" ||
		*ms[0].Year != 1999 || ms[0].PosterURL != "https://img.test/t/p/w780/hEpWvX6Bp79eLxY1kX5ZZJcme5U.jpg" {
		t.Fatalf("%+v", ms[0])
	}
	if ms[1].PosterURL != "" {
		t.Fatalf("sin póster no debe haber URL: %q", ms[1].PosterURL)
	}
}

func TestSearchSerieUsaElAnioDeEmision(t *testing.T) {
	f, c := newFake(t)
	ms, err := c.Search(context.Background(), metadata.Query{Kind: media.ItemSeries, Title: "Breaking Bad", Year: i64(2008)})
	if err != nil || len(ms) != 1 || ms[0].Title != "Breaking Bad" || *ms[0].Year != 2008 {
		t.Fatalf("%v %+v", err, ms)
	}
	if q := f.last().URL.Query(); f.last().URL.Path != "/search/tv" || q.Get("first_air_date_year") != "2008" {
		t.Fatalf("%s %v", f.last().URL.Path, q)
	}
}

func TestSearchReintentaSinAnio(t *testing.T) {
	f, c := newFake(t)
	ms, err := c.Search(context.Background(), metadata.Query{Kind: media.ItemMovie, Title: "The Matrix", Year: i64(1800)})
	if err != nil || len(ms) != 2 || f.count() != 2 || f.last().URL.Query().Has("year") {
		t.Fatalf("%v %d peticiones, %v", err, f.count(), f.last().URL.Query())
	}
}

func TestDetailsPelicula(t *testing.T) {
	f, c := newFake(t)
	d, err := c.Details(context.Background(), "603", media.ItemMovie)
	if err != nil {
		t.Fatal(err)
	}
	q := f.last().URL.Query()
	if q.Get("append_to_response") != "credits,release_dates,external_ids,images" || q.Get("include_image_language") != "es,en,null" {
		t.Fatalf("%v", q)
	}
	if d.Title != "Matrix" || d.OriginalTitle != "The Matrix" || *d.Year != 1999 || *d.ReleaseDate != "1999-03-31" ||
		d.OriginalLanguage != "en" || strings.Join(d.Genres, ",") != "Acción,Ciencia ficción" {
		t.Fatalf("%+v", d)
	}
	// España tiene clasificación de cines (tipo 3) aunque la primera esté vacía.
	if *d.AgeRating != "ES:13" || *d.MinAge != 13 {
		t.Fatalf("clasificación %v %v", *d.AgeRating, *d.MinAge)
	}
	if d.ExternalIDs["imdb"] != "tt0133093" {
		t.Fatalf("%v", d.ExternalIDs)
	}
	if d.Images[media.ImageLogo] != "https://img.test/t/p/w500/logo-es.png" ||
		d.Images[media.ImageBackdrop] != "https://img.test/t/p/w1280/fNG7i7RqMErkcqhohV2a6cV1Ehy.jpg" {
		t.Fatalf("imágenes %v", d.Images)
	}
	var roles []string
	for _, p := range d.People {
		roles = append(roles, p.Role+":"+p.Name)
	}
	want := "actor:Keanu Reeves,actor:Laurence Fishburne,director:Lilly Wachowski,director:Lana Wachowski,writer:Lana Wachowski"
	if strings.Join(roles, ",") != want {
		t.Fatalf("personas %v", roles)
	}
}

func TestDetailsSerieConClasificacionDeRespaldo(t *testing.T) {
	_, c := newFake(t)
	d, err := c.Details(context.Background(), "1396", media.ItemSeries)
	if err != nil {
		t.Fatal(err)
	}
	// Sin clasificación española se usa la de Estados Unidos.
	if *d.AgeRating != "US:TV-MA" || *d.MinAge != 17 || d.ExternalIDs["tvdb"] != "81189" {
		t.Fatalf("%v %v %v", *d.AgeRating, *d.MinAge, d.ExternalIDs)
	}
	if len(d.People) != 2 || d.People[0].Role != metadata.RoleCreator || d.People[0].Name != "Vince Gilligan" {
		t.Fatalf("%+v", d.People)
	}
	if _, ok := d.Images[media.ImageLogo]; ok {
		t.Fatal("sin logos no debe haber URL")
	}
}

func TestSeason(t *testing.T) {
	_, c := newFake(t)
	s, err := c.Season(context.Background(), "1396", 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Number != 1 || s.Title != "Temporada 1" || len(s.Episodes) != 3 || *s.AirDate != "2008-01-20" {
		t.Fatalf("%+v", s)
	}
	e := s.Episodes[0]
	if e.Number != 1 || e.Title != "Piloto" || e.StillURL != "https://img.test/t/p/w780/ydlY3iPfeOAvu8gVqrxPoMvzNCn.jpg" {
		t.Fatalf("%+v", e)
	}
	if s.Episodes[1].StillURL != "" {
		t.Fatal("still_path null no debe dar URL")
	}
	if _, err := c.Season(context.Background(), "1396", 9999); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("temporada inexistente: %v", err)
	}
}

func TestErroresSeTraducen(t *testing.T) {
	f, c := newFake(t)
	cases := map[int]error{
		http.StatusUnauthorized:        metadata.ErrUnauthorized,
		http.StatusNotFound:            metadata.ErrNotFound,
		http.StatusTooManyRequests:     metadata.ErrUnavailable,
		http.StatusInternalServerError: metadata.ErrUnavailable,
		http.StatusBadGateway:          metadata.ErrUnavailable,
	}
	for status, want := range cases {
		f.setStatus(status)
		_, err := c.Details(context.Background(), "603", media.ItemMovie)
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d: %v", status, err)
		}
	}
	f.setStatus(http.StatusBadRequest)
	_, err := c.Details(context.Background(), "603", media.ItemMovie)
	if err == nil || errors.Is(err, metadata.ErrUnavailable) || errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("HTTP 400 es permanente: %v", err)
	}
}

func TestIdentificadorNoNumericoNoLlegaAlServidor(t *testing.T) {
	f, c := newFake(t)
	for _, id := range []string{"../603", "603?x=1", "", "abc"} {
		if _, err := c.Details(context.Background(), id, media.ItemMovie); !errors.Is(err, metadata.ErrNotFound) {
			t.Fatalf("%q: %v", id, err)
		}
		if _, err := c.Season(context.Background(), id, 1); !errors.Is(err, metadata.ErrNotFound) {
			t.Fatalf("%q: %v", id, err)
		}
	}
	if f.count() != 0 {
		t.Fatalf("%d peticiones", f.count())
	}
}

// Un error de red no debe incluir la URL: lleva la clave.
func TestErrorDeRedSinClave(t *testing.T) {
	c := New(testKey, "es-ES")
	c.BaseURL = "http://127.0.0.1:1"
	c.limiter = nil
	_, err := c.Details(context.Background(), "603", media.ItemMovie)
	if !errors.Is(err, metadata.ErrUnavailable) || strings.Contains(err.Error(), testKey) {
		t.Fatalf("%v", err)
	}
}

func TestTokenV4VaEnCabecera(t *testing.T) {
	f, c := newFake(t)
	c.Key = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJ4In0.firma"
	if _, err := c.Details(context.Background(), "603", media.ItemMovie); err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Header.Get("Authorization") != "Bearer "+c.Key || r.URL.Query().Has("api_key") {
		t.Fatalf("%v %v", r.Header, r.URL.Query())
	}
}

func TestRespuestaDemasiadoGrande(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[`))
		_, _ = w.Write([]byte(strings.Repeat(" ", maxResponse)))
		_, _ = w.Write([]byte(`]}`))
	}))
	defer srv.Close()
	c := New(testKey, "es-ES")
	c.BaseURL = srv.URL
	c.limiter = nil
	_, err := c.Search(context.Background(), metadata.Query{Kind: media.ItemMovie, Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "demasiado grande") {
		t.Fatalf("%v", err)
	}
}

func TestMinAge(t *testing.T) {
	cases := []struct {
		country, cert string
		want          int64
		ok            bool
	}{
		{"ES", "A", 0, true}, {"ES", "APTA", 0, true}, {"ES", "7", 7, true}, {"ES", "7i", 7, true},
		{"ES", "12", 12, true}, {"ES", "16", 16, true}, {"ES", "18", 18, true}, {"ES", "X", 18, true},
		{"US", "G", 0, true}, {"US", "PG", 10, true}, {"US", "PG-13", 13, true}, {"US", "R", 17, true},
		{"US", "NC-17", 18, true}, {"US", "TV-Y7", 7, true}, {"US", "TV-MA", 17, true}, {"US", "tv-14", 14, true},
		{"GB", "U", 0, true}, {"GB", "PG", 8, true}, {"GB", "12A", 12, true}, {"GB", "15", 15, true},
		{"DE", "16", 16, true}, {"FR", "U", 0, true}, {"BR", "16+", 16, true},
		{"US", "NR", 0, false}, {"US", "Unrated", 0, false}, {"ES", "99", 0, false}, {"ES", "-1", 0, false},
	}
	for _, c := range cases {
		got, ok := minAge(c.country, c.cert)
		if got != c.want || ok != c.ok {
			t.Errorf("%s %q = %d %v", c.country, c.cert, got, ok)
		}
	}
}

// El estreno en cines (tipo 3) manda sobre ediciones posteriores, aunque estas
// aparezcan antes.
func TestCertificacionDeCines(t *testing.T) {
	var r detailsResponse
	data := `{"release_dates":{"results":[{"iso_3166_1":"ES","release_dates":[
		{"certification":"7","type":5},{"certification":"","type":4},{"certification":"16","type":3}]}]}}`
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		t.Fatal(err)
	}
	if got := movieCertification(r.ReleaseDates.Results[0].Dates); got != "16" {
		t.Fatalf("%q", got)
	}
	if got := movieCertification(r.ReleaseDates.Results[0].Dates[:2]); got != "7" {
		t.Fatalf("sin cines vale la primera no vacía: %q", got)
	}
}

func TestPickRating(t *testing.T) {
	label, age := pickRating(map[string]string{"ES": "NR?", "US": "PG-13"}, "ES")
	if *label != "US:PG-13" || *age != 13 {
		t.Fatalf("una española desconocida cede a la de EE. UU.: %v %v", *label, *age)
	}
	if label, age := pickRating(map[string]string{"FR": "12"}, "ES"); label != nil || age != nil {
		t.Fatal("sin región ni EE. UU. no hay clasificación")
	}
	if label, _ := pickRating(map[string]string{"US": "R"}, ""); *label != "US:R" {
		t.Fatal("sin región se usa EE. UU.")
	}
}
