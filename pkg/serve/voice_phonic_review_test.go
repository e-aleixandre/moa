package serve

import (
	"context"
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

func TestPhonicReviewDirectoryUsesSwitchForUserPlacedCall(t *testing.T) {
	f := newInviteFixture(t, 1)
	phonicKey(t, f.vpFixture)
	phonicSwitch(t)
	own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "Review owner")
	var c vpCreated
	_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0), 201), &c)
	f.created = true
	if c.Provider != "phonic" {
		t.Fatalf("directory placed call via %q", c.Provider)
	}
	f.mustRequest("POST", "/api/voice/calls/"+c.ID+"/hangup", nil, f.voice(0), 204)
}

func TestPhonicReviewTestInvitationUsesSwitchWithoutAnswerTo(t *testing.T) {
	f := newInviteFixture(t, 1)
	phonicKey(t, f.vpFixture)
	phonicSwitch(t)
	id := f.ring()["id"].(string)
	inv, _ := f.mgr.voiceRegistry().invite(id)
	if inv.answerTo != nil {
		t.Fatal("test fixture unexpectedly has an owner request")
	}
	var c vpCreated
	_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, f.voice(0), 201), &c)
	f.created = true
	if c.Provider != "phonic" {
		t.Fatalf("test invitation placed call via %q", c.Provider)
	}
	f.mustRequest("POST", "/api/voice/calls/"+c.ID+"/hangup", nil, f.voice(0), 204)
}

func TestPhonicReviewOwnerRequestKeepsItsProviderAcrossSwitch(t *testing.T) {
	f := rcNewFixture(t, "grok-plan", "ring")
	phonicKey(t, f.vpFixture)
	f.ask()
	req := f.awaitRequest("ringing")
	phonicSwitch(t)
	var c vpCreated
	_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), 201), &c)
	f.created = true
	if c.Provider != "grok-plan" {
		t.Fatalf("owner request redirected to %q", c.Provider)
	}
	f.dial(c, f.owner(), false).hangup()
}

func TestPhonicReviewOwnerCannotAcquirePaidConsentFromSwitch(t *testing.T) {
	f := rcNewFixture(t, "xai-api", "")
	phonicKey(t, f.vpFixture)
	phonicSwitch(t)
	f.mustRequest("PATCH", "/api/owners/"+f.own.ID, map[string]any{"call_level": "ring"}, f.owner(), 400)
	own, err := f.mgr.callingOwner(f.osess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := f.mgr.ownerCallProvider(own); ok {
		t.Fatalf("unacknowledged owner got %s", p)
	}
	if f.mgr.voicePlanSignedIn() {
		t.Fatal("switch was treated as a plan")
	}
}

func TestPhonicReviewSwitchDirectoryAndChatGPTStatus(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	f.harnessOnly = true
	path := core.ConfigSubdir("voice-provider-phonic")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if phonicSwitchOn() {
		t.Fatal("directory enabled Phonic")
	}
	var st voiceStatus
	_ = json.Unmarshal(f.mustRequest("GET", "/api/voice/status", nil, f.owner(), 200), &st)
	if st.Default != voice.ChatGPTPlan || st.OutgoingProvider != voice.ChatGPTPlan || len(st.Providers) != 4 {
		t.Fatalf("status=%+v", st)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	phonicSwitch(t)
	_ = json.Unmarshal(f.mustRequest("GET", "/api/voice/status", nil, f.owner(), 200), &st)
	if st.Default != voice.ChatGPTPlan || st.OutgoingProvider != voice.Phonic || st.OwnerCallsPaidProvider != "" {
		t.Fatalf("switch corrupted plan/default/cost status: %+v", st)
	}
}

func TestPhonicReviewStoreRereadsPrimaryKeyWithoutDisturbingPlan(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	_, creds, ok := f.mgr.voiceRegistry().adapter(voice.Phonic)
	if !ok {
		t.Fatal("no adapter")
	}
	if _, err := creds.Resolve(context.Background()); err == nil {
		t.Fatal("key existed before synthetic login")
	}
	before, err := f.store.ResolveSnapshot(context.Background(), "xai", auth.SelectPlanOAuth)
	if err != nil {
		t.Fatal(err)
	}
	other := auth.NewStore(filepath.Join(f.root, "auth.json"))
	if _, err := other.CommitLogin("phonic", "", auth.Credential{Type: "api_key", Key: "REVIEW-SYNTHETIC-KEY"}); err != nil {
		t.Fatal(err)
	}
	got, err := creds.Resolve(context.Background())
	if err != nil || got.Token != "REVIEW-SYNTHETIC-KEY" {
		t.Fatalf("stale key snapshot: err=%v", err)
	}
	after, err := f.store.ResolveSnapshot(context.Background(), "xai", auth.SelectPlanOAuth)
	if err != nil || after.Generation != before.Generation || after.Token != before.Token {
		t.Fatal("login disturbed existing plan")
	}
	info, err := os.Stat(filepath.Join(f.root, "auth.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("auth.json not 0600")
	}
}

func TestPhonicReviewHTTP401NeverRefreshesOrLogsKey(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	phonicKey(t, f)
	phonicSwitch(t)
	f.up.mu.Lock()
	f.up.reject = http.StatusUnauthorized
	f.up.mu.Unlock()
	raw := f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID}, f.owner(), 502)
	reqs, peers, refreshes := f.up.snapshot()
	if len(reqs) != 1 || len(peers) != 0 || refreshes != 0 || !strings.Contains(string(raw), "unauthorized") {
		t.Fatalf("attempts=%d peers=%d refreshes=%d raw=%s", len(reqs), len(peers), refreshes, raw)
	}
	if strings.Contains(string(raw)+f.log.String(), "VP-KEY-phonic") {
		t.Fatal("key leaked from rejected HTTP body")
	}
}

func TestPhonicReviewLateBriefAskSessionAndMinutesShareRealTools(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	brief := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-gate:
			return simpleResponse("REVIEW-LATE-BRIEF"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	limits := vpDefaultLimits()
	limits.grace = 3 * time.Second
	f := vpNewFixtureWithLimits(t, "grok-plan", limits, brief, func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		return simpleResponse("REVIEW-ASK-ANSWER"), nil
	})
	phonicKey(t, f)
	phonicSwitch(t)
	f.appendUser("REVIEW-FALLBACK")
	c := phonicCreate(t, f, "", 201)
	call := f.dial(c, f.owner(), true)
	call.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	p := f.up.peer(t, "phonic")
	cfg := p.await(t, "config", func(v map[string]any) bool { return v["type"] == "config" })
	prompt, _ := cfg["system_prompt"].(string)
	if !strings.Contains(prompt, voice.InstructionRelayOnRequest) || !strings.Contains(prompt, "REVIEW-FALLBACK") {
		t.Fatal("common relay policy or fallback absent")
	}
	select {
	case <-started:
	case <-time.After(vpDeadline):
		close(gate)
		t.Fatal("brief not requested")
	}
	close(gate)
	p.await(t, "late brief", func(v map[string]any) bool {
		return v["type"] == "add_system_message" && strings.Contains(vpJSON(v), "REVIEW-LATE-BRIEF")
	})
	p.tool(t, "review-ask", "ask_session", map[string]any{"question": "What is the context?"})
	if r := p.result(t, "review-ask"); r["status"] != "asked" {
		t.Fatalf("ask not immediate asked: %v", r)
	}
	p.await(t, "async answer", func(v map[string]any) bool {
		return v["type"] == "add_system_message" && strings.Contains(vpJSON(v), "REVIEW-ASK-ANSWER")
	})
	p.tool(t, "review-minutes", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}})
	call.await("ended", func(v map[string]any) bool { return v["type"] == "ended" })
	f.mustRequest("POST", "/api/voice/calls/"+c.ID+"/hangup", nil, f.owner(), 204)
}
