package serve

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// syncTapFixture builds an owner and an owner-origin child blocked on a
// synchronous requires_tap question (the blocking ask_user path).
func syncTapFixture(t *testing.T) (owner, child *ManagedSession, ask bus.PendingAskInfo) {
	t.Helper()
	pinSyncAskUser(t)
	owner, child, askID := f1bisOwnerTapChild(t)
	p, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](child.runtime.Bus, bus.GetPendingApproval{})
	if p.Ask == nil || p.Ask.ID != askID {
		t.Fatalf("pending ask = %+v, want %s", p.Ask, askID)
	}
	if p.Ask.Async || !p.Ask.RequiresTap {
		t.Fatalf("fixture is not a synchronous tap question: %+v", p.Ask)
	}
	if st := child.runtime.State.Current(); st != bus.StatePermission {
		t.Fatalf("child state = %s, want %s", st, bus.StatePermission)
	}
	return owner, child, *p.Ask
}

func pendingSteers(t *testing.T, child *ManagedSession) []core.SteerItem {
	t.Helper()
	steers, err := bus.QueryTyped[bus.GetPendingSteers, []core.SteerItem](child.runtime.Bus, bus.GetPendingSteers{})
	if err != nil {
		t.Fatalf("query pending steers: %v", err)
	}
	return steers
}

func assertStillBlockedOnTap(t *testing.T, child *ManagedSession, ask bus.PendingAskInfo, what string) {
	t.Helper()
	f1bisAssertTapPending(t, child, ask.ID, what)
	if p, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](child.runtime.Bus, bus.GetPendingApproval{}); p.Ask == nil || p.Ask.ID != ask.ID || p.Ask.Async {
		t.Errorf("%s: pending ask = %+v, want sync %s", what, p.Ask, ask.ID)
	}
	if st := child.runtime.State.Current(); st != bus.StatePermission {
		t.Errorf("%s: state = %s, want %s", what, st, bus.StatePermission)
	}
}

func humanTapsAndReleases(t *testing.T, child *ManagedSession, ask bus.PendingAskInfo) {
	t.Helper()
	user := askuser.Responder{Kind: askuser.ResponderUser, Via: "device:tap-fixture"}
	if err := child.runtime.Bus.Execute(bus.ResolveAskUser{AskID: ask.ID, Answers: []string{"Yes, by tap"}, Responder: user}); err != nil {
		t.Fatalf("human tap failed: %v", err)
	}
	pollUntil(t, 5*time.Second, "the human tap releasing the session", func() bool {
		p, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](child.runtime.Bus, bus.GetPendingApproval{})
		return p.Ask == nil && child.runtime.State.Current() != bus.StatePermission
	})
	got := f1bisRecordedReply(t, child, ask.ID)
	if got.responder != user {
		t.Fatalf("recorded responder %+v, want the human %+v", got.responder, user)
	}
}

// A send that cannot skip the question
// must not admit its message either. Today sessions send queues the steer
// first and only then tries the skip, which the tap rule refuses, so each call
// leaves an unread steer behind a session that stays blocked. The proposal is
// that these sends fail without queueing anything. The exact wording of the
// error is not asserted, and nothing here bypasses the tap authority: the
// human's tap still has to release the session.
func TestOwnerSendToSyncTapQuestionAdmitsNoMessage(t *testing.T) {
	owner, child, ask := syncTapFixture(t)
	base := len(pendingSteers(t, child))

	for i := 1; i <= 3; i++ {
		text := fmt.Sprintf("diag-unique-send-%d", i)
		res := runSessionsTool(t, owner, map[string]any{"action": "send", "session_id": child.ID, "text": text})
		t.Logf("send %d: %s", i, toolText(res))
		if !res.IsError {
			t.Errorf("send %d did not fail: %s", i, toolText(res))
		}
		if msg := toolText(res); !strings.Contains(msg, ask.ID) || !strings.Contains(msg, "only the user") {
			t.Errorf("send %d error does not name the ask and the user: %s", i, msg)
		}
		assertStillBlockedOnTap(t, child, ask, fmt.Sprintf("send %d", i))
		if got := len(pendingSteers(t, child)) - base; got != 0 {
			t.Errorf("after send %d: %d new steers queued, want 0 (%+v)", i, got, pendingSteers(t, child))
		}
	}

	res := runSessionsTool(t, owner, map[string]any{
		"action": "answer", "session_id": child.ID, "ask_id": ask.ID, "answers": []any{"Yes, deploy"},
	})
	t.Logf("owner answer: %s", toolText(res))
	if !res.IsError {
		t.Errorf("owner answer on the tap question did not fail: %s", toolText(res))
	}
	if msg := toolText(res); !strings.Contains(msg, "only the user") {
		t.Errorf("answer error does not say only the user can answer: %s", msg)
	}
	assertStillBlockedOnTap(t, child, ask, "owner answer")
	if got := len(pendingSteers(t, child)) - base; got != 0 {
		t.Errorf("after answer: %d new steers, want 0", got)
	}

	humanTapsAndReleases(t, child, ask)
}

// GREEN diagnostic: the owner's answer on a synchronous tap question is
// refused without consuming it or touching the queue, and the human's tap
// then releases the session.
func TestOwnerAnswerToSyncTapQuestionIsRefusedThenHumanReleases(t *testing.T) {
	owner, child, ask := syncTapFixture(t)
	base := len(pendingSteers(t, child))

	res := runSessionsTool(t, owner, map[string]any{
		"action": "answer", "session_id": child.ID, "ask_id": ask.ID, "answers": []any{"Yes, deploy"},
	})
	t.Logf("owner answer: %s", toolText(res))
	if !res.IsError {
		t.Fatalf("owner answer did not fail: %s", toolText(res))
	}
	assertStillBlockedOnTap(t, child, ask, "owner answer")
	if got := len(pendingSteers(t, child)) - base; got != 0 {
		t.Errorf("answer queued %d steers, want 0", got)
	}
	humanTapsAndReleases(t, child, ask)
}
