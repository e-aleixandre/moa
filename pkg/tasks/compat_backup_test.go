package tasks

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func v4DB(t *testing.T, base string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), base)
	buildV4(t, p)
	return p
}

// An interrupted or foreign file at the backup name is not a backup: the
// migration must not rely on it.
func TestCompatBackupIncompleteIsRejected(t *testing.T) {
	for _, kind := range []string{"partial", "directory"} {
		t.Run(kind, func(t *testing.T) {
			p := v4DB(t, DatabaseName)
			bak := p + ".pre-compat1.bak"
			if kind == "partial" {
				if err := os.WriteFile(bak, []byte("SQLite format 3\x00interrupted partial backup"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(bak, 0o700); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, p)
			r := New(p)
			defer func() { _ = r.Close() }()
			err := r.write(bg, func(*sql.Tx) (bool, error) { return false, nil })
			if err == nil || !strings.Contains(err.Error(), "backup before compat") {
				t.Fatalf("write with an invalid backup in place: %v", err)
			}
			if snapshot(t, p) != before {
				t.Fatal("rows changed")
			}
			if n := pragmaInt(t, p, `SELECT count(*) FROM sqlite_master WHERE name = 'tasks_compat'`); n != 0 {
				t.Fatal("compat applied without a valid backup")
			}
			if kind == "partial" {
				if b, _ := os.ReadFile(bak); !strings.Contains(string(b), "interrupted") {
					t.Fatal("the unvalidated file was overwritten")
				}
			}
		})
	}
}

// An empty file at the backup name is a backup that never finished.
func TestCompatEmptyBackupIsRecoverable(t *testing.T) {
	p := v4DB(t, DatabaseName)
	bak := p + ".pre-compat1.bak"
	if err := os.WriteFile(bak, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, p)
	r := New(p)
	defer func() { _ = r.Close() }()
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	if snapshot(t, bak) != before {
		t.Fatal("recovered backup is not the pre-migration database")
	}
	if compatVersion(t, p) != len(compatMigrations) {
		t.Fatal("compat not applied after recovering the backup")
	}
}

// The last commit of an older writer may still sit in the WAL: the backup
// must contain it.
func TestCompatBackupIncludesCommittedWAL(t *testing.T) {
	p := v4DB(t, DatabaseName)
	db, err := sql.Open("sqlite", dsn(p, true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0;UPDATE tasks SET description='last old writer commit in WAL' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("setup: no WAL: %v", err)
	}
	before := snapshot(t, p)
	r := New(p)
	defer func() { _ = r.Close() }()
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	if snapshot(t, p+".pre-compat1.bak") != before {
		t.Fatal("backup did not include the committed WAL")
	}
}

// A valid backup left by an earlier attempt is accepted and never rewritten
// (TestCompatFailureRollsBack in compat_migration_test.go covers the retry).
func TestCompatValidExistingBackupIsKept(t *testing.T) {
	p := v4DB(t, DatabaseName)
	r0 := New(p)
	if _, err := r0.Revision(bg); err != nil {
		t.Fatal(err)
	}
	_ = r0.Close()
	bak := p + ".pre-compat1.bak"
	st, err := os.Stat(bak)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup: %v %v", st, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary backup left behind: %s", e.Name())
		}
	}
}
