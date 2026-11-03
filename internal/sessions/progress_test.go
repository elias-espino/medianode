package sessions

import (
	"testing"
	"time"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/media"
)

func (f *fixture) progressService() *Progress {
	p := NewProgress(f.db, authorization.NewAuthorizer(f.db), f.bus, 0.9)
	p.now = f.clock.now
	return p
}

func TestProgresoDelCliente(t *testing.T) {
	f := newFixture(t)
	p := f.progressService()
	now := f.clock.now()

	future := now.Add(time.Hour)
	cur, err := p.Set(f.ctx, f.user, SetParams{ItemID: f.item, PositionMS: 10_000, ClientTime: &future})
	if err != nil || cur.UpdatedAt != "2026-11-03T12:00:00.000Z" {
		t.Fatalf("una hora futura se sustituye por la del servidor: %v %+v", err, cur)
	}
	past := now.Add(-time.Hour)
	cur, err = p.Set(f.ctx, f.user, SetParams{ItemID: f.item, PositionMS: 5_000, ClientTime: &past})
	if err != nil || cur.PositionMs != 10_000 {
		t.Fatalf("un dato atrasado no sustituye al vigente: %v %+v", err, cur)
	}
	f.clock.add(time.Minute)
	cur, err = p.Set(f.ctx, f.user, SetParams{ItemID: f.item, PositionMS: 92_000})
	if err != nil || cur.PositionMs != 92_000 || cur.Completed != 1 || *cur.DurationMs != duration {
		t.Fatalf("supera el 90 %% de la duración del elemento: %v %+v", err, cur)
	}
	no := false
	f.clock.add(time.Minute)
	if cur, _ = p.Set(f.ctx, f.user, SetParams{ItemID: f.item, PositionMS: 0, Completed: &no}); cur.Completed != 0 {
		t.Fatal("marcar como no visto")
	}
	if _, err := p.Set(f.ctx, f.user, SetParams{ItemID: f.item, PositionMS: -1}); core.CodeOf(err) != core.CodeValidation {
		t.Fatalf("%v", err)
	}
	_, err = p.Set(f.ctx, f.another, SetParams{ItemID: f.item, PositionMS: 1})
	wantCode(t, err, core.CodeNotFound) // Sin permiso sobre la biblioteca.
}

func TestContinuarViendo(t *testing.T) {
	f := newFixture(t)
	p := f.progressService()
	set := func(a authorization.Actor, item string, pos int64) {
		t.Helper()
		f.clock.add(time.Second)
		if _, err := p.Set(f.ctx, a, SetParams{ItemID: item, PositionMS: pos}); err != nil {
			t.Fatal(err)
		}
	}
	empezada := f.item
	terminada, _ := f.newItem(f.lib, "Terminada", &[]int64{1000}[0])
	cero, _ := f.newItem(f.lib, "Sin empezar", nil)
	set(f.user, empezada, 10_000)
	set(f.user, terminada, 1000)
	set(f.user, cero, 0)

	rows, err := p.ContinueWatching(f.ctx, f.user, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != empezada || rows[0].PositionMs != 10_000 {
		t.Fatalf("%v %+v", err, rows)
	}

	// Episodio: llega con su serie.
	series, season, ep := f.id(), f.id(), f.id()
	f.exec("INSERT INTO media_items (id, library_id, type, title) VALUES (?, ?, 'series', 'Serie')", series, f.lib)
	f.exec("INSERT INTO media_items (id, library_id, parent_id, type, title, season_number) VALUES (?, ?, ?, 'season', 'T1', 1)", season, f.lib, series)
	f.exec("INSERT INTO media_items (id, library_id, parent_id, type, title, episode_number) VALUES (?, ?, ?, 'episode', 'Piloto', 1)", ep, f.lib, season)
	set(f.user, ep, 500)
	rows, _ = p.ContinueWatching(f.ctx, f.user, 0)
	if len(rows) != 2 || rows[0].ID != ep || rows[0].SeriesTitle == nil || *rows[0].SeriesTitle != "Serie" {
		t.Fatalf("lo más reciente primero, con su serie: %+v", rows)
	}

	// Perder el permiso o desactivar la biblioteca lo oculta.
	f.exec("DELETE FROM user_permissions WHERE user_id = ?", f.user.UserID)
	f.exec("INSERT INTO user_permissions VALUES (?, ?, 'manage')", f.user.UserID, f.lib)
	if rows, _ = p.ContinueWatching(f.ctx, f.user, 0); len(rows) != 2 {
		t.Fatal("manage incluye ver: el plan pedía permission = 'view'")
	}
	f.exec("UPDATE libraries SET enabled = 0 WHERE id = ?", f.lib)
	if rows, _ = p.ContinueWatching(f.ctx, f.user, 0); len(rows) != 0 {
		t.Fatal("una biblioteca desactivada se oculta")
	}
	f.exec("UPDATE libraries SET enabled = 1 WHERE id = ?", f.lib)
	f.exec("DELETE FROM user_permissions WHERE user_id = ?", f.user.UserID)
	if rows, _ = p.ContinueWatching(f.ctx, f.user, 0); len(rows) != 0 {
		t.Fatal("sin permiso no se ve")
	}

	// Un restringido ve lo que su edad permite; el episodio hereda de la serie.
	kid := f.user
	kid.Role, kid.MaxAgeRating = media.RoleRestricted, &[]int64{12}[0]
	f.exec("UPDATE users SET role = 'restricted' WHERE id = ?", kid.UserID)
	f.exec("INSERT INTO user_permissions VALUES (?, ?, 'view')", kid.UserID, f.lib)
	f.exec("UPDATE media_items SET min_age = 16 WHERE id = ?", empezada)
	f.exec("UPDATE media_items SET min_age = 7 WHERE id = ?", series)
	rows, _ = p.ContinueWatching(f.ctx, kid, 0)
	if len(rows) != 1 || rows[0].ID != ep {
		t.Fatalf("%+v", rows)
	}
}

func TestHistorialPaginado(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		s := f.create()
		f.clock.add(time.Second)
		if _, err := f.beat(s.ID, Playing, int64(i+1)*1000); err != nil {
			t.Fatal(err)
		}
		if err := f.m.StopSession(f.ctx, f.user, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	p := f.progressService()
	var positions []int64
	cursor := ""
	for pages := 0; ; pages++ {
		rows, next, err := p.History(f.ctx, f.user, cursor, 2)
		if err != nil || pages > 3 {
			t.Fatalf("%v", err)
		}
		for _, r := range rows {
			positions = append(positions, r.PositionMs)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(positions) != 5 || positions[0] != 5000 || positions[4] != 1000 {
		t.Fatalf("del más reciente al más antiguo: %v", positions)
	}
	if rows, _, _ := p.History(f.ctx, f.another, "", 0); len(rows) != 0 {
		t.Fatal("el historial es de cada usuario")
	}
	_, _, err := p.History(f.ctx, f.user, "basura!", 0)
	wantCode(t, err, core.CodeValidation)
	// El título se conserva aunque el elemento desaparezca.
	f.exec("DELETE FROM media_items WHERE id = ?", f.item)
	rows, _, _ := p.History(f.ctx, f.user, "", 0)
	if len(rows) != 5 || rows[0].MediaItemID != nil || rows[0].Title != "Película" {
		t.Fatalf("%+v", rows[0])
	}
}
