package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func review2MoaAssignedRun(t *testing.T, h *schedHarness, r *tasks.Repo, sid string) tasks.Occurrence {
	t.Helper()
	mkTemplate(t, r, "keep this assignment on a refused delete", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{Saved: tasks.DeliverHold}))
	h.clock.Advance(time.Minute)
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("setup T0: %+v / %v", due, err)
	}
	o, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// The new SQLite refusal path must not erase a still-live automation run's
// idempotency key: an ordinary webhook retry would otherwise run twice.
func TestReview2MoaDeleteSQLRefusalKeepsAutomationKey(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
	m := h.start()
	schedReviewStopWorkers(t, m)
	req := AutomationRunRequest{Prompt: "deploy once", IdempotencyKey: "review2-deploy-once", CWD: h.root}
	sid, created, err := m.CreateAutomationRun(req)
	if err != nil || !created {
		t.Fatalf("setup automation: %s / %t / %v", sid, created, err)
	}
	r := h.repo()
	o := review2MoaAssignedRun(t, h, r, sid)
	drop := abortTrigger(t, h.dbPath(), "review2_refuse_settle", "BEFORE UPDATE ON task_occurrences")
	defer drop()
	if err := m.Delete(sid); err == nil {
		t.Fatal("test setup: Delete did not hit the SQLite refusal")
	}
	if _, live := m.Get(sid); !live || occNow(t, r, o.ID).State != tasks.OccAssigned {
		t.Fatal("test setup: refused Delete did not retain the session and its run")
	}
	retryID, retryCreated, err := m.CreateAutomationRun(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("original=%s retry=%s created=%t sessions=%d", sid, retryID, retryCreated, countSessions(t, h.base))
	if retryCreated || retryID != sid {
		t.Errorf("refused Delete lost idempotency: retry created=%t, session=%s, want original=%s", retryCreated, retryID, sid)
	}
}

// os.Stat failing to inspect an existing task database is not proof that no
// runs exist there. The session files are in a separate, accessible directory.
func TestReview2MoaDeleteStatFailureRefusesRemoval(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	dbDir := t.TempDir()
	r := tasks.New(filepath.Join(dbDir, tasks.DatabaseName))
	r.SetClock(h.clock.Now)
	h.repos = append(h.repos, r)
	m := NewManager(context.Background(), ManagerConfig{
		Tasks: r, ProviderFactory: func(core.Model) (core.Provider, error) { return h.prov, nil },
		DefaultModel:  core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot: h.root, MoaCfg: noticeTestConfig,
		ConfigLoader: isolatedTestConfigLoader(t, noticeTestConfig), SessionBaseDir: h.base, clock: h.clock,
	})
	h.mgr = m
	schedReviewStopWorkers(t, m)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	sid := round7Created(t, h, r, o, "", false)
	if err := os.Chmod(dbDir, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dbDir, 0o700) }()
	if _, err := os.Stat(r.Path()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("test setup: wanted database stat EACCES, got %v", err)
	}
	deleteErr := m.Delete(sid)
	if err := os.Chmod(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, fileErr := session.FindSessionReadOnly(h.base, sid)
	after := occNow(t, r, o.ID)
	t.Logf("database stat=EACCES; Delete=%v file=%v run=%s/%s", deleteErr, fileErr, after.State, after.Reason)
	if deleteErr == nil || fileErr != nil || after.State != tasks.OccReady {
		t.Errorf("database stat error did not refuse Delete intact: Delete=%v file=%v run=%s/%s", deleteErr, fileErr, after.State, after.Reason)
	}
}

// A saved session whose directory is readable but not writable cannot be
// unlinked. Returning that error must not fail its still-live assignment.
func TestReview2MoaSavedDeleteUnlinkRefusalSettlesNothing(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	m := h.start()
	schedReviewStopWorkers(t, m)
	sid := h.savedSession()
	r := h.repo()
	o := review2MoaAssignedRun(t, h, r, sid)
	_, store, err := session.FindSessionReadOnly(h.base, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(store.Dir(), 0o700) }()
	deleteErr := m.Delete(sid)
	if !errors.Is(deleteErr, os.ErrPermission) {
		t.Fatalf("test setup: expected unlink permission failure, got %v", deleteErr)
	}
	if _, _, err := session.FindSessionReadOnly(h.base, sid); err != nil {
		t.Fatalf("test setup: refused Delete's saved session is not readable: %v", err)
	}
	after, n := occNow(t, r, o.ID), noticeByID(t, r, o.NoticeID)
	t.Logf("Delete=%v; surviving session's run=%s/%s notice=%s/%s", deleteErr, after.State, after.Reason, n.State, n.Reason)
	if after.State != tasks.OccAssigned || n.State != tasks.NoticePending {
		t.Errorf("refused unlink settled surviving work: run=%s/%s notice=%s/%s", after.State, after.Reason, n.State, n.Reason)
	}
}
