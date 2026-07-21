package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/session"
)

func writeVerifyConfig(t *testing.T, dir, body string) {
	t.Helper()
	moaDir := filepath.Join(dir, ".moa")
	if err := os.MkdirAll(moaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moaDir, "verify.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCmdClear_StartsNewSessionKeepsOld verifies /clear gives the web the same
// semantics as the TUI: it starts a fresh session instead of wiping the
// current one in place, so the previous conversation stays recoverable on
// disk (and in the Manager) instead of being destroyed.
func TestCmdClear_StartsNewSessionKeepsOld(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/clear", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("expected OK, got: %s", res.Message)
	}
	if res.NewSessionID == "" {
		t.Fatal("expected a non-empty NewSessionID")
	}
	if res.NewSessionID == sess.ID {
		t.Fatal("NewSessionID should differ from the original session ID")
	}
	if _, ok := mgr.Get(sess.ID); !ok {
		t.Fatal("original session should still exist after /clear")
	}
	if _, ok := mgr.Get(res.NewSessionID); !ok {
		t.Fatal("new session should exist after /clear")
	}
}

func TestCmdVerify_AllPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	writeVerifyConfig(t, dir, `{"checks":[{"name":"noop","command":"true"}]}`)

	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/verify", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("expected OK, got: %s", res.Message)
	}
	if !strings.Contains(res.Message, "passed") {
		t.Fatalf("expected pass message, got: %s", res.Message)
	}
}

func TestCmdVerify_Failure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	writeVerifyConfig(t, dir, `{"checks":[{"name":"boom","command":"false"}]}`)

	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/verify", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected failure result")
	}
	if !strings.Contains(res.Message, "boom") {
		t.Fatalf("expected failed check name in message, got: %s", res.Message)
	}
}

func TestCmdVerify_NoConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/verify", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected non-OK result when no verify config exists")
	}
}

func TestCmdVerify_RejectsWhileGoalActive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.NewLocalBus()
	defer b.Close()
	b.OnQuery(func(bus.GetGoal) (bus.GoalInfo, error) {
		return bus.GoalInfo{Active: true}, nil
	})
	sess.runtime.Bus = b

	res, err := mgr.ExecCommand(sess.ID, "/verify", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Message, "goal mode is active") {
		t.Fatalf("manual verify should be rejected by active goal, got OK=%v message=%q", res.OK, res.Message)
	}
}

// TestCmdVerify_RejectsConcurrent verifies the web /verify command serializes:
// while one verify is running, a second concurrent invocation is rejected
// rather than running verify.Execute in parallel and interleaving events.
func TestCmdVerify_RejectsConcurrent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	// A slow check holds verifyRunning long enough for the second call to land.
	writeVerifyConfig(t, dir, `{"checks":[{"name":"slow","command":"sleep 0.3"}]}`)

	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	var (
		wg       sync.WaitGroup
		first    *CommandResult
		firstErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		first, firstErr = mgr.ExecCommand(sess.ID, "/verify", "")
	}()

	// Let the first verify acquire the serialization flag before the second.
	time.Sleep(50 * time.Millisecond)
	second, secondErr := mgr.ExecCommand(sess.ID, "/verify", "")
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("first verify errored: %v", firstErr)
	}
	if secondErr != nil {
		t.Fatalf("second verify errored: %v", secondErr)
	}
	if !first.OK {
		t.Fatalf("first verify should pass, got: %s", first.Message)
	}
	if second.OK || !strings.Contains(second.Message, "verify already running") {
		t.Fatalf("second verify should be rejected, got OK=%v msg=%q", second.OK, second.Message)
	}
}

func TestCmdRename_SetsManualTitle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/rename My New Title", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("expected OK, got: %s", res.Message)
	}
	if got := sess.title(); got != "My New Title" {
		t.Fatalf("title = %q, want %q", got, "My New Title")
	}
	sess.mu.Lock()
	src := sess.TitleSource
	sess.mu.Unlock()
	if src != session.TitleSourceManual {
		t.Fatalf("TitleSource = %q, want manual", src)
	}
}

func TestCmdRename_EmptyRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/rename", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("expected failure for empty rename, got OK: %s", res.Message)
	}
}

// While a run is in flight, ExecCommand classifies commands by policy: a queued
// command becomes a barrier (OK+Queued), a rejected one returns ErrBusy, and an
// instant one runs immediately.
func TestExecCommand_PolicyGateWhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(delayedResponseHandler(500*time.Millisecond, "slow"))
	mgr := newTestManager(t, ctx, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := mgr.Send(sess.ID, "go", nil, ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool {
		return sessState(sess) == StateRunning
	})

	// PolicyQueue: enqueued as a barrier under the client-minted ID.
	res, err := mgr.ExecCommand(sess.ID, "/compact", "cmd-1")
	if err != nil {
		t.Fatalf("queue command: %v", err)
	}
	if !res.OK || !res.Queued || res.ID != "cmd-1" {
		t.Fatalf("expected queued barrier under cmd-1, got %+v", res)
	}

	// PolicyReject: refused while busy.
	if _, err := mgr.ExecCommand(sess.ID, "/undo", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy for /undo while running, got %v", err)
	}

	// PolicyInstant: runs immediately (reads side state).
	res, err = mgr.ExecCommand(sess.ID, "/permissions", "")
	if err != nil {
		t.Fatalf("instant command: %v", err)
	}
	if !res.OK || res.Queued {
		t.Fatalf("expected instant execution for /permissions, got %+v", res)
	}

	pollUntil(t, 2*time.Second, "idle", func() bool {
		return sessState(sess) == StateIdle || sessState(sess) == StateError
	})
}
