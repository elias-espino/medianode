package sessions

import "testing"

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
