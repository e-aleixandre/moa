package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// Review evidence only. Every provider and HTTP request stays inside the
// existing voice fixture; questions use the fixture's real SQLite database.
func TestReviewForYouCalls_StopHangsUpActiveEscalation(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"requires_tap": true}))
	f.mgr.callRequests().ringOverride = time.Minute
	q := f.post()
	f.clock.Set(time.UnixMilli(q.CallDueAt))
	f.mgr.planner.syncPass()
	req := f.awaitRequest(crRinging)
	var created vpCreated
	_ = json.Unmarshal(f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated), &created)
	f.created = true
	call := f.dial(created, f.owner(), false)
	defer call.hangup()
	if st := f.waitEnded(); st != tasks.CallDone {
		t.Fatalf("setup: answered call ended %q", st)
	}
	_, _ = f.mgr.CancelWithDiscardedSteers(f.osess.ID, "")
	if got := f.question(q.ID); got.State != tasks.QuestionOpen {
		t.Fatalf("Stop closed the question: %q", got.State)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, with := f.mgr.voiceCallState(f.osess.ID); !with {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Stop left active escalation call %s registered; question state=%s call_state=%s", created.ID, f.question(q.ID).State, f.question(q.ID).CallState)
}

func TestReviewForYouCalls_NeverAfterClaimIsRespected(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
	q := f.post()
	f.mgr.questionCallHooks.afterClaim = func(string, string) bool {
		f.mustRequest(http.MethodPatch, "/api/owners/"+f.own.ID, map[string]any{"call_level": "never"}, f.owner(), http.StatusOK)
		return true
	}
	f.pass(time.UnixMilli(q.CallDueAt))
	st := f.waitEnded()
	if n := f.announced.Load(); n != 0 {
		t.Fatalf("owner changed to Never before placement but rang %d times; final state=%s", n, st)
	}
}

func TestReviewForYouCalls_DeleteOwnerWithdrawsRingingEscalation(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
	f.mgr.callRequests().ringOverride = time.Minute
	q := f.post()
	f.clock.Set(time.UnixMilli(q.CallDueAt))
	f.mgr.planner.syncPass()
	req := f.awaitRequest(crRinging)
	defer f.mgr.callRequests().withdraw(req.ID)
	if err := f.mgr.DeleteOwner(f.own.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.question(q.ID); got.State != tasks.QuestionInvalidated || got.CallState != tasks.CallCancelled {
		t.Fatalf("delete did not settle question: %+v", got)
	}
	if live := f.requests(); len(live) != 0 {
		t.Fatalf("deleted owner still has ringing request: %+v", live)
	}
}

// The shared placement API publishes a request before its Opened callback
// validates the durable row. Pause at that boundary, after an answer committed
// before placement, and use the actual client entry point to answer the call.
func TestReviewForYouCalls_FinalCheckCannotPublishAnsweredQuestion(t *testing.T) {
	for _, how := range []string{"answer", "dismiss"} {
		t.Run(how, func(t *testing.T) {
			f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
			f.mgr.callRequests().ringOverride = time.Minute
			q := f.post()
			f.clock.Set(time.UnixMilli(q.CallDueAt))
			attempt := newVoiceCallID()
			if won, err := f.mgr.tasks.ClaimCallEscalation(context.Background(), q.ID, attempt); err != nil || !won {
				t.Fatalf("claim: %v %v", won, err)
			}
			if how == "answer" {
				if err := f.mgr.questions.Answer(context.Background(), f.osess.ID, q.ID, []string{"Yes"}, askuser.Responder{Kind: askuser.ResponderUser}); err != nil {
					t.Fatal(err)
				}
			} else if err := f.mgr.questions.Dismiss(context.Background(), f.osess.ID, q.ID); err != nil {
				t.Fatal(err)
			}
			own, err := f.mgr.callingOwner(f.osess.ID)
			if err != nil {
				t.Fatal(err)
			}
			checking, release, ended := make(chan struct{}), make(chan struct{}), make(chan string, 1)
			go func() {
				_, st, _ := f.mgr.callRequests().place(context.Background(), callPlacement{
					ID: attempt, SessionID: f.osess.ID, Owner: own, QuestionID: q.ID, Now: f.clock.Now(),
					Opened: func(*callRequest) bool {
						close(checking)
						<-release
						cur, err := f.mgr.tasks.GetQuestion(context.Background(), q.ID)
						return err == nil && cur.State == tasks.QuestionOpen && cur.CallState == tasks.CallCalling && cur.CallAttempt == attempt
					},
				})
				ended <- st
			}()
			<-checking
			live := f.requests()
			code, body, _, err := f.request(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": attempt}, f.owner())
			close(release)
			st := <-ended
			if code == http.StatusCreated {
				var c vpCreated
				_ = json.Unmarshal(body, &c)
				f.created = true
				defer f.dial(c, f.owner(), false).hangup()
			}
			if err != nil {
				t.Fatal(err)
			}
			if code == http.StatusCreated || len(live) != 0 {
				t.Fatalf("%s committed before placement, but pre-check feed exposed %d request(s), call API returned %d, placement ended %s", how, len(live), code, st)
			}
		})
	}
}

func TestReviewForYouCalls_WriteInsteadDoesNotAnswerQuestion(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"requires_tap": true}))
	f.mgr.callRequests().ringOverride = time.Minute
	q := f.post()
	f.clock.Set(time.UnixMilli(q.CallDueAt))
	f.mgr.planner.syncPass()
	req := f.awaitRequest(crRinging)
	f.mustRequest(http.MethodPost, "/api/voice/requests/"+req.ID+"/write", nil, f.owner(), http.StatusNoContent)
	f.waitEnded()
	got := f.question(q.ID)
	if got.State != tasks.QuestionOpen || len(got.Answers) != 0 || got.EventID != "" {
		t.Fatalf("Write it instead answered the question: %+v", got)
	}
	if got.CallState != tasks.CallDone {
		t.Fatalf("call state=%s, want terminal done", got.CallState)
	}
	f.pass(f.clock.Now().Add(24 * time.Hour))
	if f.announced.Load() != 1 {
		t.Fatal("declined call retried")
	}
}

func TestReviewForYouCalls_SpringDSTUsesFirstAllowedInstant(t *testing.T) {
	for _, tc := range []struct{ name, zone, now, want string }{
		{"Madrid", "Europe/Madrid", "2026-03-29T00:00:00Z", "2026-03-29T01:00:00Z"},
		{"NewYork", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-08T07:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := core.VoiceCallsConfig{Hours: &core.VoiceCallHours{From: "02:30", Until: "04:00", TZ: tc.zone}}
			want := mustUTC(tc.want)
			if !callHoursAllow(calls.Hours, want) || callHoursAllow(calls.Hours, want.Add(-time.Nanosecond)) {
				t.Fatal("invalid expected boundary")
			}
			got := nextCallWindow(calls, mustUTC(tc.now))
			if !got.Equal(want) {
				t.Fatalf("next window=%s, want first allowed instant %s", got.UTC(), want)
			}
		})
	}
}

func TestReviewForYouCalls_BusyDefersWithoutRingingThenRingsOnce(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
	q := f.post()
	own, err := f.mgr.callingOwner(f.osess.ID)
	if err != nil {
		t.Fatal(err)
	}
	reg := f.mgr.callRequests()
	reg.ringOverride = time.Minute
	blocking, why := reg.open(f.osess.ID, own, "blocking call", "urgent", "another question")
	if blocking == nil {
		t.Fatalf("setup call: %s", why)
	}
	defer reg.withdraw(blocking.ID)
	f.pass(time.UnixMilli(q.CallDueAt))
	got := f.question(q.ID)
	want := q.CallDueAt + (2 * time.Minute).Milliseconds()
	if got.CallState != tasks.CallPending || got.CallDueAt != want || f.announced.Load() != 0 {
		t.Fatalf("busy result: call_state=%s due=%d rings=%d", got.CallState, got.CallDueAt, f.announced.Load())
	}
	reg.withdraw(blocking.ID)
	reg.ringOverride = 100 * time.Millisecond
	f.pass(time.UnixMilli(want))
	if st := f.waitEnded(); st != tasks.CallMissed || f.announced.Load() != 1 {
		t.Fatalf("after busy cleared: state=%s rings=%d", st, f.announced.Load())
	}
	f.pass(f.clock.Now().Add(24 * time.Hour))
	if f.announced.Load() != 1 {
		t.Fatal("missed call retried")
	}
}

func TestReviewForYouCalls_LiveVoiceCannotAnswerTapQuestion(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"requires_tap": true}))
	f.mgr.callRequests().ringOverride = time.Minute
	q := f.post()
	f.clock.Set(time.UnixMilli(q.CallDueAt))
	f.mgr.planner.syncPass()
	req := f.awaitRequest(crRinging)
	var created vpCreated
	_ = json.Unmarshal(f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated), &created)
	f.created = true
	call := f.dial(created, f.owner(), false)
	defer call.hangup()
	f.waitEnded()
	p := f.up.peer(t, "grok-plan")
	p.tool(t, "voice-deploy", "answer_question", map[string]any{"ask_id": q.ID, "answer": "Yes"})
	if result := p.result(t, "voice-deploy"); !vpError(result) {
		t.Fatalf("voice answered tap-only question: %s", vpJSON(result))
	}
	for _, kind := range []string{askuser.ResponderVoice, askuser.ResponderOwner, askuser.ResponderAutomation} {
		if err := f.mgr.questions.Answer(context.Background(), f.osess.ID, q.ID, []string{"Yes"}, askuser.Responder{Kind: kind}); err == nil {
			t.Fatalf("%s answered tap question", kind)
		}
	}
	if got := f.question(q.ID); got.State != tasks.QuestionOpen || got.EventID != "" {
		t.Fatalf("voice authorized question: %+v", got)
	}
	f.mustRequest(http.MethodPost, "/api/sessions/"+f.osess.ID+"/ask", map[string]any{"id": q.ID, "answers": []string{"Yes"}}, f.owner(), http.StatusNoContent)
	if got := f.question(q.ID); got.State != tasks.QuestionAnswered || got.Responder.Kind != askuser.ResponderUser {
		t.Fatalf("human answer failed: %+v", got)
	}
}

func TestReviewForYouCalls_TwoConcurrentFiresRingExactlyOnce(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
	q := f.post()
	f.clock.Set(time.UnixMilli(q.CallDueAt))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; f.mgr.fireQuestionCall(context.Background(), q) }()
	}
	close(start)
	wg.Wait()
	if n := f.announced.Load(); n != 1 {
		t.Fatalf("concurrent fires rang %d times", n)
	}
	if got := f.question(q.ID); got.CallState != tasks.CallMissed {
		t.Fatalf("call_state=%s", got.CallState)
	}
}

func TestReviewForYouCalls_CancelWhileDeliveringLeavesNoInvitation(t *testing.T) {
	for _, how := range []string{"answer", "dismiss", "stop"} {
		t.Run(how, func(t *testing.T) {
			pinAsyncAskUser(t)
			var hs []mockHandler
			for range 24 {
				hs = append(hs, qcHandler(qcArgs(map[string]any{"call_after_minutes": 10})))
			}
			inv := newInviteFixtureWith(t, 1, true, hs...)
			f := &qcFix{rcFix: rcWrap(t, inv.vpFixture, "ring"), ended: make(chan tasks.CallState, 1)}
			f.mgr.callRequests().ringOverride = time.Minute
			q := f.post()
			f.mgr.tasks.SetClock(func() time.Time { return time.UnixMilli(q.CallDueAt) })
			f.mgr.questionCallHooks.finished = func(_ string, st tasks.CallState) { f.ended <- st }
			var once sync.Once
			envelope := make(chan push.EnvelopeContent, 1)
			inv.relay.mu.Lock()
			inv.relay.onDeliver = func(c push.EnvelopeContent) {
				if c.Kind != push.KindCall {
					return
				}
				once.Do(func() {
					switch how {
					case "answer":
						if err := f.mgr.questions.Answer(context.Background(), f.osess.ID, q.ID, []string{"Yes"}, askuser.Responder{Kind: askuser.ResponderUser}); err != nil {
							t.Error(err)
						}
					case "dismiss":
						if err := f.mgr.questions.Dismiss(context.Background(), f.osess.ID, q.ID); err != nil {
							t.Error(err)
						}
					case "stop":
						_ = f.mgr.Cancel(f.osess.ID)
					}
					envelope <- c
				})
			}
			inv.relay.mu.Unlock()
			go f.mgr.fireQuestionCall(context.Background(), q)
			if st := f.waitEnded(); st != tasks.CallCancelled {
				t.Fatalf("ended %s", st)
			}
			select {
			case env := <-envelope:
				if st := inviteState(inv, env.Invite); st != "cancelled" {
					t.Fatalf("stale invitation %s: %s", env.Invite, st)
				}
				f.mustRequest(http.MethodPost, "/api/voice/invites/"+env.Invite+"/answer", map[string]any{}, inv.voice(0), http.StatusConflict)
			default:
				t.Fatal("fixture never delivered a VoIP invitation")
			}
			if live := f.requests(); len(live) != 0 {
				t.Fatalf("stale requests: %+v", live)
			}
		})
	}
}

func TestReviewForYouCalls_StartupInterruptsEstablishedEscalation(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"requires_tap": true}))
	f.mgr.callRequests().ringOverride = time.Minute
	q := f.post()
	f.clock.Set(time.UnixMilli(q.CallDueAt))
	f.mgr.planner.syncPass()
	req := f.awaitRequest(crRinging)
	var created vpCreated
	_ = json.Unmarshal(f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated), &created)
	f.created = true
	call := f.dial(created, f.owner(), false)
	defer call.hangup()
	f.waitEnded()
	if _, with := f.mgr.voiceCallState(f.osess.ID); !with {
		t.Fatal("setup: escalation call is not established")
	}

	// Simulate only the startup recovery boundary through a second real DB
	// handle. No os.Exit: the first process's fake call stays live as evidence
	// that the durable state was finalized before that call ended.
	next := tasks.New(f.mgr.tasks.Path())
	defer next.Close()
	ids, err := next.InterruptCallEscalations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := next.GetQuestion(context.Background(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != tasks.QuestionOpen || recovered.CallState != tasks.CallInterrupted {
		t.Fatalf("simulated startup left established escalation call_state=%s question=%s interrupted_ids=%v; want interrupted/open", recovered.CallState, recovered.State, ids)
	}
}

// R3: Stop of an idle owner with a pending call is the way to cancel that
// call alone; it succeeds, keeps the question and wakes nothing.
func TestReviewForYouCalls_IdleStopCancelsEscalationAndSucceeds(t *testing.T) {
	for _, via := range []string{"direct", "http"} {
		t.Run(via, func(t *testing.T) {
			f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
			q := f.post() // post waits for the owner to become idle
			if via == "direct" {
				if err := f.mgr.Cancel(f.osess.ID); err != nil {
					t.Fatalf("idle Cancel error=%v", err)
				}
			} else {
				f.mustRequest(http.MethodPost, "/api/sessions/"+f.osess.ID+"/cancel", nil, f.owner(), http.StatusNoContent)
			}
			if got := f.question(q.ID); got.State != tasks.QuestionOpen || got.CallState != tasks.CallCancelled {
				t.Fatalf("idle cancellation result: question=%s call_state=%s", got.State, got.CallState)
			}
			// Nothing left to cancel: an idle Stop is refused again.
			if err := f.mgr.Cancel(f.osess.ID); err == nil || err.Error() != "session is not running" {
				t.Fatalf("second idle Cancel error=%v", err)
			}
		})
	}
}
