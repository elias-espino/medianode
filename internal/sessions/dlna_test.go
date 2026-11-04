package sessions

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func (f *fixture) createDLNA(ip string) *Session {
	f.t.Helper()
	s, err := f.m.Create(f.ctx, CreateParams{ClientKind: KindDLNA, ItemID: f.item, FileID: f.file, Mode: "direct_play", ClientIP: ip})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func TestUnaSesionDLNANoNecesitaUsuarioNiDispositivo(t *testing.T) {
	f := newFixture(t)
	s := f.createDLNA("192.168.1.50")
	if s.ClientKind != KindDLNA || s.UserID != "" || s.DeviceID != "" || s.Status != Initializing || s.ClientIP != "192.168.1.50" || s.Title == "" {
		t.Fatalf("%+v", s)
	}
	row := f.row(s.ID)
	if row.ClientKind != "dlna" || row.UserID != nil || row.DeviceID != nil {
		t.Fatalf("fila: %+v", row)
	}
	// Una sesión de aplicación, en cambio, sigue exigiéndolos.
	if _, err := f.m.Create(f.ctx, CreateParams{ItemID: f.item, FileID: f.file}); err == nil {
		t.Fatal("una sesión de app sin usuario debe fallar")
	}
}

func TestActivarPasaALaSesionAPlaying(t *testing.T) {
	f := newFixture(t)
	s := f.createDLNA("192.168.1.50")
	if err := f.m.Activate(f.ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.row(s.ID).Status; got != "playing" {
		t.Fatalf("estado guardado = %s", got)
	}
	if snap := f.m.Snapshots(); len(snap) != 1 || snap[0].Status != Playing {
		t.Fatalf("%+v", snap)
	}
	// Idempotente, y no pisa un estado posterior.
	if err := f.m.Activate(f.ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	// Una sesión que no existe no es un error: terminó entre medias.
	if err := f.m.Activate(f.ctx, "no-existe"); err != nil {
		t.Fatal(err)
	}
	// No genera progreso ni historial: no hay a quién.
	if err := f.m.Terminate(f.ctx, func(x Session) bool { return x.ID == s.ID }); err != nil {
		t.Fatal(err)
	}
	if f.progress() != nil || len(f.history()) != 0 {
		t.Fatalf("progreso=%v historial=%v", f.progress(), f.history())
	}
}

func TestAliveYTouch(t *testing.T) {
	f := newFixture(t)
	s := f.createDLNA("1.1.1.1")
	if !f.m.Alive(s.ID) || f.m.Alive("otra") {
		t.Fatal("Alive")
	}
	f.clock.add(60 * time.Second)
	f.m.Touch(s.ID)
	if got := f.m.Snapshots()[0].LastActivity; !got.Equal(f.clock.now()) {
		t.Fatalf("Touch no renovó la actividad: %v", got)
	}
	if err := f.m.Terminate(f.ctx, func(x Session) bool { return x.ID == s.ID }); err != nil {
		t.Fatal(err)
	}
	if f.m.Alive(s.ID) {
		t.Fatal("una sesión terminada no está viva")
	}
}

// Una sesión de DLNA caduca por su propio plazo, no por el de los heartbeats:
// un televisor no los envía y puede estar un rato sin pedir nada.
func TestLaSesionDLNACaducaPorSuPropioPlazo(t *testing.T) {
	f := newFixture(t)
	m := New(f.db, f.bus, Options{
		IdleTimeout: 90 * time.Second, DLNAIdleTimeout: 300 * time.Second, FlushInterval: time.Hour, SweepInterval: time.Hour, TranscodeDir: f.dir,
	}, zerolog.Nop())
	m.now = f.clock.now
	m.AddListener(f.ended)
	if err := m.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.m = m
	d := f.createDLNA("192.168.1.50")
	app := f.create()

	f.clock.add(91 * time.Second)
	m.sweep(f.ctx)
	if m.Alive(app.ID) {
		t.Fatal("la sesión de la app debía caducar a los 90 s")
	}
	if !m.Alive(d.ID) {
		t.Fatal("la de DLNA no debe caducar a los 90 s")
	}
	f.clock.add(208 * time.Second) // 299 s en total.
	m.sweep(f.ctx)
	if !m.Alive(d.ID) {
		t.Fatal("la de DLNA sigue viva a los 299 s")
	}
	m.Touch(d.ID) // Una petición HTTP renueva el plazo.
	f.clock.add(299 * time.Second)
	m.sweep(f.ctx)
	if !m.Alive(d.ID) {
		t.Fatal("tras renovar, vuelve a tener 300 s")
	}
	f.clock.add(2 * time.Second)
	m.sweep(f.ctx)
	if m.Alive(d.ID) {
		t.Fatal("pasados 300 s sin actividad debe caducar")
	}
	if got := f.row(d.ID).Status; got != "expired" {
		t.Fatalf("estado = %s", got)
	}
	var ended int
	for _, s := range f.ended.list() {
		if s.ID == d.ID {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("el oyente de fin de sesión recibió %d avisos", ended)
	}
}

func TestPorDefectoElPlazoDLNAEsDeDosMinutos(t *testing.T) {
	o := Options{}
	o.defaults()
	if o.DLNAIdleTimeout != 120*time.Second {
		t.Fatalf("%v", o.DLNAIdleTimeout)
	}
	o = Options{IdleTimeout: 30 * time.Second, DLNAIdleTimeout: 9 * time.Second}
	o.defaults()
	if o.SweepInterval != 3*time.Second {
		t.Fatalf("el barrido debe seguir al plazo más corto: %v", o.SweepInterval)
	}
}
