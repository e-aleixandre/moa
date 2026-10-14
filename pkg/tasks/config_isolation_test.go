package tasks_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/release"
	"github.com/e-aleixandre/moa/pkg/serve"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

const incidentHelperEnv = "MOA_TEST_TASKS_INCIDENT_HELPER"

// TestIncidentHelperProcess is the body of the subprocess used below, not a
// test: it builds a Manager exactly like the serve version test does and
// reports which tasks database the process selected.
func TestIncidentHelperProcess(t *testing.T) {
	if os.Getenv(incidentHelperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := serve.ManagerConfig{ReleaseInfo: release.Info{Version: "0.8.1", Commit: "abc", Date: "today"}}
	repo := tasks.Shared()
	if dir := os.Getenv(incidentHelperEnv + "_BASEDIR"); dir != "" {
		cfg.SessionBaseDir = filepath.Join(dir, "sessions")
		repo = tasks.New(filepath.Join(dir, tasks.DatabaseName))
	}
	mgr := serve.NewManager(ctx, cfg)
	defer func() {
		cancel()
		mgr.Shutdown()
		_ = repo.Close()
	}()
	// Force the lazy open so the outcome does not depend on goroutine timing.
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	if _, err := repo.Revision(rctx); err != nil {
		t.Fatalf("revision: %v", err)
	}
	_, _ = os.Stdout.WriteString("TASKS_PATH=" + repo.Path() + "\n")
}

func dirListing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func hasCompatTable(t *testing.T, path string, immutable bool) bool {
	t.Helper()
	dsn := "file:" + path + "?mode=ro"
	if immutable {
		// Opening a WAL database read-only would itself create -wal/-shm.
		dsn += "&immutable=1"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'tasks_compat'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// runHelper starts this test binary with a controlled environment; the outer
// process's own config roots are never passed on.
func runHelper(t *testing.T, env ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestIncidentHelperProcess$", "-test.count=1")
	cmd.Env = append([]string{incidentHelperEnv + "=1", "TMPDIR=" + t.TempDir()}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errb.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if p, ok := strings.CutPrefix(line, "TASKS_PATH="); ok {
			return p
		}
	}
	t.Fatalf("helper reported no tasks path:\n%s", out.String())
	return ""
}

type seeded struct {
	db       string
	bytes    []byte
	listing  []string
	rootsDir string
}

func seed(t *testing.T, dir string) seeded {
	t.Helper()
	db := filepath.Join(dir, tasks.DatabaseName)
	tasks.SeedEmptyV4(t, db)
	b, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	return seeded{db: db, bytes: b, listing: dirListing(t, dir), rootsDir: dir}
}

func (s seeded) assertUntouched(t *testing.T) {
	t.Helper()
	got, err := os.ReadFile(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, s.bytes) {
		t.Errorf("%s changed", s.db)
	}
	if hasCompatTable(t, s.db, true) {
		t.Errorf("%s was migrated", s.db)
	}
	if l := dirListing(t, s.rootsDir); !reflect.DeepEqual(l, s.listing) {
		t.Errorf("%s listing = %v, want %v (no .bak/-wal/-shm)", s.rootsDir, l, s.listing)
	}
}

func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// A test binary started with a production-looking HOME or MOA_CONFIG_DIR must
// not migrate, back up or even open the database found there.
func TestInheritedConfigIsNeverTouchedByTestBinaries(t *testing.T) {
	cases := []struct {
		name     string
		override bool
	}{{"HOME only", false}, {"MOA_CONFIG_DIR override", true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			homeProd := seed(t, filepath.Join(home, ".config", "moa"))
			env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
			protected := []seeded{homeProd}
			if tc.override {
				prod := seed(t, filepath.Join(t.TempDir(), "override"))
				env = append(env, "MOA_CONFIG_DIR="+prod.rootsDir)
				protected = append(protected, prod)
			}
			selected := runHelper(t, env...)
			for _, p := range protected {
				p.assertUntouched(t)
				if inside(p.rootsDir, selected) {
					t.Errorf("selected tasks path %s is inside protected %s", selected, p.rootsDir)
				}
			}
			if selected == "" {
				t.Error("empty tasks path")
			}
		})
	}
}

// Positive control: with an explicit SessionBaseDir the same constructor does
// migrate the database beside it, so the untouched results above are not vacuous.
func TestExplicitSessionBaseDirMigratesItsOwnDatabase(t *testing.T) {
	home := t.TempDir()
	prod := seed(t, filepath.Join(home, ".config", "moa"))
	dir := t.TempDir()
	own := seed(t, dir)
	selected := runHelper(t, "HOME="+home, incidentHelperEnv+"_BASEDIR="+dir)
	if selected != own.db {
		t.Fatalf("selected %s, want %s", selected, own.db)
	}
	if !hasCompatTable(t, own.db, false) {
		t.Error("explicit SessionBaseDir database was not migrated")
	}
	prod.assertUntouched(t)
}
