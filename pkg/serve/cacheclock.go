package serve

import (
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// subscribeCacheClock records when the prompt cache was last written so the UI
// can tell whether it is still warm. Anthropic refreshes the TTL on every
// request, so the cache stays warm until the last request + cacheTTL; once that
// passes, the next message pays a fresh cache-write.
//
// The anchor is MessageStarted — the provider's message_start, i.e. a request
// that actually reached the API — not RunEnded. A long run issues its final
// request well before it finishes, so anchoring on RunEnded pushed the expiry
// into the future and reported a warm cache long after it had gone cold.
// info() turns lastRunAt into the CacheExpiresAt surfaced to clients.
//
// A resumed session starts from its history: the clock only lives in memory,
// so without this a restart hid the expiry of every idle session until its
// next message had already paid the cache write.
func (m *Manager) subscribeCacheClock(sess *ManagedSession) {
	b := sess.runtime.Bus
	if at := lastAnthropicResponseAt(sess.runtime.Context().Agent.Messages()); !at.IsZero() {
		sess.mu.Lock()
		if sess.lastRunAt.IsZero() {
			sess.lastRunAt = at
		}
		sess.mu.Unlock()
	}
	sess.pushUnsubs = append(sess.pushUnsubs,
		b.Subscribe(func(e bus.RunStarted) {
			// Anchor the activity-indicator elapsed counter. Recorded server-side
			// so it survives WebSocket reconnects instead of restarting at zero.
			// Track the generation so a late RunEnded from a prior run can't clear
			// a newer run's anchor (the two events race on separate subscriptions).
			sess.mu.Lock()
			sess.runStartedAt = time.Now()
			sess.runStartedGen = e.RunGen
			sess.mu.Unlock()
		}),
		b.Subscribe(func(e bus.MessageStarted) {
			// Only Anthropic requests warm a TTL-based prompt cache. Gate on the
			// message's own provider rather than the session's current model: a
			// later switch to an Anthropic model must not reinterpret a write
			// that some other provider's request never made.
			if e.Message.Provider != "anthropic" {
				return
			}
			sess.mu.Lock()
			sess.lastRunAt = time.Now()
			sess.mu.Unlock()
		}),
		b.Subscribe(func(e bus.RunEnded) {
			sess.mu.Lock()
			// Only clear if this end belongs to the run we anchored. A stale
			// RunEnded from generation N must not wipe the timer of an already
			// started generation N+1.
			if e.RunGen >= sess.runStartedGen {
				sess.runStartedAt = time.Time{}
				sess.runStartedGen = e.RunGen
			}
			sess.mu.Unlock()
		}),
	)
}

// lastAnthropicResponseAt dates the last request that warmed an Anthropic
// prompt cache, from the persisted history. The response's own timestamp is a
// little later than the request's, so the restored expiry errs late by at most
// one response's duration.
func lastAnthropicResponseAt(msgs []core.AgentMessage) time.Time {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "assistant" && m.Provider == "anthropic" && m.Timestamp > 0 {
			return time.Unix(m.Timestamp, 0)
		}
	}
	return time.Time{}
}
