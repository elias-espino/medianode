package sessions

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/events"
	"medianode/internal/media"
	"medianode/internal/testutil"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type recorder struct {
	mu    sync.Mutex
	ended []Session
}

func (r *recorder) OnSessionEnded(s Session) {
	r.mu.Lock()
	r.ended = append(r.ended, s)
	r.mu.Unlock()
}

func (r *recorder) list() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Session(nil), r.ended...)
}

type fixture struct {
	t       *testing.T
	ctx     context.Context
	db      *database.DB
	bus     *events.Bus
	clock   *clock
	m       *Manager
	ended   *recorder
	dir     string
	lib     string
	item    string
	file    string
	user    authorization.Actor
	device  string
	another authorization.Actor
}

const duration = int64(100_000)

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, ctx: context.Background(), db: testutil.NewDB(t), bus: events.NewBus(),
		clock: &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}, ended: &recorder{}, dir: t.TempDir()}
	f.lib = f.id()
	f.exec("INSERT INTO libraries (id, name, type) VALUES (?, 'l', 'movies')", f.lib)
	f.item, f.file = f.newItem(f.lib, "Película", &[]int64{duration}[0])
	f.user, f.device = f.newUser(media.RoleUser)
	f.another, _ = f.newUser(media.RoleUser)
	f.exec("INSERT INTO user_permissions VALUES (?, ?, 'view')", f.user.UserID, f.lib)
	f.m = f.newManager()
	return f
}

func (f *fixture) newManager() *Manager {
	m := New(f.db, f.bus, Options{
		IdleTimeout: 90 * time.Second, FlushInterval: time.Hour, SweepInterval: time.Hour, TranscodeDir: f.dir,
	}, zerolog.Nop())
	m.now = f.clock.now
	m.AddListener(f.ended)
	if err := m.Start(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) id() string {
	id, err := database.NewID()
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Write.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) newItem(lib, title string, dur *int64) (string, string) {
	item, path, file := f.id(), f.id(), f.id()
	f.exec("INSERT INTO media_items (id, library_id, type, title, duration_ms) VALUES (?, ?, 'movie', ?, ?)", item, lib, title, dur)
	f.exec("INSERT INTO library_paths (id, library_id, path) VALUES (?, ?, ?)", path, lib, path)
	f.exec("INSERT INTO media_files (id, library_path_id, relative_path, duration_ms, probe_status) VALUES (?, ?, 'a.mkv', ?, 'done')", file, path, dur)
	f.exec("INSERT INTO media_file_items VALUES (?, ?, 0)", file, item)
	return item, file
}

func (f *fixture) newUser(role media.Role) (authorization.Actor, string) {
	user, device := f.id(), f.id()
	f.exec("INSERT INTO users (id, username, password_hash, role) VALUES (?, ?, 'x', ?)", user, user, string(role))
	f.exec("INSERT INTO devices (id, user_id, device_name, platform) VALUES (?, ?, 'tv', 'android')", device, user)
	return authorization.Actor{UserID: user, DeviceID: device, Role: role}, device
}

func (f *fixture) create() *Session {
	f.t.Helper()
	s, err := f.m.Create(f.ctx, CreateParams{UserID: f.user.UserID, DeviceID: f.device, ItemID: f.item, FileID: f.file, Mode: "direct_play"})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) beat(id string, st Status, pos int64) (*Session, error) {
	return f.m.Heartbeat(f.ctx, f.user, id, Heartbeat{State: st, PositionMS: pos})
}

func (f *fixture) row(id string) sqlc.PlaybackSession {
	f.t.Helper()
	r, err := f.db.R.GetSession(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) progress() *sqlc.PlaybackProgress {
	p, err := f.db.R.GetProgress(f.ctx, sqlc.GetProgressParams{UserID: f.user.UserID, MediaItemID: f.item})
	if err != nil {
		return nil
	}
	return &p
}

func (f *fixture) history() []sqlc.ListHistoryRow {
	rows, err := f.db.R.ListHistory(f.ctx, sqlc.ListHistoryParams{UserID: f.user.UserID, BeforeTime: "9999", PageSize: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := core.CodeOf(err); got != code {
		t.Fatalf("código = %q (%v), se esperaba %q", got, err, code)
	}
}

var all = []Status{Created, Initializing, Playing, Paused, Stopping, Completed, Stopped, Failed, Expired}

// Toda transición fuera de la tabla se rechaza, y los estados terminales no
// tienen salida.
func TestTransiciones(t *testing.T) {
	allowed := map[[2]Status]bool{}
	for _, p := range [][2]Status{
		{Created, Initializing}, {Created, Stopping}, {Created, Failed}, {Created, Expired},
		{Initializing, Playing}, {Initializing, Paused}, {Initializing, Stopping}, {Initializing, Failed}, {Initializing, Expired},
		{Playing, Paused}, {Playing, Stopping}, {Playing, Failed}, {Playing, Expired},
		{Paused, Playing}, {Paused, Stopping}, {Paused, Failed}, {Paused, Expired},
		{Stopping, Completed}, {Stopping, Stopped}, {Stopping, Failed}, {Stopping, Expired},
	} {
		allowed[p] = true
	}
	for _, from := range all {
		for _, to := range all {
			if got := CanTransition(from, to); got != allowed[[2]Status{from, to}] {
				t.Errorf("%s → %s = %v", from, to, got)
			}
		}
		terminal := from == Completed || from == Stopped || from == Failed || from == Expired
		if from.Terminal() != terminal {
			t.Errorf("%s.Terminal() = %v", from, from.Terminal())
		}
	}
}

func TestCicloCompleto(t *testing.T) {
	f := newFixture(t)
	sub := f.bus.Subscribe()
	s := f.create()
	if s.Status != Initializing || f.row(s.ID).Status != "initializing" || s.DurationMS == nil || *s.DurationMS != duration {
		t.Fatalf("%+v", s)
	}
	// Un navegador sin reproducción automática empieza en pausa.
	if _, err := f.beat(s.ID, Paused, 0); err != nil {
		t.Fatal(err)
	}
	f.clock.add(5 * time.Second)
	if _, err := f.beat(s.ID, Playing, 10_000); err != nil {
		t.Fatal(err)
	}
	if f.row(s.ID).Status != "playing" {
		t.Fatal("el cambio de estado se guarda al momento")
	}
	if f.progress() != nil && f.progress().PositionMs == 10_000 {
		t.Fatal("la posición en reproducción no se escribe en cada heartbeat")
	}
	f.clock.add(5 * time.Second)
	if _, err := f.beat(s.ID, Paused, 20_000); err != nil {
		t.Fatal(err)
	}
	if p := f.progress(); p == nil || p.PositionMs != 20_000 {
		t.Fatalf("pausar vuelca al momento: %+v", p)
	}
	// Seguir en el mismo estado no es una transición.
	if _, err := f.beat(s.ID, Paused, 21_000); err != nil {
		t.Fatal(err)
	}
	// failed, stopping y expired están en la tabla desde paused, pero no los
	// puede pedir el cliente: dejarían la sesión a medio terminar.
	for _, bad := range []Status{Stopped, Completed, "x", Initializing, Failed, Stopping, Expired} {
		_, err := f.beat(s.ID, bad, 0)
		wantCode(t, err, core.CodeValidation)
	}
	if _, err := f.beat(s.ID, Playing, -1); core.CodeOf(err) != core.CodeValidation {
		t.Fatalf("posición negativa: %v", err)
	}
	if err := f.m.StopSession(f.ctx, f.user, s.ID); err != nil {
		t.Fatal(err)
	}
	r := f.row(s.ID)
	if r.Status != "stopped" || r.EndedAt == nil || r.PositionMs != 21_000 {
		t.Fatalf("%+v", r)
	}
	if h := f.history(); len(h) != 1 || h[0].Completed != 0 || h[0].Title != "Película" {
		t.Fatalf("una entrada de historial al cerrar: %+v", h)
	}
	if err := f.m.StopSession(f.ctx, f.user, s.ID); err != nil {
		t.Fatalf("cerrar otra vez no es un error: %v", err)
	}
	if len(f.history()) != 1 {
		t.Fatal("el historial se escribe una sola vez")
	}
	if ended := f.ended.list(); len(ended) != 1 || ended[0].Status != Stopped {
		t.Fatalf("%+v", ended)
	}
	names := map[string]bool{}
	for len(sub.C) > 0 {
		e := <-sub.C
		names[e.Name] = true
		if e.UserID != f.user.UserID {
			t.Fatalf("evento sin dueño: %+v", e)
		}
	}
	for _, n := range []string{events.SessionCreated, events.SessionUpdated, events.SessionEnded, events.PlaybackProgress} {
		if !names[n] {
			t.Fatalf("falta %s: %v", n, names)
		}
	}
}

func TestCompletadaAlSuperarElUmbral(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	if _, err := f.beat(s.ID, Playing, 95_000); err != nil {
		t.Fatal(err)
	}
	if err := f.m.StopSession(f.ctx, f.user, s.ID); err != nil {
		t.Fatal(err)
	}
	if f.row(s.ID).Status != "completed" || f.progress().Completed != 1 || f.history()[0].Completed != 1 {
		t.Fatalf("%+v %+v", f.row(s.ID), f.progress())
	}
}

func TestPosicionLimitadaALaDuracion(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	got, err := f.beat(s.ID, Playing, 10*duration)
	if err != nil || got.PositionMS != duration {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestSesionAjena(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	_, err := f.m.Heartbeat(f.ctx, f.another, s.ID, Heartbeat{State: Playing, PositionMS: 1})
	wantCode(t, err, core.CodeNotFound)
	_, err = f.m.Get(f.ctx, f.another, s.ID)
	wantCode(t, err, core.CodeNotFound)
	wantCode(t, f.m.StopSession(f.ctx, f.another, s.ID), core.CodeNotFound)
	_, err = f.m.Get(f.ctx, f.user, "no-existe")
	wantCode(t, err, core.CodeNotFound)
	// Terminada, sigue sin ser suya.
	if err := f.m.StopSession(f.ctx, f.user, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.m.Heartbeat(f.ctx, f.another, s.ID, Heartbeat{State: Playing, PositionMS: 1})
	wantCode(t, err, core.CodeNotFound)
}

// Una sesión expirada no actualiza progreso, y el cliente recibe
// SESSION_EXPIRED con la posición desde la que retomar.
func TestExpiradaNoActualizaProgreso(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	if _, err := f.beat(s.ID, Playing, 30_000); err != nil {
		t.Fatal(err)
	}
	f.clock.add(89 * time.Second)
	f.m.sweep(f.ctx)
	if f.m.Active() != 1 {
		t.Fatal("aún no ha pasado el tiempo de inactividad")
	}
	f.clock.add(2 * time.Second)
	f.m.sweep(f.ctx)
	if f.m.Active() != 0 || f.row(s.ID).Status != "expired" {
		t.Fatalf("%+v", f.row(s.ID))
	}
	_, err := f.beat(s.ID, Playing, 60_000)
	wantCode(t, err, core.CodeSessionExpired)
	if p := f.progress(); p.PositionMs != 30_000 {
		t.Fatalf("%+v", p)
	}
	got, err := f.m.Get(f.ctx, f.user, s.ID)
	if err != nil || got.Status != Expired || got.PositionMS != 30_000 || got.EndedAt == nil {
		t.Fatalf("retomar desde la última posición: %v %+v", err, got)
	}
}

func TestTouchMantieneViva(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	for range 5 {
		f.clock.add(60 * time.Second)
		f.m.Touch(s.ID)
		f.m.sweep(f.ctx)
	}
	if f.m.Active() != 1 {
		t.Fatal("la actividad de streaming cuenta como actividad")
	}
	f.m.Touch("no-existe") // No debe fallar.
}

// Un evento atrasado no sobrescribe el progreso reciente: el volcado usa la
// hora del heartbeat, no la del volcado.
func TestEventoAtrasadoNoSobrescribe(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	if _, err := f.beat(s.ID, Playing, 40_000); err != nil {
		t.Fatal(err)
	}
	// Otro dispositivo guarda después una posición más reciente.
	p := NewProgress(f.db, authorization.NewAuthorizer(f.db), f.bus, 0.9)
	p.now = func() time.Time { return f.clock.now().Add(10 * time.Second) }
	if _, err := p.Set(f.ctx, f.user, SetParams{ItemID: f.item, PositionMS: 70_000}); err != nil {
		t.Fatal(err)
	}
	f.clock.add(time.Minute)
	f.m.flush(f.ctx)
	if got := f.progress(); got.PositionMs != 70_000 {
		t.Fatalf("el volcado atrasado pisó el progreso reciente: %+v", got)
	}
}

func TestSinHeartbeatNoEscribeProgreso(t *testing.T) {
	f := newFixture(t)
	if _, err := NewProgress(f.db, authorization.NewAuthorizer(f.db), f.bus, 0.9).Set(f.ctx, f.user,
		SetParams{ItemID: f.item, PositionMS: 50_000}); err != nil {
		t.Fatal(err)
	}
	s := f.create() // Abierta desde el principio y cerrada sin reproducir.
	if err := f.m.StopSession(f.ctx, f.user, s.ID); err != nil {
		t.Fatal(err)
	}
	if f.progress().PositionMs != 50_000 || len(f.history()) != 0 {
		t.Fatalf("%+v %+v", f.progress(), f.history())
	}
}

// Tras un kill -9, Reconcile deja cero sesiones activas, cierra los trabajos
// activos y vacía transcodes.
func TestReconciliarTrasCaida(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	if _, err := f.beat(s.ID, Paused, 25_000); err != nil {
		t.Fatal(err)
	}
	other := f.create()
	jobID := f.id()
	f.exec(`INSERT INTO transcode_jobs (id, playback_session_id, job_kind, status, input_file_id, output_profile)
		VALUES (?, ?, 'video', 'running', ?, 'p')`, jobID, s.ID, f.file)
	if err := os.MkdirAll(filepath.Join(f.dir, s.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, s.ID, "seg1.m4s"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Caída: el gestor desaparece sin Stop. Uno nuevo arranca sobre la misma base.
	f.m = f.newManager()
	open, err := f.db.R.ListOpenSessions(f.ctx)
	if err != nil || len(open) != 0 || f.m.Active() != 0 {
		t.Fatalf("%v %d abiertas", err, len(open))
	}
	if f.row(s.ID).Status != "expired" || f.row(other.ID).Status != "expired" {
		t.Fatal("las sesiones abiertas pasan a expired")
	}
	var status, code string
	if err := f.db.Read.QueryRow("SELECT status, error_code FROM transcode_jobs WHERE id = ?", jobID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != "SERVER_RESTART" {
		t.Fatalf("%s %s", status, code)
	}
	if entries, _ := os.ReadDir(f.dir); len(entries) != 0 {
		t.Fatalf("transcodes no se vació: %v", entries)
	}
	// La pausa volcó la posición: el historial la recoge.
	if h := f.history(); len(h) != 1 || h[0].PositionMs != 25_000 {
		t.Fatalf("%+v", h)
	}
}

// El apagado controlado vuelca el progreso que solo estaba en memoria.
func TestApagadoVuelcaElProgreso(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	if _, err := f.beat(s.ID, Playing, 45_000); err != nil {
		t.Fatal(err)
	}
	if f.progress() != nil {
		t.Fatal("aún no debería estar en disco")
	}
	if err := f.m.Stop(f.ctx); err != nil {
		t.Fatal(err)
	}
	if p := f.progress(); p == nil || p.PositionMs != 45_000 || f.row(s.ID).Status != "expired" {
		t.Fatalf("%+v %+v", p, f.row(s.ID))
	}
}

func TestVolcadoPeriodico(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	if _, err := f.beat(s.ID, Playing, 12_000); err != nil {
		t.Fatal(err)
	}
	f.m.flush(f.ctx)
	if p := f.progress(); p == nil || p.PositionMs != 12_000 || f.row(s.ID).PositionMs != 12_000 {
		t.Fatalf("%+v", p)
	}
}

// Un escaneo que purga el elemento borra la fila en cascada: la sesión
// termina en el siguiente volcado sin intentar escribir.
func TestElementoBorradoDuranteLaSesion(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	if _, err := f.beat(s.ID, Playing, 1000); err != nil {
		t.Fatal(err)
	}
	f.exec("DELETE FROM media_items WHERE id = ?", f.item)
	f.m.flush(f.ctx)
	if f.m.Active() != 0 {
		t.Fatal("la sesión debía terminar")
	}
	if ended := f.ended.list(); len(ended) != 1 || ended[0].Status != Failed {
		t.Fatalf("%+v", ended)
	}
}

func TestTerminarPorBibliotecaUsuarioYDispositivo(t *testing.T) {
	f := newFixture(t)
	otherLib := f.id()
	f.exec("INSERT INTO libraries (id, name, type) VALUES (?, 'otra', 'movies')", otherLib)
	otherItem, otherFile := f.newItem(otherLib, "Otra", nil)
	a := f.create()
	b, err := f.m.Create(f.ctx, CreateParams{UserID: f.user.UserID, DeviceID: f.device, ItemID: otherItem, FileID: otherFile})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.TerminateLibrary(f.ctx, f.lib); err != nil {
		t.Fatal(err)
	}
	if f.row(a.ID).EndedAt == nil || f.row(b.ID).EndedAt != nil {
		t.Fatal("solo se cierran las de la biblioteca")
	}
	if err := f.m.TerminateDevice(f.ctx, "otro"); err != nil || f.m.Active() != 1 {
		t.Fatal("otro dispositivo no afecta")
	}
	if err := f.m.TerminateUser(f.ctx, f.user.UserID); err != nil || f.m.Active() != 0 {
		t.Fatal("cierra las del usuario")
	}
}

func TestCrearValidaArchivoYElemento(t *testing.T) {
	f := newFixture(t)
	otherItem, _ := f.newItem(f.lib, "Otra", nil)
	_, err := f.m.Create(f.ctx, CreateParams{UserID: f.user.UserID, DeviceID: f.device, ItemID: otherItem, FileID: f.file})
	wantCode(t, err, core.CodeNotFound) // El archivo no es de ese elemento.
	_, err = f.m.Create(f.ctx, CreateParams{UserID: f.user.UserID, DeviceID: f.device, ItemID: f.item, FileID: f.file, StartPositionMS: -5})
	wantCode(t, err, core.CodeValidation)
	if _, err := f.m.Create(f.ctx, CreateParams{ItemID: f.item, FileID: f.file}); err == nil {
		t.Fatal("una sesión de aplicación necesita usuario y dispositivo")
	}
	s, err := f.m.Create(f.ctx, CreateParams{ClientKind: KindDLNA, ItemID: f.item, FileID: f.file})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.Terminate(f.ctx, func(Session) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if f.row(s.ID).EndedAt == nil || len(f.history()) != 0 {
		t.Fatal("DLNA no escribe historial")
	}
}

func TestTokenDeStreaming(t *testing.T) {
	f := newFixture(t)
	s := f.create()
	other := f.create()
	tok := f.m.StreamToken(s.ID)
	if tok == f.m.StreamToken(other.ID) || len(tok) < 40 {
		t.Fatalf("token %q", tok)
	}
	if got, err := f.m.VerifyToken(s.ID, tok); err != nil || got.ID != s.ID {
		t.Fatalf("%v %+v", err, got)
	}
	_, err := f.m.VerifyToken(s.ID, f.m.StreamToken(other.ID))
	wantCode(t, err, core.CodeNotFound)
	// Cambiar el último carácter por otro distinto; si ya era "A", poner "B".
	last := "A"
	if tok[len(tok)-1:] == last {
		last = "B"
	}
	_, err = f.m.VerifyToken(s.ID, tok[:len(tok)-1]+last)
	wantCode(t, err, core.CodeNotFound)
	// El secreto persiste: otro gestor sobre la misma base firma igual.
	m2 := New(f.db, f.bus, Options{}, zerolog.Nop())
	if err := m2.loadSecret(f.ctx); err != nil || m2.StreamToken(s.ID) != tok {
		t.Fatalf("%v", err)
	}
	if err := f.m.StopSession(f.ctx, f.user, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.m.VerifyToken(s.ID, tok)
	wantCode(t, err, core.CodeSessionExpired) // El token muere con la sesión.
}
