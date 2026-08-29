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

// TestCacheClock_IgnoresNonAnthropicRequests guards the provider gate: only
// Anthropic writes a TTL-based prompt cache, so another provider's request
// must not make info() report a warm Anthropic cache after a model switch.
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
		Message:   core.AgentMessage{Message: core.Message{Role: "assistant", Provider: "openai"}},
	})
	sess.runtime.Bus.Drain(time.Second)

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.lastRunAt.IsZero() {
		t.Error("a non-Anthropic request warmed the Anthropic cache clock")
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
