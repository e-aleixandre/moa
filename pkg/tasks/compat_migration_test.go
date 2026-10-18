package tasks

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// buildV4 writes the database an older moa (four migrations, no compat level)
// leaves behind, with data in every table: what production holds today.
func buildV4(t *testing.T, path string) {
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
	stmts := []string{
		`INSERT INTO tasks(id,title,description,status,place,project_key,project_cwd,requester_session_id,assignee_session_id,
			completion_note,created_at,updated_at,completed_at,archived_at,revision) VALUES
			(1,'private note','','pending','you',NULL,NULL,NULL,NULL,'',1000,1000,NULL,NULL,1),
			(2,'need the key','d','done','you','p','/w/p','s1',NULL,'in vault',1000,2000,2000,NULL,3),
			(3,'pool','','pending','backlog','p','/w/p',NULL,NULL,'',1000,1000,NULL,NULL,1),
			(4,'checklist','with subtasks','in_progress','agent','p','/w/p',NULL,'s1','',1000,1500,NULL,NULL,2),
			(5,'deleted later','','pending','you',NULL,NULL,NULL,NULL,'',1000,1000,NULL,NULL,1),
			(6,'daily template','','pending','you','p','/w/p',NULL,NULL,'',1000,1000,NULL,NULL,4),
			(7,'run child','','pending','agent','p','/w/p',NULL,'s2','',3000,3000,NULL,NULL,1),
			(8,'archived','','done','agent','p','/w/p',NULL,'s1','ok',100,200,150,200,5)`,
		`DELETE FROM tasks WHERE id = 5`,
		`INSERT INTO task_subtasks(task_id,title,done,position) VALUES (4,'a',1,0),(4,'b',0,1)`,
		`INSERT INTO task_dependencies(task_id,waits_for_id) VALUES (4,3),(7,1)`,
		`INSERT INTO task_notifications(id,task_id,kind,recipient_session_id,deliver,method,title,body,state,reason,steer_id,created_at,updated_at,delivered_at) VALUES
			('tn_a',4,'assigned','s1','wake','run','Task #4 assigned','body a','delivered','',NULL,1000,1100,1100),
			('tn_b',2,'request_done','s1','hold','run','Task #2 done','body b','held','',NULL,2000,2000,NULL),
			('tn_c',5,'agent_deleted','s3','hold','append','Task #5 deleted','body c','failed','session_deleted',NULL,2100,2100,NULL),
			('tn_d',7,'assigned','s2','wake','run','Task #7 assigned','body d','sent','','st_1',3000,3000,NULL),
			('tn_e',4,'updated','s1','hold','run','Task #4 updated','body e','pending','session_limit',NULL,3100,3100,NULL)`,
		`INSERT INTO task_schedules(task_id,when_json,tz,target_json,busy,saved,late,enabled,next_due_at,created_by_session_id)
			VALUES (6,'{"kind":"repeat","every":"day","at":"09:00"}','Europe/Madrid','{"kind":"session","id":"s2"}','wait','hold','run',1,5000,'s2')`,
		`INSERT INTO task_occurrences(schedule_task_id,due_at,definition_revision,spec_json,trigger,observed_at,state,reason,note,
			confirmed_at,resolved_session_id,child_task_id,notice_id,admitted_at,completed_at,missed_count,created_at,updated_at,revision,reserved_session_id) VALUES
			(6,2000,1,'{}','timer',2000,'skipped','','',NULL,NULL,NULL,NULL,NULL,NULL,0,2000,2000,1,NULL),
			(6,3000,1,'{}','timer',3000,'assigned','','',3000,'s2',7,'tn_d',NULL,NULL,1,3000,3000,2,'s2')`,
		`INSERT INTO session_discards(session_id, created_at) VALUES ('s9', 1234)`,
		`UPDATE tasks_meta SET revision = 42, legacy_schedules_imported = 1`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
}

var v4Tables = []string{"tasks", "task_subtasks", "task_dependencies", "tasks_meta", "task_notifications",
	"task_schedules", "task_occurrences", "session_discards", "sqlite_sequence"}

// snapshot dumps every v4 table, rowids included, as text.
func snapshot(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var b strings.Builder
	for _, table := range v4Tables {
		rows, err := db.Query("SELECT rowid, * FROM " + table + " ORDER BY rowid")
		if table == "sqlite_sequence" {
			rows, err = db.Query("SELECT * FROM sqlite_sequence ORDER BY name")
		}
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		cols, _ := rows.Columns()
		fmt.Fprintf(&b, "## %s %v\n", table, cols)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				if bs, ok := v.([]byte); ok {
					v = string(bs)
				}
				fmt.Fprintf(&b, "%v|", v)
			}
			b.WriteString("\n")
		}
		_ = rows.Close()
	}
	return b.String()
}

func pragmaInt(t *testing.T, path, query string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRow(query).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

func compatVersion(t *testing.T, path string) int {
	return pragmaInt(t, path, "SELECT version FROM tasks_compat WHERE id = 1")
}

// I7: the compat level adds tables and widens a CHECK without touching
// user_version, any row, rowid, sequence or revision; it backs the old file up.
func TestCompatMigrationKeepsUserVersionAndData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", DatabaseName)
	buildV4(t, path)
	before := snapshot(t, path)

	r := openRepo(t, path)
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	if v := pragmaInt(t, path, "PRAGMA user_version"); v != 4 {
		t.Fatalf("user_version = %d, want 4", v)
	}
	if v := compatVersion(t, path); v != 2 {
		t.Fatalf("compat version = %d, want 2", v)
	}
	if after := snapshot(t, path); after != before {
		t.Fatalf("data changed by the compat migration:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, idx := range []string{"task_notifications_task", "task_notifications_open"} {
		if n := pragmaInt(t, path, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='"+idx+"'"); n != 1 {
			t.Fatalf("index %s missing after the rebuild", idx)
		}
	}
	// The widened outbox takes the new kinds; the old ones still work.
	if _, err := r.Notice(bg, "tn_b"); err != nil {
		t.Fatal(err)
	}

	bak := path + ".pre-compat1.bak"
	st, err := os.Stat(bak)
	if err != nil {
		t.Fatalf("no backup: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v", st.Mode().Perm())
	}
	if got := snapshot(t, bak); got != before {
		t.Fatalf("backup differs from the v4 database:\n%s", got)
	}
	if n := pragmaInt(t, bak, "SELECT COUNT(*) FROM sqlite_master WHERE name='task_handoffs'"); n != 0 {
		t.Fatal("backup was taken after the migration")
	}

	// The tasks read through the new code are the same tasks.
	rec, err := r.Get(bg, 4)
	if err != nil || rec.Revision != 2 || len(rec.Subtasks) != 2 || rec.Handoff != nil || rec.CommentCount != 0 {
		t.Fatalf("task 4 = %+v, %v", rec, err)
	}
	// New ids continue the sequence: 5 stays unused.
	created := mustCreate(t, r, CreateInput{Title: "after", Place: PlaceYou})
	if created.ID != 9 {
		t.Fatalf("new id = %d, want 9", created.ID)
	}
}

// A brand new database runs everything at once and needs no backup.
func TestCompatMigrationOnNewDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", DatabaseName)
	r := openRepo(t, path)
	note(t, r, "first")
	if v := pragmaInt(t, path, "PRAGMA user_version"); v != 4 {
		t.Fatalf("user_version = %d", v)
	}
	if v := compatVersion(t, path); v != 2 {
		t.Fatalf("compat = %d", v)
	}
	if _, err := os.Stat(path + ".pre-compat1.bak"); !os.IsNotExist(err) {
		t.Fatalf("a new database was backed up: %v", err)
	}
}

// I7: two processes opening at once apply it once; a later open changes
// nothing, and the backup is never rewritten.
func TestCompatMigrationIdempotentAndConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", DatabaseName)
	buildV4(t, path)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := New(path)
			defer func() { _ = r.Close() }()
			_, errs[i] = r.Revision(bg)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	once := snapshot(t, path)
	bak := path + ".pre-compat1.bak"
	b1, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	r := openRepo(t, path)
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	if again := snapshot(t, path); again != once {
		t.Fatalf("second open changed the database")
	}
	if compatVersion(t, path) != 2 {
		t.Fatal("compat version moved")
	}
	b2, _ := os.ReadFile(bak)
	if string(b1) != string(b2) {
		t.Fatal("backup rewritten")
	}
}

// I7: the binary in production today (four migrations, no compat) opens the
// migrated database for writing and keeps working, deletes included.
func TestV4BinaryWritesAfterCompat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", DatabaseName)
	buildV4(t, path)
	r := openRepo(t, path)
	moa, infra := testOwners()
	sent, err := r.SendHandoff(bg, moa, infra, true, AgentInput{Title: "LXC for the demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AgentComment(bg, ownerActor(infra), sent.ID, "2 GB?"); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tooNew, err := migrateWith(db, path, migrations, nil)
	if err != nil || tooNew {
		t.Fatalf("v4 binary: tooNew=%v err=%v", tooNew, err)
	}
	for _, s := range []string{
		`INSERT INTO tasks(title,status,place,created_at,updated_at) VALUES ('from v4','pending','you',1,1)`,
		`UPDATE tasks SET status='done', completed_at=2, revision=revision+1 WHERE id = 4`,
		`INSERT INTO task_notifications(id,task_id,kind,recipient_session_id,deliver,method,title,body,state,created_at,updated_at)
			VALUES ('tn_v4',4,'agent_done','s1','hold','run','t','b','pending',5,5)`,
		fmt.Sprintf(`DELETE FROM tasks WHERE id = %d`, sent.ID),
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("v4 write %q: %v", s, err)
		}
	}
	var left int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM task_handoffs WHERE task_id = ?) + (SELECT COUNT(*) FROM task_comments WHERE task_id = ?)`,
		sent.ID, sent.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("deleting with the v4 binary left %d handoff/comment rows", left)
	}
	if v := pragmaInt(t, path, "PRAGMA user_version"); v != 4 {
		t.Fatalf("user_version = %d", v)
	}
}
