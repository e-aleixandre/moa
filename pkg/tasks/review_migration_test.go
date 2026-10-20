package tasks

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func reviewCompat5DB(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), DatabaseName)
	buildV4(t, p)
	db, err := sql.Open("sqlite", dsn(p, true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if tooNew, err := migrateWith(db, "", migrations, compatMigrations[:5]); err != nil || tooNew {
		t.Fatalf("compat5 fixture: tooNew=%v err=%v", tooNew, err)
	}
	for _, q := range []string{
		`INSERT INTO questions(id,session_id,origin_tool_call_id,origin_tool,questions_json,requires_tap,state,asked_at,updated_at) VALUES ('q_review','s1','tc_review','ask_user','[]',1,'open',1234,1234)`,
		`INSERT INTO news(id,session_id,origin,kind,title,created_at,updated_at) VALUES ('news_review','s1','origin_review','info','Retained news',1234,1234)`,
		`INSERT INTO news_origins(session_id,origin,news_id) VALUES ('s1','origin_review','news_review')`,
		`INSERT INTO task_comments(task_id,author_kind,author_session_id,body,created_at) VALUES (2,'session','s1','Retained comment',1234)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// Include every table, rowid, sequence and old schema object, not just task counts.
func reviewFullDump(t *testing.T, p string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(p, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string]string{}
	for rows.Next() {
		var name, schema string
		if err := rows.Scan(&name, &schema); err != nil {
			t.Fatal(err)
		}
		tables[name] = schema
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for name, schema := range tables {
		rs, err := db.Query(`SELECT rowid,* FROM "` + name + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rs.Columns()
		var data [][]any
		for rs.Next() {
			vals := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range vals {
				dest[i] = &vals[i]
			}
			if err := rs.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			data = append(data, vals)
		}
		if err := rs.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rs.Close()
		j, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = schema + "\n" + string(j)
	}
	return out
}

func TestReviewCompat6BackupAdditiveIdempotent(t *testing.T) {
	p := reviewCompat5DB(t)
	before := reviewFullDump(t, p)
	r := New(p)
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got := pragmaInt(t, p, "PRAGMA user_version"); got != 4 {
		t.Fatalf("user_version=%d, want 4", got)
	}
	if got := compatVersion(t, p); got != 6 {
		t.Fatalf("compat=%d, want 6", got)
	}
	after := reviewFullDump(t, p)
	for table, want := range before {
		if table != "tasks_compat" && after[table] != want {
			t.Errorf("compat6 changed existing table %s", table)
		}
	}
	if len(after) != len(before)+1 {
		t.Fatalf("tables %d -> %d; want exactly one addition", len(before), len(after))
	}
	if _, ok := after["task_schedule_reviews"]; !ok {
		t.Fatal("Keep table absent")
	}
	bak := p + ".pre-compat6.bak"
	if got := reviewFullDump(t, bak); !reflect.DeepEqual(got, before) {
		t.Fatal("backup is not the complete compat5 database")
	}
	bytesBefore, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(bak)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v: %v", st, err)
	}
	r = New(p)
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if got := reviewFullDump(t, p); !reflect.DeepEqual(got, after) {
		t.Fatal("reopen is not idempotent")
	}
	bytesAfter, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	if string(bytesBefore) != string(bytesAfter) {
		t.Fatal("reopen rewrote backup")
	}
	for _, file := range []string{p, bak} {
		db, err := sql.Open("sqlite", dsn(file, false))
		if err != nil {
			t.Fatal(err)
		}
		var check string
		err = db.QueryRow("PRAGMA integrity_check").Scan(&check)
		_ = db.Close()
		if err != nil || check != "ok" {
			t.Fatalf("%s integrity=%q err=%v", file, check, err)
		}
	}
}

func TestReviewCompat6CascadesAndRollsBackAtomically(t *testing.T) {
	t.Run("cascade", func(t *testing.T) {
		p := reviewCompat5DB(t)
		r := New(p)
		if _, err := r.Revision(bg); err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
		db, err := sql.Open("sqlite", dsn(p, true))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(`INSERT INTO task_schedule_reviews(task_id,kept_at) VALUES (6,1234);DELETE FROM tasks WHERE id=6`); err != nil {
			t.Fatalf("old-style template deletion: %v", err)
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM task_schedule_reviews`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("orphan Keep rows=%d err=%v", n, err)
		}
	})
	t.Run("rollback", func(t *testing.T) {
		p := reviewCompat5DB(t)
		before := reviewFullDump(t, p)
		db, err := sql.Open("sqlite", dsn(p, true))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(1)
		broken := append([]string(nil), compatMigrations...)
		broken[5] += ` INSERT INTO absent_review_table VALUES (1);`
		if _, err := migrateWith(db, p, migrations, broken); err == nil {
			t.Fatal("injected migration failure succeeded")
		}
		if got := reviewFullDump(t, p); !reflect.DeepEqual(got, before) {
			t.Fatal("failed migration changed schema/data/compat marker")
		}
		if got := reviewFullDump(t, p+".pre-compat6.bak"); !reflect.DeepEqual(got, before) {
			t.Fatal("failure backup differs from compat5")
		}
		if tooNew, err := migrateWith(db, p, migrations, compatMigrations); err != nil || tooNew {
			t.Fatalf("retry tooNew=%v err=%v", tooNew, err)
		}
	})
}
