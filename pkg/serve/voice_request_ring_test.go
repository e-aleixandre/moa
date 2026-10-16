package serve

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/push"
)

// # language: en
// Feature: Ring makes the iPhone ring through CallKit
//   Scenario: an owner at Ring asks to call and an iPhone can ring
//     Then every iPhone rings with a VoIP invitation named after the owner
//     And no alert push "call_request" is sent for it
//     When the user answers on the iPhone
//     Then the call opens with the reason and the owner is told it was answered
//     And answering the same request from the web is refused
//   Scenario: the user declines on the iPhone → the written question (declined)
//   Scenario: the user answers on the web while the iPhone rings → the iPhone stops ringing
//   Scenario: an owner at Ask first never rings an iPhone; it sends the alert push
//   Scenario: a request held during a call never rings an iPhone

func rcRingFixture(t *testing.T, level string) (*rcFix, *inviteFixture) {
	t.Helper()
	inv := newInviteFixtureWith(t, 2, true, rcHandlers()...)
	return rcWrap(t, inv.vpFixture, level), inv
}

func (f *rcFix) inviteOf(inv *inviteFixture) push.EnvelopeContent {
	f.t.Helper()
	var got push.EnvelopeContent
	pollUntil(f.t, 5*time.Second, "VoIP invitation", func() bool {
		all, _ := inv.relay.delivered()
		for _, c := range all {
			if c.Kind == push.KindCall {
				got = c
				return true
			}
		}
		return false
	})
	return got
}

func inviteState(f *inviteFixture, id string) string {
	var st map[string]any
	_ = json.Unmarshal(f.mustRequest("GET", "/api/voice/invites/"+id, nil, f.voice(0), http.StatusOK), &st)
	s, _ := st["state"].(string)
	return s
}

func TestRequestCallRing_AnsweredOnIPhone(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	f.ask()
	req := f.awaitRequest("ringing")
	env := f.inviteOf(inv)
	if env.Title != "Infra" || env.Sess != f.osess.ID || env.Invite == "" {
		t.Fatalf("invitation = %+v", env)
	}
	if n := len(f.callPushes()); n != 0 {
		t.Fatalf("Ring with a ringing iPhone also sent %d call_request pushes", n)
	}
	var c vpCreated
	_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/invites/"+env.Invite+"/answer", map[string]any{}, inv.voice(0), http.StatusCreated), &c)
	f.created = true
	if outcome, _ := rcOutcome(t, f.result()); outcome != "answered" {
		t.Fatalf("outcome = %s, want answered", outcome)
	}
	f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusConflict)
	call := f.dial(c, inv.voice(0), false)
	awaitOpening(t, f.up.peer(t, c.Provider), "RC-REASON-SENTINEL")
	call.send(map[string]any{"type": "hangup"})
}

func TestRequestCallRing_DeclinedOnIPhoneWrites(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	f.ask()
	f.awaitRequest("ringing")
	env := f.inviteOf(inv)
	f.mustRequest("POST", "/api/voice/invites/"+env.Invite+"/decline", map[string]any{}, inv.voice(0), http.StatusNoContent)
	if outcome, why := rcOutcome(t, f.writtenAnswered("RC-ANSWER")); outcome != "written" || why != "declined" {
		t.Fatalf("outcome = %s/%s, want written/declined", outcome, why)
	}
}

func TestRequestCallRing_MissedStopsIPhone(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	f.setRing(300 * time.Millisecond)
	f.ask()
	env := f.inviteOf(inv)
	if outcome, why := rcOutcome(t, f.writtenAnswered("RC-ANSWER")); outcome != "written" || why != "missed" {
		t.Fatalf("outcome = %s/%s, want written/missed", outcome, why)
	}
	if st := inviteState(inv, env.Invite); st == "ringing" {
		t.Fatal("the iPhone still rings after the request was missed")
	}
}

func TestRequestCallRing_WebAnswerStopsIPhone(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	f.ask()
	req := f.awaitRequest("ringing")
	env := f.inviteOf(inv)
	var c vpCreated
	_ = json.Unmarshal(f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated), &c)
	f.created = true
	if st := inviteState(inv, env.Invite); st != "cancelled" {
		t.Fatalf("invitation = %s after answering on the web, want cancelled", st)
	}
	f.mustRequest("POST", "/api/voice/invites/"+env.Invite+"/answer", map[string]any{}, inv.voice(0), http.StatusConflict)
	f.dial(c, f.owner(), false).hangup()
}

func TestRequestCallRing_AskFirstNeverRingsIPhone(t *testing.T) {
	f, inv := rcRingFixture(t, "ask_first")
	f.ask()
	f.awaitRequest("ringing")
	pollUntil(t, 5*time.Second, "call_request alert", func() bool { return len(f.callPushes()) == 1 })
	all, _ := inv.relay.delivered()
	for _, c := range all {
		if c.Kind == push.KindCall {
			t.Fatalf("Ask first rang an iPhone: %+v", c)
		}
	}
}

func TestRequestCallRing_HeldNeverRingsIPhone(t *testing.T) {
	f, inv := rcRingFixture(t, "ring")
	c := f.call()
	f.ask()
	f.awaitRequest("held")
	time.Sleep(200 * time.Millisecond)
	all, _ := inv.relay.delivered()
	for _, e := range all {
		if e.Kind == push.KindCall {
			t.Fatalf("a held request rang an iPhone: %+v", e)
		}
	}
	c.hangup()
}

// Answering on the iPhone and on the web at once: exactly one call opens.
func TestRequestCallRing_IPhoneAndWebOneWinner(t *testing.T) {
	for range 4 {
		f, inv := rcRingFixture(t, "ring")
		f.ask()
		req := f.awaitRequest("ringing")
		env := f.inviteOf(inv)
		type res struct {
			code int
			body []byte
		}
		phone, web := make(chan res, 1), make(chan res, 1)
		go func() {
			c, b, _, _ := f.request("POST", "/api/voice/invites/"+env.Invite+"/answer", map[string]any{}, inv.voice(0))
			phone <- res{c, b}
		}()
		go func() {
			c, b, _, _ := f.request(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner())
			web <- res{c, b}
		}()
		p, w := <-phone, <-web
		if (p.code == http.StatusCreated) == (w.code == http.StatusCreated) {
			t.Fatalf("phone=%d web=%d: want exactly one call", p.code, w.code)
		}
		if outcome, _ := rcOutcome(t, f.result()); outcome != "answered" {
			t.Fatalf("outcome = %s", outcome)
		}
		won, hdr := p, inv.voice(0)
		if w.code == http.StatusCreated {
			won, hdr = w, f.owner()
		}
		var c vpCreated
		_ = json.Unmarshal(won.body, &c)
		f.created = true
		f.dial(c, hdr, false).send(map[string]any{"type": "hangup"})
	}
}
