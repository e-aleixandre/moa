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
