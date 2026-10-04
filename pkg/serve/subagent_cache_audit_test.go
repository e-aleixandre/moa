package serve

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func auditTestFingerprint(c string) core.RequestFingerprint {
	h := strings.Repeat(c, 64)
	return core.RequestFingerprint{
		BuiltAt: time.Unix(1, 0).UTC(), BodySHA256: h, OptionsSHA256: h,
		Prefixes: []core.RequestPrefixHash{{Section: "messages", Message: 0, Block: 0, SHA256: h}},
	}
}

func auditTestSnapshot(job, source string, count uint64, first, last string) session.SubagentCacheAudit {
	a := session.SubagentCacheAudit{JobID: job, ResumedFrom: source, Count: count, First: auditTestFingerprint(first)}
	if last != "" {
		l := auditTestFingerprint(last)
		a.Last = &l
	}
	return a
}

func newAuditPersister(t *testing.T) (*servePersister, *session.FileStore) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	return newServePersister(&session.Session{ID: "sess"}, store, func() (string, string, bool) { return "", "", false }), store
}

// A snapshot that was already waiting for the persister when the session was
// deleted must observe the deletion under the same lock and write nothing.
func TestSubagentCacheAuditCannotRecreateDeletedSession(t *testing.T) {
	sp, store := newAuditPersister(t)
	dir := session.NewSubagentStore(store.Dir(), "sess").Dir()

	sp.mu.Lock() // deterministic barrier: the record below blocks on it
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- sp.saveSubagentCacheAudit("sess", auditTestSnapshot("sa-1", "", 1, "a", ""))
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("save did not wait for sp.mu: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	sp.deleted = true
	sp.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a late audit recreated the deleted side directory: %v", err)
	}
}

// Start and end snapshots each overwrite the file: first without last, then
// with the final count and last.
func TestSubagentCacheAuditPersisterOverwritesWithSnapshots(t *testing.T) {
	sp, store := newAuditPersister(t)
	reader := session.NewSubagentStore(store.Dir(), "sess")
	if err := sp.saveSubagentCacheAudit("sess", auditTestSnapshot("sa-1", "sa-0", 1, "b", "")); err != nil {
		t.Fatal(err)
	}
	a, err := reader.LoadCacheAudit("sa-1")
	if err != nil || a.Count != 1 || a.Last != nil || a.ResumedFrom != "sa-0" {
		t.Fatalf("start audit=%+v err=%v", a, err)
	}
	if err := sp.saveSubagentCacheAudit("sess", auditTestSnapshot("sa-1", "sa-0", 8, "b", "c")); err != nil {
		t.Fatal(err)
	}
	a, err = reader.LoadCacheAudit("sa-1")
	if err != nil || a.Count != 8 || a.Last == nil || a.Last.BodySHA256 != strings.Repeat("c", 64) {
		t.Fatalf("end audit=%+v err=%v", a, err)
	}
}

// Neither the start nor the end snapshot may write after the session was deleted.
func TestSubagentCacheAuditSnapshotsAfterDeleteWriteNothing(t *testing.T) {
	sp, store := newAuditPersister(t)
	dir := session.NewSubagentStore(store.Dir(), "sess").Dir()
	sp.mu.Lock()
	sp.deleted = true
	sp.mu.Unlock()
	for _, a := range []session.SubagentCacheAudit{
		auditTestSnapshot("sa-1", "", 1, "a", ""),
		auditTestSnapshot("sa-1", "", 3, "a", "b"),
	} {
		if err := sp.saveSubagentCacheAudit("sess", a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("snapshot after delete wrote: %v", err)
	}
}

// Real manager, real session store and the real subagent tool: a child request
// reaches the persisted audit with the job and its resume source, the parent's
// own request carries no callback, and deleting the session removes the audit.
// The mock provider stands in for Anthropic by calling the callback it is given.
func TestSubagentRequestFingerprintReachesSessionStore(t *testing.T) {
	var mu sync.Mutex
	var parentObserved, childObserved int
	reportingHandler := func(inner mockHandler, parent bool) mockHandler {
		return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			mu.Lock()
			defer mu.Unlock()
			if cb := req.Options.OnRequestFingerprint; cb != nil {
				if parent {
					parentObserved++
				} else {
					childObserved++
					cb(func() (core.RequestFingerprint, error) { return auditTestFingerprint("c"), nil })
				}
			}
			return inner(ctx, req)
		}
	}
	mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(
		reportingHandler(toolCallHandlerFor("tc-sub", "subagent", map[string]any{"task": "look around"}), true),
		reportingHandler(simpleResponseHandler("child done"), false),
		reportingHandler(simpleResponseHandler("parent done"), true),
	), t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "delegate", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	store := sess.persister.subagentStore(sess.ID)
	var jobID string
	pollUntil(t, 5*time.Second, "child audit on disk", func() bool {
		list, _ := store.List()
		if len(list) == 0 {
			return false
		}
		jobID = list[0].JobID
		a, err := store.LoadCacheAudit(jobID)
		return err == nil && a.Last != nil
	})
	audit, err := store.LoadCacheAudit(jobID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	p, c := parentObserved, childObserved
	mu.Unlock()
	if audit.Count != 1 || audit.Last == nil || audit.ResumedFrom != "" || audit.JobID != jobID || p != 0 || c != 1 {
		t.Fatalf("audit=%+v parentObserved=%d childObserved=%d", audit, p, c)
	}
	pollUntil(t, 5*time.Second, "parent run completion", func() bool { return sessState(sess) == StateIdle })

	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Dir()); !os.IsNotExist(err) {
		t.Fatalf("side directory survived deletion: %v", err)
	}
	if _, err := store.LoadCacheAudit(jobID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("audit survived deletion: %v", err)
	}
}
