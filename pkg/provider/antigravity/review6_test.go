package antigravity

// REVIEW6 (worktree only): ReadUsage honours its deadline and leaves no
// process behind, even when agy has a child that keeps stdout open.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeUsageBin(t *testing.T, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOA_AGY_BIN", bin)
}

func TestReview6UsageHungAgyIsKilledAtDeadline(t *testing.T) {
	fakeUsageBin(t, "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := ReadUsage(ctx)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("ReadUsage returned after %v (deadline 1s)", d)
	}
	t.Logf("err=%v", err)
}

func TestReview6UsageGrandchildHoldingStdout(t *testing.T) {
	// agy starting a helper that inherits stdout (a daemon, a language server).
	fakeUsageBin(t, "sleep 20 & sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := ReadUsage(ctx); done <- err }()
	select {
	case err := <-done:
		t.Logf("returned: %v", err)
	case <-time.After(5 * time.Second):
		t.Errorf("ReadUsage still blocked 5s after its 1s deadline: cmd.Run waits for the grandchild's stdout (no WaitDelay)")
	}
}

func TestReview6UsageResetTimeShapes(t *testing.T) {
	for name, rt := range map[string]string{"null": `null`, "empty": `""`, "epoch": `"1970-01-01T00:00:00Z"`, "unix-number": `1767225600`} {
		t.Run(name, func(t *testing.T) {
			fakeUsageBin(t, `echo '{"status":"ok","command":{"data":{"groups":[{"name":"Gemini","buckets":[{"id":"g-5h","window":"5h","remaining_fraction":0.6,"reset_time":`+rt+`},{"id":"g-w","window":"week","remaining_fraction":0.9,"reset_time":"2026-11-30T00:00:00Z"}]}]}}}'`)
			s, err := ReadUsage(context.Background())
			if err != nil {
				t.Errorf("reset_time %s: whole quota lost: %v", rt, err)
				return
			}
			t.Logf("reset_time %s: %d quotas", rt, len(s.Quotas))
		})
	}
}
