package tasks

import (
	"errors"
	"testing"
	"time"
)

func newSessionDef(at time.Time) *ScheduleDef {
	return &ScheduleDef{When: When{Kind: WhenOnce, At: at.UnixMilli()}, TZ: "UTC",
		Target: Target{Kind: TargetNew, Project: "p", CWD: "/work/p", Model: "m"}}
}

// A reservation is recorded once and then kept: a second candidate gets the
// first ID, and no two runs may hold the same one.
func TestReserveSessionIsStableAndUnique(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	mkSchedule(t, r, "a", newSessionDef(clock.Now().Add(time.Minute)))
	mkSchedule(t, r, "b", newSessionDef(clock.Now().Add(time.Minute)))
	clock.Add(time.Minute)
	os := materialize(t, r)
	if len(os) != 2 {
		t.Fatalf("runs = %+v", os)
	}
	a, b := os[0], os[1]
	rev := mustOcc(t, r, a.ID).Revision
	got, err := r.ReserveSession(bg, a.ID, "aaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || got != "aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("reserve = %q, %v", got, err)
	}
	if again, err := r.ReserveSession(bg, a.ID, "bbbbbbbbbbbbbbbbbbbbbbbb"); err != nil || again != got {
		t.Fatalf("second reserve = %q, %v; want the first ID", again, err)
	}
	if o := mustOcc(t, r, a.ID); o.ReservedSessionID != got || o.Revision != rev || o.State != OccReady {
		t.Fatalf("run after reservation = %+v", o)
	}
	if _, err := r.ReserveSession(bg, b.ID, got); err == nil {
		t.Fatal("two runs reserved the same session ID")
	}
	// Kept whatever happens to the run.
	if _, err := r.FailOccurrence(bg, a.ID, "create_failed", ""); err != nil {
		t.Fatal(err)
	}
	if o := mustOcc(t, r, a.ID); o.ReservedSessionID != got {
		t.Fatalf("failed run lost its reservation: %+v", o)
	}
}

func TestReserveSessionOnlyForReadyNewRuns(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	mkSchedule(t, r, "existing", onceDef(clock.Now().Add(time.Minute), ""))
	clock.Add(time.Minute)
	o := materializeOne(t, r)
	_, err := r.ReserveSession(bg, o.ID, "aaaaaaaaaaaaaaaaaaaaaaaa")
	var conflict *OccurrenceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("reserve on an existing-session run = %v", err)
	}
}

// A v3 database (the released schema) upgrades in place: ordinary tasks are
// untouched, runs gain an empty reservation, discards hold session IDs only.
func TestMigrationFromV3AddsReservation(t *testing.T) {
	path := t.TempDir() + "/tasks.sqlite"
	db := applyV2(t, path)
	if _, err := db.Exec(migrations[2]); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"PRAGMA user_version = 3",
		`INSERT INTO tasks(id,title,status,place,requester_session_id,created_at,updated_at,revision) VALUES (1,'kept','pending','you','s1',11,11,3)`,
		`UPDATE tasks_meta SET revision = 7`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	r := openRepo(t, path)
	if got := mustGet(t, r, 1); got.Title != "kept" || got.Revision != 3 {
		t.Fatalf("task after upgrade = %+v", got)
	}
	raw := rawDB(t, path)
	var v, rev int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 4 || v != len(migrations) {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	if err := raw.QueryRow("SELECT revision FROM tasks_meta").Scan(&rev); err != nil || rev != 7 {
		t.Fatalf("revision = %d, %v", rev, err)
	}
	cols := func(table string) []string {
		rows, err := raw.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	if c := cols("session_discards"); len(c) != 2 || c[0] != "session_id" || c[1] != "created_at" {
		t.Fatalf("session_discards columns = %v", c)
	}
	if c := cols("task_occurrences"); c[len(c)-1] != "reserved_session_id" {
		t.Fatalf("task_occurrences columns = %v", c)
	}
}
