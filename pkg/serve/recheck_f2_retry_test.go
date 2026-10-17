package serve

import (
	"net/http"
	"sync"
	"testing"
)

// Suspend the first decline at entry to its real Done callback, after its
// invitation state is final. A retry is a distinct real HTTP request; its
// 204 must not precede request-level arbitration and admit a later answer.
func TestRecheckF2_DeclineRetryCompletesBeforeLaterWebAnswer(t *testing.T) {
	f, fixture := rcRingFixture(t, "ring")
	req := reviewF2Open(t, f)
	reviewF2Receive(t, reviewF2Announce(f, req))
	env := f.inviteOf(fixture)
	reg := f.mgr.voiceRegistry()
	table := reg.inviteTable()
	entered, gate := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	table.mu.Lock()
	invite := table.byID[env.Invite]
	done := invite.done
	invite.done = func(out InviteOutcome) {
		if out.State == inviteDeclined {
			entered <- struct{}{}
			<-gate
		}
		done(out)
	}
	table.mu.Unlock()
	first := make(chan reviewF2HTTPResult, 1)
	go func() {
		code, body, _, err := f.request("POST", "/api/voice/invites/"+env.Invite+"/decline", nil, fixture.voice(0))
		first <- reviewF2HTTPResult{code, body, err}
	}()
	reviewF2Receive(t, entered)
	if state := inviteState(fixture, env.Invite); state != inviteDeclined {
		t.Fatalf("control: state=%s before retry", state)
	}
	// This 204 COMPLETES before reviewF2WebAnswer is even launched.
	f.mustRequest("POST", "/api/voice/invites/"+env.Invite+"/decline", nil, fixture.voice(0), http.StatusNoContent)
	web := reviewF2Receive(t, reviewF2WebAnswer(f, req))
	release()
	phone := reviewF2Receive(t, first)
	state, why := f.mgr.callRequests().outcome(req)
	t.Logf("retry decline=204 completed before web started; web=%d request=%s/%s invite=%s upstream=%d first decline=%d", web.code, state, why, inviteState(fixture, env.Invite), fixture.upstreamRequests(), phone.code)
	reviewF2EndCreated(t, f, web, f.owner())
	if phone.err != nil || phone.code != http.StatusNoContent || web.err != nil {
		t.Fatalf("control: HTTP errors: first=%d/%v web=%d/%v", phone.code, phone.err, web.code, web.err)
	}
	if web.code != http.StatusConflict || state != crWritten || why != whyDeclined {
		t.Errorf("RED: a completed native decline retry lost to a later web answer: web=%d request=%s/%s", web.code, state, why)
	}
}
