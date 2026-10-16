package serve

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func vsStatus(t *testing.T, f *vpFixture) voiceStatus {
	t.Helper()
	raw := f.mustRequest("GET", "/api/voice/status", nil, f.owner(), 200)
	for _, secret := range []string{"VP-ACCESS", "VP-REFRESH", "VP-KEY", "vp-account"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("status leaks %q: %s", secret, raw)
		}
	}
	var s voiceStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func vsProvider(s voiceStatus, id string) voiceProviderStatus {
	for _, p := range s.Providers {
		if string(p.ID) == id {
			return p
		}
	}
	return voiceProviderStatus{}
}

// Defaults are N2 (ChatGPT, then Grok; gpt-6-sol behind the tools), the
// status says only ready/not connected, and choosing a paid provider in
// Settings never makes a call paid by itself.
func TestVoiceSettings_DefaultsSafeStatusAndPaidOnlyOffered(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	s := vsStatus(t, f)
	if s.BackendModel != core.DefaultVoiceBackendModel || s.Provider != "" || s.PaidProvider != "" || s.Default != "grok-plan" || s.ActiveCalls != 0 {
		t.Fatalf("defaults = %+v", s)
	}
	if got := len(s.Providers); got != 4 {
		t.Fatalf("providers listed = %d, want 4", got)
	}
	if p := vsProvider(s, "grok-plan"); p.State != "ready" || p.Paid || p.Billing.Kind != "plan_unverified" {
		t.Fatalf("grok-plan = %+v", p)
	}
	if p := vsProvider(s, "chatgpt-plan"); p.State != "not_connected" {
		t.Fatalf("chatgpt-plan without sign-in = %+v", p)
	}
	if p := vsProvider(s, "xai-api"); !p.Paid || p.Billing.Kind != "per_minute" || p.State != "not_connected" {
		t.Fatalf("xai-api without key = %+v", p)
	}

	for _, bad := range []map[string]any{
		{"provider": "xai-api"},        // a paid provider is never the default
		{"paid_provider": "grok-plan"}, // a plan is not a paid alternative
		{"backend_model": "no-such-model"},
	} {
		if st, body, _, _ := f.request("PATCH", "/api/voice/settings", bad, f.owner()); st != 400 {
			t.Fatalf("PATCH %v = %d %s, want 400", bad, st, body)
		}
	}
	f.mustRequest("PATCH", "/api/voice/settings", map[string]any{"provider": "chatgpt-plan", "paid_provider": "xai-api"}, f.owner(), 200)
	cfg := core.LoadGlobalConfig()
	if cfg.Voice == nil || cfg.Voice.Provider != "chatgpt-plan" || cfg.Voice.PaidProvider != "xai-api" || cfg.Voice.BackendModel != "" {
		t.Fatalf("saved voice config = %+v", cfg.Voice)
	}
	// ChatGPT is preferred but not signed in: the default falls back to the
	// signed-in plan, never to the paid provider just chosen.
	if s := vsStatus(t, f); s.Default != "grok-plan" || s.PaidProvider != "xai-api" {
		t.Fatalf("after settings = %+v", s)
	}
	raw := f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID}, f.owner(), 201)
	var c vpCreated
	_ = json.Unmarshal(raw, &c)
	if c.Provider != "grok-plan" || c.Billing.Kind != "plan_unverified" {
		t.Fatalf("unnamed call = %s, want the plan", raw)
	}
	f.created = true
	f.mustRequest("POST", "/api/voice/calls/"+c.ID+"/hangup", nil, f.owner(), 204)
	// "" restores defaults and removes the section.
	f.mustRequest("PATCH", "/api/voice/settings", map[string]any{"provider": "", "paid_provider": ""}, f.owner(), 200)
	if cfg := core.LoadGlobalConfig(); cfg.Voice != nil {
		t.Fatalf("cleared voice config = %+v", cfg.Voice)
	}
}

// A call is counted where a release preflight looks while it runs, and
// leaves its measured usage: seconds, deliveries, questions, latency and the
// session's spend delta, labelled as a delta.
func TestVoiceCall_ActivityAndUsage(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	var act activity
	_ = json.Unmarshal(f.mustRequest("GET", "/api/activity", nil, f.owner(), 200), &act)
	if act.VoiceCalls != 1 {
		t.Fatalf("activity during the call = %+v, want 1 voice call", act)
	}
	if s := vsStatus(t, f); s.ActiveCalls != 1 {
		t.Fatalf("status active_calls = %d", s.ActiveCalls)
	}
	p.tool(t, "pass", "pass_to_session", map[string]any{"summary": "USAGE-PASS"})
	_ = p.result(t, "pass")
	f.waitRecords("relay", 1)
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"USAGE-DECIDED"}, "passed": []string{}, "open": []string{}})
	c.hangup()
	minutes := f.waitRecords("minutes", 1)[0]
	deadline := time.Now().Add(vpDeadline)
	for f.mgr.ActiveVoiceCalls() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_ = json.Unmarshal(f.mustRequest("GET", "/api/activity", nil, f.owner(), 200), &act)
	if act.VoiceCalls != 0 {
		t.Fatalf("activity after hangup = %+v", act)
	}
	s := vsStatus(t, f)
	if len(s.Recent) != 1 {
		t.Fatalf("recent usage = %+v", s.Recent)
	}
	u := s.Recent[0]
	if u.CallID != c.c.ID || u.SessionID != f.sess.ID || u.Provider != "grok-plan" || u.Billing != "plan_unverified" || u.Deliveries != 1 || u.Asks != 0 || u.EndedAt == 0 {
		t.Fatalf("usage = %+v", u)
	}
	if u.SessionCostDeltaUSD < 0 {
		t.Fatalf("cost delta negative: %+v", u)
	}
	if got := vpJSON(minutes.Custom["usage"]); !strings.Contains(got, `"deliveries":1`) || !strings.Contains(got, `"session_cost_delta_usd"`) {
		t.Fatalf("minutes usage = %s", got)
	}
}
