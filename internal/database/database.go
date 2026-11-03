// Package database gestiona la conexión a SQLite, las migraciones y los backups.
//
// Un pool escritor de una sola conexión aplica la regla de un escritor lógico de
// §6.3; las lecturas usan un pool propio y no se bloquean durante una escritura.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // Registra el driver sqlite.

	"medianode/internal/config"
	"medianode/internal/database/sqlc"
	"medianode/internal/storage"
	"medianode/migrations"
)

const (
	minReadConns = 4

	minSQLiteMajor = 3
	minSQLiteMinor = 35

	backupTimeFormat = "20060102T150405.000Z"
)

// ErrNetworkPath indica que la base está en un sistema de archivos de red.
var ErrNetworkPath = errors.New("la base de datos debe estar en un disco local")

// ErrNewerDatabase indica que la base viene de una versión posterior del
// servidor. Arrancar con ella podría dañar los datos.
var ErrNewerDatabase = errors.New("la base de datos es más reciente que este servidor")

// DB agrupa los dos pools. Q escribe y R lee.
type DB struct {
	Write *sql.DB
	Read  *sql.DB
	Q     *sqlc.Queries
	R     *sqlc.Queries
}

// Open abre la base y comprueba que la configuración efectiva sea la esperada.
// No aplica migraciones.
func Open(ctx context.Context, cfg config.Database) (*DB, error) {
	dir := filepath.Dir(cfg.Path)
	if err := storage.EnsureDirs(dir); err != nil {
		return nil, err
	}
	network, err := storage.IsNetworkPath(dir)
	if err != nil {
		return nil, fmt.Errorf("comprobar ubicación de la base: %w", err)
	}
	if network {
		return nil, fmt.Errorf("%w: %s", ErrNetworkPath, cfg.Path)
	}

	write, err := sql.Open("sqlite", writeDSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("abrir pool escritor: %w", err)
	}
	write.SetMaxOpenConns(1)

	read, err := sql.Open("sqlite", readDSN(cfg))
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("abrir pool lector: %w", err)
	}
	read.SetMaxOpenConns(max(runtime.NumCPU(), minReadConns))

	db := &DB{Write: write, Read: read, Q: sqlc.New(write), R: sqlc.New(read)}
	if err := db.verify(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func baseDSN(cfg config.Database) string {
	// La ruta forma parte de una URI: espacios, # o % deben ir escapados.
	path := (&url.URL{Path: filepath.ToSlash(cfg.Path)}).EscapedPath()
	return fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
		path, cfg.BusyTimeoutMS)
}

func writeDSN(cfg config.Database) string {
	// immediate evita que una transacción falle al pasar de lectura a escritura,
	// un caso que busy_timeout no cubre.
	return baseDSN(cfg) + "&_pragma=journal_mode(" + cfg.JournalMode + ")&_txlock=immediate"
}

func readDSN(cfg config.Database) string {
	return baseDSN(cfg) + "&_pragma=query_only(1)"
}

func (db *DB) verify(ctx context.Context) error {
	var version, journal string
	var foreignKeys int

	// El escritor va primero: crea el archivo y activa WAL antes que los lectores.
	if err := db.Write.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("comprobar journal_mode: %w", err)
	}
	if !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("journal_mode = %q, se esperaba wal", journal)
	}
	if err := db.Write.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("comprobar foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("foreign_keys está desactivado")
	}
	if err := db.Read.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
		return fmt.Errorf("comprobar versión de SQLite: %w", err)
	}
	if !versionAtLeast(version, minSQLiteMajor, minSQLiteMinor) {
		return fmt.Errorf("SQLite %s no es compatible; se necesita %d.%d o superior",
			version, minSQLiteMajor, minSQLiteMinor)
	}
	return nil
}

func versionAtLeast(version string, major, minor int) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	gotMajor, errMajor := strconv.Atoi(parts[0])
	gotMinor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

// InTx ejecuta fn en una transacción de escritura. Si fn devuelve un error o
// entra en panic, se revierte. Dentro de fn hay que usar solo q: db.Q esperaría
// a la única conexión de escritura, que la propia transacción tiene ocupada.
func (db *DB) InTx(ctx context.Context, fn func(q *sqlc.Queries) error) (err error) {
	tx, err := db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("iniciar transacción: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(db.Q.WithTx(tx)); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("revertir transacción: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("confirmar transacción: %w", err)
	}
	return nil
}

// Migrate aplica las migraciones pendientes, con un backup previo si la base
// ya contiene datos.
func (db *DB) Migrate(ctx context.Context, backupDir string) error {
	return db.migrate(ctx, migrations.FS, backupDir)
}

// migrate aplica cada migración en su propia transacción. Si una falla, esa se
// revierte, las anteriores se conservan, y el siguiente arranque sigue desde ahí.
func (db *DB) migrate(ctx context.Context, fsys fs.FS, backupDir string) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, db.Write, fsys)
	if err != nil {
		return fmt.Errorf("preparar migraciones: %w", err)
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("leer versión de la base: %w", err)
	}
	if current > target {
		return fmt.Errorf("%w: base v%d, servidor v%d", ErrNewerDatabase, current, target)
	}
	if current == target {
		return nil
	}

	backup := "no necesario, base nueva"
	if current > 0 {
		backup = filepath.Join(backupDir, fmt.Sprintf("pre-migrate-v%d-%s.db",
			current, time.Now().UTC().Format(backupTimeFormat)))
		if err := db.Backup(ctx, backup); err != nil {
			return fmt.Errorf("backup previo a migrar: %w", err)
		}
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrar de v%d a v%d (backup: %s): %w", current, target, backup, err)
	}
	return nil
}

// Backup crea en dst una copia consistente, también con WAL activo, y la abre
// para comprobar que es válida. dst no debe existir.
func (db *DB) Backup(ctx context.Context, dst string) error {
	if err := storage.EnsureDirs(filepath.Dir(dst)); err != nil {
		return err
	}
	if _, err := db.Write.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("crear copia: %w", err)
	}
	return VerifyBackup(ctx, dst)
}

// VerifyBackup abre una copia en solo lectura y comprueba su integridad.
func VerifyBackup(ctx context.Context, path string) error {
	escaped := (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()
	conn, err := sql.Open("sqlite", "file:"+escaped+"?mode=ro")
	if err != nil {
		return fmt.Errorf("abrir copia: %w", err)
	}
	defer conn.Close()

	var result string
	if err := conn.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("verificar copia: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("copia dañada: %s", result)
	}
	return nil
}

// Close cierra primero los lectores para que el escritor pueda consolidar el
// WAL al cerrar la última conexión.
func (db *DB) Close() error {
	return errors.Join(db.Read.Close(), db.Write.Close())
}

// Name implementa lifecycle.Component.
func (db *DB) Name() string { return "database" }

// Start implementa lifecycle.Component. La base ya está abierta y migrada.
func (db *DB) Start(context.Context) error { return nil }

// Stop implementa lifecycle.Component.
func (db *DB) Stop(context.Context) error { return db.Close() }
