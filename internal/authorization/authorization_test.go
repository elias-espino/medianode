package authorization_test

import (
	"context"
	"testing"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/testutil"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	db  *database.DB
	a   *authorization.Authorizer
}

func newFixture(t *testing.T) *fixture {
	db := testutil.NewDB(t)
	return &fixture{t: t, ctx: context.Background(), db: db, a: authorization.NewAuthorizer(db)}
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
	err := f.db.Q.CreateUser(f.ctx, sqlc.CreateUserParams{ID: id, Username: id, PasswordHash: "x", Role: string(role)})
	if err != nil {
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
	err := f.db.Q.CreateLibrary(f.ctx, sqlc.CreateLibraryParams{ID: id, Name: "l", Type: "tv", Enabled: on, CreatedAt: database.Now()})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) grant(a authorization.Actor, lib string, p media.Permission) {
	if err := f.db.Q.AddUserPermission(f.ctx, sqlc.AddUserPermissionParams{UserID: a.UserID, LibraryID: lib, Permission: string(p)}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) item(lib string, parent *string, t media.ItemType, minAge *int64) string {
	id := f.id()
	err := f.db.Q.CreateMediaItem(f.ctx, sqlc.CreateMediaItemParams{
		ID: id, LibraryID: lib, ParentID: parent, Type: string(t), Title: "x", CreatedAt: database.Now(),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if minAge != nil {
		if _, err := f.db.Write.Exec("UPDATE media_items SET min_age = ? WHERE id = ?", *minAge, id); err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

func (f *fixture) file(lib string, items ...string) string {
	pathID, fileID := f.id(), f.id()
	if err := f.db.Q.CreateLibraryPath(f.ctx, sqlc.CreateLibraryPathParams{ID: pathID, LibraryID: lib, Path: pathID}); err != nil {
		f.t.Fatal(err)
	}
	err := f.db.Q.CreateMediaFile(f.ctx, sqlc.CreateMediaFileParams{
		ID: fileID, LibraryPathID: pathID, RelativePath: "a.mkv", ProbeStatus: "done", CreatedAt: database.Now(),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	for i, it := range items {
		if err := f.db.Q.LinkFileItem(f.ctx, sqlc.LinkFileItemParams{MediaFileID: fileID, MediaItemID: it, PartOrder: int64(i)}); err != nil {
			f.t.Fatal(err)
		}
	}
	return fileID
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := core.CodeOf(err); got != code {
		t.Fatalf("código = %q (%v), se esperaba %q", got, err, code)
	}
}

func age(n int64) *int64 { return &n }

func TestCanLibrary(t *testing.T) {
	f := newFixture(t)
	lib, off := f.library(true), f.library(false)
	owner, admin := f.user(media.RoleOwner, nil), f.user(media.RoleAdmin, nil)
	viewer, scanner, manager, none := f.user(media.RoleUser, nil), f.user(media.RoleUser, nil), f.user(media.RoleUser, nil), f.user(media.RoleUser, nil)
	f.grant(viewer, lib, media.PermissionView)
	f.grant(scanner, lib, media.PermissionScan)
	f.grant(manager, lib, media.PermissionManage)
	f.grant(manager, off, media.PermissionManage)

	cases := []struct {
		name  string
		actor authorization.Actor
		lib   string
		perm  media.Permission
		code  string
	}{
		{"owner sin filas", owner, lib, media.PermissionManage, ""},
		{"admin sin filas", admin, lib, media.PermissionManage, ""},
		{"admin ve la desactivada", admin, off, media.PermissionView, ""},
		{"view ve", viewer, lib, media.PermissionView, ""},
		{"view no escanea", viewer, lib, media.PermissionScan, core.CodeLibraryDenied},
		{"scan ve", scanner, lib, media.PermissionView, ""},
		{"scan escanea", scanner, lib, media.PermissionScan, ""},
		{"scan no gestiona", scanner, lib, media.PermissionManage, core.CodeLibraryDenied},
		{"manage gestiona", manager, lib, media.PermissionManage, ""},
		{"sin permiso no la ve", none, lib, media.PermissionView, core.CodeNotFound},
		{"desactivada oculta aunque haya permiso", manager, off, media.PermissionView, core.CodeNotFound},
		{"inexistente", owner, "nada", media.PermissionView, core.CodeNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, f.a.CanLibrary(f.ctx, c.actor, c.lib, c.perm), c.code)
		})
	}
}

func TestCanItemRestringido(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	series := f.item(lib, nil, media.ItemSeries, age(12))
	season := f.item(lib, &series, media.ItemSeason, nil)
	episode := f.item(lib, &season, media.ItemEpisode, nil)
	unrated := f.item(lib, nil, media.ItemMovie, nil)
	adult := f.item(lib, nil, media.ItemMovie, age(18))
	allAges := f.item(lib, nil, media.ItemMovie, age(0))

	kid, teen, noLimit := f.user(media.RoleRestricted, age(7)), f.user(media.RoleRestricted, age(16)), f.user(media.RoleRestricted, nil)
	user, stranger := f.user(media.RoleUser, nil), f.user(media.RoleUser, nil)
	for _, a := range []authorization.Actor{kid, teen, noLimit, user} {
		f.grant(a, lib, media.PermissionView)
	}

	cases := []struct {
		name  string
		actor authorization.Actor
		item  string
		code  string
	}{
		{"el episodio hereda la edad de la serie", teen, episode, ""},
		{"edad superior al límite", kid, episode, core.CodeNotFound},
		{"sin clasificación queda oculto", teen, unrated, core.CodeNotFound},
		{"adulto oculto", teen, adult, core.CodeNotFound},
		{"sin límite solo todos los públicos", noLimit, allAges, ""},
		{"sin límite no ve +12", noLimit, series, core.CodeNotFound},
		{"un user no tiene filtro de edad", user, unrated, ""},
		{"sin permiso no lo ve", stranger, allAges, core.CodeNotFound},
		{"inexistente", user, "nada", core.CodeNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, f.a.CanItem(f.ctx, c.actor, c.item), c.code)
		})
	}
}

func TestCanFile(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	ok := f.item(lib, nil, media.ItemMovie, age(0))
	adult := f.item(lib, nil, media.ItemMovie, age(18))
	unidentified := f.file(lib)
	mixed := f.file(lib, ok, adult)
	normal := f.file(lib, ok)

	admin, user, kid := f.user(media.RoleAdmin, nil), f.user(media.RoleUser, nil), f.user(media.RoleRestricted, age(12))
	f.grant(user, lib, media.PermissionView)
	f.grant(kid, lib, media.PermissionView)

	wantCode(t, f.a.CanFile(f.ctx, admin, unidentified), "")
	wantCode(t, f.a.CanFile(f.ctx, user, unidentified), core.CodeNotFound)
	wantCode(t, f.a.CanFile(f.ctx, user, normal), "")
	wantCode(t, f.a.CanFile(f.ctx, kid, normal), "")
	wantCode(t, f.a.CanFile(f.ctx, kid, mixed), core.CodeNotFound)
	wantCode(t, f.a.CanFile(f.ctx, user, "nada"), core.CodeNotFound)
}

func TestPermisoRetiradoSurteEfectoEnLaSiguienteComprobacion(t *testing.T) {
	f := newFixture(t)
	lib := f.library(true)
	u := f.user(media.RoleUser, nil)
	f.grant(u, lib, media.PermissionView)
	wantCode(t, f.a.CanLibrary(f.ctx, u, lib, media.PermissionView), "")
	if err := f.db.Q.DeleteUserPermissions(f.ctx, u.UserID); err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.a.CanLibrary(f.ctx, u, lib, media.PermissionView), core.CodeNotFound)
}
