package serve

// REVIEW2 tests for 72a89c70 (worktree only).

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// Invariant: Stop ends agy's turn. While moa shows agy's approval card no
// relay or watch is running, so nothing calls agySession.stop().
func TestReview2StopDuringApprovalCardLeavesAgyWaiting(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "")
	if err := sess.runtime.Bus.Execute(bus.SetPermissionMode{Mode: "ask"}); err != nil {
		t.Fatal(err)
	}
	reviewSend(t, mgr, sess.ID, "list files")
	pollUntil(t, 10*time.Second, "approval card", func() bool {
		return sess.info().State == StatePermission
	})
	if err := mgr.Cancel(sess.ID); err != nil {
		t.Fatal(err)
	}
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)
	time.Sleep(500 * time.Millisecond)
	b, _ := os.ReadFile(wire)
	w := string(b)
	if !strings.Contains(w, `"session/cancel"`) && !strings.Contains(w, `"outcome":"cancelled"`) && !strings.Contains(w, `"optionId":"no"`) {
		t.Errorf("after Stop on the approval card agy got no cancel and no answer: its turn (and process) stays open until the next message")
	}
	// The next message: the stopped turn resumes first, inside B's reply.
	reviewSend(t, mgr, sess.ID, "now something else")
	time.Sleep(300 * time.Millisecond)
	pollUntil(t, 10*time.Second, "settled", func() bool {
		st := sess.info().State
		return st == StateIdle || st == StatePermission || st == StateError
	})
	h := sess.History()
	last := h[len(h)-1]
	var text strings.Builder
	for _, c := range last.Content {
		text.WriteString(c.Text)
	}
	t.Logf("after the next message: state=%s last role=%s text=%q", sess.info().State, last.Role, text.String())
}
