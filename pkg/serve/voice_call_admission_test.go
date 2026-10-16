package serve

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
)

// N2: with both plans signed in and no provider named, the call goes to the
// ChatGPT plan, never to Grok and never to a paid provider.
func TestVoiceCall_DefaultPlanIsChatGPTWhenBothPresent(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	if _, err := f.store.CommitLogin("openai", "", auth.Credential{Type: "oauth", Access: providerTestJWT("vp-account", "VP-ACCESS-OLD"), Refresh: "VP-REFRESH-OLD", AccountID: "vp-account", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	raw := f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID}, f.owner(), http.StatusCreated)
	f.created = true
	var got vpCreated
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	f.mustRequest("POST", "/api/voice/calls/"+got.ID+"/hangup", map[string]any{}, f.owner(), http.StatusNoContent)
	if got.Provider != "chatgpt-plan" || got.Billing.Kind != "plan_unverified" {
		t.Fatalf("default with both plans = %s (%s), want chatgpt-plan", got.Provider, got.Billing.Kind)
	}
}

// The global open-attempt rate counts failed opens too: a client looping on
// a refused upstream stops reaching the provider after voiceGlobalRate
// attempts in a minute and is told to retry later.
func TestVoiceCall_GlobalAttemptRateCountsFailures(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	f.up.mu.Lock()
	f.up.reject = http.StatusForbidden
	f.up.mu.Unlock()
	var statuses []int
	for range voiceGlobalRate + 2 {
		status, _, header, err := f.request("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID, "provider": f.provider}, f.owner())
		if err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
		if status == http.StatusTooManyRequests && header.Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
	}
	requests, _, _ := f.up.snapshot()
	if len(requests) > voiceGlobalRate {
		t.Fatalf("upstream opens = %d, want at most %d; statuses %v", len(requests), voiceGlobalRate, statuses)
	}
	if statuses[len(statuses)-1] != http.StatusTooManyRequests {
		t.Fatalf("statuses = %v, want the attempts beyond the global rate refused with 429", statuses)
	}
}

// Failed opens do not spend the principal's per-minute allowance; calls that
// do open spend it, and the one beyond it is refused before any upstream.
func TestVoiceCall_PrincipalRateCountsOnlyEstablishedCalls(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.up.mu.Lock()
	f.up.reject = http.StatusForbidden
	f.up.mu.Unlock()
	for range voicePrincipalRate {
		f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID, "provider": f.provider}, f.owner(), http.StatusServiceUnavailable)
	}
	f.up.mu.Lock()
	f.up.reject = 0
	f.up.mu.Unlock()
	for range voicePrincipalRate {
		c := f.call()
		c.hangup()
	}
	before, _, _ := f.up.snapshot()
	status, _, header, err := f.request("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID, "provider": f.provider}, f.owner())
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := f.up.snapshot()
	if status != http.StatusTooManyRequests || header.Get("Retry-After") == "" || len(after) != len(before) {
		t.Fatalf("call beyond the principal rate: status %d, upstream requests %d→%d; want 429 before any upstream", status, len(before), len(after))
	}
}

// The client transport budgets audio by bytes and controls by count,
// separately: neither kind can take the other's room.
func TestVoiceCall_ClientQueueBudgetsAudioAndControlsSeparately(t *testing.T) {
	stalled := func() *voiceClient {
		cl := newVoiceClient(nil, 48_000)
		cl.cancel() // no writer drains the queue
		<-cl.done
		return cl
	}
	cl := stalled()
	for i := range 32 {
		if !cl.sendJSON(map[string]any{"type": "pong", "n": i}) {
			t.Fatalf("control %d refused within the 32-control budget", i+1)
		}
	}
	if cl.sendJSON(map[string]any{"type": "pong"}) {
		t.Fatal("33rd queued control accepted")
	}
	if !cl.sendAudio(make([]byte, 9600)) {
		t.Fatal("audio refused because controls filled the queue")
	}

	cl = stalled()
	for i := range 200 {
		if !cl.sendAudio(make([]byte, 2)) {
			t.Fatalf("small audio frame %d refused within the byte budget", i+1)
		}
	}
	if !cl.sendAudio(make([]byte, 48_000-400)) {
		t.Fatal("audio refused before reaching 48,000 bytes")
	}
	if cl.sendAudio(make([]byte, 2)) {
		t.Fatal("audio accepted beyond 48,000 queued bytes")
	}
	if !cl.sendJSON(map[string]any{"type": "pong"}) {
		t.Fatal("control refused because audio filled its budget")
	}
}
