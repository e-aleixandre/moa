package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

	res, err := mgr.ExecCommand(sess.ID, "/clear")
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

	res, err := mgr.ExecCommand(sess.ID, "/verify")
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

	res, err := mgr.ExecCommand(sess.ID, "/verify")
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

	res, err := mgr.ExecCommand(sess.ID, "/verify")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected non-OK result when no verify config exists")
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
		first, firstErr = mgr.ExecCommand(sess.ID, "/verify")
	}()

	// Let the first verify acquire the serialization flag before the second.
	time.Sleep(50 * time.Millisecond)
	second, secondErr := mgr.ExecCommand(sess.ID, "/verify")
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

	res, err := mgr.ExecCommand(sess.ID, "/rename My New Title")
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

	res, err := mgr.ExecCommand(sess.ID, "/rename")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("expected failure for empty rename, got OK: %s", res.Message)
	}
}
