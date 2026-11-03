package readmodel_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/readmodel"
	"medianode/internal/testutil"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	db  *database.DB
	c   *readmodel.Catalog
}

func newFixture(t *testing.T) *fixture {
	db := testutil.NewDB(t)
	return &fixture{t: t, ctx: context.Background(), db: db, c: readmodel.New(db, authorization.NewAuthorizer(db))}
}

func (f *fixture) id() string {
	id, err := database.NewID()
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) user(role media.Role, maxAge *int64) authorization.Actor {
	id := f.id()
	if err := f.db.Q.CreateUser(f.ctx, sqlc.CreateUserParams{ID: id, Username: id, PasswordHash: "x", Role: string(role)}); err != nil {
		f.t.Fatal(err)
	}
	return authorization.Actor{UserID: id, Role: role, MaxAgeRating: maxAge}
}

func (f *fixture) library(enabled bool) string {
	id := f.id()
	on := int64(0)
	if enabled {
		on = 1
	}
	if err := f.db.Q.CreateLibrary(f.ctx, sqlc.CreateLibraryParams{ID: id, Name: "l", Type: "movies", Enabled: on, CreatedAt: database.Now()}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) grant(a authorization.Actor, lib string, p media.Permission) {
	if err := f.db.Q.AddUserPermission(f.ctx, sqlc.AddUserPermissionParams{UserID: a.UserID, LibraryID: lib, Permission: string(p)}); err != nil {
		f.t.Fatal(err)
	}
}

// item crea un elemento con su título ordenable, como hace el escáner.
func (f *fixture) item(lib string, parent *string, t media.ItemType, title string, minAge *int64) string {
	id := f.id()
	sort := media.SortTitle(title, "")
	err := f.db.Q.CreateMediaItem(f.ctx, sqlc.CreateMediaItemParams{
		ID: id, LibraryID: lib, ParentID: parent, Type: string(t), Title: title, SortTitle: &sort, CreatedAt: database.Now(),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if minAge != nil {
		f.exec("UPDATE media_items SET min_age = ? WHERE id = ?", *minAge, id)
	}
	return id
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Write.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := core.CodeOf(err); got != code {
		t.Fatalf("código = %q (%v), se esperaba %q", got, err, code)
	}
}

func age(n int64) *int64 { return &n }

func titles(items []readmodel.ItemSummary) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Title
	}
	return out
}

func TestListadoPaginadoPorTituloOrdenable(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	admin := f.user(media.RoleAdmin, nil)
	for _, title := range []string{"Zodiac", "The Abyss", "Él", "El Bosque", "Amélie", "Babel", "Casablanca"} {
		f.item(lib, nil, media.ItemMovie, title, nil)
	}
	var all []string
	cursor := ""
	pages := 0
	for {
		page, err := f.c.ListLibraryItems(f.ctx, admin, lib, readmodel.ListParams{After: cursor, Size: 3})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		all = append(all, titles(page.Items)...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	want := []string{"The Abyss", "Amélie", "Babel", "El Bosque", "Casablanca", "Él", "Zodiac"}
	if !slices.Equal(all, want) || pages != 3 {
		t.Fatalf("orden %q en %d páginas", all, pages)
	}
}

func TestListadoPaginaExactaNoDejaCursor(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	admin := f.user(media.RoleAdmin, nil)
	for i := range 4 {
		f.item(lib, nil, media.ItemMovie, fmt.Sprintf("P%d", i), nil)
	}
	page, err := f.c.ListLibraryItems(f.ctx, admin, lib, readmodel.ListParams{Size: 4})
	if err != nil || len(page.Items) != 4 || page.NextCursor != "" {
		t.Fatalf("%v %d %q", err, len(page.Items), page.NextCursor)
	}
}

func TestListadoCursorInvalido(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	_, err := f.c.ListLibraryItems(f.ctx, f.user(media.RoleAdmin, nil), lib, readmodel.ListParams{After: "no-es-base64!"})
	wantCode(t, err, core.CodeValidation)
}

func TestListadoPorEstadoDeMetadatos(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	admin := f.user(media.RoleAdmin, nil)
	f.item(lib, nil, media.ItemMovie, "Pendiente", nil)
	done := f.item(lib, nil, media.ItemMovie, "Hecha", nil)
	lost := f.item(lib, nil, media.ItemMovie, "Sin coincidencia", nil)
	f.exec("UPDATE media_items SET metadata_status = 'done' WHERE id = ?", done)
	f.exec("UPDATE media_items SET metadata_status = 'unmatched' WHERE id = ?", lost)

	page, err := f.c.ListLibraryItems(f.ctx, admin, lib, readmodel.ListParams{Status: media.MetadataUnmatched})
	if err != nil || !slices.Equal(titles(page.Items), []string{"Sin coincidencia"}) {
		t.Fatalf("%v %q", err, titles(page.Items))
	}
	page, err = f.c.ListLibraryItems(f.ctx, admin, lib, readmodel.ListParams{})
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("sin filtro: %v %q", err, titles(page.Items))
	}
	_, err = f.c.ListLibraryItems(f.ctx, admin, lib, readmodel.ListParams{Status: "raro"})
	wantCode(t, err, core.CodeValidation)
}

func TestListadoSoloElementosPrincipales(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	series := f.item(lib, nil, media.ItemSeries, "Serie", nil)
	season := f.item(lib, &series, media.ItemSeason, "Temporada 1", nil)
	f.item(lib, &season, media.ItemEpisode, "Piloto", nil)
	page, err := f.c.ListLibraryItems(f.ctx, f.user(media.RoleAdmin, nil), lib, readmodel.ListParams{})
	if err != nil || !slices.Equal(titles(page.Items), []string{"Serie"}) {
		t.Fatalf("%v %q", err, titles(page.Items))
	}
}

func TestListadoRespetaPermisosYEdad(t *testing.T) {
	f := newFixture(t)
	lib, other := f.library(true), f.library(true)
	f.item(lib, nil, media.ItemMovie, "Todos", age(0))
	f.item(lib, nil, media.ItemMovie, "Doce", age(12))
	f.item(lib, nil, media.ItemMovie, "Dieciocho", age(18))
	f.item(lib, nil, media.ItemMovie, "Sin clasificar", nil)

	user := f.user(media.RoleUser, nil)
	f.grant(user, lib, media.PermissionScan) // scan incluye ver.
	page, err := f.c.ListLibraryItems(f.ctx, user, lib, readmodel.ListParams{})
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("usuario: %v %q", err, titles(page.Items))
	}
	_, err = f.c.ListLibraryItems(f.ctx, user, other, readmodel.ListParams{})
	wantCode(t, err, core.CodeNotFound)

	kid := f.user(media.RoleRestricted, age(12))
	f.grant(kid, lib, media.PermissionView)
	page, err = f.c.ListLibraryItems(f.ctx, kid, lib, readmodel.ListParams{})
	if err != nil || !slices.Equal(titles(page.Items), []string{"Doce", "Todos"}) {
		t.Fatalf("restringido: %v %q", err, titles(page.Items))
	}

	noLimit := f.user(media.RoleRestricted, nil)
	f.grant(noLimit, lib, media.PermissionView)
	page, err = f.c.ListLibraryItems(f.ctx, noLimit, lib, readmodel.ListParams{})
	if err != nil || !slices.Equal(titles(page.Items), []string{"Todos"}) {
		t.Fatalf("restringido sin límite: %v %q", err, titles(page.Items))
	}
}

func TestHijosHeredanLaEdadDeLaSerie(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	series := f.item(lib, nil, media.ItemSeries, "Serie", age(7))
	s1 := f.item(lib, &series, media.ItemSeason, "Temporada 1", nil)
	f.exec("UPDATE media_items SET season_number = 1 WHERE id = ?", s1)
	e1 := f.item(lib, &s1, media.ItemEpisode, "Uno", nil)
	e2 := f.item(lib, &s1, media.ItemEpisode, "Dos", age(16))
	f.exec("UPDATE media_items SET episode_number = 1 WHERE id = ?", e1)
	f.exec("UPDATE media_items SET episode_number = 2 WHERE id = ?", e2)

	kid := f.user(media.RoleRestricted, age(12))
	f.grant(kid, lib, media.PermissionView)
	kids, err := f.c.Children(f.ctx, kid, s1)
	if err != nil || len(kids) != 1 || kids[0].Title != "Uno" || kids[0].EffectiveMinAge != 7 {
		t.Fatalf("%v %+v", err, kids)
	}
	all, err := f.c.Children(f.ctx, f.user(media.RoleAdmin, nil), s1)
	if err != nil || len(all) != 2 || all[1].Title != "Dos" {
		t.Fatalf("%v %+v", err, all)
	}
}

func TestDetalleConDatosRelacionados(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	id := f.item(lib, nil, media.ItemMovie, "Matrix", nil)
	f.exec("INSERT INTO genres (id, name) VALUES ('g1', 'Acción')")
	f.exec("INSERT INTO media_item_genres VALUES (?, 'g1')", id)
	f.exec("INSERT INTO media_external_ids VALUES (?, 'tmdb', '603')", id)
	f.exec("INSERT INTO media_images (id, media_item_id, type, local_path, source_url) VALUES ('i1', ?, 'poster', 'x/poster.jpg', 'u')", id)
	f.exec("INSERT INTO media_images (id, media_item_id, type, source_url) VALUES ('i2', ?, 'backdrop', 'u2')", id)

	d, err := f.c.Item(f.ctx, f.user(media.RoleAdmin, nil), id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Genres, []string{"Acción"}) || len(d.ExternalIDs) != 1 ||
		!slices.Equal(d.Images, []media.ImageType{media.ImagePoster}) {
		t.Fatalf("%+v", d)
	}
}

func TestRutasSoloParaAdministradores(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	id := f.item(lib, nil, media.ItemMovie, "Matrix", age(0))
	pathID, fileID := f.id(), f.id()
	f.exec("INSERT INTO library_paths (id, library_id, path) VALUES (?, ?, 'C:/x')", pathID, lib)
	f.exec("INSERT INTO media_files (id, library_path_id, relative_path, probe_status) VALUES (?, ?, 'Pelis/Matrix.mkv', 'done')", fileID, pathID)
	f.exec("INSERT INTO media_file_items VALUES (?, ?, 0)", fileID, id)
	f.exec("INSERT INTO subtitles (id, media_file_id, relative_path, language) VALUES ('s1', ?, 'Pelis/Matrix.es.srt', 'spa')", fileID)

	user := f.user(media.RoleUser, nil)
	f.grant(user, lib, media.PermissionView)
	files, err := f.c.Files(f.ctx, user, id)
	if err != nil || len(files) != 1 || files[0].RelativePath != "" {
		t.Fatalf("usuario: %v %+v", err, files)
	}
	subs, err := f.c.Subtitles(f.ctx, user, id)
	if err != nil || len(subs) != 1 || subs[0].RelativePath != "" {
		t.Fatalf("usuario: %v %+v", err, subs)
	}
	files, err = f.c.Files(f.ctx, f.user(media.RoleAdmin, nil), id)
	if err != nil || files[0].RelativePath != "Pelis/Matrix.mkv" {
		t.Fatalf("admin: %v %+v", err, files)
	}
}

func TestImagenSinPermisoOSinDescargar(t *testing.T) {
	f := newFixture(t)
	lib, other := f.library(true), f.library(true)
	id := f.item(lib, nil, media.ItemMovie, "Matrix", age(0))
	f.exec("INSERT INTO media_images (id, media_item_id, type, local_path, source_url) VALUES ('i1', ?, 'poster', 'x/poster.jpg', 'u')", id)
	f.exec("INSERT INTO media_images (id, media_item_id, type, source_url) VALUES ('i2', ?, 'backdrop', 'u2')", id)

	user := f.user(media.RoleUser, nil)
	f.grant(user, other, media.PermissionView)
	_, err := f.c.Image(f.ctx, user, id, media.ImagePoster)
	wantCode(t, err, core.CodeNotFound) // Sin permiso: no sabe que existe.

	f.grant(user, lib, media.PermissionView)
	if p, err := f.c.Image(f.ctx, user, id, media.ImagePoster); err != nil || p != "x/poster.jpg" {
		t.Fatalf("%v %q", err, p)
	}
	_, err = f.c.Image(f.ctx, user, id, media.ImageBackdrop)
	wantCode(t, err, core.CodeNotFound) // Aún sin descargar: nunca se descarga aquí.
	_, err = f.c.Image(f.ctx, user, id, "../../etc")
	wantCode(t, err, core.CodeNotFound)
}
