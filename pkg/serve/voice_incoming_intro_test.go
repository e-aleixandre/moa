package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Who speaks first is the phone's rule (owner's decision, 17/10, after
// CarPlay): whoever picks up. Calling an owner, the owner picks up, so the
// voice greets at once with one line on where things stand. When an owner
// calls the user, the user picks up: the voice waits for their hello and
// answers it with the reason; if they say nothing for ~2 s it speaks anyway.
//
// # language: en
// Feature: whoever picks up speaks first
//   Scenario: an owner's request_call is answered and the user says hello
//     Given "Infra" is at Ring and asks to call with a reason, an urgency and a written question
//     When the user answers and says hello within 2 seconds
//     Then the voice's initial context carries the reason, the urgency and the question
//     And says to wait for the user's hello and answer it with the reason
//     And the provider is not told to speak before the user
//   Scenario: the user answers and says nothing
//     When the user answers and stays silent for 2 seconds
//     Then the provider is told to speak
//   Scenario: a test invitation with or without a reason
//     Then the voice opens with that reason, or says it is a test call
//   Scenario: the user calls
//     When the user places a call
//     Then the voice is told to greet and say where things stand at once
//     And that what came before is context: the user has asked nothing yet
//     And the provider is told to speak before any user audio

const incomingMarker = "INCOMING CALL"
const outgoingMarker = "OUTGOING CALL"

// openingTrigger: the provider was asked to speak before the user's audio.
func openingTrigger(v map[string]any) bool { return v["type"] == "response.create" }

// awaitOpening checks the incoming context and that, with the user silent,
// the voice speaks after the short wait (not at once).
func awaitOpening(t *testing.T, p *vpPeer, wants ...string) {
	t.Helper()
	start := time.Now()
	cfg := vpJSON(p.await(t, "incoming context", func(v map[string]any) bool { return strings.Contains(vpJSON(v), incomingMarker) }))
	for _, w := range wants {
		if !strings.Contains(cfg, w) {
			t.Errorf("incoming context lacks %q: %s", w, cfg)
		}
	}
	p.await(t, "the voice speaks after the user's silence", openingTrigger)
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Errorf("the voice spoke %v after pickup, before the user had ~2 s to say hello", d)
	}
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
			awaitOpening(t, f.up.peer(t, c.Provider), "RC-REASON-SENTINEL", "RC-QUESTION-SENTINEL", "urgent", "hello")
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

func TestIncoming_UserHelloIsAnsweredWithoutSpeakingFirst(t *testing.T) {
	for _, provider := range []string{"grok-plan", "openai-live-api"} {
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
			p := f.up.peer(t, c.Provider)
			p.await(t, "incoming context", func(v map[string]any) bool { return strings.Contains(vpJSON(v), incomingMarker) })
			p.user(t, "u1", "hola")
			time.Sleep(2500 * time.Millisecond)
			for _, v := range p.snapshot() {
				if openingTrigger(v) {
					t.Fatalf("the voice was told to speak although the user said hello: %s", vpJSON(v))
				}
			}
			call.hangup()
		})
	}
}

func TestIncoming_OutgoingCallGreetsAtOnce(t *testing.T) {
	// chatgpt-plan gets the same InitialContext inside its session
	// instructions (chatgpt_plan.go) and opens from them alone (I10); its fake
	// does not record the session body, so real calls check it.
	for _, provider := range []string{"grok-plan", "openai-live-api"} {
		t.Run(provider, func(t *testing.T) {
			f := vpNewFixture(t, provider, vpBriefHandlers()...)
			call := f.call()
			p := f.up.peer(t, f.provider)
			cfg := vpJSON(p.await(t, "outgoing context", func(v map[string]any) bool { return strings.Contains(vpJSON(v), outgoingMarker) }))
			for _, w := range []string{"greet", "where things stand", "has not asked anything"} {
				if !strings.Contains(cfg, w) {
					t.Errorf("outgoing context lacks %q: %s", w, cfg)
				}
			}
			if strings.Contains(cfg, incomingMarker) {
				t.Errorf("an outgoing call got the incoming opening: %s", cfg)
			}
			p.await(t, "the voice speaks at once", openingTrigger)
			call.hangup()
		})
	}
}

// A brief that arrives mid-call is context, never something the user said
// (call B2Lrf50d0v9Yr: the plan answered "lo nuevo que me has dicho" to it).
func TestIncoming_BriefIsMarkedAsContextNotTheUser(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	call := f.call()
	p := f.up.peer(t, f.provider)
	brief := vpJSON(p.await(t, "brief", func(v map[string]any) bool { return strings.Contains(vpJSON(v), "BRIEF-READY") }))
	if !strings.Contains(brief, "not something the user said") || !strings.Contains(brief, "do not answer it") {
		t.Errorf("brief not marked as context: %s", brief)
	}
	call.hangup()
}

// The ChatGPT plan has no "speak now" command and cannot count seconds: after
// ~2 s of silence moa appends a nudge that says the user has said nothing
// (I11, call 2: instructions alone left it silent). A hello cancels it.
func TestIncoming_ChatGPTPlanNudgedOnlyAfterSilence(t *testing.T) {
	for _, hello := range []bool{false, true} {
		t.Run(map[bool]string{false: "silent", true: "hello"}[hello], func(t *testing.T) {
			f := rcNewFixture(t, "chatgpt-plan", "ring")
			f.ask()
			req := f.awaitRequest("ringing")
			var c vpCreated
			_ = json.Unmarshal(f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated), &c)
			f.created = true
			start := time.Now()
			call := f.dial(c, f.owner(), false)
			p := f.up.peer(t, c.Provider)
			nudge := func(v map[string]any) bool { return strings.Contains(vpJSON(v), "has not said anything yet") }
			if hello {
				p.user(t, "u1", "hola")
				time.Sleep(2500 * time.Millisecond)
				for _, v := range p.snapshot() {
					if nudge(v) {
						t.Fatalf("nudged although the user said hello: %s", vpJSON(v))
					}
				}
			} else {
				p.await(t, "silence nudge", nudge)
				if d := time.Since(start); d < 1500*time.Millisecond {
					t.Errorf("nudged %v after pickup, before ~2 s of silence", d)
				}
			}
			call.hangup()
		})
	}
}
