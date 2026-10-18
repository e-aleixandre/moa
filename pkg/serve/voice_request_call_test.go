package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/push"
)

// Phase 2 of voice: an owner asks to call the user (request_call). These
// tests pin the policy in code (DISENO-F2.md): calls from owners start off,
// never fall to a paid API on their own, ring once, and whatever does not
// become a call becomes the owner's written question.

const rcCallID = "rc-1"

var rcArgs = map[string]any{
	"reason":           "RC-REASON-SENTINEL: OK to deploy 0.47",
	"urgency":          "urgent",
	"written_fallback": map[string]any{"question": "RC-QUESTION-SENTINEL: deploy 0.47 now?", "options": []any{"Yes", "No"}},
}

// rcHandler drives the owner: its first turn asks for a call, the turn after
// the tool result ends; every other request (briefs, minutes) is answered.
func rcHandler() mockHandler {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		for _, m := range req.Messages {
			if m.Role == "tool_result" && m.ToolCallID == rcCallID {
				return simpleResponse("OWNER-DONE"), nil
			}
		}
		if strings.Contains(vpLastUser(req), "PLEASE-CALL") {
			return toolCallHandlerFor(rcCallID, "request_call", rcArgs)(ctx, req)
		}
		return simpleResponse("BRIEF-READY"), nil
	}
}

type rcFix struct {
	*vpFixture
	own   OwnerInfo
	osess *ManagedSession
	probe *pushProbe
}

func rcHandlers() []mockHandler {
	var hs []mockHandler
	for range 24 {
		hs = append(hs, rcHandler())
	}
	return hs
}

func rcNewFixture(t *testing.T, provider, level string) *rcFix {
	t.Helper()
	return rcWrap(t, vpNewFixture(t, provider, rcHandlers()...), level)
}

// rcWrap turns a voice fixture into an owner at level.
func rcWrap(t *testing.T, f *vpFixture, level string) *rcFix {
	t.Helper()
	f.harnessOnly = true
	probe := probePush(f.mgr, push.SummariesOff)
	own, err := f.mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Infra"})
	if err != nil {
		t.Fatal(err)
	}
	osess, ok := f.mgr.Get(own.SessionID)
	if !ok {
		t.Fatal("owner session not loaded")
	}
	rc := &rcFix{vpFixture: f, own: own, osess: osess, probe: probe}
	if level != "" && level != "never" {
		rc.mustRequest(http.MethodPatch, "/api/owners/"+own.ID, map[string]any{"call_level": level}, f.owner(), http.StatusOK)
	}
	return rc
}

func (f *rcFix) ask() {
	f.t.Helper()
	if _, _, _, err := f.mgr.Send(f.osess.ID, "PLEASE-CALL", nil, "", ""); err != nil {
		f.t.Fatal(err)
	}
}

type rcRequest struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	OwnerID   string `json:"owner_id"`
	OwnerName string `json:"owner_name"`
	Reason    string `json:"reason"`
	Level     string `json:"level"`
	State     string `json:"state"`
}

func (f *rcFix) requests() []rcRequest {
	f.t.Helper()
	raw := f.mustRequest(http.MethodGet, "/api/voice/requests", nil, f.owner(), http.StatusOK)
	var out struct {
		Requests []rcRequest `json:"requests"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		f.t.Fatal(err)
	}
	return out.Requests
}

func (f *rcFix) awaitRequest(state string) rcRequest {
	f.t.Helper()
	var got rcRequest
	pollUntil(f.t, 5*time.Second, "call request "+state, func() bool {
		for _, r := range f.requests() {
			if r.SessionID == f.osess.ID && r.State == state {
				got = r
				return true
			}
		}
		return false
	})
	return got
}

// result waits for the request_call tool result of the owner.
func (f *rcFix) result() core.AgentMessage {
	f.t.Helper()
	var out core.AgentMessage
	pollUntil(f.t, 5*time.Second, "request_call result", func() bool {
		for _, m := range f.osess.History() {
			if m.Role == "tool_result" && m.ToolCallID == rcCallID {
				out = m
				return true
			}
		}
		return false
	})
	return out
}

func rcText(m core.AgentMessage) string {
	var b strings.Builder
	for _, c := range m.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func rcOutcome(t *testing.T, m core.AgentMessage) (outcome, why string) {
	t.Helper()
	raw, _ := json.Marshal(m.Custom["call_request"])
	var v struct {
		Outcome string `json:"outcome"`
		Why     string `json:"why"`
	}
	_ = json.Unmarshal(raw, &v)
	if v.Outcome == "" {
		t.Fatalf("request_call result has no call_request record: %s / %s", rcText(m), raw)
	}
	return v.Outcome, v.Why
}

// writtenAnswered waits for the written fallback question, answers it by tap
// and returns the tool result.
func (f *rcFix) writtenAnswered(answer string) core.AgentMessage {
	f.t.Helper()
	ask := waitPendingAsk(f.t, f.osess)
	if len(ask.Questions) != 1 || !strings.Contains(ask.Questions[0].Text, "RC-QUESTION-SENTINEL") {
		f.t.Fatalf("pending question is not the written fallback: %+v", ask.Questions)
	}
	f.mustRequest(http.MethodPost, "/api/sessions/"+f.osess.ID+"/ask", map[string]any{"id": ask.ID, "answers": []string{answer}}, f.owner(), http.StatusNoContent)
	return f.result()
}

func (f *rcFix) callPushes() []push.Notification {
	var out []push.Notification
	for _, n := range f.probe.delivered() {
		if n.Kind == push.KindCallRequest {
			out = append(out, n)
		}
	}
	return out
}

func (f *rcFix) setRing(d time.Duration) {
	f.mgr.callRequests().ringOverride = d
}

// F2-1: only owners may ask for a call.
func TestRequestCall_UnavailableToNonOwnerSessions(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	if _, _, _, err := f.mgr.Send(f.sess.ID, "PLEASE-CALL", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var res core.AgentMessage
	pollUntil(t, 5*time.Second, "tool result in plain session", func() bool {
		for _, m := range f.sess.History() {
			if m.Role == "tool_result" && m.ToolCallID == rcCallID {
				res = m
				return true
			}
		}
		return false
	})
	if !res.IsError {
		t.Fatalf("a plain session could use request_call: %s", rcText(res))
	}
	if len(f.requests()) != 0 || len(f.callPushes()) != 0 {
		t.Fatal("a plain session created a call request")
	}
}

// F2-2 (I23): a new owner is at Never; request_call goes straight to writing.
func TestOwnerCallLevel_DefaultNever(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "")
	raw := f.mustRequest(http.MethodGet, "/api/owners/"+f.own.ID, nil, f.owner(), http.StatusOK)
	var info struct {
		CallLevel string `json:"call_level"`
	}
	_ = json.Unmarshal(raw, &info)
	if info.CallLevel != "never" {
		t.Fatalf("new owner call_level = %q, want never", info.CallLevel)
	}
	f.ask()
	res := f.writtenAnswered("RC-ANSWER")
	if outcome, why := rcOutcome(t, res); outcome != "written" || why != "never" {
		t.Fatalf("outcome = %s/%s, want written/never", outcome, why)
	}
	if !strings.Contains(rcText(res), "RC-ANSWER") {
		t.Fatalf("the written answer did not reach the owner: %s", rcText(res))
	}
	if len(f.callPushes()) != 0 {
		t.Fatal("an owner at Never sent a call push")
	}
}

// F2-3 (I22): without a plan, a level other than Never needs the cost of the
// chosen paid provider acknowledged, checked by the server.
func TestOwnerCallLevel_NonNeverWithoutPlanRequiresCostConfirmation(t *testing.T) {
	f := rcNewFixture(t, "xai-api", "")
	f.mustRequest(http.MethodPatch, "/api/voice/settings", map[string]any{"paid_provider": "xai-api"}, f.owner(), http.StatusOK)
	path := "/api/owners/" + f.own.ID
	f.mustRequest(http.MethodPatch, path, map[string]any{"call_level": "ask_first"}, f.owner(), http.StatusBadRequest)
	// A stale acknowledgement says which cost to confirm, as a code.
	code, body, _, err := f.request(http.MethodPatch, path, map[string]any{"call_level": "ring", "cost_ack_provider": "openai-live-api"}, f.owner())
	var rej struct{ Code, Provider string }
	if err != nil || code != http.StatusBadRequest || json.Unmarshal(body, &rej) != nil || rej.Code != "cost_ack_required" || rej.Provider != "xai-api" {
		t.Fatalf("stale ack: %d %s %v", code, body, err)
	}
	var info struct {
		CallLevel string `json:"call_level"`
	}
	_ = json.Unmarshal(f.mustRequest(http.MethodGet, path, nil, f.owner(), http.StatusOK), &info)
	if info.CallLevel != "never" {
		t.Fatalf("level changed without the cost confirmation: %q", info.CallLevel)
	}
	raw := f.mustRequest(http.MethodPatch, path, map[string]any{"call_level": "ask_first", "cost_ack_provider": "xai-api"}, f.owner(), http.StatusOK)
	var saved struct {
		CallLevel string `json:"call_level"`
		Ack       string `json:"call_cost_ack_provider"`
	}
	_ = json.Unmarshal(raw, &saved)
	if saved.CallLevel != "ask_first" || saved.Ack != "xai-api" {
		t.Fatalf("confirmed level not saved: %s", raw)
	}
	// The acknowledgement is bound to the provider: once paid_provider
	// changes, nothing rings until it is confirmed again.
	f.mustRequest(http.MethodPatch, "/api/voice/settings", map[string]any{"paid_provider": "openai-live-api"}, f.owner(), http.StatusOK)
	f.ask()
	res := f.writtenAnswered("RC-ANSWER")
	if outcome, why := rcOutcome(t, res); outcome != "written" || why != "no_plan" {
		t.Fatalf("stale acknowledgement: outcome = %s/%s, want written/no_plan", outcome, why)
	}
	if len(f.callPushes()) != 0 {
		t.Fatal("a stale cost acknowledgement still rang")
	}
}

// With a plan signed in, no cost acknowledgement is asked.
func TestOwnerCallLevel_WithPlanNeedsNoCostConfirmation(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "")
	f.mustRequest(http.MethodPatch, "/api/owners/"+f.own.ID, map[string]any{"call_level": "ask_first"}, f.owner(), http.StatusOK)
}

// A paid provider with an acknowledged cost and no plan rings, and answering
// opens that paid provider (the only way an owner call is paid).
func TestRequestCall_AcknowledgedPaidProviderRings(t *testing.T) {
	f := rcNewFixture(t, "xai-api", "")
	f.mustRequest(http.MethodPatch, "/api/voice/settings", map[string]any{"paid_provider": "xai-api"}, f.owner(), http.StatusOK)
	f.mustRequest(http.MethodPatch, "/api/owners/"+f.own.ID, map[string]any{"call_level": "ring", "cost_ack_provider": "xai-api"}, f.owner(), http.StatusOK)
	f.ask()
	req := f.awaitRequest("ringing")
	raw := f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated)
	var c vpCreated
	_ = json.Unmarshal(raw, &c)
	if c.Provider != "xai-api" {
		t.Fatalf("answered with %q, want the acknowledged paid provider", c.Provider)
	}
	f.created = true
	if outcome, _ := rcOutcome(t, f.result()); outcome != "answered" {
		t.Fatalf("outcome = %s, want answered", outcome)
	}
	f.dial(c, f.owner(), false).hangup()
}

// Ring: the request rings with an urgent call push; answering opens the
// call through the plan with the reason in the context, and the tool tells
// the owner it is on a call.
func TestRequestCall_RingThenAnswerOpensCallWithReason(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.ask()
	req := f.awaitRequest("ringing")
	if req.Level != "ring" || req.OwnerName != "Infra" || !strings.Contains(req.Reason, "RC-REASON-SENTINEL") {
		t.Fatalf("request = %+v", req)
	}
	pushes := f.callPushes()
	if len(pushes) != 1 {
		t.Fatalf("call pushes = %d, want 1", len(pushes))
	}
	n := pushes[0]
	if n.Level != push.LevelUrgent || n.SessionID != f.osess.ID || n.Tag != "req:"+f.osess.ID || n.Title == "" || n.Body == "" {
		t.Fatalf("call push is not an urgent request notification legacy clients can show: %+v", n)
	}
	if strings.Contains(n.Title+n.Body, "RC-REASON") {
		t.Fatalf("the call reason reached the lock screen: %+v", n)
	}
	raw := f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated)
	var c vpCreated
	_ = json.Unmarshal(raw, &c)
	f.created = true
	res := f.result()
	if outcome, _ := rcOutcome(t, res); outcome != "answered" {
		t.Fatalf("outcome = %s, want answered: %s", outcome, rcText(res))
	}
	call := f.dial(c, f.owner(), false)
	p := f.up.peer(t, f.provider)
	p.await(t, "reason in the call context", func(v map[string]any) bool { return strings.Contains(vpJSON(v), "RC-REASON-SENTINEL") })
	call.hangup()
	// Answered once: a second answer is refused.
	f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusConflict)
}

// Ask first pushes "wants to talk", never the ringing headline.
func TestRequestCall_AskFirstPushesWantsToTalk(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ask_first")
	f.ask()
	f.awaitRequest("ringing")
	pushes := f.callPushes()
	if len(pushes) != 1 || !strings.Contains(pushes[0].Title, "wants to talk") {
		t.Fatalf("ask-first push = %+v", pushes)
	}
	f.mustRequest(http.MethodPost, "/api/voice/requests/"+f.requests()[0].ID+"/write", nil, f.owner(), http.StatusNoContent)
	f.writtenAnswered("RC-ANSWER")
}

// F2-7: unanswered for ring time → missed → the written question.
func TestRequestCall_MissedBecomesWrittenQuestion(t *testing.T) {
	pinSyncAskUser(t)
	f := rcNewFixture(t, "grok-plan", "ring")
	f.setRing(150 * time.Millisecond)
	f.ask()
	res := f.writtenAnswered("RC-ANSWER")
	if outcome, why := rcOutcome(t, res); outcome != "written" || why != "missed" {
		t.Fatalf("outcome = %s/%s, want written/missed", outcome, why)
	}
	if !strings.Contains(rcText(res), "RC-ANSWER") {
		t.Fatalf("answer missing: %s", rcText(res))
	}
	for _, r := range f.requests() {
		if r.State == "ringing" {
			t.Fatal("a missed request still rings")
		}
	}
}

// R3: Write it instead turns the request into the written question.
func TestRequestCall_WriteInsteadBecomesWrittenQuestion(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.ask()
	req := f.awaitRequest("ringing")
	f.mustRequest(http.MethodPost, "/api/voice/requests/"+req.ID+"/write", nil, f.owner(), http.StatusNoContent)
	res := f.writtenAnswered("RC-ANSWER")
	if outcome, why := rcOutcome(t, res); outcome != "written" || why != "declined" {
		t.Fatalf("outcome = %s/%s, want written/declined", outcome, why)
	}
	// Resolved once: answering it now is refused.
	f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusConflict)
}

// F2-4 (I21, D7): the plan fails when answering → no paid provider is
// tried, no paid option is offered, and the request becomes the question.
func TestCallRequest_PlanDownNeverFallsBackToPaid(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	gen, _ := f.store.StoredGeneration("__voice_xai_api_v1")
	if _, err := f.store.CommitLogin("__voice_xai_api_v1", gen, auth.Credential{Type: "api_key", Key: "VP-PAID-KEY"}); err != nil {
		t.Fatal(err)
	}
	f.mustRequest(http.MethodPatch, "/api/voice/settings", map[string]any{"paid_provider": "xai-api"}, f.owner(), http.StatusOK)
	f.ask()
	req := f.awaitRequest("ringing")
	f.up.mu.Lock()
	f.up.reject = http.StatusForbidden
	f.up.mu.Unlock()
	raw := f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusServiceUnavailable)
	if strings.Contains(string(raw), "paid_option") {
		t.Fatalf("an owner call offered the paid API: %s", raw)
	}
	requests, _, _ := f.up.snapshot()
	for _, r := range requests {
		if strings.Contains(r.authorization, "VP-PAID-KEY") {
			t.Fatal("the paid key was used after the plan failed")
		}
	}
	res := f.writtenAnswered("RC-ANSWER")
	if outcome, why := rcOutcome(t, res); outcome != "written" || why != "plan_failed" {
		t.Fatalf("outcome = %s/%s, want written/plan_failed", outcome, why)
	}
}

// R6: paused or outside the allowed hours → written, no push.
func TestRequestCall_PausedOrOutsideHoursWritesWithoutPush(t *testing.T) {
	for name, calls := range map[string]map[string]any{
		"paused":        {"paused_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		"outside_hours": {"hours": rcClosedHours()},
	} {
		t.Run(name, func(t *testing.T) {
			f := rcNewFixture(t, "grok-plan", "ring")
			f.mustRequest(http.MethodPatch, "/api/voice/settings", map[string]any{"calls": calls}, f.owner(), http.StatusOK)
			f.ask()
			res := f.writtenAnswered("RC-ANSWER")
			if outcome, why := rcOutcome(t, res); outcome != "written" || why != name {
				t.Fatalf("outcome = %s/%s, want written/%s", outcome, why, name)
			}
			if len(f.callPushes()) != 0 {
				t.Fatal("a call push left while calls were not allowed")
			}
		})
	}
}

// rcClosedHours is a one-minute window that does not contain now.
func rcClosedHours() map[string]any {
	now := time.Now().UTC().Add(2 * time.Hour)
	from := now.Format("15:04")
	until := now.Add(time.Minute).Format("15:04")
	return map[string]any{"from": from, "until": until, "tz": "UTC"}
}

// F2-9: with the owner's conversation in view there is no push; the request
// still rings in the app.
func TestRequestCall_WatchedConversationGetsNoPush(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	watch(f.osess)
	f.ask()
	f.awaitRequest("ringing")
	if len(f.callPushes()) != 0 {
		t.Fatal("pushed a call request for a conversation in view")
	}
}

// R4: on a call, the request is held (no expiry); hanging up without a
// choice turns it into the written question.
func TestRequestCall_HeldDuringCallBecomesWrittenOnHangup(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.setRing(100 * time.Millisecond)
	c := f.call() // the user talks to another session
	f.ask()
	f.awaitRequest("held")
	time.Sleep(300 * time.Millisecond)
	if len(f.requests()) != 1 || f.requests()[0].State != "held" {
		t.Fatalf("held request changed during the call: %+v", f.requests())
	}
	if len(f.callPushes()) != 0 {
		t.Fatal("a held request pushed during the call")
	}
	c.hangup()
	res := f.writtenAnswered("RC-ANSWER")
	if outcome, why := rcOutcome(t, res); outcome != "written" || why != "missed" {
		t.Fatalf("outcome = %s/%s, want written/missed", outcome, why)
	}
}

// R4: Call on a held request: accept, hang up the current call, answer.
func TestRequestCall_HeldAcceptSwitchesCall(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	c := f.call()
	f.ask()
	req := f.awaitRequest("held")
	f.mustRequest(http.MethodPost, "/api/voice/requests/"+req.ID+"/accept", nil, f.owner(), http.StatusNoContent)
	c.hangup()
	time.Sleep(100 * time.Millisecond)
	if st := f.requests(); len(st) != 1 || st[0].State != "accepted" {
		t.Fatalf("accepted request did not survive the hangup: %+v", st)
	}
	raw := f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated)
	var nc vpCreated
	_ = json.Unmarshal(raw, &nc)
	if outcome, _ := rcOutcome(t, f.result()); outcome != "answered" {
		t.Fatalf("switch did not answer")
	}
	f.dial(nc, f.owner(), false).hangup()
}

// R4: Cancel on a held request writes it.
func TestRequestCall_HeldCancelWrites(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	c := f.call()
	f.ask()
	req := f.awaitRequest("held")
	f.mustRequest(http.MethodPost, "/api/voice/requests/"+req.ID+"/write", nil, f.owner(), http.StatusNoContent)
	res := f.writtenAnswered("RC-ANSWER")
	if outcome, why := rcOutcome(t, res); outcome != "written" || why != "declined" {
		t.Fatalf("outcome = %s/%s, want written/declined", outcome, why)
	}
	c.hangup()
}

// F2-8: stopping the owner's run withdraws the ringing request.
func TestRequestCall_StopWithdrawsRequest(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.ask()
	req := f.awaitRequest("ringing")
	if err := f.mgr.Cancel(f.osess.ID); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "request withdrawn", func() bool {
		for _, r := range f.requests() {
			if r.ID == req.ID {
				return false
			}
		}
		return true
	})
	f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusConflict)
}

// F2-6: write and answer racing have exactly one winner.
func TestRequestCall_SingleWinner(t *testing.T) {
	for range 5 {
		f := rcNewFixture(t, "grok-plan", "ring")
		f.ask()
		req := f.awaitRequest("ringing")
		var wg sync.WaitGroup
		var callStatus, writeStatus int
		var callBody []byte
		wg.Add(2)
		go func() {
			defer wg.Done()
			callStatus, callBody, _, _ = f.request(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner())
		}()
		go func() {
			defer wg.Done()
			writeStatus, _, _, _ = f.request(http.MethodPost, "/api/voice/requests/"+req.ID+"/write", nil, f.owner())
		}()
		wg.Wait()
		wonCall, wonWrite := callStatus == http.StatusCreated, writeStatus == http.StatusNoContent
		if wonCall == wonWrite {
			t.Fatalf("winners: call=%d write=%d", callStatus, writeStatus)
		}
		outcome, _ := rcOutcome(t, func() core.AgentMessage {
			if wonWrite {
				return f.writtenAnswered("RC-ANSWER")
			}
			return f.result()
		}())
		if (outcome == "answered") != wonCall {
			t.Fatalf("outcome %s disagrees with the winner (call=%d)", outcome, callStatus)
		}
		if wonCall {
			var c vpCreated
			_ = json.Unmarshal(callBody, &c)
			f.created = true
			f.dial(c, f.owner(), false).hangup()
		}
	}
}

// A second owner asking while one rings goes to writing (never two at once).
func TestRequestCall_SecondOwnerWhileRingingWrites(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.ask()
	f.awaitRequest("ringing")
	own2, err := f.mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	f.mustRequest(http.MethodPatch, "/api/owners/"+own2.ID, map[string]any{"call_level": "ring"}, f.owner(), http.StatusOK)
	second := &rcFix{vpFixture: f.vpFixture, own: own2, probe: f.probe}
	second.osess, _ = f.mgr.Get(own2.SessionID)
	second.ask()
	res := second.writtenAnswered("RC-ANSWER-2")
	if outcome, why := rcOutcome(t, res); outcome != "written" || why != "busy" {
		t.Fatalf("outcome = %s/%s, want written/busy", outcome, why)
	}
}

// Calling an owner yourself still works at Never (F2-10).
func TestOwnerAtNeverCanStillBeCalled(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "")
	c := f.createFor(f.osess.ID, f.provider, f.owner())
	f.dial(c, f.owner(), false).hangup()
}

// Settings validate the calls block.
func TestVoiceSettings_CallsValidation(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "")
	for _, bad := range []map[string]any{
		{"ring_seconds": 3},
		{"ring_seconds": 600},
		{"hours": map[string]any{"from": "25:00", "until": "08:00", "tz": "UTC"}},
		{"hours": map[string]any{"from": "08:00", "until": "20:00", "tz": "Mars/Base"}},
		{"paused_until": "tomorrow"},
	} {
		f.mustRequest(http.MethodPatch, "/api/voice/settings", map[string]any{"calls": bad}, f.owner(), http.StatusBadRequest)
	}
	f.mustRequest(http.MethodPatch, "/api/voice/settings", map[string]any{"calls": map[string]any{"ring_seconds": 30, "hours": map[string]any{"from": "22:00", "until": "08:00", "tz": "Europe/Madrid"}}}, f.owner(), http.StatusOK)
	got := core.GetVoiceConfig(core.LoadGlobalConfig()).Calls
	if got.RingSeconds != 30 || got.Hours == nil || got.Hours.From != "22:00" {
		t.Fatalf("calls settings not saved: %+v", got)
	}
}

// The allowed window may cross midnight.
func TestCallHoursWindow(t *testing.T) {
	loc := time.UTC
	at := func(h, m int) time.Time { return time.Date(2026, 10, 16, h, m, 0, 0, loc) }
	night := &core.VoiceCallHours{From: "22:00", Until: "08:00", TZ: "UTC"}
	day := &core.VoiceCallHours{From: "09:00", Until: "21:00", TZ: "UTC"}
	cases := []struct {
		h    *core.VoiceCallHours
		t    time.Time
		want bool
	}{
		{nil, at(3, 0), true},
		{day, at(9, 0), true}, {day, at(20, 59), true}, {day, at(21, 0), false}, {day, at(8, 59), false},
		{night, at(23, 0), true}, {night, at(7, 59), true}, {night, at(8, 0), false}, {night, at(12, 0), false},
	}
	for _, c := range cases {
		if got := callHoursAllow(c.h, c.t); got != c.want {
			t.Errorf("%+v at %s = %v, want %v", c.h, c.t.Format("15:04"), got, c.want)
		}
	}
}

// W1: the call push names the request, for the deep link ?call_request=<id>.
func TestRequestCall_PushCarriesRequestID(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ask_first")
	f.ask()
	req := f.awaitRequest("ringing")
	pushes := f.callPushes()
	if len(pushes) != 1 {
		t.Fatalf("pushes = %+v", pushes)
	}
	raw, _ := json.Marshal(pushes[0])
	if !strings.Contains(string(raw), `"request_id":"`+req.ID+`"`) {
		t.Fatalf("payload %s lacks request_id %s", raw, req.ID)
	}
	f.mustRequest(http.MethodPost, "/api/voice/requests/"+req.ID+"/write", nil, f.owner(), http.StatusNoContent)
	f.writtenAnswered("RC-ANSWER")
}

// W3: any visible client suppresses the call push, not just the owner's
// conversation. Other kinds keep asking their own session.
func TestRequestCall_AnyVisibleClientGetsNoPush(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	other, err := f.mgr.CreateSession(CreateOpts{Title: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	watch(other)
	f.ask()
	f.awaitRequest("ringing")
	if n := len(f.callPushes()); n != 0 {
		t.Fatalf("pushed %d call requests while a client is visible on another conversation", n)
	}
}

// W2: a request nobody picked up leaves «Missed call from <owner>» in place
// of the ringing notification: the written question replaces it on the same
// tag. It never carries the question text (lock screen).
func TestRequestCall_MissedReplacesNotificationWithMissedCall(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.setRing(150 * time.Millisecond)
	f.ask()
	ask := waitPendingAsk(f.t, f.osess)
	var asks []push.Notification
	pollUntil(t, 5*time.Second, "written question push", func() bool {
		asks = asks[:0]
		for _, n := range f.probe.delivered() {
			if n.Kind == push.KindAsk {
				asks = append(asks, n)
			}
		}
		return len(asks) == 1
	})
	if asks[0].Title != "Missed call from Infra" || asks[0].Tag != "req:"+f.osess.ID {
		t.Fatalf("replacement push = %+v", asks[0])
	}
	if strings.Contains(asks[0].Body, "RC-QUESTION-SENTINEL") {
		t.Fatalf("question text on the lock screen: %q", asks[0].Body)
	}
	f.mustRequest(http.MethodPost, "/api/sessions/"+f.osess.ID+"/ask", map[string]any{"id": ask.ID, "answers": []string{"Yes"}}, f.owner(), http.StatusNoContent)
	f.result()
}

// W2: Write it instead is somebody's choice, not a missed call.
func TestRequestCall_WriteInsteadIsNotMissedCall(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.ask()
	req := f.awaitRequest("ringing")
	f.mustRequest(http.MethodPost, "/api/voice/requests/"+req.ID+"/write", nil, f.owner(), http.StatusNoContent)
	f.writtenAnswered("RC-ANSWER")
	for _, n := range f.probe.delivered() {
		if n.Kind == push.KindAsk && strings.Contains(n.Title, "Missed") {
			t.Fatalf("a chosen written answer was announced as missed: %+v", n)
		}
	}
}

// feedClient opens the requests feed as a client would and reports its
// document visibility on it, which is what W3 reads: a client showing Tasks,
// the inbox or a detail still rings from its incoming card.
func (f *rcFix) feedClient(visible bool) *websocket.Conn {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	f.t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, f.srv.URL+"/api/voice/requests/ws", &websocket.DialOptions{HTTPHeader: f.owner()})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = conn.CloseNow() })
	// The snapshot comes first; reading it also keeps the socket serviced.
	if _, _, err := conn.Read(ctx); err != nil {
		f.t.Fatal(err)
	}
	frame := `{"type":"presence","visible":` + map[bool]string{true: "true", false: "false"}[visible] + `}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		f.t.Fatal(err)
	}
	return conn
}

// askAndSettle runs one request_call at Ring that nobody answers and waits for
// its written fallback, so "no push" is observed after the announcement.
func (f *rcFix) askAndSettle() {
	f.t.Helper()
	f.setRing(200 * time.Millisecond)
	f.ask()
	waitPendingAsk(f.t, f.osess)
	f.osess.runtime.Bus.Drain(5 * time.Second)
}

// R1 / W3: a client visible on its requests feed suppresses the call push
// whatever view it shows.
func TestRequestCall_VisibleFeedClientGetsNoPush(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.feedClient(true)
	pollUntil(t, 3*time.Second, "feed presence", f.mgr.callRequests().clients.watched)
	f.askAndSettle()
	if n := len(f.callPushes()); n != 0 {
		t.Fatalf("visible Tasks/Inbox client still got %d call push", n)
	}
}

// A hidden feed client, or one whose report lapsed, does not silence it.
func TestRequestCall_HiddenOrLapsedFeedClientStillPushes(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	f.feedClient(false)
	f.askAndSettle()
	if n := len(f.callPushes()); n != 1 {
		t.Fatalf("hidden feed client: %d call pushes, want 1", n)
	}
}

func TestRequestCall_LapsedFeedPresenceStillPushes(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	v := f.mgr.callRequests().clients.join()
	v.apply([]byte(`{"type":"presence","visible":true}`))
	v.mu.Lock()
	v.visibleUntil = time.Now().Add(-time.Nanosecond)
	v.mu.Unlock()
	f.askAndSettle()
	if n := len(f.callPushes()); n != 1 {
		t.Fatalf("lapsed feed presence: %d call pushes, want 1", n)
	}
}
