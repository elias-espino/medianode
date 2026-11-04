package sessions

import (
	"testing"
	"time"
)

func TestSnapshotsDevuelveLasActivasConSuIP(t *testing.T) {
	f := newFixture(t)
	if got := f.m.Snapshots(); len(got) != 0 {
		t.Fatalf("sin sesiones: %+v", got)
	}
	mk := func(ip string) *Session {
		t.Helper()
		s, err := f.m.Create(f.ctx, CreateParams{UserID: f.user.UserID, DeviceID: f.device, ItemID: f.item, FileID: f.file,
			Mode: "direct_play", ClientIP: ip})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := mk("192.168.1.9"), mk("8.8.8.8")
	got := f.m.Snapshots()
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("de la más antigua a la más reciente: %+v", got)
	}
	if got[0].ClientIP != "192.168.1.9" || got[1].ClientIP != "8.8.8.8" || got[0].Title == "" {
		t.Fatalf("%+v", got)
	}
	// Es una copia: tocarla no cambia la sesión.
	got[0].PositionMS = 99999
	if f.m.Snapshots()[0].PositionMS == 99999 {
		t.Fatal("Snapshots debe devolver copias")
	}
	if err := f.m.Terminate(f.ctx, func(s Session) bool { return s.ID == a.ID }); err != nil {
		t.Fatal(err)
	}
	if got := f.m.Snapshots(); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("una sesión terminada ya no figura: %+v", got)
	}
	// Una sesión restaurada de la base conserva la IP.
	var stored *string
	if err := f.db.Write.QueryRow("SELECT client_ip FROM playback_sessions WHERE id = ?", b.ID).Scan(&stored); err != nil || stored == nil || *stored != "8.8.8.8" {
		t.Fatalf("client_ip guardada = %v (%v)", stored, err)
	}
}

// Con muchas sesiones, el orden es siempre el de creación, aunque varias
// compartan el mismo instante.
func TestSnapshotsOrdenEstable(t *testing.T) {
	f := newFixture(t)
	var want []string
	for i := 0; i < 25; i++ {
		s, err := f.m.Create(f.ctx, CreateParams{UserID: f.user.UserID, DeviceID: f.device, ItemID: f.item, FileID: f.file, Mode: "direct_play"})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, s.ID)
	}
	for round := 0; round < 5; round++ {
		got := f.m.Snapshots()
		if len(got) != len(want) {
			t.Fatalf("%d sesiones", len(got))
		}
		for i, s := range got {
			if s.ID != want[i] {
				t.Fatalf("ronda %d: posición %d es %s, se esperaba %s", round, i, s.ID, want[i])
			}
		}
	}
}

// El orden principal es el instante de inicio; el identificador solo desempata.
func TestSnapshotsOrdenaPorInstanteDeInicio(t *testing.T) {
	f := newFixture(t)
	mk := func() string {
		t.Helper()
		s, err := f.m.Create(f.ctx, CreateParams{UserID: f.user.UserID, DeviceID: f.device, ItemID: f.item, FileID: f.file, Mode: "direct_play"})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	a := mk() // Mismo instante que b: manda el identificador.
	b := mk()
	f.clock.add(time.Minute)
	c := mk()
	f.clock.add(time.Minute)
	d := mk()
	got := f.m.Snapshots()
	if len(got) != 4 || got[0].ID != a || got[1].ID != b || got[2].ID != c || got[3].ID != d {
		var ids []string
		for _, s := range got {
			ids = append(ids, s.ID[len(s.ID)-4:])
		}
		t.Fatalf("orden %v, se esperaba %s %s %s %s", ids, a[len(a)-4:], b[len(b)-4:], c[len(c)-4:], d[len(d)-4:])
	}
}
