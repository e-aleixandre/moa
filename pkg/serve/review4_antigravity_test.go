package serve

// REVIEW4 tests for 5ec98fa8 (worktree only).

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

func r4Prompts(wire, text string) int {
	b, _ := os.ReadFile(wire)
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, `"session/prompt"`) && strings.Contains(l, `"text":"`+text+`"`) {
			n++
		}
	}
	return n
}

func r4Reload(t *testing.T, mgr *Manager, id string) *ManagedSession {
	t.Helper()
	if err := mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	s, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func r4Settle(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	pollUntil(t, 10*time.Second, "settled", func() bool {
		s, ok := mgr.Get(id)
		if !ok {
			return false
		}
		st := s.info().State
		return st == StateIdle || st == StateError
	})
	if s, _ := mgr.Get(id); s.info().State == StateError {
		t.Fatalf("session in error: %+v", s.info())
	}
}

// Invariant: after Compact and after Start fresh, a reloaded session (its
// transcript read back from disk) still finds the mark: no ErrHistoryDiverged,
// nothing sent twice, nothing lost.
func TestReview4CompactAndStartFreshSurviveReload(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "plain")
	reviewSend(t, mgr, sess.ID, "FIRST")
	r4Settle(t, mgr, sess.ID)
	if err := sess.runtime.Bus.Execute(bus.CompactSession{}); err != nil {
		t.Fatal(err)
	}
	r4Settle(t, mgr, sess.ID)
	for _, m := range sess.runtime.Context().Agent.Messages() {
		t.Logf("  ctx after compact: %s %q", m.Role, f1MsgText(m))
	}
	sess = r4Reload(t, mgr, sess.ID)
	for _, m := range sess.runtime.Context().Agent.Messages() {
		t.Logf("  ctx after compact+reload: %s %q", m.Role, f1MsgText(m))
	}
	reviewSend(t, mgr, sess.ID, "SECOND")
	r4Settle(t, mgr, sess.ID)

	if err := sess.runtime.Bus.Execute(bus.StartFreshSession{}); err != nil {
		t.Fatal(err)
	}
	sess = r4Reload(t, mgr, sess.ID)
	reviewSend(t, mgr, sess.ID, "THIRD")
	r4Settle(t, mgr, sess.ID)

	for _, text := range []string{"FIRST", "SECOND", "THIRD"} {
		if n := r4Prompts(wire, text); n != 1 {
			t.Errorf("%s reached agy %d times", text, n)
		}
	}
	summaries := 0
	for _, m := range sess.runtime.Context().Agent.Messages() {
		if m.Role == "compaction_summary" {
			summaries++
		}
	}
	t.Logf("after reload: %d compaction summaries in context, history %d", summaries, len(sess.History()))
	for _, m := range sess.History() {
		t.Logf("  history: %s %q", m.Role, f1MsgText(m))
	}
	for _, m := range sess.runtime.Context().Agent.Messages() {
		t.Logf("  context: %s %q", m.Role, f1MsgText(m))
	}
}

// /compact right after a Stop: either refused clearly or done, never hung,
// and the stopped turn's tail never shows up afterwards.
func TestReview4CompactRightAfterStop(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "stream")
	reviewSend(t, mgr, sess.ID, "LONG")
	pollUntil(t, 10*time.Second, "streaming", func() bool { return r4Prompts(wire, "LONG") == 1 })
	time.Sleep(100 * time.Millisecond)
	if err := mgr.Cancel(sess.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.runtime.Bus.Execute(bus.CompactSession{}) }()
	select {
	case err := <-done:
		t.Logf("compact right after Stop: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("compact right after Stop hung")
	}
	r4Settle(t, mgr, sess.ID)
	reviewSend(t, mgr, sess.ID, "AFTER")
	r4Settle(t, mgr, sess.ID)
	h := sess.History()
	for _, m := range h {
		if strings.Contains(f1MsgText(m), "late") {
			t.Errorf("the stopped turn's tail reached the transcript: %q", f1MsgText(m))
		}
	}
	if n := r4Prompts(wire, "LONG"); n != 1 {
		t.Errorf("LONG reached agy %d times", n)
	}
	if n := r4Prompts(wire, "AFTER"); n != 1 {
		t.Errorf("AFTER reached agy %d times", n)
	}
}

// Start fresh with no earlier compaction, then reload: the mark (last user
// message before the fresh start) must still be in the reloaded context.
func TestReview4StartFreshThenReload(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "plain")
	reviewSend(t, mgr, sess.ID, "FIRST")
	r4Settle(t, mgr, sess.ID)
	reviewSend(t, mgr, sess.ID, "SECOND")
	r4Settle(t, mgr, sess.ID)
	if err := sess.runtime.Bus.Execute(bus.StartFreshSession{}); err != nil {
		t.Fatal(err)
	}
	for _, m := range sess.runtime.Context().Agent.Messages() {
		t.Logf("  live ctx: %s %q", m.Role, f1MsgText(m))
	}
	sess = r4Reload(t, mgr, sess.ID)
	for _, m := range sess.runtime.Context().Agent.Messages() {
		t.Logf("  reloaded ctx: %s %q", m.Role, f1MsgText(m))
	}
	reviewSend(t, mgr, sess.ID, "THIRD")
	r4Settle(t, mgr, sess.ID)
	for _, text := range []string{"FIRST", "SECOND", "THIRD"} {
		if n := r4Prompts(wire, text); n != 1 {
			t.Errorf("%s reached agy %d times", text, n)
		}
	}
}

// Compact, then Start fresh straight away, then reload (moa restart): the
// next message must still reach agy.
func TestReview4CompactThenStartFreshThenReload(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "plain")
	reviewSend(t, mgr, sess.ID, "FIRST")
	r4Settle(t, mgr, sess.ID)
	if err := sess.runtime.Bus.Execute(bus.CompactSession{}); err != nil {
		t.Fatal(err)
	}
	r4Settle(t, mgr, sess.ID)
	if err := sess.runtime.Bus.Execute(bus.StartFreshSession{}); err != nil {
		t.Fatal(err)
	}
	sess = r4Reload(t, mgr, sess.ID)
	for _, m := range sess.runtime.Context().Agent.Messages() {
		t.Logf("  reloaded ctx: %s %q", m.Role, f1MsgText(m))
	}
	reviewSend(t, mgr, sess.ID, "THIRD")
	time.Sleep(150 * time.Millisecond)
	pollUntil(t, 10*time.Second, "settled", func() bool {
		st := sess.info().State
		return st == StateIdle || st == StateError
	})
	if n := r4Prompts(wire, "THIRD"); n != 1 {
		h := sess.History()
		t.Errorf("THIRD reached agy %d times; state=%s last=%s %q", n, sess.info().State, h[len(h)-1].Role, f1MsgText(h[len(h)-1]))
	}
}
