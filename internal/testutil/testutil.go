// Package testutil reúne utilidades compartidas por las pruebas.
package testutil

import (
	"context"
	"path/filepath"
	"testing"

	"medianode/internal/config"
	"medianode/internal/database"
)

// NewDB abre una base temporal con todas las migraciones aplicadas.
func NewDB(t testing.TB) *database.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(context.Background(), config.Database{
		Path: filepath.Join(dir, "t.db"), JournalMode: "WAL", BusyTimeoutMS: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background(), filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	return db
}
