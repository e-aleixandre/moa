package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
)

func (e *rsEnv) mentionRows(callerID, query string) (int, []mentionSessionRow, string) {
	e.t.Helper()
	resp := apiReq(e.t, e.srv, "GET", "/api/sessions/"+callerID+"/mention-sessions"+query, "")
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var rows []mentionSessionRow
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &rows); err != nil {
			e.t.Fatalf("body %s: %v", b, err)
		}
	}
	return resp.StatusCode, rows, string(b)
}

func (e *rsEnv) titled(cwd, title string, age time.Duration) *ManagedSession {
	e.t.Helper()
	s, err := e.mgr.CreateSession(CreateOpts{CWD: cwd, Title: title})
	if err != nil {
		e.t.Fatal(err)
	}
	s.mu.Lock()
	s.Updated = time.Now().Add(-age)
	s.mu.Unlock()
	return s
}

func rowIDs(rows []mentionSessionRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// The @ menu offers live and saved sessions of the caller's codebase, newest
// first, and never the conversation itself, an owner or another codebase.
func TestMentionSessionsListsSameCodebaseLiveAndSaved(t *testing.T) {
	env := newRSEnv(t, nil)
	_, ownerSess := ownerWithSession(t, env.mgr, env.root, "Proj")
	caller := env.titled(env.root, "caller", 0)
	newest := env.titled(env.root, "Newest", time.Minute)
	saved := env.titled(env.root, "Was Saved", time.Hour)
	if err := env.mgr.CloseSession(saved.ID); err != nil {
		t.Fatal(err)
	}
	oldest := env.titled(env.root, "Oldest", 48*time.Hour)
	foreign := env.titled(t.TempDir(), "Foreign", 0)

	status, rows, body := env.mentionRows(caller.ID, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d %s", status, body)
	}
	got := rowIDs(rows)
	if len(got) != 3 {
		t.Fatalf("rows = %v, want 3 (newest, saved, oldest)", got)
	}
	for _, r := range rows {
		switch r.ID {
		case caller.ID, ownerSess.ID, foreign.ID:
			t.Errorf("row %s must not be offered", r.ID)
		case saved.ID:
			if !r.Saved || r.State != StateSaved || r.Title != "Was Saved" {
				t.Errorf("saved row = %+v", r)
			}
		case newest.ID, oldest.ID:
			if r.Saved || r.State == StateSaved {
				t.Errorf("live row marked saved: %+v", r)
			}
		}
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Updated.Before(rows[i].Updated) {
			t.Errorf("rows not sorted by updated desc: %v", rows)
		}
	}
	if got[len(got)-1] != oldest.ID {
		t.Errorf("last row = %s, want the oldest %s", got[len(got)-1], oldest.ID)
	}
}

func TestMentionSessionsFilterAndLimit(t *testing.T) {
	env := newRSEnv(t, nil)
	caller := env.titled(env.root, "caller", 0)
	for i := 0; i < 7; i++ {
		env.titled(env.root, fmt.Sprintf("Bench cache %d", i), time.Duration(i+1)*time.Minute)
	}
	env.titled(env.root, "unrelated", 0)

	_, rows, _ := env.mentionRows(caller.ID, "")
	if len(rows) != defaultMentionSessionsLimit {
		t.Errorf("default limit: %d rows, want %d", len(rows), defaultMentionSessionsLimit)
	}
	_, rows, _ = env.mentionRows(caller.ID, "?q=BENCH&limit=20")
	if len(rows) != 7 {
		t.Errorf("case-insensitive filter: %d rows, want 7", len(rows))
	}
	for _, r := range rows {
		if !strings.Contains(r.Title, "Bench") {
			t.Errorf("row %q does not match the filter", r.Title)
		}
	}
	if _, rows, _ = env.mentionRows(caller.ID, "?q=bench&limit=3"); len(rows) != 3 || rows[0].Title != "Bench cache 0" {
		t.Errorf("limit=3 rows = %+v", rows)
	}
	for _, q := range []string{"?limit=0", "?limit=-4", "?limit=abc"} {
		if _, rows, _ = env.mentionRows(caller.ID, q); len(rows) != defaultMentionSessionsLimit {
			t.Errorf("%s: %d rows, want the default %d", q, len(rows), defaultMentionSessionsLimit)
		}
	}
	for i := 0; i < maxMentionSessionsLimit; i++ {
		env.titled(env.root, fmt.Sprintf("extra %d", i), time.Hour)
	}
	if _, rows, _ = env.mentionRows(caller.ID, "?limit=500"); len(rows) != maxMentionSessionsLimit {
		t.Errorf("cap: %d rows, want %d", len(rows), maxMentionSessionsLimit)
	}
	status, rows, body := env.mentionRows(caller.ID, "?q=zzz-no-match")
	if status != http.StatusOK || rows == nil || strings.TrimSpace(body) != "[]" {
		t.Errorf("no match = %d %q, want an empty array", status, body)
	}
}

func TestMentionSessionsUnknownCallerIs404(t *testing.T) {
	env := newRSEnv(t, nil)
	if status, _, body := env.mentionRows(strings.Repeat("0", 24), ""); status != http.StatusNotFound {
		t.Errorf("status = %d %s, want 404", status, body)
	}
}

// A detached session is offered to a normal conversation but not to the
// owner's, which cannot read it. An owner never sees another owner either.
func TestMentionSessionsDetachedOnlyHiddenFromOwnerConversation(t *testing.T) {
	env := newRSEnv(t, nil)
	_, ownerSess := ownerWithSession(t, env.mgr, env.root, "Proj")
	caller := env.titled(env.root, "caller", 0)
	attached := env.titled(env.root, "attached", time.Minute)
	detached := env.titled(env.root, "detached", 2*time.Minute)
	if _, err := env.mgr.SetOwnerDetached(detached.ID, true); err != nil {
		t.Fatal(err)
	}

	_, rows, _ := env.mentionRows(caller.ID, "")
	if got := rowIDs(rows); len(got) != 2 || got[0] != attached.ID || got[1] != detached.ID {
		t.Errorf("normal conversation rows = %v, want attached then detached", got)
	}
	_, rows, _ = env.mentionRows(ownerSess.ID, "")
	for _, r := range rows {
		if r.ID == detached.ID || r.ID == ownerSess.ID {
			t.Errorf("owner conversation was offered %s", r.ID)
		}
	}
	if got := rowIDs(rows); len(got) != 2 || got[0] != caller.ID || got[1] != attached.ID {
		t.Errorf("owner conversation rows = %v, want caller then attached", got)
	}
}

// Another worktree of the same repository is the same codebase.
func TestMentionSessionsIncludesOtherWorktreeOfTheSameRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	env := newRSEnv(t, nil)
	repo := filepath.Join(env.root, "repo")
	worktree := filepath.Join(env.root, "wt")
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "worktree", "add", "-q", "-b", "other", worktree},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	caller := env.titled(repo, "caller", 0)
	sibling := env.titled(worktree, "sibling", time.Minute)
	elsewhere := env.titled(t.TempDir(), "elsewhere", 0)

	_, rows, _ := env.mentionRows(caller.ID, "")
	if got := rowIDs(rows); len(got) != 1 || got[0] != sibling.ID {
		t.Errorf("rows = %v, want only the sibling worktree session (not %s)", got, elsewhere.ID)
	}
}

// The roster only enumerates candidates: a saved session whose persisted CWD
// moved to another codebase after the roster was cached must not be offered.
func TestMentionSessionsRevalidatesSavedRecordAgainstDisk(t *testing.T) {
	env := newRSEnv(t, nil)
	env.mgr.savedCacheTTL = time.Hour
	caller := env.titled(env.root, "caller", 0)
	target := env.titled(env.root, "MOVED_TARGET_TITLE", time.Minute)
	if err := env.mgr.CloseSession(target.ID); err != nil {
		t.Fatal(err)
	}
	if _, rows, body := env.mentionRows(caller.ID, "?q=MOVED_TARGET_TITLE"); len(rows) != 1 {
		t.Fatalf("warm roster should offer the saved target: %s", body)
	}
	saved, store, err := session.FindSessionReadOnly(env.mgr.sessionBaseDir, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved.Metadata[session.MetaCWD] = t.TempDir()
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	if _, rows, body := env.mentionRows(caller.ID, "?q=MOVED_TARGET_TITLE"); len(rows) != 0 || strings.Contains(body, target.ID) {
		t.Errorf("moved saved session still offered from the cached roster: %s", body)
	}
}
