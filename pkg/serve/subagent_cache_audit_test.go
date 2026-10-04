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

func newAuditPersister(t *testing.T) (*servePersister, *session.FileStore) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	return newServePersister(&session.Session{ID: "sess"}, store, func() (string, string, bool) { return "", "", false }), store
}

// A record that was already waiting for the persister when the session was
// deleted must observe the deletion under the same lock and write nothing.
func TestSubagentCacheAuditCannotRecreateDeletedSession(t *testing.T) {
	sp, store := newAuditPersister(t)
	dir := session.NewSubagentStore(store.Dir(), "sess").Dir()

	sp.mu.Lock() // deterministic barrier: the record below blocks on it
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- sp.recordSubagentRequestFingerprint("sess", "sa-1", "", auditTestFingerprint("a"))
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("record did not wait for sp.mu: %v", err)
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

func TestSubagentCacheAuditPersisterRecordsAndKeepsCountAcrossCalls(t *testing.T) {
	sp, store := newAuditPersister(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sp.recordSubagentRequestFingerprint("sess", "sa-1", "sa-0", auditTestFingerprint("b")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	a, err := session.NewSubagentStore(store.Dir(), "sess").LoadCacheAudit("sa-1")
	if err != nil || a.Count != 8 || a.ResumedFrom != "sa-0" {
		t.Fatalf("audit=%+v err=%v", a, err)
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
					cb(auditTestFingerprint("c"))
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
		_, err := store.LoadCacheAudit(jobID)
		return err == nil
	})
	audit, err := store.LoadCacheAudit(jobID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	p, c := parentObserved, childObserved
	mu.Unlock()
	if audit.Count != 1 || audit.ResumedFrom != "" || audit.JobID != jobID || p != 0 || c != 1 {
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
