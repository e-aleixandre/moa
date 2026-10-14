package tasks

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// SeedEmptyV4 writes the empty database an older moa (four base migrations,
// no compat level) leaves behind, for the external config-isolation tests.
func SeedEmptyV4(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if tooNew, err := migrateWith(db, "", migrations, nil); err != nil || tooNew {
		t.Fatalf("v4 migrate: tooNew=%v err=%v", tooNew, err)
	}
}
