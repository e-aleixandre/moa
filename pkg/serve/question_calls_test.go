package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// # language: en
// Feature: Call escalation of an unanswered question (GHERKIN.md, B2)
//
// An owner's own asynchronous question calls the user once when it is still
// open at its time. The owner, its voice plan and the ringing are the real
// request_call machinery (rcFix); time is a fake task clock that drives the
// real scheduler; the questions live in the real task database.

const qcCallID = "qc-ask-1"

// qcHandler drives the owner: "PLEASE-ASK" posts the question with args, the
// turn after the tool result ends.
func qcHandler(args map[string]any) mockHandler {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		for _, m := range req.Messages {
			if m.Role == "tool_result" && m.ToolCallID == qcCallID {
				return simpleResponse("OWNER-DONE"), nil
			}
		}
		if strings.Contains(vpLastUser(req), "PLEASE-ASK") {
			return toolCallHandlerFor(qcCallID, "ask_user", args)(ctx, req)
		}
		return simpleResponse("BRIEF-READY"), nil
	}
}

func qcArgs(extra map[string]any) map[string]any {
	args := map[string]any{"questions": []any{map[string]any{"question": "QC-QUESTION: deploy 0.48 tonight?", "options": []any{"Yes", "No"}}}}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

type qcFix struct {
	*rcFix
	clock     *fakeTaskClock
	announced atomic.Int32
	ended     chan tasks.CallState
	lastReq   atomic.Pointer[callRequest]
}

func qcNewFixture(t *testing.T, at, level string, args map[string]any) *qcFix {
	t.Helper()
	clock := newFakeTaskClock(mustUTC(at))
	limits := vpDefaultLimits()
	limits.clock = clock
	var hs []mockHandler
	for range 24 {
		hs = append(hs, qcHandler(args))
	}
	f := &qcFix{rcFix: rcWrap(t, vpNewFixtureWithLimits(t, "grok-plan", limits, hs...), level), clock: clock, ended: make(chan tasks.CallState, 8)}
	reg := f.mgr.callRequests()
	reg.ringOverride = 400 * time.Millisecond
	reg.announced = func(req *callRequest) {
		f.announced.Add(1)
		c := *req
		f.lastReq.Store(&c)
	}
	f.mgr.questionCallHooks.finished = func(_ string, st tasks.CallState) { f.ended <- st }
	return f
}

// post makes the owner post its question and returns the stored row.
func (f *qcFix) post() tasks.Question {
	f.t.Helper()
	if _, _, _, err := f.mgr.Send(f.osess.ID, "PLEASE-ASK", nil, "", ""); err != nil {
		f.t.Fatal(err)
	}
	var q tasks.Question
	pollUntil(f.t, 5*time.Second, "the owner's question", func() bool {
		open, _ := f.mgr.tasks.OpenQuestions(context.Background(), f.osess.ID)
		if len(open) == 1 {
			q = open[0]
			return true
		}
		return false
	})
	waitIdle(f.t, f.mgr, f.osess.ID)
	return q
}

func (f *qcFix) question(id string) tasks.Question {
	f.t.Helper()
	q, err := f.mgr.tasks.GetQuestion(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return q
}

// pass moves the clock to at and runs one full scheduler pass after it,
// waiting for whatever it fired to settle.
func (f *qcFix) pass(at time.Time) {
	f.t.Helper()
	f.clock.Set(at)
	f.mgr.planner.syncPass()
	pollUntil(f.t, 5*time.Second, "escalations to settle", func() bool {
		busy := false
		f.mgr.questionCalls.Range(func(any, any) bool { busy = true; return false })
		return !busy
	})
}

func (f *qcFix) waitEnded() tasks.CallState {
	f.t.Helper()
	select {
	case st := <-f.ended:
		return st
	case <-time.After(10 * time.Second):
		f.t.Fatal("the escalation never ended")
	}
	return ""
}

type qcForYouItem struct {
	ID        string     `json:"id"`
	CallAt    *time.Time `json:"call_at"`
	CallState string     `json:"call_state"`
}

func (f *qcFix) forYou(id string) qcForYouItem {
	f.t.Helper()
	var body struct {
		Items []qcForYouItem `json:"items"`
	}
	_ = json.Unmarshal(f.mustRequest(http.MethodGet, "/api/for-you", nil, f.owner(), http.StatusOK), &body)
	for _, it := range body.Items {
		if it.ID == id {
			return it
		}
	}
	f.t.Fatalf("question %s is not in For you", id)
	return qcForYouItem{}
}

func setCallsConfig(t *testing.T, calls core.VoiceCallsConfig) {
	t.Helper()
	if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		v := core.VoiceConfig{}
		if cfg.Voice != nil {
			v = *cfg.Voice
		}
		v.Calls = calls
		cfg.Voice = &v
	}); err != nil {
		t.Fatal(err)
	}
}

// Scenario: the owner asks for a call if unanswered; a missed call is not
// retried. Normal path: exactly one attempt, about that same question.
func TestQuestionCall_RingsOnceAtItsTimeAndAMissedCallIsNeverRetried(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 20}))
	q := f.post()
	due := mustUTC("2026-10-19T08:20:00Z")
	if q.CallState != tasks.CallPending || q.CallDueAt != due.UnixMilli() {
		t.Fatalf("posted escalation %q at %v, want pending at %v", q.CallState, time.UnixMilli(q.CallDueAt).UTC(), due)
	}
	if it := f.forYou(q.ID); it.CallAt == nil || !it.CallAt.Equal(due) || it.CallState != "pending" {
		t.Fatalf("For you: call_at %v state %q, want %v pending", it.CallAt, it.CallState, due)
	}
	if ask := waitPendingAsk(t, f.osess); !ask.CallAt.Equal(due) || ask.CallState != "pending" {
		t.Fatalf("the ask projection carries call_at %v state %q", ask.CallAt, ask.CallState)
	}

	f.pass(due.Add(-time.Minute))
	if n := f.announced.Load(); n != 0 {
		t.Fatalf("rang %d times before its time", n)
	}
	f.pass(due)
	if st := f.waitEnded(); st != tasks.CallMissed {
		t.Fatalf("unanswered call ended %q, want missed", st)
	}
	if n := f.announced.Load(); n != 1 {
		t.Fatalf("rang %d times, want exactly 1", n)
	}
	req := f.lastReq.Load()
	if req.QuestionID != q.ID || req.SessionID != f.osess.ID || !strings.Contains(req.question, "QC-QUESTION") || req.Urgency != "urgent" {
		t.Fatalf("the call is not about the question: %+v / %q", req, req.question)
	}
	missedPush := false
	for _, n := range f.probe.delivered() {
		if n.Kind == push.KindAsk && strings.Contains(n.Title+" "+n.Body, "Missed call from Infra") {
			missedPush = true
		}
	}
	if !missedPush {
		t.Fatalf("no urgent 'Missed call from Infra' notification: %+v", f.probe.delivered())
	}
	if it := f.forYou(q.ID); it.CallState != "missed" || it.CallAt != nil {
		t.Fatalf("For you after the missed call: %+v, want missed without call_at", it)
	}
	// Never again, however long it stays open.
	f.pass(due.Add(48 * time.Hour))
	if n := f.announced.Load(); n != 1 {
		t.Fatalf("a missed call rang again: %d attempts", n)
	}
	if got := f.question(q.ID); got.CallState != tasks.CallMissed || got.State != tasks.QuestionOpen {
		t.Fatalf("after two days: call %q question %q", got.CallState, got.State)
	}
	// The written question is the original one: no second question.
	if open, _ := f.mgr.tasks.OpenQuestions(context.Background(), f.osess.ID); len(open) != 1 || open[0].ID != q.ID {
		t.Fatalf("open questions after the call: %+v", open)
	}
}

// Scenario: answer races the call. Answered between the scan and the claim,
// or between the claim and the request going live: no call at all.
func TestQuestionCall_AnswerRacingTheClaimMeansNoCall(t *testing.T) {
	for _, at := range []string{"before claim", "after claim"} {
		t.Run(at, func(t *testing.T) {
			f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
			q := f.post()
			answer := func() {
				f.mustRequest(http.MethodPost, "/api/sessions/"+f.osess.ID+"/ask", map[string]any{"id": q.ID, "answers": []string{"Yes"}}, f.owner(), http.StatusNoContent)
			}
			if at == "before claim" {
				f.mgr.questionCallHooks.beforeClaim = func(string) { answer() }
			} else {
				f.mgr.questionCallHooks.afterClaim = func(string, string) bool { answer(); return true }
			}
			f.pass(f.clock.Now().Add(10 * time.Minute))
			if at == "after claim" {
				if st := f.waitEnded(); st != tasks.CallCancelled {
					t.Fatalf("ended %q, want cancelled", st)
				}
			}
			if n := f.announced.Load(); n != 0 {
				t.Fatalf("an answered question rang %d times", n)
			}
			if got := f.question(q.ID); got.State != tasks.QuestionAnswered || got.CallState != tasks.CallCancelled {
				t.Fatalf("question %q call %q, want answered and cancelled", got.State, got.CallState)
			}
			if live := f.requests(); len(live) != 0 {
				t.Fatalf("a call request is still live: %+v", live)
			}
		})
	}
}

// Scenario: answer, Dismiss, withdraw or Stop cancels the call; if it was
// ringing it stops ringing. After Stop the question is still open.
func TestQuestionCall_AnswerDismissOrStopWhileRingingWithdrawsTheCall(t *testing.T) {
	for _, how := range []string{"answer", "dismiss", "stop"} {
		t.Run(how, func(t *testing.T) {
			f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
			f.mgr.callRequests().ringOverride = time.Minute // rings until withdrawn
			q := f.post()
			f.clock.Set(f.clock.Now().Add(10 * time.Minute))
			f.mgr.planner.syncPass()
			f.awaitRequest("ringing")
			switch how {
			case "answer":
				f.mustRequest(http.MethodPost, "/api/sessions/"+f.osess.ID+"/ask", map[string]any{"id": q.ID, "answers": []string{"No"}}, f.owner(), http.StatusNoContent)
			case "dismiss":
				if err := f.mgr.questions.Dismiss(context.Background(), f.osess.ID, q.ID); err != nil {
					t.Fatal(err)
				}
			case "stop":
				_ = f.mgr.Cancel(f.osess.ID) // idle owner: "not running", the call is cancelled all the same
			}
			if st := f.waitEnded(); st != tasks.CallCancelled {
				t.Fatalf("ended %q, want cancelled", st)
			}
			if live := f.requests(); len(live) != 0 {
				t.Fatalf("still ringing: %+v", live)
			}
			got := f.question(q.ID)
			if got.CallState != tasks.CallCancelled {
				t.Fatalf("call %q, want cancelled", got.CallState)
			}
			if how == "stop" {
				if got.State != tasks.QuestionOpen {
					t.Fatalf("Stop closed the question: %q", got.State)
				}
				if it := f.forYou(q.ID); it.CallAt != nil || it.CallState != "" {
					t.Fatalf("For you after Stop still shows a call: %+v", it)
				}
			}
			f.pass(f.clock.Now().Add(24 * time.Hour))
			if n := f.announced.Load(); n != 1 {
				t.Fatalf("attempts = %d, want the one that was withdrawn", n)
			}
		})
	}
}

// Stop before the call is due: nothing ever rings, the question stays open.
func TestQuestionCall_StopCancelsAPendingCall(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 30}))
	q := f.post()
	_ = f.mgr.Cancel(f.osess.ID)
	f.pass(f.clock.Now().Add(2 * time.Hour))
	if n := f.announced.Load(); n != 0 {
		t.Fatalf("rang %d times after Stop", n)
	}
	if got := f.question(q.ID); got.State != tasks.QuestionOpen || got.CallState != tasks.CallCancelled {
		t.Fatalf("question %q call %q, want open and cancelled", got.State, got.CallState)
	}
}

// Scenario: a crash while calling is never replayed. The process that claimed
// dies (after the claim, or while ringing); the next one marks the attempt
// interrupted at start and nobody is called again: at most one attempt.
func TestQuestionCall_CrashAfterTheClaimIsInterruptedNeverReplayed(t *testing.T) {
	for _, when := range []string{"before ringing", "while ringing"} {
		t.Run(when, func(t *testing.T) {
			f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
			q := f.post()
			if when == "before ringing" {
				f.mgr.questionCallHooks.afterClaim = func(string, string) bool { return false } // the process dies here
			} else {
				f.mgr.callRequests().ringOverride = time.Minute
			}
			f.clock.Set(f.clock.Now().Add(10 * time.Minute))
			f.mgr.planner.syncPass()
			if when == "while ringing" {
				f.awaitRequest("ringing")
			}
			pollUntil(t, 5*time.Second, "the claim", func() bool { return f.question(q.ID).CallState == tasks.CallCalling })

			// The next process: a second handle on the same database, its own
			// scheduler on the same clock.
			next := tasks.New(f.mgr.tasks.Path())
			t.Cleanup(func() { _ = next.Close() })
			m2 := NewManager(context.Background(), ManagerConfig{
				ProviderFactory: func(core.Model) (core.Provider, error) { return f.prov, nil },
				DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
				WorkspaceRoot:   t.TempDir(), MoaCfg: core.MoaConfig{DisableSandbox: true},
				SessionBaseDir: t.TempDir(), Tasks: next, clock: f.clock,
			})
			t.Cleanup(m2.Shutdown)
			m2.planner.syncPass()
			if got := f.question(q.ID); got.CallState != tasks.CallInterrupted || got.State != tasks.QuestionOpen {
				t.Fatalf("after restart: call %q question %q, want interrupted and open", got.CallState, got.State)
			}
			if when == "while ringing" {
				// The dead process's call ends late: it cannot overwrite it.
				f.mgr.callRequests().withdraw(f.lastReq.Load().ID)
				f.waitEnded()
			}
			f.clock.Set(f.clock.Now().Add(24 * time.Hour))
			m2.planner.syncPass()
			f.mgr.planner.syncPass()
			if n := f.announced.Load(); n > 1 {
				t.Fatalf("attempts = %d, want at most 1", n)
			}
			if got := f.question(q.ID); got.CallState != tasks.CallInterrupted {
				t.Fatalf("call %q, want interrupted for good", got.CallState)
			}
		})
	}
}

// Scenario: outside the allowed hours. Posted late at night on the day Madrid
// leaves summer time: it calls at 08:00 local the next morning, which is 07:00
// UTC after the change, not 06:00.
func TestQuestionCall_OutsideHoursCallsAtTheNextWindowAcrossDST(t *testing.T) {
	f := qcNewFixture(t, "2026-10-24T21:20:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 10}))
	setCallsConfig(t, core.VoiceCallsConfig{Hours: &core.VoiceCallHours{From: "08:00", Until: "22:00", TZ: "Europe/Madrid"}})
	q := f.post() // 23:20 CEST
	want := mustUTC("2026-10-25T07:00:00Z")
	if q.CallDueAt != want.UnixMilli() {
		t.Fatalf("call_at = %v, want %v (08:00 CET)", time.UnixMilli(q.CallDueAt).UTC(), want)
	}
	f.pass(want.Add(-time.Minute))
	if n := f.announced.Load(); n != 0 {
		t.Fatalf("rang %d times outside the hours", n)
	}
	f.pass(want)
	if f.waitEnded(); f.announced.Load() != 1 {
		t.Fatalf("attempts at the window = %d, want 1", f.announced.Load())
	}
}

// The hours change after the post: the fire re-reads them and defers to the
// next window; there the level is Never, so it does not call at all.
func TestQuestionCall_ReadsTheConfigurationAgainWhenDue(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T10:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 20}))
	q := f.post()
	setCallsConfig(t, core.VoiceCallsConfig{Hours: &core.VoiceCallHours{From: "08:00", Until: "09:00", TZ: "Europe/Madrid"}})
	f.pass(mustUTC("2026-10-19T10:20:00Z"))
	got := f.question(q.ID)
	next := mustUTC("2026-10-20T06:00:00Z") // 08:00 CEST
	if got.CallState != tasks.CallPending || got.CallDueAt != next.UnixMilli() || f.announced.Load() != 0 {
		t.Fatalf("outside the new hours: %q at %v, %d attempts; want pending at %v, none", got.CallState, time.UnixMilli(got.CallDueAt).UTC(), f.announced.Load(), next)
	}
	f.mustRequest(http.MethodPatch, "/api/owners/"+f.own.ID, map[string]any{"call_level": "never"}, f.owner(), http.StatusOK)
	f.pass(next)
	if st := f.waitEnded(); st != tasks.CallCancelled || f.announced.Load() != 0 {
		t.Fatalf("level Never at the window: %q, %d attempts", st, f.announced.Load())
	}
}

// Scenario: level Never. The question posts without a call time.
func TestQuestionCall_LevelNeverPostsNoCall(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "never", qcArgs(map[string]any{"call_after_minutes": 10, "requires_tap": true}))
	q := f.post()
	if q.CallState != tasks.CallNone || q.CallDueAt != 0 {
		t.Fatalf("level Never posted a call: %q at %d", q.CallState, q.CallDueAt)
	}
	if it := f.forYou(q.ID); it.CallAt != nil || it.CallState != "" {
		t.Fatalf("For you shows a call: %+v", it)
	}
	f.pass(f.clock.Now().Add(time.Hour))
	if n := f.announced.Load(); n != 0 {
		t.Fatalf("rang %d times", n)
	}
}

// Scenario: a tap question escalates by default, after the Settings minutes;
// an answer relayed by voice does not close it.
func TestQuestionCall_TapQuestionEscalatesByDefaultAndStaysTapOnly(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"requires_tap": true}))
	q := f.post()
	if want := mustUTC("2026-10-19T08:15:00Z").UnixMilli(); q.CallState != tasks.CallPending || q.CallDueAt != want {
		t.Fatalf("tap question: %q at %v, want pending at 08:15", q.CallState, time.UnixMilli(q.CallDueAt).UTC())
	}
	err := f.mgr.questions.Answer(context.Background(), f.osess.ID, q.ID, []string{"Yes"},
		askuser.Responder{Kind: askuser.ResponderVoice, Via: "call", CallID: "c1"})
	if !errors.Is(err, askuser.ErrRequiresTap) {
		t.Fatalf("voice answer to a tap question = %v, want ErrRequiresTap", err)
	}
	if got := f.question(q.ID); got.State != tasks.QuestionOpen || got.CallState != tasks.CallPending {
		t.Fatalf("after the refused voice answer: question %q call %q", got.State, got.CallState)
	}
	// Settings change the default for the next questions.
	setCallsConfig(t, core.VoiceCallsConfig{QuestionCallMinutes: 30})
	if due := f.mgr.questionCallDue(f.osess.ID, true, 0); due != f.clock.Now().Add(30*time.Minute).UnixMilli() {
		t.Fatalf("Settings default not applied: %v", time.UnixMilli(due).UTC())
	}
	if due := f.mgr.questionCallDue(f.osess.ID, false, 0); due != 0 {
		t.Fatal("a question without tap or call_after_minutes got a call")
	}
}

// Scenario: validation. The post is rejected and nothing opens.
func TestQuestionCall_CallAfterMinutesIsValidated(t *testing.T) {
	for _, bad := range []any{3, 4.0, 7.5, "10", 241, -5, true} {
		if _, err := parseCallAfterMinutes(map[string]any{"call_after_minutes": bad}); err == nil {
			t.Errorf("call_after_minutes %v accepted", bad)
		}
	}
	for _, good := range []float64{5, 15, 240} {
		if n, err := parseCallAfterMinutes(map[string]any{"call_after_minutes": good}); err != nil || n != int(good) {
			t.Errorf("call_after_minutes %v = %d, %v", good, n, err)
		}
	}
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(map[string]any{"call_after_minutes": 2}))
	if _, _, _, err := f.mgr.Send(f.osess.ID, "PLEASE-ASK", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var res core.AgentMessage
	pollUntil(t, 5*time.Second, "the ask_user result", func() bool {
		for _, m := range f.osess.History() {
			if m.Role == "tool_result" && m.ToolCallID == qcCallID {
				res = m
				return true
			}
		}
		return false
	})
	if !res.IsError || !strings.Contains(rcText(res), "call_after_minutes") {
		t.Fatalf("result = %q (error %v), want the validation error", rcText(res), res.IsError)
	}
	if open, _ := f.mgr.tasks.OpenQuestions(context.Background(), f.osess.ID); len(open) != 0 {
		t.Fatalf("a rejected post opened %d questions", len(open))
	}
}

// Scenario: only owners. A non-owner's ask_user has no call_after_minutes and
// its questions never call, even with the default rule's requires_tap.
func TestQuestionCall_OnlyOwnersHaveIt(t *testing.T) {
	f := qcNewFixture(t, "2026-10-19T08:00:00Z", "ring", qcArgs(nil))
	ownerTool, _ := f.osess.infra.toolReg.Get("ask_user")
	plainTool, _ := f.sess.infra.toolReg.Get("ask_user")
	if !strings.Contains(string(ownerTool.Parameters), "call_after_minutes") || !strings.Contains(ownerTool.Description, "call_after_minutes") {
		t.Fatal("the owner's ask_user has no call_after_minutes")
	}
	if strings.Contains(string(plainTool.Parameters), "call_after_minutes") || plainTool.Description != asyncAskDescription {
		t.Fatal("a non-owner's ask_user changed")
	}
	if due := f.mgr.questionCallDue(f.sess.ID, true, 20); due != 0 {
		t.Fatal("a non-owner's question got a call time")
	}
}
