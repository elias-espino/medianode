package devices

import (
	"context"
	"strings"
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
	s   *Service
	db  *database.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testutil.NewDB(t)
	return &fixture{t: t, ctx: context.Background(), s: New(db), db: db}
}

func (f *fixture) user(role media.Role) authorization.Actor {
	f.t.Helper()
	id, err := database.NewID()
	if err != nil {
		f.t.Fatal(err)
	}
	err = f.db.Q.CreateUser(f.ctx, sqlc.CreateUserParams{ID: id, Username: id, PasswordHash: "x", Role: string(role)})
	if err != nil {
		f.t.Fatal(err)
	}
	return authorization.Actor{UserID: id, Role: role}
}

func (f *fixture) device(owner authorization.Actor) string {
	f.t.Helper()
	id, err := database.NewID()
	if err != nil {
		f.t.Fatal(err)
	}
	err = f.db.Q.CreateDevice(f.ctx, sqlc.CreateDeviceParams{ID: id, UserID: owner.UserID, DeviceName: "x", Platform: "ios"})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := core.CodeOf(err); got != code {
		t.Fatalf("código = %q (%v), se esperaba %q", got, err, code)
	}
}

func TestUnUsuarioNoPuedeTocarDispositivosAjenos(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user(media.RoleUser), f.user(media.RoleUser)
	id := f.device(alice)

	_, err := f.s.Get(f.ctx, bob, id)
	wantCode(t, err, core.CodeNotFound)
	_, err = f.s.Rename(f.ctx, bob, id, "robado")
	wantCode(t, err, core.CodeNotFound)
	wantCode(t, f.s.Revoke(f.ctx, bob, id), core.CodeNotFound)

	dev, err := f.db.R.GetDevice(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if dev.RevokedAt != nil || dev.DeviceName != "x" {
		t.Fatalf("el dispositivo no debe cambiar: %+v", dev)
	}
}

func TestPropietarioYAdministradores(t *testing.T) {
	f := newFixture(t)
	owner, admin, other := f.user(media.RoleOwner), f.user(media.RoleAdmin), f.user(media.RoleAdmin)
	user := f.user(media.RoleUser)

	cases := []struct {
		name  string
		actor authorization.Actor
		of    authorization.Actor
		code  string
	}{
		{"el dueño revoca el suyo", user, user, ""},
		{"admin revoca el de un user", admin, user, ""},
		{"owner revoca el de un admin", owner, admin, ""},
		{"admin no ve el de otro admin", admin, other, core.CodeNotFound},
		{"admin no ve el del owner", admin, owner, core.CodeNotFound},
		// Solo los administradores gestionan dispositivos ajenos, aunque haya rango.
		{"user no ve el de un restricted", user, f.user(media.RoleRestricted), core.CodeNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, f.s.Revoke(f.ctx, c.actor, f.device(c.of)), c.code)
		})
	}
}

func TestRevocarAlcanzaLosTokens(t *testing.T) {
	f := newFixture(t)
	u := f.user(media.RoleUser)
	id := f.device(u)
	err := f.db.Q.CreateRefreshToken(f.ctx, sqlc.CreateRefreshTokenParams{
		ID: "t1", DeviceID: id, TokenHash: "h", ExpiresAt: "2099-01-01T00:00:00.000Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := f.s.Revoke(f.ctx, u, id); err != nil {
		t.Fatal(err)
	}
	row, err := f.db.R.GetRefreshTokenByHash(f.ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	if row.RevokedAt == nil {
		t.Fatal("el token debe quedar revocado")
	}
}

func TestRevocarDosVecesConservaLaFecha(t *testing.T) {
	f := newFixture(t)
	u := f.user(media.RoleUser)
	id := f.device(u)
	if err := f.s.Revoke(f.ctx, u, id); err != nil {
		t.Fatal(err)
	}
	first, _ := f.db.R.GetDevice(f.ctx, id)
	err := f.db.Q.RevokeDevice(f.ctx, sqlc.RevokeDeviceParams{ID: id, RevokedAt: new("2099-01-01T00:00:00.000Z")})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := f.db.R.GetDevice(f.ctx, id)
	if *first.RevokedAt != *second.RevokedAt {
		t.Fatal("la primera revocación debe conservarse")
	}
}

func TestList(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user(media.RoleUser), f.user(media.RoleUser)
	admin := f.user(media.RoleAdmin)
	f.device(alice)
	f.device(bob)

	own, err := f.s.List(f.ctx, alice, false)
	if err != nil || len(own) != 1 || own[0].UserID != alice.UserID {
		t.Fatalf("%+v %v", own, err)
	}
	_, err = f.s.List(f.ctx, alice, true)
	wantCode(t, err, core.CodeForbidden)
	all, err := f.s.List(f.ctx, admin, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("%d %v", len(all), err)
	}
}

func TestRenombrar(t *testing.T) {
	f := newFixture(t)
	u := f.user(media.RoleUser)
	id := f.device(u)

	dev, err := f.s.Rename(f.ctx, u, id, "  Salón  ")
	if err != nil || dev.DeviceName != "Salón" {
		t.Fatalf("%+v %v", dev, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", maxNameLength+1)} {
		_, err := f.s.Rename(f.ctx, u, id, bad)
		wantCode(t, err, core.CodeValidation)
	}
}

func TestDispositivoInexistente(t *testing.T) {
	f := newFixture(t)
	_, err := f.s.Get(f.ctx, f.user(media.RoleOwner), "no-existe")
	wantCode(t, err, core.CodeNotFound)
}
