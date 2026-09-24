package serve

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// TestCacheClock_AnchorsOnRequestNotRunEnd locks in that the prompt-cache
// expiry is measured from the last request that reached the API, not from the
// end of the run. A long run issues its final request well before it finishes;
// anchoring on RunEnded pushed CacheExpiresAt into the future and reported a
// warm cache long after the provider had let it go cold.
func TestCacheClock_AnchorsOnRequestNotRunEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := newTestManager(t, ctx, newMockProvider())
	defer mgr.Shutdown()
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	// A request reaches the API. This is what warms the cache.
	sess.runtime.Bus.Publish(bus.MessageStarted{
		SessionID: sess.ID,
		Message:   core.AgentMessage{Message: core.Message{Role: "assistant", Provider: "anthropic"}},
	})
	pollUntil(t, time.Second, "cache clock anchored on request", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return !sess.lastRunAt.IsZero()
	})

	sess.mu.Lock()
	atRequest := sess.lastRunAt
	sess.mu.Unlock()

	// The run keeps working for a while and only then ends. RunEnded must not
	// move the anchor: no further request was sent, so the cache has been
	// ageing since atRequest.
	time.Sleep(20 * time.Millisecond)
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 1})
	sess.runtime.Bus.Drain(time.Second)

	sess.mu.Lock()
	after := sess.lastRunAt
	sess.mu.Unlock()
	if !after.Equal(atRequest) {
		t.Errorf("RunEnded moved the cache anchor: %v -> %v", atRequest, after)
	}
}

// TestCacheClock_IgnoresNonAnthropicRequests guards the provider gate: a
// provider without a documented TTL (xAI) never warms the clock, and another
// provider's request (OpenAI) must not make info() report a warm Anthropic
// cache.
func TestCacheClock_IgnoresNonAnthropicRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := newTestManager(t, ctx, newMockProvider())
	defer mgr.Shutdown()
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	sess.runtime.Bus.Publish(bus.MessageStarted{
		SessionID: sess.ID,
		Message:   core.AgentMessage{Message: core.Message{Role: "assistant", Provider: "xai"}},
	})
	sess.runtime.Bus.Drain(time.Second)
	sess.mu.Lock()
	warmed := !sess.lastRunAt.IsZero()
	sess.mu.Unlock()
	if warmed {
		t.Error("an xAI request warmed the cache clock")
	}

	sess.runtime.Bus.Publish(bus.MessageStarted{
		SessionID: sess.ID,
		Message:   core.AgentMessage{Message: core.Message{Role: "assistant", Provider: "openai"}},
	})
	sess.runtime.Bus.Drain(time.Second)
	if got := sess.info().CacheExpiresAt; !got.IsZero() {
		t.Errorf("an OpenAI request surfaced an expiry on an Anthropic session: %v", got)
	}
}

// TestCacheClock_ModelSwitchDoesNotLeakExpiry covers both directions of a model
// switch, since the clock (MessageStarted) and the display gate (info()) look
// at different things: the message's provider and the session's current model.
//
// OpenAI and xAI run through the same responses stream, which reports no cache
// write and honors no TTL — their caching is automatic and not user-tunable —
// so neither may ever produce an Anthropic cache countdown.
func TestCacheClock_ModelSwitchDoesNotLeakExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := newTestManager(t, ctx, newMockProvider())
	defer mgr.Shutdown()
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	// An OpenAI and an xAI request must not warm the clock, even though the
	// session's default model is Anthropic: no Anthropic request ever ran.
	for _, provider := range []string{"openai", "xai"} {
		sess.runtime.Bus.Publish(bus.MessageStarted{
			SessionID: sess.ID,
			Message:   core.AgentMessage{Message: core.Message{Role: "assistant", Provider: provider}},
		})
	}
	sess.runtime.Bus.Drain(time.Second)
	if got := sess.info().CacheExpiresAt; !got.IsZero() {
		t.Errorf("non-Anthropic requests surfaced an expiry: %v", got)
	}

	// After a real Anthropic request the countdown appears, anchored on that
	// request rather than on the end of the run.
	sess.runtime.Bus.Publish(bus.MessageStarted{
		SessionID: sess.ID,
		Message:   core.AgentMessage{Message: core.Message{Role: "assistant", Provider: "anthropic"}},
	})
	pollUntil(t, time.Second, "expiry surfaced after an Anthropic request", func() bool {
		return !sess.info().CacheExpiresAt.IsZero()
	})
}

// The clock lives in memory. A resumed session restores it from the last
// Anthropic response in its history, so an idle session still says its cache
// expired after a restart — before the next message pays for it.
func TestCacheClock_RestoredOnResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The real Anthropic provider stamps its responses; the mock does too.
	anthropicReply := func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		msg := core.Message{Role: "assistant", Provider: "anthropic", Content: []core.Content{core.TextContent("hi")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}
	mgr := newTestManager(t, ctx, newMockProvider(anthropicReply))
	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "run finished", func() bool { return sessState(sess) == StateIdle && len(sess.runtime.Context().Agent.Messages()) >= 2 })
	sess.runtime.Bus.Drain(2 * time.Second)
	msgs := sess.runtime.Context().Agent.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || last.Provider != "anthropic" || last.Timestamp == 0 {
		t.Fatalf("last message = %s/%s/%d, want a dated Anthropic response", last.Role, last.Provider, last.Timestamp)
	}

	id := sess.ID
	mgr.mu.Lock()
	delete(mgr.sessions, id)
	mgr.mu.Unlock()
	sess.runtime.Close()
	resumed, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(last.Timestamp, 0).Add(resumed.cacheTTL)
	if got := resumed.info().CacheExpiresAt; !got.Equal(want) {
		t.Fatalf("resumed expiry = %v, want %v", got, want)
	}
}

// Scenario: the warning appears 30 minutes after the last OpenAI request —
// what OpenAI guarantees. xAI documents none at all: no warning.
func TestCacheClock_ProviderWindows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider())

	cases := []struct {
		model, provider string
		window          time.Duration
	}{
		{"openai/gpt-5.6-sol", "openai", 30 * time.Minute},
		{"xai/grok-4.6", "xai", 0},
	}
	for _, tc := range cases {
		sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir(), Model: tc.model})
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		if got := sess.info().Provider; got != tc.provider {
			t.Fatalf("%s resolved to provider %q", tc.model, got)
		}
		before := time.Now()
		sess.runtime.Bus.Publish(bus.MessageStarted{
			SessionID: sess.ID,
			Message:   core.AgentMessage{Message: core.Message{Role: "assistant", Provider: tc.provider}},
		})
		sess.runtime.Bus.Drain(time.Second)
		got := sess.info().CacheExpiresAt
		if tc.window == 0 {
			if !got.IsZero() {
				t.Errorf("%s: expiry %v, want none", tc.provider, got)
			}
			continue
		}
		if got.Before(before.Add(tc.window)) || got.After(time.Now().Add(tc.window)) {
			t.Errorf("%s: expiry %v, want last request + %v", tc.provider, got, tc.window)
		}
	}
}

// The restored clock takes the last response of any provider with a window,
// skipping one that has none.
func TestCacheClock_LastCachedResponse(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "assistant", Provider: "anthropic", Timestamp: 100}},
		{Message: core.Message{Role: "assistant", Provider: "openai", Timestamp: 200}},
		{Message: core.Message{Role: "assistant", Provider: "xai", Timestamp: 300}},
		{Message: core.Message{Role: "user", Timestamp: 400}},
	}
	at, provider := lastCachedResponse(msgs, 5*time.Minute)
	if provider != "openai" || at.Unix() != 200 {
		t.Fatalf("restored %s at %d, want openai at 200", provider, at.Unix())
	}
	if _, provider := lastCachedResponse(msgs[2:], 5*time.Minute); provider != "" {
		t.Fatalf("restored %q from a provider without a window", provider)
	}
}

// A restart remembers the last cut from the transcript's fresh markers, so
// the action stays spent until a request warms the cache again.
func TestCacheClock_LastFreshAt(t *testing.T) {
	transcript := []core.AgentMessage{
		{Message: core.Message{Role: "assistant", Provider: "anthropic", Timestamp: 100}},
		{Message: core.Message{Role: "session_event", Timestamp: 200}, Custom: map[string]any{"type": "fresh_marker"}},
		{Message: core.Message{Role: "user", Timestamp: 150}},
	}
	if got := lastFreshAt(transcript); got.Unix() != 200 {
		t.Fatalf("restored cut at %d, want 200", got.Unix())
	}
}
