package serve

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

const scheduledModel = "anthropic/claude-haiku-4-5-20251001"

func newTarget(t *testing.T, dir string) tasks.Target {
	t.Helper()
	cwd, err := core.CanonicalizePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tasks.Target{Kind: tasks.TargetNew, Project: core.CodebaseKey(cwd), CWD: cwd, Model: scheduledModel, Thinking: "low"}
}

// readyNewRun creates a once template for a new session and consumes its
// slot on time (T0) with no Manager running: occurrence #1 of a fresh DB.
func readyNewRun(t *testing.T, h *schedHarness, r *tasks.Repo, target tasks.Target) tasks.Occurrence {
	t.Helper()
	mkTemplate(t, r, "fresh start", onceAt(h.clock.Now().Add(time.Minute), target, tasks.Delivery{}))
	h.clock.Advance(time.Minute)
	os, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(os) != 1 || os[0].State != tasks.OccReady {
		t.Fatalf("T0 = %+v, %v", os, err)
	}
	return os[0]
}

// markedSession creates a session carrying occurrence occID's markers, as the
// planner would, and closes it.
func markedSession(t *testing.T, h *schedHarness, occID, parentID int64) string {
	t.Helper()
	s, err := h.mgr.CreateSession(CreateOpts{extraMeta: map[string]any{
		session.MetaScheduledOccurrenceID: strconv.FormatInt(occID, 10),
		session.MetaScheduledTaskID:       strconv.FormatInt(parentID, 10),
	}})
	if err != nil {
		t.Fatal(err)
	}
	closeSession(t, h.mgr, s.ID)
	return s.ID
}

func countSessions(t *testing.T, base string) int {
	t.Helper()
	all, err := session.ListAll(base)
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

// TestScheduledNewSessionCrashChild is the crashing process of
// TestScheduledNewSessionCrashBoundaries; it does nothing on its own.
func TestScheduledNewSessionCrashChild(t *testing.T) {
	dir := os.Getenv("MOA_SCHED_CHILD_DIR")
	if dir == "" {
		t.Skip("helper process")
	}
	h := newSchedHarnessIn(t, dir, newMockProvider(simpleResponseHandler("ok")), os.Getenv("MOA_SCHED_CHILD_AT"))
	switch os.Getenv("MOA_SCHED_CRASH") {
	case "after_save":
		h.hooks.afterMarkedSave = func(string, int64) { os.Exit(3) }
	case "after_assign":
		h.hooks.afterAssign = func(int64) { os.Exit(3) }
	}
	h.start()
	h.pass()
	os.Exit(0)
}

func crashChild(t *testing.T, h *schedHarness, boundary string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestScheduledNewSessionCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "MOA_SCHED_CHILD_DIR="+h.dir, "MOA_SCHED_CHILD_AT="+h.clock.Now().Format(time.RFC3339Nano),
		"MOA_SCHED_CRASH="+boundary, "MOA_CONFIG_DIR="+os.Getenv("MOA_CONFIG_DIR"))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("child did not crash at %s: %v\n%s", boundary, err, out)
	}
}

// A process that dies after creating a run's session (marked, first save)
// but before T1, or after T1 but before delivery, leaves exactly one session
// for the run: the restart finds and uses it. A failed T1 commit keeps the
// run ready and the retry reuses the same session.
func TestScheduledNewSessionCrashBoundaries(t *testing.T) {
	for _, boundary := range []string{"after_save", "after_assign"} {
		t.Run(boundary, func(t *testing.T) {
			h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
			r := h.repo()
			target := newTarget(t, h.root)
			o := readyNewRun(t, h, r, target)
			crashChild(t, h, boundary)
			marked := markedSessions(t, h.base, o.ID)
			if len(marked) != 1 {
				t.Fatalf("%d marked sessions after the crash", len(marked))
			}
			want := tasks.OccReady
			if boundary == "after_assign" {
				want = tasks.OccAssigned
			}
			if got := occNow(t, r, o.ID); got.State != want {
				t.Fatalf("run after crash = %+v", got)
			}
			h.start()
			h.pass()
			got := assignedAndDelivered(t, h, r, o.ID)
			marked = markedSessions(t, h.base, o.ID)
			if len(marked) != 1 || countSessions(t, h.base) != 1 || got.SessionID != marked[0].ID {
				t.Fatalf("sessions = %+v, run = %+v", marked, got)
			}
			meta := marked[0].Metadata
			if meta[session.MetaModel] != scheduledModel || meta[session.MetaThinking] != "low" || meta[session.MetaCWD] != target.CWD ||
				meta[session.MetaScheduledTaskID] != strconv.FormatInt(o.ScheduleTaskID, 10) || meta[session.MetaOrigin] != scheduledOrigin {
				t.Fatalf("session metadata = %+v", meta)
			}
			if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM tasks WHERE place = 'agent'"); n != 1 {
				t.Fatalf("%d children", n)
			}
			if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM task_notifications"); n != 1 {
				t.Fatalf("%d notices", n)
			}
		})
	}
	t.Run("outbox_failure", func(t *testing.T) {
		h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
		r := h.repo()
		o := readyNewRun(t, h, r, newTarget(t, h.root))
		drop := abortTrigger(t, h.dbPath(), "no_outbox", "BEFORE INSERT ON task_notifications")
		h.start()
		h.pass()
		if got := occNow(t, r, o.ID); got.State != tasks.OccReady {
			t.Fatalf("run after failed T1 = %+v", got)
		}
		first := markedSessions(t, h.base, o.ID)
		if len(first) != 1 {
			t.Fatalf("%d marked sessions", len(first))
		}
		drop()
		h.pass()
		got := assignedAndDelivered(t, h, r, o.ID)
		if got.SessionID != first[0].ID || countSessions(t, h.base) != 1 {
			t.Fatalf("retry made another session: %+v", got)
		}
	})
}

// The roster scan is the authority for "not created yet": when it cannot be
// complete (a damaged file that may be the run's session) or is ambiguous
// (two sessions claim the run), nothing is created and the run fails
// visibly, keeping the evidence.
func TestScheduledNewSessionScanFailsClosed(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
		r := h.repo()
		o := readyNewRun(t, h, r, newTarget(t, h.root))
		broken := filepath.Join(h.base, "damaged", "broken.json")
		if err := os.MkdirAll(filepath.Dir(broken), 0o700); err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"id":"x","metadata":{"` + session.MetaScheduledOccurrenceID + `":"` + strconv.FormatInt(o.ID, 10) + `"`)
		if err := os.WriteFile(broken, body, 0o600); err != nil {
			t.Fatal(err)
		}
		h.start()
		h.pass()
		got := occNow(t, r, o.ID)
		if got.State != tasks.OccFailed || got.Reason != reasonDestinationUncertain {
			t.Fatalf("run = %+v", got)
		}
		if countSessions(t, h.base) != 0 {
			t.Fatal("a session was created from an incomplete scan")
		}
		if b, err := os.ReadFile(broken); err != nil || string(b) != string(body) {
			t.Fatal("evidence changed")
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
		h.start()
		a := markedSession(t, h, 1, 1)
		b := markedSession(t, h, 1, 1)
		h.stop()
		r := h.repo()
		o := readyNewRun(t, h, r, newTarget(t, h.root))
		if o.ID != 1 {
			t.Fatalf("occurrence id = %d", o.ID)
		}
		h.start()
		h.pass()
		got := occNow(t, r, o.ID)
		if got.State != tasks.OccFailed || got.Reason != reasonDestinationAmbiguous {
			t.Fatalf("run = %+v", got)
		}
		if n := countSessions(t, h.base); n != 2 {
			t.Fatalf("%d sessions, want the two claimants", n)
		}
		for _, id := range []string{a, b} {
			if _, _, err := session.FindSessionReadOnly(h.base, id); err != nil {
				t.Fatalf("claimant %s: %v", id, err)
			}
		}
	})
}
