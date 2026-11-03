package metadata

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/images"
	"medianode/internal/media"
	"medianode/internal/readmodel"
	"medianode/internal/testutil"
)

// fakeProvider responde desde memoria y cuenta las llamadas.
type fakeProvider struct {
	mu       sync.Mutex
	err      error // Si no es nil, todas las llamadas fallan con él.
	matches  map[string][]Match
	details  map[string]*Details
	seasons  map[int64]*Season
	searches int
	detailed []string
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *fakeProvider) Search(_ context.Context, q Query) ([]Match, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.searches++
	if p.err != nil {
		return nil, p.err
	}
	return p.matches[q.Title], nil
}

func (p *fakeProvider) Details(_ context.Context, id string, _ media.ItemType) (*Details, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.detailed = append(p.detailed, id)
	if p.err != nil {
		return nil, p.err
	}
	d, ok := p.details[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *d
	return &cp, nil
}

func (p *fakeProvider) Season(_ context.Context, _ string, n int64) (*Season, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	s, ok := p.seasons[n]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	db     *database.DB
	p      *fakeProvider
	e      *Enricher
	svc    *Service
	dir    string
	imgSrv *httptest.Server
	lib    string
}

func pngBytes(t *testing.T) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newFixture(t *testing.T) *fixture {
	db := testutil.NewDB(t)
	pic := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing.png":
			w.WriteHeader(http.StatusNotFound)
		case "/broken.png":
			w.WriteHeader(http.StatusBadGateway)
		case "/text.png":
			_, _ = w.Write([]byte("<html>no soy una imagen</html>"))
		default:
			_, _ = w.Write(pic)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	p := &fakeProvider{matches: map[string][]Match{}, details: map[string]*Details{}, seasons: map[int64]*Season{}}
	e := NewEnricher(db, p, images.New(dir, db, srv.Client()), "es-ES", Options{
		Poll: time.Hour, MinBackoff: 20 * time.Millisecond, MaxBackoff: 80 * time.Millisecond,
	}, zerolog.Nop())
	f := &fixture{t: t, ctx: context.Background(), db: db, p: p, e: e, dir: dir, imgSrv: srv}
	f.svc = NewService(db, authorization.NewAuthorizer(db), e)
	f.lib = f.library()
	return f
}

func (f *fixture) id() string {
	id, err := database.NewID()
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) library() string {
	id := f.id()
	if err := f.db.Q.CreateLibrary(f.ctx, sqlc.CreateLibraryParams{ID: id, Name: "l", Type: "movies", Enabled: 1, CreatedAt: database.Now()}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) user(role media.Role) authorization.Actor {
	id := f.id()
	if err := f.db.Q.CreateUser(f.ctx, sqlc.CreateUserParams{ID: id, Username: id, PasswordHash: "x", Role: string(role)}); err != nil {
		f.t.Fatal(err)
	}
	return authorization.Actor{UserID: id, Role: role}
}

func (f *fixture) item(parent *string, t media.ItemType, title string, year *int64, number *int64) string {
	id := f.id()
	p := sqlc.CreateMediaItemParams{ID: id, LibraryID: f.lib, ParentID: parent, Type: string(t), Title: title, Year: year, CreatedAt: database.Now()}
	switch t {
	case media.ItemSeason:
		p.SeasonNumber = number
	case media.ItemEpisode:
		p.EpisodeNumber = number
	default:
		sort := media.SortTitle(title, "")
		p.SortTitle = &sort
	}
	if err := f.db.Q.CreateMediaItem(f.ctx, p); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) get(id string) sqlc.MediaItem {
	f.t.Helper()
	it, err := f.db.R.GetMediaItem(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return it
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Write.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func i64(n int64) *int64   { return &n }
func str(s string) *string { return &s }

func (f *fixture) matrix() {
	f.p.matches["The Matrix"] = []Match{
		{ExternalID: "999", Title: "The Matrix Revisited", Year: i64(2001)},
		{ExternalID: "603", Title: "Matrix", OriginalTitle: "The Matrix", Year: i64(1999)},
	}
	f.p.details["603"] = &Details{
		ExternalID: "603", Title: "Matrix", OriginalTitle: "The Matrix", Overview: "Neo.",
		OriginalLanguage: "en", Year: i64(1999), ReleaseDate: str("1999-03-31"),
		AgeRating: str("ES:13"), MinAge: i64(13), Genres: []string{"Acción", "Ciencia ficción"},
		People: []Person{
			{ExternalID: "6384", Name: "Keanu Reeves", Role: RoleActor, Character: "Neo"},
			{ExternalID: "9340", Name: "Lana Wachowski", Role: RoleDirector},
		},
		ExternalIDs: map[string]string{"imdb": "tt0133093"},
		Images: map[media.ImageType]string{
			media.ImagePoster:   f.imgSrv.URL + "/poster.png",
			media.ImageBackdrop: f.imgSrv.URL + "/missing.png",
		},
	}
}

func TestEnriquecePeliculaCompleta(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	id := f.item(nil, media.ItemMovie, "The Matrix", i64(1999), nil)
	if err := f.e.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	it := f.get(id)
	if it.Title != "Matrix" || *it.OriginalTitle != "The Matrix" || *it.SortTitle != "matrix" ||
		*it.MinAge != 13 || *it.AgeRating != "ES:13" || it.MetadataStatus != "done" || *it.OriginalLanguage != "en" {
		t.Fatalf("%+v", it)
	}
	genres, _ := f.db.R.ListItemGenres(f.ctx, id)
	people, _ := f.db.R.ListItemPeople(f.ctx, id)
	ids, _ := f.db.R.ListItemExternalIDs(f.ctx, id)
	if !slices.Equal(genres, []string{"Acción", "Ciencia ficción"}) || len(people) != 2 || len(ids) != 2 {
		t.Fatalf("%v %v %v", genres, people, ids)
	}
	// El póster se descarga; el fondo que da 404 se olvida en vez de reintentarse.
	imgs, _ := f.db.R.ListItemImages(f.ctx, id)
	if len(imgs) != 1 || imgs[0].Type != "poster" || *imgs[0].LocalPath != id+"/poster.png" {
		t.Fatalf("%+v", imgs)
	}
	if _, err := os.Stat(filepath.Join(f.dir, id, "poster.png")); err != nil {
		t.Fatal(err)
	}
	// El índice de búsqueda sigue al título del proveedor.
	c := readmodel.New(f.db, authorization.NewAuthorizer(f.db))
	rs, err := c.Search(f.ctx, f.user(media.RoleAdmin), "neo matrix", 0)
	if err != nil || len(rs) != 0 {
		t.Fatalf("%v %+v", err, rs)
	}
	rs, _ = c.Search(f.ctx, f.user(media.RoleAdmin), "matrix", 0)
	if len(rs) != 1 || rs[0].Title != "Matrix" {
		t.Fatalf("%+v", rs)
	}
}

func TestSinCoincidenciaYNoEncontrado(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	none := f.item(nil, media.ItemMovie, "Película casera", nil, nil)
	gone := f.item(nil, media.ItemMovie, "Desaparecida", i64(2000), nil)
	f.p.matches["Desaparecida"] = []Match{{ExternalID: "404", Title: "Desaparecida", Year: i64(2000)}}
	if err := f.e.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	if s := f.get(none).MetadataStatus; s != "unmatched" {
		t.Fatalf("sin resultados: %s", s)
	}
	if s := f.get(gone).MetadataStatus; s != "failed" {
		t.Fatalf("detalle 404: %s", s)
	}
	if f.get(none).Title != "Película casera" {
		t.Fatal("sin metadatos se conserva el título del archivo")
	}
}

// Sin Internet el catálogo sigue con los títulos del archivo, y los pendientes
// se retoman cuando el proveedor vuelve, también tras reiniciar.
func TestSinRedLosPendientesSeRetoman(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	id := f.item(nil, media.ItemMovie, "The Matrix", i64(1999), nil)
	f.p.setErr(ErrUnavailable)
	err := f.e.drain(f.ctx)
	if !errors.Is(err, ErrUnavailable) || f.get(id).MetadataStatus != "pending" || f.get(id).Title != "The Matrix" {
		t.Fatalf("%v %+v", err, f.get(id))
	}
	f.p.setErr(ErrUnauthorized)
	if err := f.e.drain(f.ctx); !errors.Is(err, ErrUnauthorized) || f.get(id).MetadataStatus != "pending" {
		t.Fatalf("una clave rechazada no marca nada como fallido: %v", err)
	}

	// Un servidor nuevo sobre la misma base retoma la cola desde metadata_status.
	f.p.setErr(nil)
	restarted := NewEnricher(f.db, f.p, nil, "es-ES", Options{}, zerolog.Nop())
	if err := restarted.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	if it := f.get(id); it.MetadataStatus != "done" || it.Title != "Matrix" {
		t.Fatalf("%+v", it)
	}
}

func TestBucleEsperaYReintenta(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	id := f.item(nil, media.ItemMovie, "The Matrix", i64(1999), nil)
	f.p.setErr(ErrUnavailable)
	if err := f.e.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer f.e.Stop(f.ctx)
	deadline := time.Now().Add(5 * time.Second)
	for f.e.Status().PausedUntil.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("la cola no se pausó")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := f.e.Status(); st.LastError != "El proveedor no responde" {
		t.Fatalf("%+v", st)
	}
	f.p.setErr(nil)
	for f.get(id).MetadataStatus != "done" {
		if time.Now().After(deadline) {
			t.Fatal("la cola no se reanudó tras la pausa")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for !f.e.Status().PausedUntil.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("el estado sigue en pausa")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// En pausa, los avisos (un escaneo terminado, una actualización forzada) no
// adelantan el reintento: el proveedor sigue caído.
func TestEnPausaLosAvisosNoReintentan(t *testing.T) {
	f := newFixture(t)
	f.item(nil, media.ItemMovie, "The Matrix", i64(1999), nil)
	f.e.opts.MinBackoff, f.e.opts.MaxBackoff = time.Hour, time.Hour
	f.p.setErr(ErrUnavailable)
	if err := f.e.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer f.e.Stop(f.ctx)
	deadline := time.Now().Add(5 * time.Second)
	for f.e.Status().PausedUntil.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("la cola no se pausó")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.p.mu.Lock()
	before := f.p.searches
	f.p.mu.Unlock()
	for range 5 {
		f.e.Notify()
		time.Sleep(20 * time.Millisecond)
	}
	f.p.mu.Lock()
	after := f.p.searches
	f.p.mu.Unlock()
	if after != before {
		t.Fatalf("%d búsquedas durante la pausa", after-before)
	}
}

func TestBloqueadoNoSeSobrescribe(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	id := f.item(nil, media.ItemMovie, "The Matrix", i64(1999), nil)
	f.exec("UPDATE media_items SET metadata_locked = 1, title = 'Mi título' WHERE id = ?", id)
	if err := f.e.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	if it := f.get(id); it.Title != "Mi título" || it.MetadataStatus != "pending" || f.p.searches != 0 {
		t.Fatalf("%+v, %d búsquedas", it, f.p.searches)
	}
	// Aunque el bloqueo llegue entre la consulta al proveedor y el guardado.
	var applied bool
	err := f.db.InTx(f.ctx, func(q *sqlc.Queries) error {
		var err error
		applied, err = apply(f.ctx, q, "fake", "es-ES", id, f.p.details["603"], nil)
		return err
	})
	if err != nil || applied || f.get(id).Title != "Mi título" {
		t.Fatalf("%v %v %+v", err, applied, f.get(id))
	}
}

func TestSerieConTemporadasYEpisodios(t *testing.T) {
	f := newFixture(t)
	series := f.item(nil, media.ItemSeries, "Breaking Bad", i64(2008), nil)
	s1 := f.item(&series, media.ItemSeason, "Temporada 1", nil, i64(1))
	s9 := f.item(&series, media.ItemSeason, "Temporada 9", nil, i64(9))
	e1 := f.item(&s1, media.ItemEpisode, "Episodio 1", nil, i64(1))
	e7 := f.item(&s1, media.ItemEpisode, "Episodio 7", nil, i64(7))
	f.p.matches["Breaking Bad"] = []Match{{ExternalID: "1396", Title: "Breaking Bad", Year: i64(2008)}}
	f.p.details["1396"] = &Details{ExternalID: "1396", Title: "Breaking Bad", Year: i64(2008), MinAge: i64(17)}
	f.p.seasons[1] = &Season{Number: 1, Title: "Temporada 1", PosterURL: f.imgSrv.URL + "/s1.png", Episodes: []Episode{
		{Number: 1, Title: "Piloto", AirDate: str("2008-01-20"), StillURL: f.imgSrv.URL + "/e1.png"},
		{Number: 2, Title: "Otro"},
	}}
	if err := f.e.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	if ep := f.get(e1); ep.Title != "Piloto" || *ep.Year != 2008 || ep.MinAge != nil {
		t.Fatalf("el episodio toma el título y hereda la edad: %+v", ep)
	}
	if f.get(e7).Title != "Episodio 7" || f.get(s9).Title != "Temporada 9" {
		t.Fatal("lo que el proveedor no conoce conserva el título local")
	}
	if imgs, _ := f.db.R.ListItemImages(f.ctx, e1); len(imgs) != 1 || imgs[0].Type != "thumbnail" || imgs[0].LocalPath == nil {
		t.Fatalf("%+v", imgs)
	}
	if imgs, _ := f.db.R.ListItemImages(f.ctx, s1); len(imgs) != 1 || imgs[0].Type != "poster" {
		t.Fatalf("%+v", imgs)
	}
}

func TestActualizarUsaElIdentificadorGuardado(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	id := f.item(nil, media.ItemMovie, "The Matrix", i64(1999), nil)
	if err := f.e.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	admin := f.user(media.RoleAdmin)
	if err := f.svc.Refresh(f.ctx, admin, id); err != nil {
		t.Fatal(err)
	}
	if f.get(id).MetadataStatus != "pending" {
		t.Fatal("refresh debe devolverlo a la cola")
	}
	f.p.details["603"].Overview = "Sinopsis nueva."
	before := f.p.searches
	if err := f.e.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.p.searches != before || *f.get(id).Overview != "Sinopsis nueva." {
		t.Fatalf("%d búsquedas nuevas, %+v", f.p.searches-before, f.get(id))
	}
}

func TestIdentificacionManual(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	id := f.item(nil, media.ItemMovie, "Matriz casera", nil, nil)
	f.exec("UPDATE media_items SET metadata_status = 'unmatched' WHERE id = ?", id)
	admin := f.user(media.RoleAdmin)

	ms, err := f.svc.Matches(f.ctx, admin, id, "The Matrix", nil)
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %+v", err, ms)
	}
	if err := f.svc.Identify(f.ctx, admin, id, "otro", "603"); core.CodeOf(err) != core.CodeValidation {
		t.Fatalf("proveedor desconocido: %v", err)
	}
	if err := f.svc.Identify(f.ctx, admin, id, "fake", "12345"); core.CodeOf(err) != core.CodeValidation {
		t.Fatalf("identificador inexistente: %v", err)
	}
	if err := f.svc.Identify(f.ctx, admin, id, "fake", "603"); err != nil {
		t.Fatal(err)
	}
	if it := f.get(id); it.Title != "Matrix" || it.MetadataStatus != "done" {
		t.Fatalf("%+v", it)
	}
	ext, err := f.db.R.GetExternalID(f.ctx, sqlc.GetExternalIDParams{MediaItemID: id, Provider: "fake"})
	if err != nil || ext != "603" {
		t.Fatalf("%v %q", err, ext)
	}

	f.p.setErr(ErrUnavailable)
	if _, err := f.svc.Matches(f.ctx, admin, id, "", nil); core.CodeOf(err) != core.CodeMetadataUnavailable {
		t.Fatalf("proveedor caído: %v", err)
	}
}

func TestIdentificarSoloElementosPrincipales(t *testing.T) {
	f := newFixture(t)
	series := f.item(nil, media.ItemSeries, "Serie", nil, nil)
	season := f.item(&series, media.ItemSeason, "Temporada 1", nil, i64(1))
	err := f.svc.Identify(f.ctx, f.user(media.RoleAdmin), season, "fake", "1")
	if core.CodeOf(err) != core.CodeValidation {
		t.Fatalf("%v", err)
	}
}

func TestEditarBloqueaYPermisos(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	id := f.item(nil, media.ItemMovie, "The Matrix", i64(1999), nil)
	admin := f.user(media.RoleAdmin)

	viewer := f.user(media.RoleUser)
	f.exec("INSERT INTO user_permissions VALUES (?, ?, 'view')", viewer.UserID, f.lib)
	err := f.svc.Edit(f.ctx, viewer, id, EditParams{Title: str("x")})
	if core.CodeOf(err) != core.CodeLibraryDenied {
		t.Fatalf("view no edita: %v", err)
	}
	stranger := f.user(media.RoleUser)
	if err := f.svc.Edit(f.ctx, stranger, id, EditParams{Title: str("x")}); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("sin permiso no sabe que existe: %v", err)
	}

	err = f.svc.Edit(f.ctx, admin, id, EditParams{
		Title: str("  El Origen  "), MinAge: Field[int64]{Set: true, Value: i64(12)},
		Overview: Field[string]{Set: true, Value: str("  ")},
	})
	if err != nil {
		t.Fatal(err)
	}
	it := f.get(id)
	if it.Title != "El Origen" || *it.SortTitle != "origen" || *it.MinAge != 12 || it.Overview != nil || it.MetadataLocked != 1 {
		t.Fatalf("%+v", it)
	}
	if err := f.svc.Refresh(f.ctx, admin, id); core.CodeOf(err) != core.CodeMetadataLocked {
		t.Fatalf("refresh bloqueado: %v", err)
	}
	calls := len(f.p.detailed)
	if err := f.svc.Identify(f.ctx, admin, id, "fake", "603"); core.CodeOf(err) != core.CodeMetadataLocked {
		t.Fatalf("identify bloqueado: %v", err)
	}
	if len(f.p.detailed) != calls {
		t.Fatal("un elemento bloqueado no debe consultar al proveedor")
	}
	// Desbloquear sin editar nada más.
	if err := f.svc.Edit(f.ctx, admin, id, EditParams{Locked: new(bool)}); err != nil || f.get(id).MetadataLocked != 0 {
		t.Fatalf("%v %+v", err, f.get(id))
	}
	if f.get(id).Title != "El Origen" {
		t.Fatal("desbloquear no cambia los datos")
	}

	for _, p := range []EditParams{
		{Title: str("")},
		{Year: Field[int64]{Set: true, Value: i64(1500)}},
		{MinAge: Field[int64]{Set: true, Value: i64(-1)}},
		{MinAge: Field[int64]{Set: true, Value: i64(22)}},
		{ReleaseDate: Field[string]{Set: true, Value: str("31/03/1999")}},
	} {
		if err := f.svc.Edit(f.ctx, admin, id, p); core.CodeOf(err) != core.CodeValidation {
			t.Fatalf("%+v: %v", p, err)
		}
	}
}

func TestSinProveedor(t *testing.T) {
	f := newFixture(t)
	id := f.item(nil, media.ItemMovie, "Peli", nil, nil)
	e := NewEnricher(f.db, nil, nil, "es-ES", Options{}, zerolog.Nop())
	svc := NewService(f.db, authorization.NewAuthorizer(f.db), e)
	if err := e.drain(f.ctx); err != nil || f.get(id).MetadataStatus != "pending" {
		t.Fatalf("%v %+v", err, f.get(id))
	}
	admin := f.user(media.RoleAdmin)
	if _, err := svc.Matches(f.ctx, admin, id, "", nil); core.CodeOf(err) != core.CodeMetadataUnavailable {
		t.Fatalf("%v", err)
	}
	// Editar a mano no necesita proveedor.
	if err := svc.Edit(f.ctx, admin, id, EditParams{Title: str("Otra")}); err != nil {
		t.Fatal(err)
	}
}

func TestChoose(t *testing.T) {
	m := func(id, title, orig string, year int64) Match {
		return Match{ExternalID: id, Title: title, OriginalTitle: orig, Year: i64(year)}
	}
	cases := []struct {
		name string
		q    Query
		ms   []Match
		want string
	}{
		{"primer resultado con el año", Query{Title: "Alien", Year: i64(1979)}, []Match{m("1", "Alien, el octavo pasajero", "Alien", 1979)}, "1"},
		{"año a uno", Query{Title: "Amélie", Year: i64(2002)}, []Match{m("2", "Amélie", "Le Fabuleux Destin", 2001)}, "2"},
		{"remake con otro año", Query{Title: "Dune", Year: i64(1984)}, []Match{m("3", "Dune", "Dune", 2021)}, ""},
		{"título igual más abajo", Query{Title: "Dune", Year: i64(1984)}, []Match{m("3", "Dune", "Dune", 2021), m("4", "Dune", "Dune", 1984)}, "4"},
		{"otro título y no es el primero", Query{Title: "Heat", Year: i64(1995)}, []Match{m("5", "Heat", "", 1986), m("6", "Otra", "", 1995)}, ""},
		{"sin año exige título", Query{Title: "Matrix"}, []Match{m("7", "Matrix Reloaded", "", 2003), m("8", "Matrix", "The Matrix", 1999)}, "8"},
		{"sin año ni título igual", Query{Title: "Matrix"}, []Match{m("7", "Matrix Reloaded", "", 2003)}, ""},
		{"título original sin acentos", Query{Title: "El laberinto del fauno"}, []Match{m("9", "Pan's Labyrinth", "El laberinto del fauno", 2006)}, "9"},
		{"sin resultados", Query{Title: "x"}, nil, ""},
	}
	for _, c := range cases {
		got := choose(c.q, c.ms)
		id := ""
		if got != nil {
			id = got.ExternalID
		}
		if id != c.want {
			t.Errorf("%s: %q, se esperaba %q", c.name, id, c.want)
		}
	}
}

// TMDB prohíbe guardar su contenido más de seis meses: lo antiguo se vuelve a
// pedir, con sus imágenes, salvo lo bloqueado.
func TestRenuevaMetadatosAntiguos(t *testing.T) {
	f := newFixture(t)
	f.matrix()
	old := f.item(nil, media.ItemMovie, "The Matrix", i64(1999), nil)
	if err := f.e.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	locked := f.item(nil, media.ItemMovie, "Mía", nil, nil)
	recent := f.item(nil, media.ItemMovie, "Reciente", nil, nil)
	f.exec("UPDATE media_items SET metadata_status = 'done', metadata_locked = 1 WHERE id = ?", locked)
	f.exec("UPDATE media_items SET metadata_status = 'done' WHERE id = ?", recent)
	f.exec("UPDATE media_items SET updated_at = '2020-01-01T00:00:00.000Z' WHERE id IN (?, ?)", old, locked)

	n, err := f.e.requeueStale(f.ctx)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if f.get(old).MetadataStatus != "pending" || f.get(locked).MetadataStatus != "done" || f.get(recent).MetadataStatus != "done" {
		t.Fatal("solo lo antiguo y sin bloquear vuelve a la cola")
	}
	if imgs, _ := f.db.R.ListItemImages(f.ctx, old); len(imgs) != 1 || imgs[0].LocalPath != nil {
		t.Fatalf("la imagen debe volver a descargarse: %+v", imgs)
	}
	if err := f.e.drain(f.ctx); err != nil {
		t.Fatal(err)
	}
	if imgs, _ := f.db.R.ListItemImages(f.ctx, old); imgs[0].LocalPath == nil || f.get(old).MetadataStatus != "done" {
		t.Fatalf("%+v", imgs)
	}
	if n, _ := f.e.requeueStale(f.ctx); n != 0 {
		t.Fatalf("recién renovado: %d", n)
	}
}

func TestFillSortTitles(t *testing.T) {
	f := newFixture(t)
	id := f.item(nil, media.ItemMovie, "Los Otros", nil, nil)
	f.exec("UPDATE media_items SET sort_title = NULL")
	// Start los rellena antes de volver, es decir, antes de que el servidor
	// atienda peticiones.
	if err := f.e.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer f.e.Stop(f.ctx)
	if s := f.get(id).SortTitle; s == nil || *s != "otros" {
		t.Fatalf("%v", s)
	}
}
