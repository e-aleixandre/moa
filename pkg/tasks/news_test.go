package tasks

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func mustPublish(t *testing.T, r *Repo, in NewsInput) NewsPublished {
	t.Helper()
	p, err := r.PublishNews(bg, in)
	if err != nil {
		t.Fatalf("publish %q: %v", in.Title, err)
	}
	return p
}

func newsByID(t *testing.T, r *Repo, id string) (News, bool) {
	t.Helper()
	list, err := r.ListNews(bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range list {
		if n.ID == id {
			return n, true
		}
	}
	return News{}, false
}

// Two processes (two repositories on one file): a device that saw revision 1
// cannot archive the update another process published since, and a reopen by
// a binary that predates News keeps writing tasks without touching
// user_version or the news.
func TestNewsStaleAckAcrossHandlesAndPreNewsBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", DatabaseName)
	a := openRepo(t, path)
	b := openRepo(t, path)

	first := mustPublish(t, a, NewsInput{SessionID: "s1", Key: "deploy/x", Kind: "deployed", Title: "In preflight"})
	if first.Outcome != NewsCreated || first.Revision != 1 || first.ReadAt != 0 {
		t.Fatalf("first publish = %+v", first)
	}
	second := mustPublish(t, b, NewsInput{SessionID: "s1", Key: "deploy/x", Kind: "deployed", Title: "Deployed"})
	if second.Outcome != NewsUpdated || second.ID != first.ID || second.Revision != 2 || second.CreatedAt != first.CreatedAt {
		t.Fatalf("keyed update = %+v (first %+v)", second, first)
	}

	n, err := a.ReadNews(bg, []NewsAck{{ID: first.ID, Revision: first.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a stale acknowledgement archived %d items", n)
	}
	got, ok := newsByID(t, b, first.ID)
	if !ok || got.ReadAt != 0 || got.Revision != 2 || got.Title != "Deployed" {
		t.Fatalf("after a stale ack: %+v ok=%v", got, ok)
	}
	if n, err := a.ReadNews(bg, []NewsAck{{ID: first.ID, Revision: 2}}); err != nil || n != 1 {
		t.Fatalf("current ack: n=%d err=%v", n, err)
	}
	_ = a.Close()
	_ = b.Close()

	// The binary before News: same base migrations, compat without News.
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if tooNew, err := migrateWith(db, path, migrations, compatMigrations[:newsCompatLevel-1]); err != nil || tooNew {
		t.Fatalf("pre-News binary: tooNew=%v err=%v", tooNew, err)
	}
	if _, err := db.Exec(`INSERT INTO tasks(title,status,place,created_at,updated_at) VALUES ('from the old binary','pending','you',1,1)`); err != nil {
		t.Fatalf("pre-News binary write: %v", err)
	}
	_ = db.Close()
	if v := pragmaInt(t, path, "PRAGMA user_version"); v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}

	c := openRepo(t, path)
	got, ok = newsByID(t, c, first.ID)
	if !ok || got.ReadAt == 0 || got.Revision != 2 {
		t.Fatalf("news after the old binary wrote: %+v ok=%v", got, ok)
	}
}

func TestNewsPublishValidatesAndDedupesRetries(t *testing.T) {
	r := newRepo(t)
	for _, in := range []NewsInput{
		{SessionID: "", Kind: "info", Title: "x"},
		{SessionID: "s", Kind: "info", Title: "  "},
		{SessionID: "s", Kind: "gossip", Title: "x"},
	} {
		if _, err := r.PublishNews(bg, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("publish %+v: err=%v, want ErrInvalid", in, err)
		}
	}

	// The same tool call retried is the same item, not an update that would
	// make a read item unread again.
	in := NewsInput{SessionID: "s", Kind: "done", Title: "Tests green", Origin: "m1/c1"}
	p := mustPublish(t, r, in)
	if _, err := r.ReadNews(bg, []NewsAck{{ID: p.ID, Revision: p.Revision}}); err != nil {
		t.Fatal(err)
	}
	again := mustPublish(t, r, in)
	if again.Outcome != NewsDuplicate || again.ID != p.ID || again.Revision != 1 || again.ReadAt == 0 {
		t.Fatalf("retry = %+v", again)
	}
	keyed := NewsInput{SessionID: "s", Key: "k", Kind: "info", Title: "One", Origin: "m2/c1"}
	k1 := mustPublish(t, r, keyed)
	if k2 := mustPublish(t, r, keyed); k2.Outcome != NewsDuplicate || k2.Revision != k1.Revision {
		t.Fatalf("keyed retry = %+v", k2)
	}
	// Without a key, a new call is a new item; same key in another session too.
	other := mustPublish(t, r, NewsInput{SessionID: "s", Kind: "done", Title: "Tests green", Origin: "m3/c1"})
	if other.ID == p.ID || other.Outcome != NewsCreated {
		t.Fatalf("a new unkeyed call = %+v", other)
	}
	if o := mustPublish(t, r, NewsInput{SessionID: "s2", Key: "k", Kind: "info", Title: "One"}); o.ID == k1.ID {
		t.Fatal("a key is per session")
	}
}

func TestNewsReadRetentionAndOrdering(t *testing.T) {
	r := newRepo(t)
	base := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	now := base
	r.SetClock(func() time.Time { return now })

	old := mustPublish(t, r, NewsInput{SessionID: "s", Kind: "info", Title: "old"})
	now = now.Add(time.Minute)
	fresh := mustPublish(t, r, NewsInput{SessionID: "s", Kind: "problem", Title: "fresh"})

	if _, err := r.ReadNews(bg, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty ack list: err=%v, want ErrInvalid", err)
	}
	if _, err := r.ReadNews(bg, []NewsAck{{ID: "", Revision: 1}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty id: err=%v, want ErrInvalid", err)
	}

	list, err := r.ListNews(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != fresh.ID || list[1].ID != old.ID {
		t.Fatalf("order = %+v", list)
	}

	if n, err := r.ReadNews(bg, []NewsAck{{ID: old.ID, Revision: 1}, {ID: "news_missing", Revision: 1}}); err != nil || n != 1 {
		t.Fatalf("read: n=%d err=%v", n, err)
	}
	readAt := now
	// Acknowledging again later does not push the retention forward.
	now = now.Add(6 * 24 * time.Hour)
	if n, err := r.ReadNews(bg, []NewsAck{{ID: old.ID, Revision: 1}}); err != nil || n != 0 {
		t.Fatalf("repeat ack: n=%d err=%v", n, err)
	}
	got, _ := newsByID(t, r, old.ID)
	if got.ReadAt != readAt.UnixMilli() {
		t.Fatalf("read_at moved to %d, want %d", got.ReadAt, readAt.UnixMilli())
	}

	now = readAt.Add(7*24*time.Hour + time.Second)
	if _, ok := newsByID(t, r, old.ID); ok {
		t.Fatal("a read item is still listed seven days after it was read")
	}
	// Unread never expires.
	now = base.Add(90 * 24 * time.Hour)
	if _, ok := newsByID(t, r, fresh.ID); !ok {
		t.Fatal("an unread item expired")
	}
	// A later write drops the expired rows for good.
	mustPublish(t, r, NewsInput{SessionID: "s", Kind: "info", Title: "later"})
	var rows int
	if err := r.w.QueryRow(`SELECT COUNT(*) FROM news WHERE id = ?`, old.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("the expired item was not purged")
	}
}
