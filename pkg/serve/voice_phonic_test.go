package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// phonicSwitch turns the experimental switch on in the fixture's config dir.
func phonicSwitch(t *testing.T) {
	t.Helper()
	path := core.ConfigSubdir("voice-provider-phonic")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func phonicKey(t *testing.T, f *vpFixture) {
	t.Helper()
	if _, err := f.store.CommitLogin("phonic", "", auth.Credential{Type: "api_key", Key: "VP-KEY-phonic"}); err != nil {
		t.Fatal(err)
	}
}

func phonicCreate(t *testing.T, f *vpFixture, provider string, want int) vpCreated {
	t.Helper()
	body := map[string]any{"session_id": f.sess.ID}
	if provider != "" {
		body["provider"] = provider
	}
	raw := f.mustRequest("POST", "/api/voice/calls", body, f.owner(), want)
	var c vpCreated
	_ = json.Unmarshal(raw, &c)
	if want == http.StatusCreated {
		f.created = true
	}
	return c
}

func TestPhonicSwitchOffKeepsThePlanDefault(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	phonicKey(t, f) // a key alone never selects a paid provider
	c := phonicCreate(t, f, "", http.StatusCreated)
	if c.Provider != "grok-plan" {
		t.Fatalf("provider = %q, want the plan", c.Provider)
	}
	var st voiceStatus
	_ = json.Unmarshal(f.mustRequest("GET", "/api/voice/status", nil, f.owner(), 200), &st)
	if st.OutgoingProvider != "grok-plan" {
		t.Fatalf("outgoing_provider = %q", st.OutgoingProvider)
	}
}

func TestPhonicSwitchOnPlacesTheUsersCallWithTheSameBriefAndTools(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	phonicKey(t, f)
	phonicSwitch(t)
	// Before the call: the brief run would make a later append fail.
	f.appendUser("PHONIC-READ-CONTROL")
	var st voiceStatus
	_ = json.Unmarshal(f.mustRequest("GET", "/api/voice/status", nil, f.owner(), 200), &st)
	if st.OutgoingProvider != voice.Phonic || st.Default != "grok-plan" {
		t.Fatalf("status default=%q outgoing=%q: default must stay the plan", st.Default, st.OutgoingProvider)
	}
	c := phonicCreate(t, f, "", http.StatusCreated)
	if c.Provider != "phonic" || c.Billing.Kind != "per_minute" || c.Billing.Price != 0.07 {
		t.Fatalf("created = %+v", c)
	}
	call := f.dial(c, f.owner(), true)
	call.await("listening state", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	peer := f.up.peer(t, "phonic")
	cfg := peer.await(t, "config", func(v map[string]any) bool { return v["type"] == "config" })
	if prompt, _ := cfg["system_prompt"].(string); !strings.HasPrefix(prompt, voice.CommonInstructions) || !strings.Contains(prompt, "OUTGOING CALL") {
		t.Fatal("phonic must get the common instructions and the call context")
	}
	if tools, _ := cfg["tools"].([]any); len(tools) != len(voice.ToolNames) {
		t.Fatalf("tools = %d, want the common allowlist", len(tools))
	}
	peer.tool(t, "call-read", "read_session", map[string]any{})
	if r := peer.result(t, "call-read"); vpError(r) || !strings.Contains(vpJSON(r), "PHONIC-READ-CONTROL") {
		t.Fatalf("read_session failed: %v", r)
	}
	f.mustRequest("POST", "/api/voice/calls/"+c.ID+"/hangup", nil, f.owner(), http.StatusNoContent)
	peer.await(t, "minutes request", func(v map[string]any) bool {
		return v["type"] == "generate_reply" && v["system_message"] == voice.MinutesRequest
	})
	select {
	case <-peer.closed:
	case <-time.After(vpDeadline):
		t.Fatal("phonic socket not closed at hangup")
	}
}

func TestPhonicSwitchOnRespectsAnExplicitProvider(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	phonicKey(t, f)
	phonicSwitch(t)
	if c := phonicCreate(t, f, "grok-plan", http.StatusCreated); c.Provider != "grok-plan" {
		t.Fatalf("provider = %q", c.Provider)
	}
}

func TestPhonicSwitchOnWithoutKeyFailsWithoutFallingBack(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	phonicSwitch(t)
	raw := f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID}, f.owner(), http.StatusBadGateway)
	if !strings.Contains(string(raw), "provider_unavailable") {
		t.Fatalf("body = %s", raw)
	}
	reqs, _, _ := f.up.snapshot()
	for _, r := range reqs {
		if r.origin == "api.x.ai" {
			t.Fatal("a failed phonic call must not open the plan instead")
		}
	}
	f.harnessOnly = true
}

func TestPhonicIsNotASettingsChoice(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	phonicKey(t, f)
	raw := f.mustRequest("PATCH", "/api/voice/settings", map[string]any{"paid_provider": "phonic"}, f.owner(), http.StatusBadRequest)
	if !strings.Contains(string(raw), "invalid_paid_provider") {
		t.Fatalf("body = %s", raw)
	}
}
