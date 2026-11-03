package readmodel_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"medianode/internal/core"
	"medianode/internal/media"
	"medianode/internal/readmodel"
)

func resultTitles(rs []readmodel.SearchResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	slices.Sort(out)
	return out
}

func TestBusquedaSinAcentosNiMayusculas(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	f.item(lib, nil, media.ItemMovie, "Película de prueba", nil)
	f.item(lib, nil, media.ItemMovie, "Otra cosa", nil)
	admin := f.user(media.RoleAdmin, nil)

	for _, q := range []string{"pelicula", "PELÍCULA", "pelic", "prueba pelicula", "  película,  "} {
		rs, err := f.c.Search(f.ctx, admin, q, 0)
		if err != nil || !slices.Equal(resultTitles(rs), []string{"Película de prueba"}) {
			t.Fatalf("%q: %v %q", q, err, resultTitles(rs))
		}
	}
	rs, err := f.c.Search(f.ctx, admin, "pelicula otra", 0)
	if err != nil || len(rs) != 0 {
		t.Fatalf("todas las palabras deben coincidir: %v %q", err, resultTitles(rs))
	}
}

func TestBusquedaPorTituloOriginal(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	id := f.item(lib, nil, media.ItemMovie, "El viaje de Chihiro", nil)
	f.exec("UPDATE media_items SET original_title = 'Sen to Chihiro no Kamikakushi' WHERE id = ?", id)
	rs, err := f.c.Search(f.ctx, f.user(media.RoleAdmin, nil), "kamikakushi", 0)
	if err != nil || len(rs) != 1 || rs[0].ID != id {
		t.Fatalf("%v %+v", err, rs)
	}
}

// El texto del usuario nunca se interpreta como sintaxis FTS5.
func TestBusquedaConSintaxisFTS(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	f.item(lib, nil, media.ItemMovie, "Matrix", nil)
	admin := f.user(media.RoleAdmin, nil)
	for _, q := range []string{`matrix"`, `matrix AND`, `NEAR(matrix`, `matrix*`, `title:matrix`, `-matrix`, `^matrix`} {
		if _, err := f.c.Search(f.ctx, admin, q, 0); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}
	for _, q := range []string{"", "   ", `"*()`} {
		_, err := f.c.Search(f.ctx, admin, q, 0)
		wantCode(t, err, core.CodeValidation)
	}
}

func TestBusquedaRespetaPermisosYEdad(t *testing.T) {
	f := newFixture(t)
	visible, hidden, off := f.library(true), f.library(true), f.library(false)
	f.item(visible, nil, media.ItemMovie, "Aventura infantil", age(0))
	f.item(visible, nil, media.ItemMovie, "Aventura adulta", age(18))
	f.item(visible, nil, media.ItemMovie, "Aventura sin clasificar", nil)
	f.item(hidden, nil, media.ItemMovie, "Aventura ajena", age(0))
	f.item(off, nil, media.ItemMovie, "Aventura desactivada", age(0))

	admin := f.user(media.RoleAdmin, nil)
	rs, err := f.c.Search(f.ctx, admin, "aventura", 0)
	if err != nil || len(rs) != 5 {
		t.Fatalf("admin: %v %q", err, resultTitles(rs))
	}

	// manage incluye ver: el plan pedía permission = 'view' y lo habría ocultado.
	manager := f.user(media.RoleUser, nil)
	f.grant(manager, visible, media.PermissionManage)
	f.grant(manager, off, media.PermissionView)
	rs, err = f.c.Search(f.ctx, manager, "aventura", 0)
	want := []string{"Aventura adulta", "Aventura infantil", "Aventura sin clasificar"}
	if err != nil || !slices.Equal(resultTitles(rs), want) {
		t.Fatalf("usuario: %v %q", err, resultTitles(rs))
	}

	kid := f.user(media.RoleRestricted, age(12))
	f.grant(kid, visible, media.PermissionView)
	f.grant(kid, hidden, media.PermissionView)
	rs, err = f.c.Search(f.ctx, kid, "aventura", 0)
	if err != nil || !slices.Equal(resultTitles(rs), []string{"Aventura ajena", "Aventura infantil"}) {
		t.Fatalf("restringido: %v %q", err, resultTitles(rs))
	}
}

func TestBusquedaDeEpisodiosConSuSerie(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	series := f.item(lib, nil, media.ItemSeries, "Los Simpson", age(7))
	season := f.item(lib, &series, media.ItemSeason, "Temporada 1", nil)
	f.item(lib, &season, media.ItemEpisode, "Sin blanca Navidad", nil)

	kid := f.user(media.RoleRestricted, age(10))
	f.grant(kid, lib, media.PermissionView)
	rs, err := f.c.Search(f.ctx, kid, "navidad", 0)
	if err != nil || len(rs) != 1 || rs[0].ParentTitle == nil || *rs[0].ParentTitle != "Los Simpson" {
		t.Fatalf("%v %+v", err, rs)
	}
	// La temporada no se indexa.
	rs, err = f.c.Search(f.ctx, kid, "temporada", 0)
	if err != nil || len(rs) != 0 {
		t.Fatalf("%v %+v", err, rs)
	}
	young := f.user(media.RoleRestricted, age(5))
	f.grant(young, lib, media.PermissionView)
	if rs, err = f.c.Search(f.ctx, young, "navidad", 0); err != nil || len(rs) != 0 {
		t.Fatalf("el episodio hereda la edad de la serie: %v %+v", err, rs)
	}
}

// Los disparadores mantienen el índice en la misma transacción que el
// catálogo, también en los borrados en cascada.
func TestIndiceSincronizadoConElCatalogo(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	admin := f.user(media.RoleAdmin, nil)
	id := f.item(lib, nil, media.ItemMovie, "Nombre de archivo", nil)
	series := f.item(lib, nil, media.ItemSeries, "Serie", nil)
	season := f.item(lib, &series, media.ItemSeason, "Temporada 1", nil)
	f.item(lib, &season, media.ItemEpisode, "Episodio único", nil)

	count := func(q string) int {
		rs, err := f.c.Search(f.ctx, admin, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		return len(rs)
	}
	f.exec("UPDATE media_items SET title = 'Título del proveedor' WHERE id = ?", id)
	if count("archivo") != 0 || count("proveedor") != 1 {
		t.Fatal("el cambio de título no llegó al índice")
	}
	f.exec("UPDATE media_items SET overview = 'x' WHERE id = ?", id)
	if count("proveedor") != 1 {
		t.Fatal("un cambio ajeno al título no debe duplicar ni borrar la fila")
	}
	f.exec("DELETE FROM media_items WHERE id = ?", series) // Cascada a temporada y episodio.
	if count("episodio") != 0 {
		t.Fatal("el borrado en cascada dejó el episodio en el índice")
	}
	f.exec("DELETE FROM libraries WHERE id = ?", lib)
	var rows, maps int
	if err := f.db.Read.QueryRow("SELECT (SELECT COUNT(*) FROM media_search), (SELECT COUNT(*) FROM media_search_rows)").Scan(&rows, &maps); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || maps != 0 {
		t.Fatalf("quedan %d filas en el índice y %d en el mapa", rows, maps)
	}
}

// Borrar una biblioteca grande no debe recorrer el índice por cada elemento.
func TestBorrarBibliotecaGrande(t *testing.T) {
	if testing.Short() {
		t.Skip("prueba de rendimiento")
	}
	f := newFixture(t)
	lib := f.library(true)
	tx, err := f.db.Write.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10000 {
		if _, err := tx.Exec("INSERT INTO media_items (id, library_id, type, title) VALUES (?, ?, 'movie', ?)",
			fmt.Sprintf("id-%d", i), lib, fmt.Sprintf("Película %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f.exec("DELETE FROM libraries WHERE id = ?", lib)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("borrar 10 000 elementos tardó %s", d)
	} else {
		t.Logf("borrar 10 000 elementos: %s", d)
	}
}
