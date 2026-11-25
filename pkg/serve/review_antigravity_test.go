package serve

// Tests from the independent review of 9a4df3da. Each one drives a Gemini
// session through the real Manager and the fake agy.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/provider/antigravity"
)

func reviewGemini(t *testing.T, mode string) (*Manager, *ManagedSession, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wire := filepath.Join(t.TempDir(), "wire.log")
	t.Setenv("MOA_AGY_BIN", exe)
	t.Setenv("FAKE_AGY", "1")
	t.Setenv("FAKE_AGY_LOG", wire)
	t.Setenv("FAKE_AGY_MODE", mode)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := antigravity.Default("test")
	mgr := newTestManager(t, ctx, p)
	p.CWD = func(id string) string {
		if s, ok := mgr.Get(id); ok {
			return s.CWD
		}
		return ""
	}
	sess, err := mgr.CreateSession(CreateOpts{Model: "gemini", PermissionMode: "yolo"})
	if err != nil {
		t.Fatal(err)
	}
	return mgr, sess, wire
}

func reviewSend(t *testing.T, mgr *Manager, id, text string) {
	t.Helper()
	if _, _, _, err := mgr.sendValidated(id, text, nil, "", "", nil, false, nil); err != nil {
		t.Fatal(err)
	}
}

func reviewWaitIdle(t *testing.T, mgr *Manager, id string, d time.Duration) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	pollUntil(t, d, "idle", func() bool {
		s, ok := mgr.Get(id)
		return ok && s.info().State == StateIdle
	})
}

func reviewPrompts(wire, text string) int {
	b, _ := os.ReadFile(wire)
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, `"session/prompt"`) && strings.Contains(l, text) {
			n++
		}
	}
	return n
}

// Invariant: after a rewind, agy's conversation matches moa's transcript
// (or the rewind is refused).
func TestReviewRewindDesyncsAgy(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "plain")
	reviewSend(t, mgr, sess.ID, "SECRET-PLAN-A")
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)
	entry := ""
	for _, en := range sess.runtime.Context().Tree.Path() {
		if en.Message.Role == "user" && f1MsgText(en.Message) == "SECRET-PLAN-A" {
			entry = en.ID
		}
	}
	if entry == "" {
		t.Fatal("entry A not found")
	}
	err := mgr.questions.branch(sess, bus.BranchTo{EntryID: entry, Before: true})
	if err != nil {
		t.Logf("rewind refused: %v (acceptable)", err)
		return
	}
	reviewSend(t, mgr, sess.ID, "PLAN-B")
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)
	b, _ := os.ReadFile(wire)
	news := strings.Count(string(b), `"session/new"`) + strings.Count(string(b), `"session/resume"`)
	for _, m := range sess.History() {
		if f1MsgText(m) == "SECRET-PLAN-A" {
			t.Fatal("A still in moa's transcript after the rewind")
		}
	}
	if reviewPrompts(wire, "PLAN-B") == 1 && news == 1 {
		t.Errorf("rewind accepted; moa's transcript no longer has A but B went to the same agy conversation that does (1 session/new, no reset)")
	}
}

// Invariant: moa never sends agy a prompt of its own (system-reminder).
func TestReviewDoomLoopNudgeReachesAgy(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "repeat")
	reviewSend(t, mgr, sess.ID, "check the tree")
	reviewWaitIdle(t, mgr, sess.ID, 15*time.Second)
	time.Sleep(300 * time.Millisecond)
	if n := reviewPrompts(wire, "system-reminder"); n > 0 {
		t.Errorf("moa's doom-loop <system-reminder> was sent to agy as a user prompt (%d)", n)
	}
}

// Invariant: an agy error is never retried automatically with the same prompt
// (antigravity-cli #1708: retry manual, never automatic).
func TestReviewStreamRepairResendsPrompt(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "unknown")
	reviewSend(t, mgr, sess.ID, "ONLY-ONCE")
	// The run ends in error: agy's error is shown, not repaired.
	time.Sleep(100 * time.Millisecond)
	pollUntil(t, 20*time.Second, "error", func() bool {
		s, ok := mgr.Get(sess.ID)
		return ok && s.info().State == StateError
	})
	time.Sleep(300 * time.Millisecond)
	if n := reviewPrompts(wire, "ONLY-ONCE"); n != 1 {
		t.Errorf("agy received the same prompt %d times after one error", n)
	}
}
