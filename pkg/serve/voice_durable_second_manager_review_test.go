package serve

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/voice"
)

func TestDurableReviewSecondManagerReapsLiveCall(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	u := vaNewControllerUpstream()
	u.onMinutes = func(context.Context) error { u.events <- vaEndEvent([]string{"NORMAL-END-DECISION"}, nil); return nil }
	c, made := vaControllerCall(t, f, u)
	cl := f.dial(made, f.owner(), true)
	vaWaitBrief(t, f, u)
	before := f.prov.calls.Load()
	u.events <- voice.Event{Kind: voice.EventTranscript, Role: "user", Text: "LIVE-BEFORE-SECOND-START", Final: true}
	cl.await("live initial transcript", func(v map[string]any) bool { return v["text"] == "LIVE-BEFORE-SECOND-START" })
	s := vwCrashState{Root: f.root, Base: f.mgr.sessionBaseDir, Workspace: f.mgr.workspaceRoot, SessionID: f.sess.ID}
	m, p, stop := vwRestart(t, s, simpleResponseHandler("FALSE-RECOVERY"))
	// The live call holds its journal: the second Manager leaves it alone.
	vpStable(t, func() bool { return p.calls.Load() == 0 }, "second manager woke a live call's session")
	if _, ok := m.Get(f.sess.ID); ok {
		t.Error("second manager loaded the live call's session")
	}
	if n := len(drJournals(t, f.mgr.sessionBaseDir)); n != 1 {
		t.Errorf("live journal after the second manager started: %d, want 1", n)
	}
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	select {
	case <-c.done:
		t.Fatal("original call unexpectedly ended")
	default:
	}
	u.events <- voice.Event{Kind: voice.EventTranscript, Role: "user", Text: "STILL-LIVE-AFTER-SECOND-START", Final: true}
	cl.await("original socket still works", func(v map[string]any) bool { return v["text"] == "STILL-LIVE-AFTER-SECOND-START" })
	cl.hangup()
	vaWaitDone(t, c)
	pollUntil(t, vpDeadline, "original call wake", func() bool { return f.prov.calls.Load() > before })
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("original wake not settled")
	}
	t.Logf("same_call_id=%s false_server_lost_runs=%d original_end_runs=%d", c.id, p.calls.Load(), f.prov.calls.Load()-before)
	if p.calls.Load() > 0 && f.prov.calls.Load() > before {
		t.Error("LIVE_CALL_REAPED_AND_WAKE_DUPLICATED: a second Manager assumes journal owner died; original call stays usable and later wakes again")
	}
}
