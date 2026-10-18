package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/push"
)

// These probes inject only transport/scheduler barriers. Production transitions
// and the HTTP answer/write/decline handlers run unchanged.
func reviewF2Open(t *testing.T, f *rcFix) *callRequest {
	t.Helper()
	f.mgr.voiceRegistry().opts.Limits.Grace = 2 * time.Second
	own, err := f.mgr.callingOwner(f.osess.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, why := f.mgr.callRequests().open(f.osess.ID, own, "REVIEW-LIFECYCLE", "normal", "REVIEW-QUESTION")
	if req == nil {
		t.Fatalf("open request: %s", why)
	}
	t.Cleanup(func() {
		f.mgr.callRequests().resolve(req.ID, []string{crRinging, crHeld, crAccepted, crConnecting}, crCancelled, "")
	})
	return req
}

func reviewF2GateRelay(t *testing.T, inv *inviteFixture) (<-chan push.EnvelopeContent, func()) {
	t.Helper()
	entered := make(chan push.EnvelopeContent, 1)
	gate := make(chan struct{})
	var releaseOnce, hookOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	inv.relay.mu.Lock()
	inv.relay.onDeliver = func(c push.EnvelopeContent) {
		if c.Kind == push.KindCall {
			hookOnce.Do(func() { entered <- c; <-gate })
		}
	}
	inv.relay.mu.Unlock()
	return entered, release
}

func reviewF2Receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("probe barrier timed out")
		var zero T
		return zero
	}
}

func reviewF2Announce(f *rcFix, req *callRequest) <-chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); f.mgr.callRequests().announce(context.Background(), req) }()
	return done
}

type reviewF2HTTPResult struct {
	code int
	body []byte
	err  error
}

func reviewF2WebAnswer(f *rcFix, req *callRequest) <-chan reviewF2HTTPResult {
	result := make(chan reviewF2HTTPResult, 1)
	go func() {
		code, body, _, err := f.request("POST", "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner())
		result <- reviewF2HTTPResult{code, body, err}
	}()
	return result
}

func reviewF2EndCreated(t *testing.T, f *rcFix, res reviewF2HTTPResult, headers http.Header) {
	t.Helper()
	if res.code != http.StatusCreated {
		return
	}
	var c vpCreated
	if err := json.Unmarshal(res.body, &c); err != nil {
		t.Fatal(err)
	}
	f.created = true
	f.dial(c, headers, false)
	f.mustRequest("POST", "/api/voice/calls/"+c.ID+"/hangup", nil, headers, http.StatusNoContent)
}

// The phone's decline has returned 204 before the browser starts answering.
// Delivery is still awaiting the relay's acknowledgement, not the device.
func TestReviewF2Lifecycle_DeclineBeforeWebAnswerDuringDelivery(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	entered, release := reviewF2GateRelay(t, inv)
	req := reviewF2Open(t, f)
	announced := reviewF2Announce(f, req)
	env := reviewF2Receive(t, entered)
	f.mustRequest("POST", "/api/voice/invites/"+env.Invite+"/decline", nil, inv.voice(0), http.StatusNoContent)
	res := reviewF2Receive(t, reviewF2WebAnswer(f, req))
	release()
	reviewF2Receive(t, announced)
	state, why := f.mgr.callRequests().outcome(req)
	t.Logf("phone decline=204, later web answer=%d, request=%s/%s, invite=%s, upstream_requests=%d", res.code, state, why, inviteState(inv, env.Invite), inv.upstreamRequests())
	reviewF2EndCreated(t, f, res, f.owner())
	if res.err != nil || res.code != http.StatusConflict || state != crWritten {
		t.Errorf("RED: completed iPhone decline lost to a later web answer: code=%d state=%s err=%v", res.code, state, res.err)
	}
}

type reviewF2RoundTripper func(*http.Request) (*http.Response, error)

func (fn reviewF2RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func reviewF2GateProvider(t *testing.T, f *rcFix) (<-chan struct{}, func()) {
	t.Helper()
	reg := f.mgr.voiceRegistry()
	base := reg.opts.HTTPClient.Transport
	entered, gate := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	reg.opts.HTTPClient = &http.Client{Transport: reviewF2RoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.x.ai" && req.URL.Path == "/v1/realtime" {
			entered <- struct{}{}
			select {
			case <-gate:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		return base.RoundTrip(req)
	})}
	reg.opts.Limits.Grace = 2 * time.Second
	return entered, release
}

// A web winner is already opening its provider when RingDevices returns.
// Its invite must still be cancelled, rather than left ringing until TTL.
func TestReviewF2Lifecycle_WebClaimBeforeInviteBindingStopsRing(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	relayEntered, releaseRelay := reviewF2GateRelay(t, inv)
	providerEntered, releaseProvider := reviewF2GateProvider(t, f)
	req := reviewF2Open(t, f)
	announced := reviewF2Announce(f, req)
	env := reviewF2Receive(t, relayEntered)
	answer := reviewF2WebAnswer(f, req)
	reviewF2Receive(t, providerEntered)
	releaseRelay()
	reviewF2Receive(t, announced)
	releaseProvider()
	res := reviewF2Receive(t, answer)
	state := inviteState(inv, env.Invite)
	t.Logf("web answer=%d, request=%s, invite after web answer=%s", res.code, req.State, state)
	reviewF2EndCreated(t, f, res, f.owner())
	if res.err != nil || res.code != http.StatusCreated {
		t.Fatalf("control: web answer=%d err=%v", res.code, res.err)
	}
	if state != inviteCancelled {
		t.Errorf("RED: iPhone invitation still %s after web won and its call opened", state)
	}
}

// A relay can deliver to APNs and then lose its HTTP acknowledgement. The
// answer proves delivery; a lost acknowledgement must not orphan connecting.
func TestReviewF2Lifecycle_PhoneAnswerThenLostRelayAckTerminates(t *testing.T) {
	inv := newInviteFixtureWith(t, 1, true, rcHandlers()...)
	f := rcWrap(t, inv.vpFixture, "ring")
	ackLost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inv.relay.ServeHTTP(httptest.NewRecorder(), r)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(ackLost.Close)
	old := f.mgr.pushVoIP
	for _, registration := range old.Store().All() {
		registration.RelayURL = ackLost.URL
		if err := old.Store().Put(registration); err != nil {
			t.Fatal(err)
		}
	}
	f.mgr.pushVoIP = push.NewNativeSender(old.Store(), ackLost.URL)
	f.mgr.pushVoIP.SetActive(func(string) bool { return true })
	created := make(chan reviewF2HTTPResult, 1)
	inv.relay.mu.Lock()
	inv.relay.onDeliver = func(env push.EnvelopeContent) {
		// The server's narrow voice checker captured the original relay URL.
		// A paired device credential stays valid when this test swaps the relay.
		code, body, _, err := f.request("POST", "/api/voice/invites/"+env.Invite+"/answer", nil, inv.device(0))
		created <- reviewF2HTTPResult{code, body, err}
	}
	inv.relay.mu.Unlock()
	req := reviewF2Open(t, f)
	f.mgr.callRequests().announce(context.Background(), req)
	res := reviewF2Receive(t, created)
	select {
	case <-req.done:
	case <-time.After(200 * time.Millisecond):
	}
	state, why := f.mgr.callRequests().outcome(req)
	t.Logf("phone answer=%d; relay ack=EOF; request=%s/%s; expires=%v", res.code, state, why, req.Expires)
	reviewF2EndCreated(t, f, res, inv.device(0))
	if res.err != nil || res.code != http.StatusCreated {
		t.Fatalf("control: phone answer=%d err=%v", res.code, res.err)
	}
	if state != crAnswered {
		t.Errorf("RED: answered phone left request %s after relay acknowledgement loss", state)
	}
}

// The tool's real context has ended while announce is awaiting delivery.
// Cancellation must withdraw the request before a later phone can claim it.
func TestReviewF2Lifecycle_StopDuringDeliveryWithdrawsBeforePhoneAnswer(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	f.mgr.voiceRegistry().opts.Limits.Grace = 2 * time.Second
	entered, release := reviewF2GateRelay(t, inv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = f.mgr.newRequestCallTool(f.osess.ID, nil, true).Execute(ctx, rcArgs, nil)
	}()
	env := reviewF2Receive(t, entered)
	cancel()
	code, body, _, err := f.request("POST", "/api/voice/invites/"+env.Invite+"/answer", nil, inv.voice(0))
	release()
	reviewF2Receive(t, finished)
	t.Logf("tool context cancelled before phone answer; answer=%d; upstream_requests=%d; requests=%+v", code, inv.upstreamRequests(), f.requests())
	reviewF2EndCreated(t, f, reviewF2HTTPResult{code, body, err}, inv.voice(0))
	if err != nil || code == http.StatusCreated {
		t.Errorf("RED: phone opened provider after tool context had already been cancelled: code=%d err=%v", code, err)
	}
}

// The same cancellation interleaving through the actual owner run and the
// public Stop endpoint, not only an Execute context supplied by a test.
func TestReviewF2Lifecycle_StopHTTPDuringDeliveryWithdrawsBeforePhoneAnswer(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	f.mgr.voiceRegistry().opts.Limits.Grace = 2 * time.Second
	entered, release := reviewF2GateRelay(t, inv)
	f.ask()
	env := reviewF2Receive(t, entered)
	f.mustRequest("POST", "/api/sessions/"+f.osess.ID+"/cancel", nil, f.owner(), http.StatusNoContent)
	code, body, _, err := f.request("POST", "/api/voice/invites/"+env.Invite+"/answer", nil, inv.voice(0))
	release()
	pollUntil(t, 5*time.Second, "cancelled request removed", func() bool { return len(f.requests()) == 0 })
	t.Logf("Stop HTTP returned 204 before phone answer=%d; upstream_requests=%d", code, inv.upstreamRequests())
	reviewF2EndCreated(t, f, reviewF2HTTPResult{code, body, err}, inv.voice(0))
	if err != nil || code == http.StatusCreated {
		t.Errorf("RED: phone opened provider after public Stop returned 204: code=%d err=%v", code, err)
	}
}

// Even once delivery is complete and the tool is selecting on ctx.Done,
// cancelling connecting must also cancel its provider-opening scope.
func TestReviewF2Lifecycle_StopHTTPWhileProviderOpening(t *testing.T) {
	for _, answerer := range []string{"web", "phone"} {
		t.Run(answerer, func(t *testing.T) {
			f, inv := rcRingFixture(t, "ring")
			providerEntered, releaseProvider := reviewF2GateProvider(t, f)
			f.ask()
			visible := f.awaitRequest(crRinging)
			env := f.inviteOf(inv)
			requests := f.mgr.callRequests()
			var req *callRequest
			pollUntil(t, 5*time.Second, "invitation bound after delivery", func() bool {
				requests.mu.Lock()
				defer requests.mu.Unlock()
				req = requests.live[visible.ID]
				return req != nil && req.invite != ""
			})
			headers := f.owner()
			var answer <-chan reviewF2HTTPResult
			if answerer == "web" {
				answer = reviewF2WebAnswer(f, req)
			} else {
				headers = inv.voice(0)
				result := make(chan reviewF2HTTPResult, 1)
				go func() {
					code, body, _, err := f.request("POST", "/api/voice/invites/"+env.Invite+"/answer", nil, headers)
					result <- reviewF2HTTPResult{code, body, err}
				}()
				answer = result
			}
			reviewF2Receive(t, providerEntered)
			f.mustRequest("POST", "/api/sessions/"+f.osess.ID+"/cancel", nil, f.owner(), http.StatusNoContent)
			pollUntil(t, 5*time.Second, "connecting request withdrawn", func() bool { return len(f.requests()) == 0 })
			releaseProvider()
			res := reviewF2Receive(t, answer)
			state, _ := requests.outcome(req)
			t.Logf("Stop HTTP=204 while provider opening; request=%s; later %s answer=%d; upstream_requests=%d", state, answerer, res.code, inv.upstreamRequests())
			reviewF2EndCreated(t, f, res, headers)
			if res.err != nil || res.code == http.StatusCreated {
				t.Errorf("RED: %s opening survived withdrawal and returned a live call: code=%d err=%v", answerer, res.code, res.err)
			}
		})
	}
}

// A lower-level scheduler probe: an opening request has observed the last
// active call, then waits for the request lock. The last call finishes before
// that lock is acquired. A stale snapshot must not create a held request.
func TestReviewF2Lifecycle_LastCallFinishesBeforeHeldInsertion(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	f := rcNewFixture(t, "grok-plan", "ring")
	reg, requests := f.mgr.voiceRegistry(), f.mgr.callRequests()
	own, err := f.mgr.callingOwner(f.osess.ID)
	if err != nil {
		t.Fatal(err)
	}
	active := &voiceCall{id: "REVIEW-LAST-CALL", principal: "REVIEW-PRINCIPAL", sessionID: f.sess.ID}
	reg.mu.Lock()
	reg.calls[active.id] = active
	reg.wg.Add(1)
	reg.mu.Unlock()
	requests.mu.Lock()
	opened := make(chan *callRequest, 1)
	go func() {
		req, _ := requests.open(f.osess.ID, own, "REVIEW", "normal", "REVIEW")
		opened <- req
	}()
	blocked := false
	for range 100 {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, "(*callRequestRegistry).open(") && strings.Contains(stack, "sync.(*Mutex).Lock") {
				blocked = true
				break
			}
		}
		if blocked {
			break
		}
		runtime.Gosched()
	}
	if !blocked {
		requests.mu.Unlock()
		reg.finish(active)
		t.Fatal("control: open did not reach request lock after observing active call")
	}
	requests.mu.Unlock()
	reg.finish(active)
	req := reviewF2Receive(t, opened)
	if req == nil {
		t.Fatal("control: no request opened")
	}
	state, _ := requests.outcome(req)
	isActive, _ := f.mgr.voiceCallState("")
	requests.resolve(req.ID, []string{crRinging, crHeld, crAccepted, crConnecting}, crCancelled, "")
	t.Logf("request inserted after finish=%s; active calls=%v; expiry=%v", state, isActive, req.Expires)
	if !isActive && state == crHeld {
		t.Error("RED: held request has no live call and no future callsEnded event")
	}
}

// Positive lifecycle control uses the actual 30-second product window, not
// a replaced timer: Call chosen, current call hung up, no new call created.
func TestReviewF2Lifecycle_AcceptedExpiresAfterReal30Seconds(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	current := f.call()
	f.ask()
	visible := f.awaitRequest(crHeld)
	f.mustRequest("POST", "/api/voice/requests/"+visible.ID+"/accept", nil, f.owner(), http.StatusNoContent)
	reg := f.mgr.callRequests()
	reg.mu.Lock()
	req := reg.live[visible.ID]
	expires := *req.Expires
	reg.mu.Unlock()
	current.hangup()
	select {
	case <-req.done:
	case <-time.After(callAcceptWindow + 3*time.Second):
		t.Fatal("accepted request never reached a terminal state")
	}
	state, why := reg.outcome(req)
	t.Logf("accepted request terminal=%s/%s at deadline delta=%s", state, why, time.Since(expires))
	if state != crWritten || why != whyMissed {
		t.Fatalf("accepted expiry=%s/%s", state, why)
	}
	if outcome, why := rcOutcome(t, f.writtenAnswered("REVIEW-ANSWER")); outcome != crWritten || why != whyMissed {
		t.Fatalf("tool result=%s/%s", outcome, why)
	}
	all, _ := inv.relay.delivered()
	for _, env := range all {
		if env.Kind == push.KindCall {
			t.Errorf("held/accepted request sent VoIP: %+v", env)
		}
	}
}
