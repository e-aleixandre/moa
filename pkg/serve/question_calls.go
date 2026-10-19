package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// An owner's own asynchronous question can escalate to a call: if it is still
// open when its time comes, the owner calls the user about that question once,
// through the same orchestration as request_call (call level, allowed hours,
// pause, ring time). The deadline and the escalation's state live on the
// question row (tasks/question_calls.go); the task scheduler's pass is what
// fires it, so there is no loop of its own.
//
// Which questions escalate: the owner's own ask_user posts that set
// call_after_minutes, and those marked requires_tap, after the minutes set in
// Settings (15 by default). Nothing else: request_call's written fallback
// comes from a call that already failed, other sessions do not call, and the
// blocking ask, permissions and `tasks ask` have no durable row to carry it.
//
// The call only alerts: an answer relayed by voice still goes through the
// question's own acceptance, so a requires_tap question stays tap-only.

// questionCallBusyRetry is how long an escalation waits when the line is busy
// (the user is on a call, or another owner is ringing): it never rang, so it
// is not a retry of a missed call.
const questionCallBusyRetry = 2 * time.Minute

// questionCallHooks are test seams at the escalation's crash boundaries. All
// nil in production.
type questionCallHooks struct {
	// beforeClaim runs once the question was read as due, before its claim.
	beforeClaim func(questionID string)
	// afterClaim runs once the attempt is committed, before anything rings.
	// Returning false stops there, as if the process died.
	afterClaim func(questionID, attempt string) bool
	// finished sees how each attempt that rang (or would have) ended its
	// ringing: done for an answered call, told when it is answered, while
	// the call goes on.
	finished func(questionID string, state tasks.CallState)
}

// parseCallAfterMinutes reads call_after_minutes: absent is 0.
func parseCallAfterMinutes(params map[string]any) (int, error) {
	raw, ok := params["call_after_minutes"]
	if !ok || raw == nil {
		return 0, nil
	}
	bad := fmt.Errorf("call_after_minutes must be a whole number of minutes between %d and %d", core.MinQuestionCallMinutes, core.MaxQuestionCallMinutes)
	var f float64
	switch v := raw.(type) {
	case float64:
		f = v
	case int:
		f = float64(v)
	case int64:
		f = float64(v)
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return 0, bad
		}
		f = n
	default:
		return 0, bad
	}
	if math.Trunc(f) != f || f < core.MinQuestionCallMinutes || f > core.MaxQuestionCallMinutes {
		return 0, bad
	}
	return int(f), nil
}

// questionCallMinutes is the Settings wait for tap-only questions.
func questionCallMinutes() int {
	if n := voiceSettings().Calls.QuestionCallMinutes; n > 0 {
		return n
	}
	return core.DefaultQuestionCallMinutes
}

// questionCallDue is when an owner's new question calls if still open, 0 for
// no call: the session is not an owner's own conversation, its level is
// Never, or neither the agent nor the default asked for one. A time outside
// the allowed hours moves to the next window, so what the user sees is when
// it will really ring.
func (m *Manager) questionCallDue(sessionID string, requiresTap bool, after int) int64 {
	minutes := after
	if minutes == 0 && requiresTap {
		minutes = questionCallMinutes()
	}
	if minutes == 0 {
		return 0
	}
	own, err := m.callingOwner(sessionID)
	if err != nil || own.Calls() == owner.CallNever {
		return 0
	}
	due := m.clock.Now().Add(time.Duration(minutes) * time.Minute)
	return nextCallWindow(voiceSettings().Calls, due).UnixMilli()
}

// questionCallView is what clients are told about a question's escalation:
// when it calls (only while pending) and its state, if it says anything.
func questionCallView(q tasks.Question) (time.Time, string) {
	switch q.CallState {
	case tasks.CallPending:
		return time.UnixMilli(q.CallDueAt).UTC(), string(q.CallState)
	case tasks.CallCalling, tasks.CallMissed:
		return time.Time{}, string(q.CallState)
	}
	return time.Time{}, ""
}

// withdrawQuestionCall stops the call a question that just ended was
// escalating to, if it is ringing. Its row was cancelled in the same
// transaction that ended the question.
func (m *Manager) withdrawQuestionCall(q tasks.Question) {
	if q.CallAttempt != "" {
		m.callRequests().withdraw(q.CallAttempt)
	}
}

// cancelQuestionCalls is Stop's part: the session's escalations are
// cancelled, a ringing one stops, and the questions stay open.
func (m *Manager) cancelQuestionCalls(ctx context.Context, sessionID string) error {
	if !m.questions.engaged() {
		return nil
	}
	attempts, err := m.tasks.CancelSessionCalls(ctx, sessionID)
	for _, a := range attempts {
		m.callRequests().withdraw(a)
		if c, ok := m.escalationCalls.Load(a); ok {
			c.(*voiceCall).requestEnd("hangup")
		}
	}
	if err != nil {
		return err
	}
	if sess, ok := m.Get(sessionID); ok {
		m.questions.reproject(sess)
	}
	return nil
}

// interruptQuestionCalls marks interrupted the attempts a previous process
// left calling: whether they rang is unknown, so they are never replayed.
func (m *Manager) interruptQuestionCalls(ctx context.Context) {
	if !m.questions.engaged() {
		return
	}
	ids, err := m.tasks.InterruptCallEscalations(ctx)
	if err != nil {
		slog.Warn("question calls: marking interrupted attempts failed", "error", err)
		return
	}
	if len(ids) > 0 {
		slog.Info("question calls: attempts of a previous process marked interrupted", "n", len(ids))
	}
}

// escalateQuestions is the scheduler's part: every due escalation is fired
// on its own goroutine, so a ringing call never holds the pass.
func (s *taskScheduler) escalateQuestions(ctx context.Context) {
	m := s.m
	if !m.questions.engaged() {
		return
	}
	due, err := m.tasks.DueCallEscalations(ctx, s.clock.Now().UnixMilli())
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("question calls: reading due escalations failed", "error", err)
		}
		return
	}
	for _, q := range due {
		if _, busy := m.questionCalls.LoadOrStore(q.ID, struct{}{}); busy {
			continue
		}
		go func() {
			defer m.questionCalls.Delete(q.ID)
			m.fireQuestionCall(ctx, q)
		}()
	}
}

// fireQuestionCall claims one due escalation and places its call. The claim
// commits before anything rings; whatever ends the call is written against
// that attempt only, so an answer, a Stop or a restart that got there first
// is never overwritten.
func (m *Manager) fireQuestionCall(ctx context.Context, q tasks.Question) {
	sess, ok := m.Get(q.SessionID)
	if !ok {
		// A closed owner is loaded to call: the call lands in its
		// conversation.
		var err error
		if sess, err = m.ResumeSession(q.SessionID); err != nil {
			if !errors.Is(err, ErrBusy) {
				slog.Warn("question calls: loading the owner failed; will retry", "session", q.SessionID, "error", err)
			}
			return
		}
	}
	own, err := m.callingOwner(sess.ID)
	if err != nil {
		// No longer an owner's own conversation: nobody can call.
		if _, err := m.tasks.CancelPendingCall(ctx, q.ID); err != nil {
			slog.Warn("question calls: cancelling failed", "question", q.ID, "error", err)
		}
		return
	}
	if h := m.questionCallHooks.beforeClaim; h != nil {
		h(q.ID)
	}
	attempt := newVoiceCallID()
	if claimed, err := m.tasks.ClaimCallEscalation(ctx, q.ID, attempt); err != nil || !claimed {
		if err != nil {
			slog.Warn("question calls: claiming failed", "question", q.ID, "error", err)
		}
		return
	}
	if h := m.questionCallHooks.afterClaim; h != nil && !h(q.ID, attempt) {
		return
	}
	m.questions.reproject(sess)
	text := questionCallText(q)
	reason := "Waiting for your answer: " + text
	if q.RequiresTap {
		reason = "Waiting for your tap in moa: " + text
	}
	// The call outlives the pass that fired it and a shutdown: a process
	// that dies leaves the attempt calling, and the next one marks it
	// interrupted.
	callCtx := context.WithoutCancel(ctx)
	req, state, why := m.callRequests().place(callCtx, callPlacement{
		ID: attempt, SessionID: sess.ID, Owner: own, Reason: cutRunes(reason, callIntroMax), Urgency: "urgent",
		Question: text, QuestionID: q.ID, Now: m.clock.Now(),
		// The question may have been answered between the claim and the
		// request holding the line, before anything could withdraw it: look
		// again before anyone can see or answer the request.
		Opened: func(*callRequest) bool {
			cur, err := m.tasks.GetQuestion(callCtx, q.ID)
			return err == nil && cur.State == tasks.QuestionOpen && cur.CallState == tasks.CallCalling && cur.CallAttempt == attempt
		},
	})
	end := tasks.CallCancelled
	switch {
	case req == nil && (why == whyOutsideHours || why == whyPaused):
		at := nextCallWindow(voiceSettings().Calls, m.clock.Now())
		m.deferQuestionCall(callCtx, sess, q.ID, attempt, at)
		return
	case req == nil && why == whyBusy:
		m.deferQuestionCall(callCtx, sess, q.ID, attempt, m.clock.Now().Add(questionCallBusyRetry))
		return
	case req == nil:
		// Never, or no plan: nothing will ever ring for it.
	case state == crAnswered:
		end = tasks.CallDone
	case state == crWritten && why == whyDeclined:
		// "Write it instead": the question already is in writing.
		end = tasks.CallDone
	case state == crWritten:
		end = tasks.CallMissed
	}
	onCall := state == crAnswered && req.call != nil
	if onCall {
		if h := m.questionCallHooks.finished; h != nil {
			h(q.ID, end)
		}
		m.awaitQuestionCall(callCtx, q.ID, attempt, req.call)
	}
	if _, err := m.tasks.FinishCallEscalation(callCtx, q.ID, attempt, end); err != nil {
		slog.Warn("question calls: recording the end failed", "question", q.ID, "state", end, "error", err)
	}
	if end == tasks.CallMissed {
		m.missedQuestionCall(sess, q.ID, own.Name)
	}
	m.questions.reproject(sess)
	if h := m.questionCallHooks.finished; h != nil && !onCall {
		h(q.ID, end)
	}
}

// awaitQuestionCall waits for the call the user answered to end. Until then
// the attempt stays calling: Stop finds it and hangs the call up, and a
// process that dies meanwhile leaves it for the next one to mark interrupted.
func (m *Manager) awaitQuestionCall(ctx context.Context, id, attempt string, c *voiceCall) {
	m.escalationCalls.Store(attempt, c)
	defer m.escalationCalls.Delete(attempt)
	// A Stop that committed before the call was stored here did not see it.
	if cur, err := m.tasks.GetQuestion(ctx, id); err == nil && cur.State == tasks.QuestionOpen && cur.CallState == tasks.CallCancelled && cur.CallAttempt == attempt {
		c.requestEnd("hangup")
	}
	<-c.done
}

// deferQuestionCall puts an attempt that could not ring back to pending at
// its next possible time; the configuration is read again then.
func (m *Manager) deferQuestionCall(ctx context.Context, sess *ManagedSession, id, attempt string, at time.Time) {
	if _, err := m.tasks.DeferCallEscalation(ctx, id, attempt, at.UnixMilli()); err != nil {
		slog.Warn("question calls: deferring failed", "question", id, "error", err)
	}
	m.questions.reproject(sess)
}

// missedQuestionCall tells the user, urgently, that the owner called about
// this question and nobody answered. The durable notice is the question's
// own missed mark, which For you and the pin show; this is its alert.
func (m *Manager) missedQuestionCall(sess *ManagedSession, questionID, from string) {
	if m.pushPolicy == nil {
		return
	}
	approvals := sess.runtime.Context().Approvals
	m.pushPolicy.Handle(push.Signal{
		Kind: push.KindAsk, SessionID: sess.ID, Title: sess.title(), Headline: "Missed call from " + from,
		RequestID: questionID, Watched: sess.presence.watched(),
		StillPending: func() bool { return approvals.AskPending(questionID) },
	})
}

// questionCallText is the question the call is about, in one line.
func questionCallText(q tasks.Question) string {
	parts := make([]string, 0, len(q.Questions))
	for _, item := range q.Questions {
		parts = append(parts, strings.Join(strings.Fields(item.Text), " "))
	}
	return strings.Join(parts, " / ")
}
