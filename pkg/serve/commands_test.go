package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
