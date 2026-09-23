package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/usage"
)

// TestUsageCache_MidRunProviderSwitchDoesNotMisattributeRateLimit reproduces
// P2 #4: a session can switch model/provider mid-run (fix/config-while-running
// applies the change at the next request boundary, not instantly), but the
// header from the request already in flight when the switch lands still
// belongs to the OLD provider. The usage cache must attribute each
// RateLimitUpdated reading to the provider that actually served that specific
// request — not to whatever provider the session happened to be running under
// when the run started — or a later, unrelated provider's header can silently
// overwrite the first provider's real reading.
//
// The first response emits a tool call (bash) so a SECOND provider request
// happens within the SAME run: the model is switched to anthropic between the
// two, so the second request genuinely goes out under a different provider —
// exactly the fix/config-while-running scenario, not a second unrelated run.
//
// Before the fix, subscribeUsageCache froze sess.runProvider once at
// RunStarted and reused it for every RateLimitUpdated of the whole run: the
// second (anthropic) header would still have been filed as openai, clobbering
// the real reading with numbers that were never openai's.
func TestUsageCache_MidRunProviderSwitchDoesNotMisattributeRateLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var reqMu sync.Mutex
	var reqs []core.Request
	inFlight, release := make(chan struct{}), make(chan struct{})

	// rateLimitedTurn behaves like a real provider implementation: it stamps
	// the RateLimitUpdated header with the provider that is actually serving
	// THIS request (from req.Model.Provider), exactly like
	// pkg/provider/{anthropic,openai} do.
	rateLimitedTurn := func(msg core.Message, five, seven float64) mockHandler {
		return func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			reqMu.Lock()
			reqs = append(reqs, req)
			reqMu.Unlock()
			ch := make(chan core.AssistantEvent, 8)
			m := msg
			m.Timestamp = time.Now().Unix()
			go func() {
				defer close(ch)
				ch <- core.AssistantEvent{
					Type:      core.ProviderEventRateLimit,
					Provider:  req.Model.Provider,
					RateLimit: &core.RateLimit{FiveHourUtil: five, SevenDayUtil: seven},
				}
				ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
				ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
			}()
			return ch, nil
		}
	}

	bashCall := core.Message{
		Role:       "assistant",
		Content:    []core.Content{core.ToolCallContent("tc-1", "bash", map[string]any{"command": "echo hi"})},
		StopReason: "tool_use",
	}
	doneMsg := core.Message{
		Role:       "assistant",
		Content:    []core.Content{core.TextContent("done")},
		StopReason: "end_turn",
	}

	// First request: openai, correct header, a tool call so the run continues.
	first := rateLimitedTurn(bashCall, 0.11, 0.22)
	// Second request (after the mid-run switch): anthropic, deliberately very
	// different numbers so any misattribution onto the openai cache is obvious.
	second := rateLimitedTurn(doneMsg, 0.99, 0.98)

	prov := newMockProvider(
		func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			close(inFlight)
			<-release
			return first(ctx, req)
		},
		second,
	)

	moaCfg := core.MoaConfig{
		DisableSandbox:    true,
		AutoTitleModel:    "off",
		SessionBriefModel: "haiku",
		Permissions:       core.PermissionsConfig{Mode: "yolo"},
	}
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), moaCfg)
	mgr.usagePoller = &usage.MultiPoller{}
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	sess, err := mgr.CreateSession(CreateOpts{Model: "openai/gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := mgr.Send(sess.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	<-inFlight
	if got := sessState(sess); got != StateRunning {
		t.Fatalf("state = %q, want running", got)
	}

	// Switch provider mid-run: the request already in flight keeps using
	// openai; only the NEXT request (after the tool call) picks up anthropic.
	resp := apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/config", `{"model":"claude-opus-5"}`)
	defer resp.Body.Close() //nolint:errcheck
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH model while running = %d (%v)", resp.StatusCode, err)
	}
	close(release)

	pollUntil(t, 5*time.Second, "run finished", func() bool { return sessState(sess) == StateIdle })
	sess.runtime.Bus.Drain(time.Second)

	reqMu.Lock()
	n := len(reqs)
	reqMu.Unlock()
	if n != 2 {
		t.Fatalf("provider requests = %d, want 2 (the tool call must have produced a second, post-switch request)", n)
	}

	// The first (openai) header must be the one on record: the second
	// (anthropic) header must never have reached the openai cache.
	pollUntil(t, time.Second, "OpenAI usage observed", func() bool {
		snap, _ := mgr.usagePoller.GetProvider(context.Background(), "openai")
		return snap != nil && snap.FiveHour != nil && snap.SevenDay != nil
	})
	snap, _ := mgr.usagePoller.GetProvider(context.Background(), "openai")
	if snap.FiveHour.Utilization != 11 || snap.SevenDay.Utilization != 22 {
		t.Fatalf("OpenAI windows = (%v, %v), want (11, 22) — the run's second (anthropic) header must not overwrite them",
			snap.FiveHour.Utilization, snap.SevenDay.Utilization)
	}

	// No provider other than openai is ever recorded by this cache (it is
	// openai-only by design), so the anthropic header must simply be absent.
	if anthropicSnap, _ := mgr.usagePoller.GetProvider(context.Background(), "anthropic"); anthropicSnap != nil {
		t.Fatalf("anthropic snapshot = %#v, want none — this cache tracks openai headers only", anthropicSnap)
	}
}
