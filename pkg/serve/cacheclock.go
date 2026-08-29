package serve

import (
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
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
func (m *Manager) subscribeCacheClock(sess *ManagedSession) {
	b := sess.runtime.Bus
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
