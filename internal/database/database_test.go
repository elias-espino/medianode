package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"medianode/internal/config"
	"medianode/internal/database/sqlc"
)

func testConfig(path string) config.Database {
	return config.Database{Path: path, JournalMode: "WAL", BusyTimeoutMS: 5000}
}

// testContext evita que un bloqueo cuelgue la suite completa.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(testContext(t), testConfig(filepath.Join(t.TempDir(), "t.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func migratedTest(t *testing.T) *DB {
	t.Helper()
	db := openTest(t)
	if err := db.Migrate(testContext(t), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOpenAplicaLosPragmas(t *testing.T) {
	db := openTest(t)
	ctx := testContext(t)

	var journal, sync int
	var mode string
	if err := db.Write.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := db.Write.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := db.Write.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || journal != 5000 || sync != 1 {
		t.Fatalf("journal=%s busy=%d synchronous=%d", mode, journal, sync)
	}
}

func TestPoolEscritorTieneUnaSolaConexion(t *testing.T) {
	db := openTest(t)
	if got := db.Write.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("conexiones de escritura = %d", got)
	}
	if got := db.Read.Stats().MaxOpenConnections; got < minReadConns {
		t.Fatalf("conexiones de lectura = %d", got)
	}
}

func TestOpenRutaConCaracteresEspeciales(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "datos con espacios #1 100% ñandú")
	path := filepath.Join(dir, "base?.db")
	if strings.ContainsRune(path, '?') && os.PathSeparator == '\\' {
		path = filepath.Join(dir, "base.db") // Windows no admite ? en nombres de archivo.
	}

	db, err := Open(testContext(t), testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(testContext(t), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("la base no quedó en la ruta pedida: %v", err)
	}
}

func TestClavesForaneasEnTodasLasConexiones(t *testing.T) {
	db := openTest(t)
	ctx := testContext(t)

	var conns []*sql.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for range minReadConns {
		conn, err := db.Read.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)

		var fk, queryOnly int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || queryOnly != 1 {
			t.Fatalf("foreign_keys=%d query_only=%d", fk, queryOnly)
		}
	}

	mustExec(t, db, "CREATE TABLE p(id INTEGER PRIMARY KEY)")
	mustExec(t, db, "CREATE TABLE c(id INTEGER, p INTEGER NOT NULL REFERENCES p(id))")
	if _, err := db.Write.ExecContext(ctx, "INSERT INTO c VALUES (1, 99)"); err == nil {
		t.Fatal("se esperaba una violación de clave foránea")
	}
}

func mustExec(t *testing.T, db *DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Write.ExecContext(testContext(t), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func TestLectorRechazaEscrituras(t *testing.T) {
	db := migratedTest(t)
	if err := db.R.SetSetting(testContext(t), sqlc.SetSettingParams{Key: "a", Value: "b"}); err == nil {
		t.Fatal("el pool lector no debe poder escribir")
	}
}

func TestSettings(t *testing.T) {
	db := migratedTest(t)
	ctx := testContext(t)

	for _, v := range []string{"uno", "dos"} {
		if err := db.Q.SetSetting(ctx, sqlc.SetSettingParams{Key: "clave", Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.R.GetSetting(ctx, "clave")
	if err != nil {
		t.Fatal(err)
	}
	if got != "dos" {
		t.Fatalf("valor = %q", got)
	}

	rows, err := db.R.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("filas = %d", len(rows))
	}
	if _, err := ParseTime(rows[0].UpdatedAt); err != nil {
		t.Fatalf("la fecha de SQLite no sigue el formato único: %v", err)
	}

	if err := db.Q.DeleteSetting(ctx, "clave"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.R.GetSetting(ctx, "clave"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error = %v", err)
	}
}

func TestEscritoresConcurrentesSinBusy(t *testing.T) {
	db := migratedTest(t)
	ctx := testContext(t)

	const writers, each = 50, 20
	errs := make(chan error, writers*each*2)
	var wg sync.WaitGroup
	for g := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				err := db.Q.SetSetting(ctx, sqlc.SetSettingParams{Key: fmt.Sprintf("d%d-%d", g, i), Value: "x"})
				if err != nil {
					errs <- err
				}
				err = db.InTx(ctx, func(q *sqlc.Queries) error {
					_, err := q.GetSetting(ctx, fmt.Sprintf("d%d-%d", g, i))
					if err != nil {
						return err
					}
					return q.SetSetting(ctx, sqlc.SetSettingParams{Key: fmt.Sprintf("t%d-%d", g, i), Value: "y"})
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	rows, err := db.R.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := writers * each * 2; len(rows) != want {
		t.Fatalf("filas = %d, se esperaban %d", len(rows), want)
	}
}

func TestLectorNoSeBloqueaDuranteEscrituraLarga(t *testing.T) {
	db := migratedTest(t)
	ctx := testContext(t)
	if err := db.Q.SetSetting(ctx, sqlc.SetSettingParams{Key: "k", Value: "antes"}); err != nil {
		t.Fatal(err)
	}

	inTx := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.InTx(ctx, func(q *sqlc.Queries) error {
			if err := q.SetSetting(ctx, sqlc.SetSettingParams{Key: "k", Value: "despues"}); err != nil {
				return err
			}
			close(inTx)
			<-release
			return nil
		})
	}()
	<-inTx

	start := time.Now()
	got, err := db.R.GetSetting(ctx, "k")
	elapsed := time.Since(start)
	close(release)

	if err != nil {
		t.Fatal(err)
	}
	if got != "antes" {
		t.Fatalf("lectura no aislada: %q", got)
	}
	if elapsed > time.Second {
		t.Fatalf("la lectura tardó %v", elapsed)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestInTxRevierteAnteError(t *testing.T) {
	db := migratedTest(t)
	ctx := testContext(t)
	boom := errors.New("boom")

	err := db.InTx(ctx, func(q *sqlc.Queries) error {
		if err := q.SetSetting(ctx, sqlc.SetSettingParams{Key: "k", Value: "v"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	if _, err := db.R.GetSetting(ctx, "k"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no se revirtió: %v", err)
	}
}

func TestInTxRevierteAntePanicYLiberaLaConexion(t *testing.T) {
	db := migratedTest(t)
	ctx := testContext(t)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("el panic debe propagarse")
			}
		}()
		_ = db.InTx(ctx, func(q *sqlc.Queries) error {
			_ = q.SetSetting(ctx, sqlc.SetSettingParams{Key: "k", Value: "v"})
			panic("fallo")
		})
	}()

	if _, err := db.R.GetSetting(ctx, "k"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no se revirtió: %v", err)
	}
	if err := db.Q.SetSetting(ctx, sqlc.SetSettingParams{Key: "x", Value: "y"}); err != nil {
		t.Fatalf("la conexión de escritura quedó ocupada: %v", err)
	}
}

func migration(up, down string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("-- +goose Up\n" + up + "\n-- +goose Down\n" + down + "\n")}
}

func backupsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

func TestMigrarBaseNuevaNoCreaBackup(t *testing.T) {
	db := openTest(t)
	dir := filepath.Join(t.TempDir(), "backups")
	if err := db.Migrate(testContext(t), dir); err != nil {
		t.Fatal(err)
	}
	if got := backupsIn(t, dir); len(got) != 0 {
		t.Fatalf("backups = %v", got)
	}
}

func TestMigrarDosVecesEsIdempotente(t *testing.T) {
	db := openTest(t)
	dir := filepath.Join(t.TempDir(), "backups")
	ctx := testContext(t)
	if err := db.Migrate(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if got := backupsIn(t, dir); len(got) != 0 {
		t.Fatalf("sin migraciones pendientes no debe haber backup: %v", got)
	}
}

func TestMigrarCreaBackupVerificable(t *testing.T) {
	db := openTest(t)
	ctx := testContext(t)
	dir := filepath.Join(t.TempDir(), "backups")

	v1 := fstest.MapFS{"00001_a.sql": migration("CREATE TABLE a(v TEXT);", "DROP TABLE a;")}
	if err := db.migrate(ctx, v1, dir); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO a VALUES ('dato')")

	v2 := fstest.MapFS{
		"00001_a.sql": v1["00001_a.sql"],
		"00002_b.sql": migration("CREATE TABLE b(v TEXT);", "DROP TABLE b;"),
	}
	if err := db.migrate(ctx, v2, dir); err != nil {
		t.Fatal(err)
	}

	backups := backupsIn(t, dir)
	if len(backups) != 1 || !strings.Contains(filepath.Base(backups[0]), "pre-migrate-v1") {
		t.Fatalf("backups = %v", backups)
	}
	if err := VerifyBackup(ctx, backups[0]); err != nil {
		t.Fatal(err)
	}

	copied, err := Open(ctx, testConfig(backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	var got string
	if err := copied.Read.QueryRowContext(ctx, "SELECT v FROM a").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "dato" {
		t.Fatalf("dato en la copia = %q", got)
	}
	if err := copied.Read.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE name = 'b'").Scan(&got); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("la copia no debe tener la migración nueva: %v", err)
	}
}

func TestMigracionFallidaRevierteYConservaBackup(t *testing.T) {
	db := openTest(t)
	ctx := testContext(t)
	dir := filepath.Join(t.TempDir(), "backups")

	v1 := fstest.MapFS{"00001_a.sql": migration("CREATE TABLE a(v TEXT);", "DROP TABLE a;")}
	if err := db.migrate(ctx, v1, dir); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO a VALUES ('dato')")

	broken := fstest.MapFS{
		"00001_a.sql": v1["00001_a.sql"],
		"00002_b.sql": migration("CREATE TABLE b(v TEXT);\nINSERT INTO no_existe VALUES (1);", "DROP TABLE b;"),
	}
	err := db.migrate(ctx, broken, dir)
	if err == nil {
		t.Fatal("se esperaba un error")
	}
	if !strings.Contains(err.Error(), "backup:") || !strings.Contains(err.Error(), "pre-migrate-v1") {
		t.Fatalf("el error debe indicar el backup: %v", err)
	}

	var name string
	if err := db.Read.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE name = 'b'").Scan(&name); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("la migración fallida dejó cambios: %v", err)
	}
	var got string
	if err := db.Read.QueryRowContext(ctx, "SELECT v FROM a").Scan(&got); err != nil || got != "dato" {
		t.Fatalf("se perdieron datos: %q %v", got, err)
	}
	backups := backupsIn(t, dir)
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if err := VerifyBackup(ctx, backups[0]); err != nil {
		t.Fatal(err)
	}

	fixed := fstest.MapFS{
		"00001_a.sql": v1["00001_a.sql"],
		"00002_b.sql": migration("CREATE TABLE b(v TEXT);", "DROP TABLE b;"),
	}
	if err := db.migrate(ctx, fixed, dir); err != nil {
		t.Fatalf("debe poder continuar tras corregir: %v", err)
	}
}

func TestBaseMasRecienteQueElServidor(t *testing.T) {
	db := openTest(t)
	ctx := testContext(t)
	dir := filepath.Join(t.TempDir(), "backups")

	v2 := fstest.MapFS{
		"00001_a.sql": migration("CREATE TABLE a(v TEXT);", "DROP TABLE a;"),
		"00002_b.sql": migration("CREATE TABLE b(v TEXT);", "DROP TABLE b;"),
	}
	if err := db.migrate(ctx, v2, dir); err != nil {
		t.Fatal(err)
	}

	v1 := fstest.MapFS{"00001_a.sql": v2["00001_a.sql"]}
	if err := db.migrate(ctx, v1, dir); !errors.Is(err, ErrNewerDatabase) {
		t.Fatalf("error = %v", err)
	}
}

func TestBackupDestinoExistenteFalla(t *testing.T) {
	db := migratedTest(t)
	ctx := testContext(t)
	dst := filepath.Join(t.TempDir(), "copia.db")
	if err := db.Backup(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if err := db.Backup(ctx, dst); err == nil {
		t.Fatal("no debe sobrescribir una copia")
	}
}

func TestVerifyBackupDetectaArchivoInvalido(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roto.db")
	if err := os.WriteFile(path, []byte("esto no es una base de datos, solo texto de relleno"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(testContext(t), path); err == nil {
		t.Fatal("se esperaba un error")
	}
}

func TestDatosSobrevivenAlReabrir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	ctx := testContext(t)

	db, err := Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := db.Q.SetSetting(ctx, sqlc.SetSettingParams{Key: "k", Value: "v"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, err := db.R.GetSetting(ctx, "k"); err != nil || got != "v" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"3.35.0", true}, {"3.54.1", true}, {"4.0.0", true},
		{"3.34.9", false}, {"2.99.0", false}, {"x", false}, {"3.y", false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.version, 3, 35); got != c.want {
			t.Errorf("%s = %v", c.version, got)
		}
	}
}
