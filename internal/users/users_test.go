package users

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"medianode/internal/auth"
	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/testutil"
)

const password = "contraseña-segura"

type fixture struct {
	t     *testing.T
	ctx   context.Context
	s     *Service
	auth  *auth.Service
	db    *database.DB
	owner authorization.Actor
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testutil.NewDB(t)
	hasher := auth.NewHasher(auth.Params{MemoryKiB: 8, Iterations: 1, Threads: 1})
	a := auth.New(db, hasher, zerolog.Nop())
	ctx := context.Background()
	code, err := a.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Setup(ctx, code, "propietario", password)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		t: t, ctx: ctx, s: New(db, hasher), auth: a, db: db,
		owner: authorization.Actor{UserID: owner.ID, Role: media.RoleOwner},
	}
}

func (f *fixture) create(name string, role media.Role) *sqlc.User {
	f.t.Helper()
	u, err := f.s.Create(f.ctx, f.owner, CreateParams{Username: name, Password: password, Role: role})
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func actorOf(u *sqlc.User) authorization.Actor {
	return authorization.Actor{UserID: u.ID, Role: media.Role(u.Role)}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := core.CodeOf(err); got != code {
		t.Fatalf("código = %q (%v), se esperaba %q", got, err, code)
	}
}

func ptr[T any](v T) *T { return &v }

func TestQuienPuedeCrearCadaRol(t *testing.T) {
	f := newFixture(t)
	admin := actorOf(f.create("admin", media.RoleAdmin))
	user := actorOf(f.create("normal", media.RoleUser))

	cases := []struct {
		name  string
		actor authorization.Actor
		role  media.Role
		code  string
	}{
		{"owner crea admin", f.owner, media.RoleAdmin, ""},
		{"owner no crea otro owner", f.owner, media.RoleOwner, core.CodeForbidden},
		{"admin crea user", admin, media.RoleUser, ""},
		{"admin crea restricted", admin, media.RoleRestricted, ""},
		{"admin no crea admin", admin, media.RoleAdmin, core.CodeForbidden},
		{"admin no crea owner", admin, media.RoleOwner, core.CodeForbidden},
		{"user no crea nada", user, media.RoleRestricted, core.CodeForbidden},
		{"rol desconocido", f.owner, media.Role("root"), core.CodeValidation},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.s.Create(f.ctx, c.actor, CreateParams{
				Username: "nuevo" + string(rune('a'+i)), Password: password, Role: c.role,
			})
			wantCode(t, err, c.code)
		})
	}
}

func TestUsuarioDuplicadoIgnoraMayusculas(t *testing.T) {
	f := newFixture(t)
	f.create("Maria", media.RoleUser)
	_, err := f.s.Create(f.ctx, f.owner, CreateParams{Username: "MARIA", Password: password, Role: media.RoleUser})
	wantCode(t, err, core.CodeUsernameTaken)

	other := f.create("Pedro", media.RoleUser)
	_, err = f.s.Update(f.ctx, f.owner, other.ID, UpdateParams{Username: ptr("maria")})
	wantCode(t, err, core.CodeUsernameTaken)
	if _, err := f.s.Update(f.ctx, f.owner, other.ID, UpdateParams{Username: ptr("PEDRO")}); err != nil {
		t.Fatalf("cambiar solo las mayúsculas del propio nombre: %v", err)
	}
}

func TestCrearUsuarioCreaPreferencias(t *testing.T) {
	f := newFixture(t)
	u := f.create("normal", media.RoleUser)
	p, err := f.s.Preferences(f.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.SubtitleMode != media.SubtitleDefault {
		t.Fatalf("modo = %q", p.SubtitleMode)
	}
}

func TestNadieEscalaPrivilegiosConUpdate(t *testing.T) {
	f := newFixture(t)
	adminUser := f.create("admin", media.RoleAdmin)
	otherAdmin := f.create("admin2", media.RoleAdmin)
	user := f.create("normal", media.RoleUser)
	admin := actorOf(adminUser)
	owner, ownerRole := f.owner.UserID, ptr(media.RoleOwner)

	cases := []struct {
		name  string
		actor authorization.Actor
		id    string
		p     UpdateParams
		code  string
	}{
		{"nadie se cambia su rol", admin, adminUser.ID, UpdateParams{Role: ptr(media.RoleUser)}, core.CodeForbidden},
		{"nadie se desactiva", f.owner, owner, UpdateParams{IsActive: ptr(false)}, core.CodeForbidden},
		{"la contraseña propia va por otra ruta", admin, adminUser.ID, UpdateParams{Password: ptr(password)}, core.CodeForbidden},
		{"admin no toca al owner", admin, owner, UpdateParams{Username: ptr("nuevo")}, core.CodeForbidden},
		{"admin no toca a otro admin", admin, otherAdmin.ID, UpdateParams{IsActive: ptr(false)}, core.CodeForbidden},
		{"admin no asciende a admin", admin, user.ID, UpdateParams{Role: ptr(media.RoleAdmin)}, core.CodeForbidden},
		{"owner no crea otro owner", f.owner, user.ID, UpdateParams{Role: ownerRole}, core.CodeForbidden},
		{"user no entra", actorOf(user), user.ID, UpdateParams{Username: ptr("nuevo")}, core.CodeForbidden},
		{"usuario inexistente", f.owner, "no-existe", UpdateParams{Username: ptr("nuevo")}, core.CodeNotFound},
		{"owner degrada a admin", f.owner, otherAdmin.ID, UpdateParams{Role: ptr(media.RoleUser)}, ""},
		{"admin desactiva a user", admin, user.ID, UpdateParams{IsActive: ptr(false)}, ""},
		{"se renombra a sí mismo", admin, adminUser.ID, UpdateParams{Username: ptr("admin-nuevo")}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.s.Update(f.ctx, c.actor, c.id, c.p)
			wantCode(t, err, c.code)
		})
	}
}

func TestUpdateConservaLoQueNoCambia(t *testing.T) {
	f := newFixture(t)
	u, err := f.s.Create(f.ctx, f.owner, CreateParams{
		Username: "peque", Password: password, Role: media.RoleRestricted, MaxAgeRating: ptr(int64(12)),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := f.s.Update(f.ctx, f.owner, u.ID, UpdateParams{Username: ptr("peque2")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "peque2" || got.Role != u.Role || got.MaxAgeRating == nil || *got.MaxAgeRating != 12 || got.IsActive != 1 {
		t.Fatalf("usuario = %+v", got)
	}

	got, err = f.s.Update(f.ctx, f.owner, u.ID, UpdateParams{MaxAgeRating: ptr(int64(16))})
	if err != nil || *got.MaxAgeRating != 16 {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = f.s.Update(f.ctx, f.owner, u.ID, UpdateParams{ClearAgeRating: true})
	if err != nil || got.MaxAgeRating != nil {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = f.s.Update(f.ctx, f.owner, u.ID, UpdateParams{MaxAgeRating: ptr(int64(-1))})
	wantCode(t, err, core.CodeValidation)
}

func TestRestablecerContrasenaRevocaDispositivos(t *testing.T) {
	f := newFixture(t)
	u := f.create("normal", media.RoleUser)
	pair, err := f.auth.Login(f.ctx, u.Username, password, auth.DeviceInfo{Name: "x", Platform: "ios"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.s.Update(f.ctx, f.owner, u.ID, UpdateParams{Password: ptr("otra-contraseña")}); err != nil {
		t.Fatal(err)
	}
	_, err = f.auth.Authenticate(f.ctx, pair.AccessToken)
	wantCode(t, err, core.CodeDeviceRevoked)
	if _, err := f.auth.Login(f.ctx, u.Username, "otra-contraseña", auth.DeviceInfo{Name: "x", Platform: "ios"}); err != nil {
		t.Fatal(err)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	adminUser := f.create("admin", media.RoleAdmin)
	admin := actorOf(adminUser)
	user := f.create("normal", media.RoleUser)
	pair, err := f.auth.Login(f.ctx, user.Username, password, auth.DeviceInfo{Name: "x", Platform: "ios"})
	if err != nil {
		t.Fatal(err)
	}

	wantCode(t, f.s.Delete(f.ctx, admin, adminUser.ID), core.CodeForbidden)
	wantCode(t, f.s.Delete(f.ctx, admin, f.owner.UserID), core.CodeForbidden)
	wantCode(t, f.s.Delete(f.ctx, f.owner, f.owner.UserID), core.CodeForbidden)
	wantCode(t, f.s.Delete(f.ctx, actorOf(user), adminUser.ID), core.CodeForbidden)
	wantCode(t, f.s.Delete(f.ctx, f.owner, "no-existe"), core.CodeNotFound)

	if err := f.s.Delete(f.ctx, admin, user.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.auth.Authenticate(f.ctx, pair.AccessToken)
	wantCode(t, err, core.CodeAuthRequired)
	if _, err := f.db.R.GetDevice(f.ctx, pair.DeviceID); err == nil {
		t.Fatal("los dispositivos deben caer en cascada")
	}
}

func TestListYGetSoloParaAdministradores(t *testing.T) {
	f := newFixture(t)
	user := actorOf(f.create("normal", media.RoleUser))

	_, err := f.s.List(f.ctx, user)
	wantCode(t, err, core.CodeForbidden)
	_, err = f.s.Get(f.ctx, user, user.UserID)
	wantCode(t, err, core.CodeForbidden)

	rows, err := f.s.List(f.ctx, f.owner)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%d %v", len(rows), err)
	}
	_, err = f.s.Get(f.ctx, f.owner, "no-existe")
	wantCode(t, err, core.CodeNotFound)
}

func TestValidarPreferencias(t *testing.T) {
	f := newFixture(t)
	id := f.owner.UserID

	want := Preferences{
		AudioLanguage: ptr("es"), SubtitleLanguage: ptr("en-US"),
		SubtitleMode: media.SubtitleForced, MaxBitrateRemote: ptr(int64(8_000_000)),
	}
	if err := f.s.SetPreferences(f.ctx, id, want); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.Preferences(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if *got.AudioLanguage != "es" || *got.SubtitleLanguage != "en-US" || got.SubtitleMode != media.SubtitleForced || *got.MaxBitrateRemote != 8_000_000 {
		t.Fatalf("preferencias = %+v", got)
	}

	for _, bad := range []Preferences{
		{SubtitleMode: "raro"},
		{SubtitleMode: media.SubtitleOff, AudioLanguage: ptr("  ")},
		{SubtitleMode: media.SubtitleOff, SubtitleLanguage: ptr(strings.Repeat("x", 36))},
		{SubtitleMode: media.SubtitleOff, MaxBitrateRemote: ptr(int64(0))},
	} {
		wantCode(t, f.s.SetPreferences(f.ctx, id, bad), core.CodeValidation)
	}
}
