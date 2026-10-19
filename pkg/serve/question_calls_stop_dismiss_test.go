package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestRecheckR2_StopThenDismissBeforeCallLinkStillHangsUp(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"requires_tap": true}))
	f.mgr.callRequests().ringOverride = time.Minute
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.mgr.questionCallHooks.finished = func(_ string, st tasks.CallState) {
		if st == tasks.CallDone {
			close(entered)
			<-release
		}
	}
	q := f.post()
	f.clock.Set(time.UnixMilli(q.CallDueAt))
	f.mgr.planner.syncPass()
	req := f.awaitRequest(crRinging)
	var created vpCreated
	_ = json.Unmarshal(f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated), &created)
	f.created = true
	call := f.dial(created, f.owner(), true)
	defer call.hangup()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("call never reached link boundary")
	}
	if _, with := f.mgr.voiceCallState(f.osess.ID); !with {
		t.Fatal("setup: no active call")
	}
	if _, present := f.mgr.escalationCalls.Load(req.ID); present {
		t.Fatal("setup: call already linked")
	}
	if err := f.mgr.Cancel(f.osess.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.question(q.ID); got.State != tasks.QuestionOpen || got.CallState != tasks.CallCancelled {
		t.Fatalf("Stop did not commit: %+v", got)
	}
	if err := f.mgr.questions.Dismiss(context.Background(), f.osess.ID, q.ID); err != nil {
		t.Fatal(err)
	}
	unblock()
	var active *voiceCall
	pollUntil(t, 5*time.Second, "the escalation call link", func() bool {
		v, ok := f.mgr.escalationCalls.Load(req.ID)
		if ok {
			active = v.(*voiceCall)
		}
		return ok
	})
	select {
	case <-active.endCh:
		return
	case <-time.After(time.Second):
		got := f.question(q.ID)
		t.Fatalf("Stop committed before linking, but later Dismiss defeated hangup: no end request; question=%s call_state=%s", got.State, got.CallState)
	}
}
