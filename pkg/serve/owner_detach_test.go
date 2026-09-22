package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
)

func postSessionOwner(t *testing.T, mgr *Manager, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/owner", handleSessionOwner(mgr))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/owner", bytes.NewBufferString(body)))
	return response
}

func findInfo(t *testing.T, mgr *Manager, id string) SessionInfo {
	t.Helper()
	for _, info := range mgr.ListWith(ListOptions{IncludeOwners: true}) {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("session %s is not listed", id)
	return SessionInfo{}
}

// waitIdle waits for a live session to finish the run a Send started.
func waitIdle(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	pollUntil(t, 10*time.Second, "the session going idle", func() bool {
		sess, ok := mgr.Get(id)
		return ok && sess.info().State == StateIdle && len(sess.History()) >= 2
	})
}

// A detached session's outcomes — a finished turn, a question, a failure —
// never reach the owner's outbox; once reattached, the next one does.
func TestDetachedSessionSendsNoReports(t *testing.T) {
	shortReportWindow(t, time.Hour) // nothing is delivered; the outbox is the assertion
	shortOwnerQuiescence(t, 20*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "the child")

	if _, err := mgr.SetOwnerDetached(child.ID, true); err != nil {
		t.Fatal(err)
	}
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 1, "finished while detached")
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.AskUserRequested{
		SessionID: child.ID, RunGen: 2, ID: "ask-1",
		Questions: []bus.AskQuestion{{Text: "which branch?"}},
	})
	child.runtime.Bus.Publish(bus.RunEnded{SessionID: child.ID, RunGen: 2, Err: context.DeadlineExceeded})
	// Every event above is consumed and every worker it launched has emitted
	// (or not): after sync no new worker can be added.
	child.ownerObserver.sync()
	child.ownerObserver.workers.Wait()
	if got := outbox(t, mgr, info.CodebaseKey); len(got) != 0 {
		t.Fatalf("a detached session reported to its owner: %+v", got)
	}

	if _, err := mgr.SetOwnerDetached(child.ID, false); err != nil {
		t.Fatal(err)
	}
	publishRunStart(child, 3, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 3, "finished after reattach")
	got := waitForOutbox(t, mgr, info.CodebaseKey, 1)
	if len(got) != 1 || !strings.Contains(got[0].FinalText, "after reattach") {
		t.Fatalf("reattach did not resume reporting exactly the new turn: %+v", got)
	}
}

// The owner sees a detached session only as the fact that it exists: no
// owner_id for its rows, no title in its list, and no read, send or answer.
func TestDetachedSessionIsHiddenFromItsOwner(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	own, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "private title"})
	if err != nil {
		t.Fatal(err)
	}

	detached, err := mgr.SetOwnerDetached(child.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if detached.OwnerID != "" || detached.DetachedOwnerID != own.ID || detached.DetachedOwnerName != "Winerim" {
		t.Fatalf("detached projection = owner %q, detached %q/%q", detached.OwnerID, detached.DetachedOwnerID, detached.DetachedOwnerName)
	}
	raw, err := json.Marshal(mgr.sessionInfo(child))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"owner_id"`) || !strings.Contains(string(raw), `"detached_owner_id":"`+own.ID+`"`) {
		t.Fatalf("detached session JSON: %s", raw)
	}
	if listed := findInfo(t, mgr, child.ID); listed.OwnerID != "" || listed.DetachedOwnerID != own.ID {
		t.Fatalf("roster still counts the detached session: %+v", listed)
	}

	listed := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if !strings.Contains(listed, "- "+child.ID+" detached by the user\n") || strings.Contains(listed, "private title") {
		t.Fatalf("list must show only the detached one-liner:\n%s", listed)
	}
	for _, params := range []map[string]any{
		{"action": "read", "session_id": child.ID},
		{"action": "send", "session_id": child.ID, "text": "hello"},
		{"action": "answer", "session_id": child.ID, "ask_id": "ask-1", "answers": []any{"yes"}},
	} {
		res := runSessionsTool(t, ownerSess, params)
		if !res.IsError || !strings.Contains(toolText(res), "detached") {
			t.Fatalf("%s on a detached session = %q, want a detached refusal", params["action"], toolText(res))
		}
	}
	if sessions := newHeartbeatService(mgr).sessionsOf(mustOwner(t, mgr, own.ID)); len(sessions) != 0 {
		t.Fatalf("the heartbeat still watches a detached session: %+v", sessions)
	}

	reattached, err := mgr.SetOwnerDetached(child.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if reattached.OwnerID != own.ID || reattached.DetachedOwnerID != "" {
		t.Fatalf("reattached projection = owner %q, detached %q", reattached.OwnerID, reattached.DetachedOwnerID)
	}
	if res := runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID}); res.IsError {
		t.Fatalf("read after reattach: %s", toolText(res))
	}
	listed = toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if !strings.Contains(listed, "private title") {
		t.Fatalf("list after reattach lost the session:\n%s", listed)
	}
}

// Detaching is persisted: it survives the reactor rebuilding metadata on the
// next snapshot, a restart, and applies to a saved session without loading it.
// Reattaching removes the key from disk for good.
func TestOwnerDetachSurvivesSnapshotsAndRestart(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	sessionDir := t.TempDir()
	root := t.TempDir()
	mgr := newRestartableManager(t, ctx, sessionDir)
	own, _ := ownerWithSession(t, mgr, root, "Winerim")

	live, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "live"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "saved"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(saved.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.SetOwnerDetached(live.ID, true); err != nil {
		t.Fatal(err)
	}
	// A turn makes the reactor rebuild the metadata from scratch.
	if _, _, _, err := mgr.Send(live.ID, "hi", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, mgr, live.ID)
	if err := live.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	if !loadDetached(t, sessionDir, live.ID) {
		t.Fatal("the snapshot after the turn dropped the detach marker")
	}

	info, err := mgr.SetOwnerDetached(saved.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != StateSaved || info.DetachedOwnerID != own.ID {
		t.Fatalf("saved session projection after detach: %+v", info)
	}
	if _, loaded := mgr.Get(saved.ID); loaded {
		t.Fatal("detaching a saved session loaded it")
	}

	// Close the child first: a second Shutdown (the helper's cleanup) would
	// wait on the observer of a runtime the first one already tore down.
	if err := mgr.CloseSession(live.ID); err != nil {
		t.Fatal(err)
	}
	mgr.Shutdown()
	restarted := newRestartableManager(t, ctx, sessionDir)
	for _, id := range []string{live.ID, saved.ID} {
		if info := findInfo(t, restarted, id); info.OwnerID != "" || info.DetachedOwnerID != own.ID {
			t.Fatalf("session %s after restart: owner %q, detached %q", id, info.OwnerID, info.DetachedOwnerID)
		}
	}
	resumed, err := restarted.ResumeSession(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.ownerDetached.Load() {
		t.Fatal("a resumed session forgot it was detached")
	}

	// Reattach the live one, then run a turn: a preserved copy of the key left
	// behind would put it back on this snapshot.
	if _, err := restarted.SetOwnerDetached(live.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := restarted.Send(live.ID, "again", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 10*time.Second, "the second turn", func() bool {
		return len(resumed.History()) >= 4 && resumed.info().State == StateIdle
	})
	if err := resumed.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	if loadDetached(t, sessionDir, live.ID) {
		t.Fatal("reattach did not survive the next snapshot")
	}
	if _, err := restarted.SetOwnerDetached(saved.ID, false); err != nil {
		t.Fatal(err)
	}
	if loadDetached(t, sessionDir, saved.ID) {
		t.Fatal("reattaching a saved session left the marker on disk")
	}
	if info := findInfo(t, restarted, saved.ID); info.OwnerID != own.ID {
		t.Fatalf("saved session after reattach: %+v", info)
	}
}

func TestOwnerDetachSaveFailureDoesNotLeakIntoSnapshots(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, _ = ownerWithSession(t, mgr, root, "Winerim")

	for _, initiallyDetached := range []bool{false, true} {
		t.Run(fmt.Sprintf("initially detached=%t", initiallyDetached), func(t *testing.T) {
			child, err := mgr.CreateSession(CreateOpts{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			if initiallyDetached {
				if _, err := mgr.SetOwnerDetached(child.ID, true); err != nil {
					t.Fatal(err)
				}
			}

			dir := child.persister.store.Dir()
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			if _, err := mgr.SetOwnerDetached(child.ID, !initiallyDetached); err == nil {
				t.Fatal("SetOwnerDetached succeeded with an unwritable store")
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}

			if got := child.ownerDetached.Load(); got != initiallyDetached {
				t.Fatalf("live detach state = %t, want %t", got, initiallyDetached)
			}
			if got := child.persister.persisted.OwnerDetached(); got != initiallyDetached {
				t.Fatalf("persisted detach state = %t, want %t", got, initiallyDetached)
			}
			_, preserved := child.persister.preserved[session.MetaOwnerDetached]
			if preserved != initiallyDetached {
				t.Fatalf("preserved detach marker = %t, want %t", preserved, initiallyDetached)
			}

			if err := child.persister.Snapshot(nil, 0, map[string]any{}); err != nil {
				t.Fatal(err)
			}
			onDisk, err := child.persister.store.LoadReadOnly(child.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := onDisk.OwnerDetached(); got != initiallyDetached {
				t.Fatalf("snapshot wrote detach state %t, want %t", got, initiallyDetached)
			}
		})
	}
}

func loadDetached(t *testing.T, sessionDir, id string) bool {
	t.Helper()
	sess, _, err := session.FindSessionReadOnly(sessionDir, id)
	if err != nil {
		t.Fatal(err)
	}
	return sess.OwnerDetached()
}

func TestSessionOwnerRouteRefusesWhatCannotBeDetached(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	own, _ := ownerWithSession(t, mgr, root, "Winerim")
	byOwner, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	unowned, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	user, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}

	// Ordered: the last two cases depend on each other.
	for _, tc := range []struct {
		name, id, body string
		want           int
	}{
		{"owner conversation", own.SessionID, `{"detached":true}`, http.StatusConflict},
		{"reattach owner conversation", own.SessionID, `{"detached":false}`, http.StatusConflict},
		{"opened by the owner", byOwner.ID, `{"detached":true}`, http.StatusConflict},
		{"codebase without owner", unowned.ID, `{"detached":true}`, http.StatusConflict},
		{"missing field", user.ID, `{}`, http.StatusBadRequest},
		{"unknown session", "nope", `{"detached":true}`, http.StatusNotFound},
		{"reattach when attached", user.ID, `{"detached":false}`, http.StatusOK},
		{"detach a user session", user.ID, `{"detached":true}`, http.StatusOK},
	} {
		if got := postSessionOwner(t, mgr, tc.id, tc.body); got.Code != tc.want {
			t.Errorf("%s: status %d (%s), want %d", tc.name, got.Code, strings.TrimSpace(got.Body.String()), tc.want)
		}
	}
	if !user.ownerDetached.Load() {
		t.Fatal("the user session was not detached by the route")
	}
	for _, sess := range []*ManagedSession{byOwner, unowned} {
		if sess.ownerDetached.Load() {
			t.Fatalf("session %s was detached despite the refusal", sess.ID)
		}
	}
}

// The persister's preserved set is what carries the marker across snapshot
// rebuilds; clearing it must remove it from there too.
func TestRecordOwnerDetachedSurvivesRebuildAndClearsFromPreserved(t *testing.T) {
	store, err := session.NewFileStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	persisted := store.Create()
	sp := newServePersister(persisted, store, func() (string, string, bool) { return "t", "", false })
	onDisk := func() bool {
		loaded, err := store.Load(persisted.ID)
		if err != nil {
			t.Fatal(err)
		}
		return loaded.OwnerDetached()
	}

	if err := sp.recordOwnerDetached(true); err != nil {
		t.Fatal(err)
	}
	if err := sp.Snapshot(nil, 0, map[string]any{"model": "m"}); err != nil {
		t.Fatal(err)
	}
	if !onDisk() {
		t.Fatal("a rebuilt snapshot dropped the detach marker")
	}
	if err := sp.recordOwnerDetached(false); err != nil {
		t.Fatal(err)
	}
	if _, kept := sp.preserved[session.MetaOwnerDetached]; kept {
		t.Fatal("reattach left the marker in the preserved set")
	}
	if err := sp.Snapshot(nil, 0, map[string]any{"model": "m"}); err != nil {
		t.Fatal(err)
	}
	if onDisk() {
		t.Fatal("a rebuilt snapshot brought the detach marker back")
	}
}

func mustOwner(t *testing.T, mgr *Manager, id string) owner.Owner {
	t.Helper()
	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	own, found, err := store.FindByID(id)
	if err != nil || !found {
		t.Fatalf("owner %s: found=%v err=%v", id, found, err)
	}
	return own
}
