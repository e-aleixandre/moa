package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Incoming calls open with the reason (owner's decision, 16/10, after his
// first real incoming call): when moa calls, the voice speaks first and says
// why; when the user calls, nothing changes.
//
// # language: en
// Feature: an incoming call says why it rings
//   Scenario: an owner's request_call is answered
//     Given "Infra" is at Ring and asks to call with a reason, an urgency and a written question
//     When the user answers
//     Then the voice's initial context says it is an incoming call and to speak first
//     And it carries the reason, the urgency and the question
//     And the provider is told to respond before any user audio
//   Scenario: a test invitation with a reason
//     When the user rings themselves with reason "RC-TEST-REASON" and answers
//     Then the voice opens with that reason
//   Scenario: a test invitation without a reason
//     When the user rings themselves without a reason and answers
//     Then the voice says it is a test call
//   Scenario: the user calls
//     When the user places a call
//     Then the voice gets no incoming-call opening and waits for the user

const incomingMarker = "INCOMING CALL"

// openingTrigger: the provider was asked to speak before the user's audio.
func openingTrigger(v map[string]any) bool { return v["type"] == "response.create" }

func awaitOpening(t *testing.T, p *vpPeer, wants ...string) {
	t.Helper()
	cfg := vpJSON(p.await(t, "incoming context", func(v map[string]any) bool { return strings.Contains(vpJSON(v), incomingMarker) }))
	for _, w := range wants {
		if !strings.Contains(cfg, w) {
			t.Errorf("incoming context lacks %q: %s", w, cfg)
		}
	}
	p.await(t, "the voice speaks first", openingTrigger)
}

func TestIncoming_RequestCallOpensWithReason(t *testing.T) {
	for _, provider := range []string{"grok-plan", "xai-api", "openai-live-api"} {
		t.Run(provider, func(t *testing.T) {
			f := rcNewFixture(t, "grok-plan", "ring")
			if provider != "grok-plan" {
				f = rcNewFixture(t, provider, "")
				f.mustRequest(http.MethodPatch, "/api/voice/settings", map[string]any{"paid_provider": provider}, f.owner(), http.StatusOK)
				f.mustRequest(http.MethodPatch, "/api/owners/"+f.own.ID, map[string]any{"call_level": "ring", "cost_ack_provider": provider}, f.owner(), http.StatusOK)
			}
			f.ask()
			req := f.awaitRequest("ringing")
			var c vpCreated
			_ = json.Unmarshal(f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated), &c)
			f.created = true
			call := f.dial(c, f.owner(), false)
			awaitOpening(t, f.up.peer(t, c.Provider), "RC-REASON-SENTINEL", "RC-QUESTION-SENTINEL", "urgent", "Speak first")
			call.hangup()
		})
	}
}

func TestIncoming_TestInviteOpensWithReasonOrSaysTest(t *testing.T) {
	for name, reason := range map[string]string{"with_reason": "RC-TEST-REASON", "without_reason": ""} {
		t.Run(name, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			body := map[string]any{"session_id": f.sess.ID}
			if reason != "" {
				body["reason"] = reason
			}
			var inv map[string]any
			_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/invites", body, f.owner(), http.StatusCreated), &inv)
			var c vpCreated
			_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/invites/"+inv["id"].(string)+"/answer", map[string]any{}, f.voice(0), http.StatusCreated), &c)
			f.created = true
			call := f.dial(c, f.voice(0), false)
			want := "test call"
			if reason != "" {
				want = reason
			}
			awaitOpening(t, f.up.peer(t, c.Provider), want)
			call.send(map[string]any{"type": "hangup"})
		})
	}
}

func TestIncoming_OutgoingCallWaitsForTheUser(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	call := f.call()
	p := f.up.peer(t, f.provider)
	p.await(t, "initial configuration", func(v map[string]any) bool { return v["type"] == "session.update" })
	vpAwaitBrief(t, f, p)
	time.Sleep(200 * time.Millisecond)
	for _, v := range p.snapshot() {
		if strings.Contains(vpJSON(v), incomingMarker) {
			t.Fatalf("an outgoing call got the incoming opening: %s", vpJSON(v))
		}
		if openingTrigger(v) {
			t.Fatalf("an outgoing call spoke before the user: %s", vpJSON(v))
		}
	}
	call.hangup()
}
