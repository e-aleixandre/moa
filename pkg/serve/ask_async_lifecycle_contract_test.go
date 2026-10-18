package serve

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// Lifecycle contract for the asynchronous ask_user (F1 bis): tool surface,
// dismiss, compaction and Stop versus an accepted-but-not-admitted answer.
// Same fixtures and assumptions as ask_async_contract_test.go; no production
// hooks are added. Not covered here: transaction windows between the answer's
// durable write and its delivery (no seam exists for them besides the one
// used by the last test), and Stop racing the origin rewind.

// 1. The tool advertises its management actions: post, list and withdraw,
// with question_id and reason, and a root that does not demand questions for
// list. It also runs list with no questions and nothing open.
func TestF1BisREDToolAdvertisesManagementActions(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	tool, ok := sess.infra.toolReg.Get("ask_user")
	if !ok {
		t.Fatal("no ask_user tool")
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		t.Fatalf("ask_user parameters are not a JSON schema: %v", err)
	}
	for _, action := range []string{"post", "list", "withdraw"} {
		if !slices.Contains(schema.Properties["action"].Enum, action) {
			t.Errorf("schema action enum %v lacks %q", schema.Properties["action"].Enum, action)
		}
	}
	for _, field := range []string{"question_id", "reason"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("schema has no %q property", field)
		}
	}
	if slices.Contains(schema.Required, "questions") {
		t.Errorf("schema requires questions at the root, so list/withdraw cannot be called: required=%v", schema.Required)
	}

	ctx, cancel := context.WithTimeout(context.Background(), f1Guard)
	defer cancel()
	res, err := tool.Execute(ctx, map[string]any{"action": "list"}, nil)
	if err != nil || res.IsError {
		t.Fatalf("list with nothing open = %q (IsError=%v, err=%v), want an empty listing", toolText(res), res.IsError, err)
	}
}

// 2. Dismiss closes an open question with no model call and no answer, is
// idempotent, and makes a later answer stale. New contract: POST
// /api/sessions/{sid}/questions/{qid}/dismiss with body {} answers 204. Today
// the endpoint does not exist (the router rejects the method/path).
func TestF1BisREDDismissIsZeroCall(t *testing.T) {
	f1Each(t, func(t *testing.T, kind string) {
		e, sess, qid := f1Open(t, kind, false)
		before := e.prov.countSession(t, sess.ID)
		dismiss := func() int {
			resp := automationReq(t, e.srv, "/api/sessions/"+sess.ID+"/questions/"+qid+"/dismiss", "", `{}`, true)
			resp.Body.Close() //nolint:errcheck
			return resp.StatusCode
		}
		if code := dismiss(); code != 204 {
			t.Fatalf("dismiss = %d, want 204", code)
		}
		sess.runtime.Bus.Drain(2 * time.Second)
		if ask := f1Pending(sess); ask != nil && ask.ID == qid {
			t.Fatalf("dismissed question %s is still pending", qid)
		}
		if n := len(f1Answers(sess.History())); n != 0 {
			t.Fatalf("dismiss produced %d question_answer event(s); it is not an answer", n)
		}
		if code := dismiss(); code != 204 {
			t.Fatalf("repeated dismiss = %d, want 204", code)
		}
		if code, body := f1PostAsk(t, e, sess.ID, qid, "late yes"); code != 409 {
			t.Fatalf("answer after dismiss = %d %s, want 409", code, body)
		}
		if !f1Stable(400*time.Millisecond, func() bool {
			return e.prov.countSession(t, sess.ID) == before && len(f1Answers(sess.History())) == 0
		}) {
			t.Fatalf("dismiss, its retry or the stale answer called the model (+%d) or left an event", e.prov.countSession(t, sess.ID)-before)
		}
	})
}

// f1bisCompactScript answers the summarizer (recognised by its system
// prompt) with a fixed text, so the trigger inside the summarised
// conversation does not make it ask; everything else follows f1Script.
func f1bisCompactScript(tap bool) func(core.Request) <-chan core.AssistantEvent {
	base := f1Script(tap)
	return func(req core.Request) <-chan core.AssistantEvent {
		if f1bisIsSummary(req) {
			return f1Text("F1-IDLE")
		}
		return base(req)
	}
}

func f1bisIsSummary(req core.Request) bool {
	return strings.HasPrefix(req.System, "You are a conversation summarizer")
}

func TestF1BisGREENCompactFixtureWorksWhenIdle(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1bisCompactScript(false)})
	sess := e.session(f1KindHuman)
	for i := range 6 {
		sendAndWait(t, e.mgr, sess, turn(i))
	}
	ended := make(chan bus.CompactionEnded, 1)
	t.Cleanup(sess.runtime.Bus.Subscribe(func(ev bus.CompactionEnded) {
		select {
		case ended <- ev:
		default:
		}
	}))
	res, err := e.mgr.ExecCommand(sess.ID, "/compact", "")
	if err != nil || res == nil || !res.OK || res.ID != "" {
		t.Fatalf("idle compact fixture was not accepted: %+v, %v", res, err)
	}
	select {
	case ev := <-ended:
		if ev.Err != nil {
			t.Fatalf("idle compact fixture failed: %v", ev.Err)
		}
	case <-time.After(f1Guard):
		t.Fatal("idle compact fixture never completed")
	}
	f1Settle(t, sess)
	if n := e.prov.countWhere(f1bisIsSummary); n != 1 {
		t.Fatalf("idle compact fixture made %d summary calls, want 1", n)
	}
}

// 3. Compacting with an open question works, keeps the question, keeps its
// tool pair and the next request still carries the ephemeral block. The
// compaction itself does call the summarizer: no saving is claimed.
//
// Precondition (async): today the session is blocked in StatePermission and
// /compact is refused as busy; that refusal is the RED reason.
func TestF1BisREDCompactKeepsOpenQuestion(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1bisCompactScript(false)})
	sess := e.session(f1KindHuman)
	for i := range 6 {
		sendAndWait(t, e.mgr, sess, turn(i))
	}
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)

	ended := make(chan bus.CompactionEnded, 4)
	t.Cleanup(sess.runtime.Bus.Subscribe(func(ev bus.CompactionEnded) {
		select {
		case ended <- ev:
		default:
		}
	}))
	notSummary := func(r core.Request) bool { return !f1bisIsSummary(r) }
	turnsBefore := e.prov.countWhere(notSummary)

	res, err := e.mgr.ExecCommand(sess.ID, "/compact", "")
	if err != nil || res == nil || !res.OK {
		t.Fatalf("compact with an open question = %+v, %v (state=%s); it must be allowed", res, err, sessState(sess))
	}
	if res.ID != "" {
		t.Fatalf("compact was queued behind the unanswered question instead of starting: %+v (state=%s)", res, sessState(sess))
	}
	select {
	case ev := <-ended:
		if ev.Err != nil {
			t.Fatalf("compaction failed: %v", ev.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("compaction never ended")
	}
	f1Settle(t, sess)
	if got := e.prov.countWhere(f1bisIsSummary); got != 1 {
		t.Fatalf("summarizer calls = %d, want 1", got)
	}
	if got := e.prov.countWhere(notSummary); got != turnsBefore {
		t.Fatalf("compaction made %d model turn(s) besides the summary", got-turnsBefore)
	}
	if ask := f1Pending(sess); ask == nil || ask.ID != qid {
		t.Fatalf("compaction lost the open question %s: %+v", qid, ask)
	}
	if n := len(f1Answers(sess.History())); n != 0 {
		t.Fatalf("compaction produced %d answer event(s)", n)
	}
	if n := len(f1Results(displayed(t, sess))); n != 1 {
		t.Fatalf("transcript tool_results for the call = %d, want exactly 1", n)
	}
	if ok, why := f1PairsValid(sess.History()); !ok {
		t.Fatalf("invalid tool pairs after compaction: %s", why)
	}

	// The compacted context still tells the model about the open question,
	// through the ephemeral block (more mentions than the history holds).
	histMentions := f1CountIn(f1Blob(sess.History()), f1Question)
	e.send(sess, "F1-NEXT anything new?")
	if !f1Eventually(f1Guard, func() bool { return e.prov.countWhere(notSummary) > turnsBefore && sessState(sess) == StateIdle }) {
		t.Fatalf("the follow-up turn did not finish (state=%s)", sessState(sess))
	}
	req := e.prov.lastSession(t, sess.ID)
	if f1CountIn(req.System, f1Question) != 0 {
		t.Fatal("open question was injected into the system prompt")
	}
	msgs, _ := json.Marshal(req.Messages)
	if got := f1CountIn(string(msgs), f1Question); got <= histMentions {
		t.Fatalf("question text appears %d time(s) in the request and %d in history: no block about the open question after compaction", got, histMentions)
	}
}

// 4. Stop fences an accepted answer that has not been admitted yet. The
// answer travels the notice dispatcher, whose single admission seam
// (taskSchedulerHooks.beforeAdmit, installed before NewManager) is held open:
// Stop lands between "accepted" and "admitted", then admission is released.
// Nothing may wake the model by itself afterwards, and the answer must still
// be there for the user's explicit next message, read exactly once.
//
// Today the answer never enters the dispatcher (it resolves the blocked tool
// call directly), so the seam is never reached: that is the RED reason, and
// the race is not pretended. Rewind-before-origin racing this window is not
// covered.
func TestF1BisREDStopFencesAcceptedAnswerBeforeAdmission(t *testing.T) {
	f1Isolate(t)
	reached := make(chan string, 16)
	release := make(chan struct{})
	var once sync.Once
	hooks := taskSchedulerHooks{beforeAdmit: func(noticeID string) {
		select {
		case reached <- noticeID:
		default:
		}
		<-release
	}}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)}, hooks)
	// Registered after the env, so it runs first and never leaves the
	// dispatcher parked while the Manager shuts down.
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	f1ActiveForStop(t, e, sess)

	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
		t.Fatalf("answer = %d %s, want 2xx", code, body)
	}
	select {
	case <-reached:
	case <-time.After(f1Guard):
		t.Fatal("accepted answer did not enter durable dispatcher before admission")
	}

	before := e.prov.countSession(t, sess.ID)
	f1Stop(t, e, sess)
	once.Do(func() { close(release) })
	// The admission seam runs under the dispatcher's mutex. Acquiring it
	// after releasing the seam proves this attempt has finished, rather
	// than treating a quiet interval as proof it was ever processed.
	attemptFinished := make(chan struct{})
	go func() {
		e.mgr.notices.mu.Lock()
		defer e.mgr.notices.mu.Unlock()
		close(attemptFinished)
	}()
	select {
	case <-attemptFinished:
	case <-time.After(f1Guard):
		t.Fatal("the held dispatch attempt did not finish after Stop")
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	if !f1Stable(500*time.Millisecond, func() bool { return e.prov.countSession(t, sess.ID) == before }) {
		t.Fatalf("the fenced answer woke the model after Stop (+%d call(s))", e.prov.countSession(t, sess.ID)-before)
	}

	e.send(sess, "F1-NEXT anything new?")
	if !f1Eventually(f1Guard, func() bool { return e.prov.countSession(t, sess.ID) > before && sessState(sess) == StateIdle }) {
		t.Fatalf("the explicit message did not run (state=%s)", sessState(sess))
	}
	answers := f1Answers(sess.History())
	if len(answers) != 1 || answers[0].Custom["question_id"] != qid {
		t.Fatalf("answer events after the explicit message = %+v, want exactly one for %s", answers, qid)
	}
	if r, ok := askuser.ResponderFromCustom(answers[0].Custom[askuser.CustomKey]); !ok || r.Kind != askuser.ResponderUser {
		t.Fatalf("event responder = %+v ok=%v, want the user", r, ok)
	}
	// Count over the message text itself: json.Marshal escapes '<' as \u003c,
	// so the literal would never be found in the marshalled request.
	var read strings.Builder
	for _, m := range e.prov.lastSession(t, sess.ID).Messages {
		for _, c := range m.Content {
			read.WriteString(c.Text)
		}
	}
	if n := f1CountIn(read.String(), "<question_answer"); n != 1 {
		t.Fatalf("the model read the answer %d time(s) in the next request, want once", n)
	}
}
