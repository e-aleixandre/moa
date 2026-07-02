package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
