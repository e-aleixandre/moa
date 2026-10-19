package serve

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// The transport barrier is BEFORE the real fake-provider HTTP handshake.
// Stop completes and the request disappears before we allow any upstream I/O.
func TestRecheckF2_StopWithdrawnBeforeUpstreamHandshake(t *testing.T) {
	for _, answerer := range []string{"web", "phone"} {
		t.Run(answerer, func(t *testing.T) {
			f, inv := rcRingFixture(t, "ring")
			entered, release := reviewF2GateProvider(t, f)
			f.ask()
			visible := f.awaitRequest(crRinging)
			env := f.inviteOf(inv)
			requests := f.mgr.callRequests()
			var req *callRequest
			pollUntil(t, 5*time.Second, "invitation bound", func() bool {
				requests.mu.Lock()
				defer requests.mu.Unlock()
				req = requests.live[visible.ID]
				return req != nil && req.invite != ""
			})
			var answer <-chan reviewF2HTTPResult
			if answerer == "web" {
				answer = reviewF2WebAnswer(f, req)
			} else {
				result := make(chan reviewF2HTTPResult, 1)
				go func() {
					code, body, _, err := f.request("POST", "/api/voice/invites/"+env.Invite+"/answer", nil, inv.voice(0))
					result <- reviewF2HTTPResult{code, body, err}
				}()
				answer = result
			}
			reviewF2Receive(t, entered)
			if n := inv.upstreamRequests(); n != 0 {
				t.Fatalf("control: %d upstream requests before transport release", n)
			}
			f.mustRequest("POST", "/api/sessions/"+f.osess.ID+"/cancel", nil, f.owner(), http.StatusNoContent)
			pollUntil(t, 5*time.Second, "request withdrawn", func() bool { return len(f.requests()) == 0 })
			if state, _ := requests.outcome(req); state != crCancelled {
				t.Fatalf("control: request is %s before transport release", state)
			}
			release()
			res := reviewF2Receive(t, answer)
			n := inv.upstreamRequests()
			t.Logf("Stop=204, request=cancelled, upstream handshakes before release=0, AFTER withdrawal=%d; %s answer=%d body=%s", n, answerer, res.code, res.body)
			if res.err != nil || res.code == http.StatusCreated {
				t.Fatalf("control: expected withdrawn answer refusal, got %d/%v", res.code, res.err)
			}
			if n != 0 {
				t.Errorf("RED: %s opened an upstream after request was already withdrawn", answerer)
			}
		})
	}
}

// A callback-tail barrier is a scheduler suspension between the production
// committer's return and register, not a different transition. Real registry
// Close wins publication while the already-completed tool has said answered.
func TestRecheckF2_CommitAnsweredBeforeRegisterFails(t *testing.T) {
	f, fixture := rcRingFixture(t, "ring")
	f.ask()
	visible := f.awaitRequest(crRinging)
	env := f.inviteOf(fixture)
	requests := f.mgr.callRequests()
	var req *callRequest
	pollUntil(t, 5*time.Second, "invitation bound", func() bool {
		requests.mu.Lock()
		defer requests.mu.Unlock()
		req = requests.live[visible.ID]
		return req != nil && req.invite != ""
	})
	reg := f.mgr.voiceRegistry()
	table := reg.inviteTable()
	committed, gate := make(chan bool, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	table.mu.Lock()
	invite := table.byID[env.Invite]
	// Adapted to the single decision point: suspend right before it, so
	// Close wins and registration fails inside the decision.
	answerTo := *invite.answerTo
	publish := answerTo.publish
	answerTo.publish = func(c *voiceCall, register func() bool) (bool, bool) {
		committed <- true
		<-gate
		return publish(c, register)
	}
	invite.answerTo = &answerTo
	table.mu.Unlock()
	answer := make(chan reviewF2HTTPResult, 1)
	go func() {
		code, body, _, err := f.request("POST", "/api/voice/invites/"+env.Invite+"/answer", nil, fixture.voice(0))
		answer <- reviewF2HTTPResult{code, body, err}
	}()
	if ok := reviewF2Receive(t, committed); !ok {
		t.Fatal("control: request commit lost before shutdown")
	}
	reg.mu.Lock()
	publishedBeforeClose := len(reg.calls)
	reg.mu.Unlock()
	closing := make(chan struct{})
	go func() { defer close(closing); reg.Close() }()
	pollUntil(t, 5*time.Second, "real registry Close entered", func() bool {
		reg.mu.Lock()
		defer reg.mu.Unlock()
		return reg.closed
	})
	release()
	res := reviewF2Receive(t, answer)
	reviewF2Receive(t, closing)
	// The tool now asks in writing (and waits for that answer), so the
	// request's outcome is what the run was told.
	state, why := requests.outcome(req)
	outcome := state
	reg.mu.Lock()
	calls := len(reg.calls)
	reg.mu.Unlock()
	t.Logf("tool outcome before publication=%s, calls before Close=%d, Close wins -> answer=%d %s, request=%s/%s, calls=%d", outcome, publishedBeforeClose, res.code, res.body, state, why, calls)
	if res.err != nil || res.code != http.StatusServiceUnavailable || publishedBeforeClose != 0 || calls != 0 {
		t.Fatalf("control: publication failure not reproduced: code=%d calls=%d/%d err=%v", res.code, publishedBeforeClose, calls, res.err)
	}
	if outcome == crAnswered || state == crAnswered {
		t.Error("RED: owner was told answered although registration failed and no call ever existed")
	}
}

// A callback-entry barrier permits the same scheduling gap as suspension
// immediately after the invite mutex is released, before Done arbitrates.
func TestRecheckF2_DeclineTransitionAndWebClaimHaveOneWinner(t *testing.T) {
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
	declined := make(chan reviewF2HTTPResult, 1)
	go func() {
		code, body, _, err := f.request("POST", "/api/voice/invites/"+env.Invite+"/decline", nil, fixture.voice(0))
		declined <- reviewF2HTTPResult{code, body, err}
	}()
	reviewF2Receive(t, entered)
	if state := inviteState(fixture, env.Invite); state != inviteDeclined {
		t.Fatalf("control: invite state=%s before web claim", state)
	}
	web := reviewF2Receive(t, reviewF2WebAnswer(f, req))
	release()
	phone := reviewF2Receive(t, declined)
	state, why := f.mgr.callRequests().outcome(req)
	t.Logf("native transition=declined before web claim; overlapping decline HTTP=%d, web HTTP=%d, request=%s/%s, invite=%s", phone.code, web.code, state, why, inviteState(fixture, env.Invite))
	reviewF2EndCreated(t, f, web, f.owner())
	if phone.err != nil || web.err != nil {
		t.Fatalf("control: HTTP errors: phone=%v web=%v", phone.err, web.err)
	}
	if phone.code == http.StatusNoContent && web.code == http.StatusCreated {
		t.Error("RED: decline and answer both reported success; request and invitation disagree")
	}
}
