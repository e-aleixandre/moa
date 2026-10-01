package serve

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func round4DeleteFixture(t *testing.T) (*schedHarness, *tasks.Repo, tasks.Occurrence, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	m := h.start()
	schedReviewStopWorkers(t, m)
	r := h.repo()
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	sid := round7Created(t, h, r, o, "", false)
	closeSession(t, m, sid) // saved: its file is unlinked inside Delete
	store, err := session.FindSessionStoreReadOnly(h.base, sid)
	if err != nil {
		t.Fatal(err)
	}
	return h, r, o, sid, filepath.Join(store.Dir(), sid+".json")
}

// round4AfterLostSettlement checks a Delete whose file went but whose
// settlement did not commit: nothing recreates the session, and the next
// start settles the run as deleted.
func round4AfterLostSettlement(t *testing.T, h *schedHarness, r *tasks.Repo, o tasks.Occurrence, sid string) {
	t.Helper()
	provisioned := h.mgr.planner.provisionNew(bgc, o)
	after := occNow(t, r, o.ID)
	t.Logf("planner before restart: provision=%t session=%q run=%s/%s sessions=%d", provisioned, after.SessionID, after.State, after.Reason, countSessions(t, h.base))
	if provisioned || (after.SessionID != "" && after.SessionID != sid) || countSessions(t, h.base) != 0 {
		t.Fatal("the deleted marked session was recreated before its settlement committed")
	}
	h.mgr.recoverScheduledTasks(bgc)
	after = occNow(t, r, o.ID)
	provisioned = h.mgr.planner.provisionNew(bgc, o)
	t.Logf("after restart recovery: run=%s/%s provision=%t sessions=%d", after.State, after.Reason, provisioned, countSessions(t, h.base))
	if after.State != tasks.OccFailed || after.Reason != tasks.ReasonSessionDeleted {
		t.Errorf("restart did not settle the deleted session's run: %s/%s", after.State, after.Reason)
	}
	if provisioned || countSessions(t, h.base) != 0 {
		t.Error("restart recreated the deleted marked session")
	}
}

// SQLite failing the settlement after the unlink cannot roll the file back.
func TestRound4DeletePostUnlinkSQLFailureCannotRecreate(t *testing.T) {
	h, r, o, sid, path := round4DeleteFixture(t)
	drop := abortTrigger(t, h.dbPath(), "round4_delete_after_unlink", "BEFORE UPDATE OF revision ON tasks_meta")
	deleteErr := h.mgr.Delete(sid)
	drop()
	_, fileErr := os.Stat(path)
	after := occNow(t, r, o.ID)
	t.Logf("after settlement ABORT: Delete=%v file=%v run=%s/%s", deleteErr, fileErr, after.State, after.Reason)
	if fileErr == nil {
		if deleteErr == nil || after.State != tasks.OccReady {
			t.Fatalf("a Delete that kept the file must be refused and settle nothing: %v %+v", deleteErr, after)
		}
		return
	}
	if !errors.Is(fileErr, os.ErrNotExist) {
		t.Fatalf("unexpected session inspection error: %v", fileErr)
	}
	round4AfterLostSettlement(t, h, r, o, sid)
}

const round4DeleteCrashEnv = "MOA_ROUND4_DELETE_CRASH_DIR"

// The real Manager.Delete, killed right after the unlink and before its
// settlement commits.
func TestRound4DeleteCrashChild(t *testing.T) {
	dir := os.Getenv(round4DeleteCrashEnv)
	if dir == "" {
		t.Skip("SIGKILL helper")
	}
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Clean(dir), filepath.Clean(os.TempDir())+string(os.PathSeparator)) {
		t.Fatal("crash helper accepts only an explicit temporary directory")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	h := newSchedHarnessIn(t, dir, newMockProvider(), os.Getenv("MOA_ROUND4_DELETE_CRASH_AT"))
	m := h.start()
	schedReviewStopWorkers(t, m)
	sid := os.Getenv("MOA_ROUND4_DELETE_CRASH_SID")
	if _, live := m.Get(sid); live {
		closeSession(t, m, sid) // restored open at start; Delete it saved
	}
	afterSessionUnlink = func(id string) {
		if id != sid {
			return
		}
		fmt.Println("round4 delete: session unlinked; settlement not committed")
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	err := m.Delete(sid)
	t.Fatalf("child missed the crash window: %v", err)
}

func TestRound4DeletePostUnlinkSIGKILLCannotRecreate(t *testing.T) {
	h, r, o, sid, path := round4DeleteFixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRound4DeleteCrashChild$", "-test.count=1", "-test.timeout=30s", "-test.v")
	cmd.Env = append(os.Environ(), round4DeleteCrashEnv+"="+h.dir,
		"MOA_ROUND4_DELETE_CRASH_AT="+h.clock.Now().Format(time.RFC3339Nano),
		"MOA_ROUND4_DELETE_CRASH_SID="+sid, "MOA_ROUND4_DELETE_CRASH_OCC="+strconv.FormatInt(o.ID, 10))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("child was not killed: %v\n%s", err, out)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL || !strings.Contains(string(out), "session unlinked; settlement not committed") {
		t.Fatalf("child did not die at the post-unlink seam: %v\n%s", err, out)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child did not unlink the session file: %v\n%s", err, out)
	}
	after := occNow(t, r, o.ID)
	t.Logf("after SIGKILL: run=%s/%s", after.State, after.Reason)
	round4AfterLostSettlement(t, h, r, o, sid)
}

// A delete interrupted before its unlink leaves the file: the start withdraws
// the mark and the run goes on to its session. A refused unlink withdraws it
// at once.
func TestRound4DeleteMarkWithoutUnlinkIsUndone(t *testing.T) {
	h, r, o, sid, path := round4DeleteFixture(t)
	if err := r.MarkSessionDiscarding(bgc, sid); err != nil {
		t.Fatal(err)
	}
	if h.mgr.planner.provisionNew(bgc, o) {
		t.Fatal("a run whose session is being deleted was provisioned")
	}
	h.mgr.recoverScheduledTasks(bgc)
	if ds, err := r.SessionDiscards(bgc); err != nil || len(ds) != 0 {
		t.Fatalf("start kept the mark of a session still on disk: %+v %v", ds, err)
	}
	if !h.mgr.planner.provisionNew(bgc, o) || occNow(t, r, o.ID).SessionID != sid {
		t.Fatalf("run did not go on to its surviving session: %+v", occNow(t, r, o.ID))
	}

	if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Dir(path), 0o700) }()
	if err := h.mgr.Delete(sid); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected a refused unlink, got %v", err)
	}
	if ds, err := r.SessionDiscards(bgc); err != nil || len(ds) != 0 {
		t.Fatalf("refused unlink kept its discard mark: %+v %v", ds, err)
	}
}
