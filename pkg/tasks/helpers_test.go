package tasks

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

var bg = context.Background()

func newRepo(t *testing.T) *Repo {
	t.Helper()
	return openRepo(t, filepath.Join(t.TempDir(), "cfg", DatabaseName))
}

// openRepo opens another repository on path: a second process's connection to
// the same database, as far as SQLite can tell.
func openRepo(t *testing.T, path string) *Repo {
	t.Helper()
	r := New(path)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func actor(session, project string) Actor {
	return Actor{SessionID: session, ProjectKey: project, ProjectCWD: "/work/" + project}
}

func mustCreate(t *testing.T, r *Repo, in CreateInput) Record {
	t.Helper()
	rec, err := r.Create(bg, in)
	if err != nil {
		t.Fatalf("create %q: %v", in.Title, err)
	}
	return rec
}

func backlog(t *testing.T, r *Repo, title, project string) Record {
	t.Helper()
	return mustCreate(t, r, CreateInput{Title: title, Place: PlaceBacklog, ProjectKey: project})
}

func note(t *testing.T, r *Repo, title string) Record {
	t.Helper()
	return mustCreate(t, r, CreateInput{Title: title, Place: PlaceYou})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithCancel(context.Background())
}

// afterArchive is a clock eight days from now: every task completed so far is
// past its seven days.
func afterArchive(r *Repo) func() time.Time {
	at := r.now().Add(8 * 24 * time.Hour)
	return func() time.Time { return at }
}
