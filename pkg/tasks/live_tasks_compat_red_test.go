package tasks

import (
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"
)

func liveTasksSQL(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func liveTasksCompat6DB(t *testing.T) string {
	t.Helper()
	p := reviewCompat5DB(t)
	db := liveTasksSQL(t, p)
	if tooNew, err := migrateWith(db, "", migrations, compatMigrations[:6]); err != nil || tooNew {
		t.Fatalf("compat6 fixture: tooNew=%v err=%v", tooNew, err)
	}
	for _, q := range []string{
		`INSERT INTO task_schedule_reviews(task_id,kept_at) VALUES (6,1234)`,
		`INSERT INTO task_handoffs(task_id,from_owner_id,from_owner_name,from_session_id,to_owner_id,to_owner_name,to_session_id,state,created_at,updated_at)
		 VALUES (4,'sender','Sender','s-other','recipient','Recipient','s1','sent',1000,1500)`,
		`INSERT INTO task_comments(task_id,author_kind,author_session_id,body,created_at) VALUES (4,'session','s1','Handoff retained',1500)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLiveTasksCompat7PreservesDataAndBackup(t *testing.T) {
	p := liveTasksCompat6DB(t)
	before := reviewFullDump(t, p)
	r := openRepo(t, p)
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	if v := pragmaInt(t, p, "PRAGMA user_version"); v != 4 {
		t.Errorf("user_version=%d, want unchanged base 4", v)
	}
	if v := compatVersion(t, p); v != 7 {
		t.Errorf("compat=%d, want 7 (execution-link migration missing)", v)
	}
	after := reviewFullDump(t, p)
	for table, want := range before {
		if table != "tasks_compat" && after[table] != want {
			t.Errorf("compat7 changed existing schema/rowids/data/revisions in %s", table)
		}
	}
	if len(after) != len(before)+1 || after["task_execution_links"] == "" {
		t.Errorf("want only task_execution_links added: tables %d -> %d", len(before), len(after))
	}
	bak := p + ".pre-compat7.bak"
	st, err := os.Stat(bak)
	if err != nil {
		t.Fatalf("compat6 database was not backed up before compat7: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("backup mode=%v, want 0600", st.Mode().Perm())
	}
	if got := reviewFullDump(t, bak); !reflect.DeepEqual(got, before) {
		t.Fatal("pre-compat7 backup does not preserve the complete compat6 database")
	}
	if n := pragmaInt(t, p, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='task_execution_links_session'`); n != 1 {
		t.Fatal("missing execution session index")
	}
	if n := pragmaInt(t, p, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name='task_execution_links_revoke'`); n != 1 {
		t.Fatal("missing old-binary revocation trigger")
	}
	bytesBefore, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	r = openRepo(t, p)
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	if got := reviewFullDump(t, p); !reflect.DeepEqual(got, after) {
		t.Error("reopen changed compat7 data/schema")
	}
	bytesAfter, err := os.ReadFile(bak)
	if err != nil || string(bytesBefore) != string(bytesAfter) {
		t.Fatalf("reopen rewrote the compat7 backup: %v", err)
	}
}

// Simulate the prior binary with exactly six compat migrations, then issue
// only statements that know about tasks, not about execution links.
func TestLiveTasksCompat7OldBinaryWritesAndRevokesLinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		keep bool
	}{
		{"move", `UPDATE tasks SET place='backlog',assignee_session_id=NULL WHERE id=4`, false},
		{"reassign", `UPDATE tasks SET assignee_session_id='s2' WHERE id=4`, false},
		{"project", `UPDATE tasks SET project_key='another-project' WHERE id=4`, false},
		{"project_to_null", `UPDATE tasks SET project_key=NULL WHERE id=4`, false},
		{"reopen", `UPDATE tasks SET status='pending' WHERE id=4`, false},
		{"reopen_done", `UPDATE tasks SET status='done',completed_at=2000 WHERE id=4; UPDATE tasks SET status='pending',completed_at=NULL WHERE id=4`, false},
		{"delete_cascades", `DELETE FROM tasks WHERE id=4`, false},
		{"same_values", `UPDATE tasks SET place=place,assignee_session_id=assignee_session_id,project_key=project_key,status=status WHERE id=4`, true},
		{"done_keeps_reference", `UPDATE tasks SET status='done',completed_at=2000 WHERE id=4`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := liveTasksCompat6DB(t)
			r := openRepo(t, p)
			if _, err := r.Revision(bg); err != nil {
				t.Fatal(err)
			}
			_ = r.Close()
			db, err := sql.Open("sqlite", dsn(p, true))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			if tooNew, err := migrateWith(db, p, migrations, compatMigrations[:6]); err != nil || tooNew {
				t.Fatalf("old binary rejected additive compat7: tooNew=%v err=%v", tooNew, err)
			}
			if _, err := db.Exec(`INSERT INTO task_execution_links(task_id,session_id,linked_at) VALUES (4,'executor',2000)`); err != nil {
				t.Fatalf("compat7 execution-link schema unavailable: %v", err)
			}
			if _, err := db.Exec(tc.sql); err != nil {
				t.Fatalf("old binary cannot write compat7: %v", err)
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM task_execution_links WHERE task_id=4`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.keep {
				want = 1
			}
			if n != want {
				t.Fatalf("old UPDATE/DELETE left %d links, want %d", n, want)
			}
			if v := compatVersion(t, p); v != 7 {
				t.Fatalf("old binary rewrote compat level to %d", v)
			}
		})
	}
}

func TestLiveTasksCompat7Constraints(t *testing.T) {
	r := newRepo(t)
	note(t, r, "task")
	db := liveTasksSQL(t, r.Path())
	if _, err := db.Exec(`INSERT INTO task_execution_links(task_id,session_id,linked_at) VALUES (1,'executor',1000)`); err != nil {
		t.Fatalf("compat7 execution-link schema unavailable: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO task_execution_links(task_id,session_id,linked_at) VALUES (1,'second-executor',1000)`,
		`INSERT INTO task_execution_links(task_id,session_id,linked_at) VALUES (9999,'executor',1000)`,
		`UPDATE task_execution_links SET session_id=''`,
		`UPDATE task_execution_links SET session_id='   '`,
		`UPDATE task_execution_links SET session_id=NULL`,
		`UPDATE task_execution_links SET linked_at=NULL`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("invalid execution link accepted: %s", q)
		}
	}
}

// This is a regression control: newer BASE schemas remain read-only even
// though newer COMPAT levels deliberately do not prohibit old writers.
func TestLiveTasksCompat7StillRejectsNewerBaseWrites(t *testing.T) {
	p := liveTasksCompat6DB(t)
	r := openRepo(t, p)
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	db := liveTasksSQL(t, p)
	if _, err := db.Exec(`PRAGMA user_version=99`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before := reviewFullDump(t, p)
	r = openRepo(t, p)
	if rec, err := r.Get(bg, 4); err != nil || rec.Title != "checklist" {
		t.Fatalf("newer base must remain readable: %+v, %v", rec, err)
	}
	if _, err := r.Create(bg, CreateInput{Title: "must not write", Place: PlaceYou}); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("newer base write error=%v, want ErrSchemaTooNew", err)
	}
	if got := reviewFullDump(t, p); !reflect.DeepEqual(got, before) || pragmaInt(t, p, "PRAGMA user_version") != 99 {
		t.Fatal("newer-base open modified the database")
	}
}
