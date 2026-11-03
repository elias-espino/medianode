package libraries

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/probe"
	"medianode/internal/scanner"
	"medianode/internal/testutil"
)

// gatedProber bloquea cada análisis hasta que se cierra gate.
type gatedProber struct{ gate chan struct{} }

func (p *gatedProber) Run(ctx context.Context, _ string) (*probe.Result, error) {
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &probe.Result{FormatName: "matroska,webm", Streams: []probe.Stream{{Type: media.StreamVideo}}}, nil
}

type fixture struct {
	t        *testing.T
	ctx      context.Context
	db       *database.DB
	s        *Service
	sc       *scanner.Scanner
	prober   *gatedProber
	reserved string
}

func newFixture(t *testing.T) *fixture {
	db := testutil.NewDB(t)
	prober := &gatedProber{}
	sc := scanner.New(db, prober, scanner.Options{Workers: 1}, zerolog.Nop())
	t.Cleanup(sc.Close)
	reserved := t.TempDir()
	return &fixture{
		t: t, ctx: context.Background(), db: db, sc: sc, prober: prober, reserved: reserved,
		s: New(db, authorization.NewAuthorizer(db), sc, []string{reserved}),
	}
}

func (f *fixture) user(role media.Role) authorization.Actor {
	id, err := database.NewID()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Q.CreateUser(f.ctx, sqlc.CreateUserParams{ID: id, Username: id, PasswordHash: "x", Role: string(role)}); err != nil {
		f.t.Fatal(err)
	}
	return authorization.Actor{UserID: id, Role: role}
}

func (f *fixture) create(admin authorization.Actor, paths ...string) *Library {
	f.t.Helper()
	lib, err := f.s.Create(f.ctx, admin, CreateParams{Name: "Películas", Type: media.LibraryMovies, Paths: paths})
	if err != nil {
		f.t.Fatal(err)
	}
	return lib
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := core.CodeOf(err); got != code {
		t.Fatalf("código = %q (%v), se esperaba %q", got, err, code)
	}
}

func video(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte("contenido de "+name), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCrearValida(t *testing.T) {
	f := newFixture(t)
	admin := f.user(media.RoleAdmin)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	_ = os.Mkdir(sub, 0o755)

	cases := []struct {
		name string
		p    CreateParams
		code string
	}{
		{"nombre vacío", CreateParams{Name: "  ", Type: media.LibraryMovies}, core.CodeValidation},
		{"tipo desconocido", CreateParams{Name: "x", Type: "music"}, core.CodeValidation},
		{"ruta relativa", CreateParams{Name: "x", Type: media.LibraryTV, Paths: []string{"media"}}, core.CodePathUnavailable},
		{"ruta inexistente", CreateParams{Name: "x", Type: media.LibraryTV, Paths: []string{filepath.Join(root, "nada")}}, core.CodePathUnavailable},
		{"directorio del servidor", CreateParams{Name: "x", Type: media.LibraryTV, Paths: []string{f.reserved}}, core.CodePathUnavailable},
		{"rutas anidadas", CreateParams{Name: "x", Type: media.LibraryTV, Paths: []string{root, sub}}, core.CodeValidation},
		{"rutas repetidas", CreateParams{Name: "x", Type: media.LibraryTV, Paths: []string{root, root}}, core.CodeValidation},
		{"válida sin rutas", CreateParams{Name: "x", Type: media.LibraryTV}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.s.Create(f.ctx, admin, c.p)
			wantCode(t, err, c.code)
		})
	}
}

func TestSoloAdministradoresCreanEliminanYTocanRutas(t *testing.T) {
	f := newFixture(t)
	admin, manager := f.user(media.RoleAdmin), f.user(media.RoleUser)
	lib := f.create(admin)
	_ = f.db.Q.AddUserPermission(f.ctx, sqlc.AddUserPermissionParams{UserID: manager.UserID, LibraryID: lib.ID, Permission: "manage"})

	_, err := f.s.Create(f.ctx, manager, CreateParams{Name: "x", Type: media.LibraryTV})
	wantCode(t, err, core.CodeForbidden)
	_, err = f.s.AddPath(f.ctx, manager, lib.ID, t.TempDir())
	wantCode(t, err, core.CodeForbidden)
	wantCode(t, f.s.RemovePath(f.ctx, manager, lib.ID, "x"), core.CodeForbidden)
	wantCode(t, f.s.Delete(f.ctx, manager, lib.ID), core.CodeForbidden)

	// manage sí permite editar y escanear, pero no ver las rutas físicas.
	name := "Cine"
	got, err := f.s.Update(f.ctx, manager, lib.ID, UpdateParams{Name: &name})
	if err != nil || got.Name != "Cine" || got.Paths != nil {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := f.s.StartScan(f.ctx, manager, lib.ID); err != nil {
		t.Fatal(err)
	}
}

func TestListYGetSegunPermisos(t *testing.T) {
	f := newFixture(t)
	admin, viewer, stranger := f.user(media.RoleAdmin), f.user(media.RoleUser), f.user(media.RoleUser)
	visible := f.create(admin, t.TempDir())
	f.create(admin)
	_ = f.db.Q.AddUserPermission(f.ctx, sqlc.AddUserPermissionParams{UserID: viewer.UserID, LibraryID: visible.ID, Permission: "view"})

	all, _ := f.s.List(f.ctx, admin)
	mine, _ := f.s.List(f.ctx, viewer)
	none, _ := f.s.List(f.ctx, stranger)
	if len(all) != 2 || len(all[0].Paths)+len(all[1].Paths) != 1 || len(mine) != 1 || mine[0].Paths != nil || len(none) != 0 {
		t.Fatalf("admin %d, viewer %d, ajeno %d", len(all), len(mine), len(none))
	}
	_, err := f.s.Get(f.ctx, stranger, visible.ID)
	wantCode(t, err, core.CodeNotFound)
	_, err = f.s.StartScan(f.ctx, viewer, visible.ID)
	wantCode(t, err, core.CodeLibraryDenied)
	rename := "otro"
	_, err = f.s.Update(f.ctx, viewer, visible.ID, UpdateParams{Name: &rename})
	wantCode(t, err, core.CodeLibraryDenied)

	off := false
	if _, err := f.s.Update(f.ctx, admin, visible.ID, UpdateParams{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if mine, _ := f.s.List(f.ctx, viewer); len(mine) != 0 {
		t.Fatal("una biblioteca desactivada desaparece para quien no es administrador")
	}
}

func TestAddPathRechazaSolapes(t *testing.T) {
	f := newFixture(t)
	admin := f.user(media.RoleAdmin)
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "sub"), 0o755)
	lib := f.create(admin, root)

	_, err := f.s.AddPath(f.ctx, admin, lib.ID, filepath.Join(root, "sub"))
	wantCode(t, err, core.CodeValidation)
	_, err = f.s.AddPath(f.ctx, admin, "nada", t.TempDir())
	wantCode(t, err, core.CodeNotFound)

	other := f.create(admin)
	if _, err := f.s.AddPath(f.ctx, admin, other.ID, root); err != nil {
		t.Fatalf("otra biblioteca puede compartir la ruta: %v", err)
	}
}

func TestQuitarRutaBorraSusArchivosYElementos(t *testing.T) {
	f := newFixture(t)
	admin := f.user(media.RoleAdmin)
	a, b := t.TempDir(), t.TempDir()
	video(t, a, "A (2001).mkv")
	video(t, b, "B (2002).mkv")
	lib := f.create(admin, a, b)
	if _, err := f.sc.Scan(f.ctx, lib.ID, media.TriggerManual); err != nil {
		t.Fatal(err)
	}
	var pathID string
	for _, p := range lib.Paths {
		if p.Path == a {
			pathID = p.ID
		}
	}
	if err := f.s.RemovePath(f.ctx, admin, lib.ID, pathID); err != nil {
		t.Fatal(err)
	}
	var files, items int
	_ = f.db.Read.QueryRow("SELECT COUNT(*) FROM media_files").Scan(&files)
	_ = f.db.Read.QueryRow("SELECT COUNT(*) FROM media_items").Scan(&items)
	if files != 1 || items != 1 {
		t.Fatalf("archivos %d, elementos %d", files, items)
	}
	wantCode(t, f.s.RemovePath(f.ctx, admin, lib.ID, pathID), core.CodeNotFound)
}

func TestEliminarBibliotecaEscaneada(t *testing.T) {
	f := newFixture(t)
	admin := f.user(media.RoleAdmin)
	root := t.TempDir()
	video(t, root, "A (2001).mkv")
	lib := f.create(admin, root)
	if _, err := f.sc.Scan(f.ctx, lib.ID, media.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Delete(f.ctx, admin, lib.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = f.db.Read.QueryRow("SELECT (SELECT COUNT(*) FROM media_files) + (SELECT COUNT(*) FROM media_items) + (SELECT COUNT(*) FROM scan_jobs)").Scan(&n)
	if n != 0 {
		t.Fatalf("quedan %d filas", n)
	}
	wantCode(t, f.s.Delete(f.ctx, admin, lib.ID), core.CodeNotFound)
}

func TestEliminarDuranteUnEscaneoLoDetiene(t *testing.T) {
	f := newFixture(t)
	admin := f.user(media.RoleAdmin)
	root := t.TempDir()
	video(t, root, "A (2001).mkv")
	lib := f.create(admin, root)
	f.prober.gate = make(chan struct{})
	if _, err := f.s.StartScan(f.ctx, admin, lib.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- f.s.Delete(f.ctx, admin, lib.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("eliminar no debe esperar a que el escaneo termine solo")
	}
	if _, running := f.sc.Running(lib.ID); running {
		t.Fatal("el escaneo debe haberse detenido")
	}
}

func TestPermisos(t *testing.T) {
	f := newFixture(t)
	owner, admin, other := f.user(media.RoleOwner), f.user(media.RoleAdmin), f.user(media.RoleAdmin)
	u := f.user(media.RoleRestricted)
	a, b := f.create(admin), f.create(admin)

	got, err := f.s.SetPermissions(f.ctx, admin, u.UserID, []Grant{
		{LibraryID: a.ID, Permissions: []media.Permission{media.PermissionView, media.PermissionView}},
		{LibraryID: b.ID, Permissions: []media.Permission{media.PermissionScan}},
	})
	if err != nil || len(got) != 2 || len(got[0].Permissions)+len(got[1].Permissions) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = f.s.SetPermissions(f.ctx, admin, u.UserID, []Grant{{LibraryID: b.ID, Permissions: []media.Permission{media.PermissionManage}}})
	if err != nil || len(got) != 1 || got[0].Permissions[0] != media.PermissionManage {
		t.Fatalf("debe sustituir, no sumar: %+v %v", got, err)
	}

	cases := []struct {
		name   string
		actor  authorization.Actor
		target string
		grants []Grant
		code   string
	}{
		{"permiso desconocido", admin, u.UserID, []Grant{{LibraryID: a.ID, Permissions: []media.Permission{"root"}}}, core.CodeValidation},
		{"biblioteca repetida", admin, u.UserID, []Grant{{LibraryID: a.ID}, {LibraryID: a.ID}}, core.CodeValidation},
		{"biblioteca inexistente", admin, u.UserID, []Grant{{LibraryID: "nada"}}, core.CodeValidation},
		{"a otro administrador no", admin, other.UserID, nil, core.CodeForbidden},
		{"el owner a un admin no hace falta", owner, admin.UserID, nil, core.CodeValidation},
		{"un user no administra", f.user(media.RoleUser), u.UserID, nil, core.CodeForbidden},
		{"usuario inexistente", admin, "nada", nil, core.CodeNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.s.SetPermissions(f.ctx, c.actor, c.target, c.grants)
			wantCode(t, err, c.code)
		})
	}
	// Un fallo no deja los permisos a medias.
	got, _ = f.s.Permissions(f.ctx, admin, u.UserID)
	if len(got) != 1 || got[0].LibraryID != b.ID {
		t.Fatalf("%+v", got)
	}
}

func TestScanStatus(t *testing.T) {
	f := newFixture(t)
	admin := f.user(media.RoleAdmin)
	lib := f.create(admin)
	job, err := f.s.ScanStatus(f.ctx, admin, lib.ID)
	if err != nil || job != nil {
		t.Fatalf("sin escaneos: %v %v", job, err)
	}
	if _, err := f.sc.Scan(f.ctx, lib.ID, media.TriggerManual); err != nil {
		t.Fatal(err)
	}
	job, err = f.s.ScanStatus(f.ctx, admin, lib.ID)
	if err != nil || job == nil || job.Status != string(media.ScanCompleted) {
		t.Fatalf("%+v %v", job, err)
	}
}
