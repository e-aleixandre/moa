package serve

import (
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

// subscribeCacheClock records when each run finishes so the UI can tell whether
// the prompt cache is still warm. Anthropic refreshes the cache TTL on every
// request, so it stays warm until lastRunAt + cacheTTL; once that passes, the
// next message pays a fresh cache-write. info() turns lastRunAt into the
// CacheExpiresAt surfaced to clients (Anthropic models only).
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

			// Only successful runs are guaranteed to have reached the API and
			// (re)written the cache. Refreshing on a failed run could falsely
			// report the cache as warm; skipping errors errs toward an early
			// "expired" warning, which is the safe direction.
			if e.Err != nil {
				return
			}
			// Only Anthropic requests warm a TTL-based prompt cache. Recording
			// the timestamp for other providers would make info() report a warm
			// cache after a later switch to an Anthropic model, even though no
			// Anthropic request ever ran. Gate on the model that actually ran.
			model, _ := bus.QueryTyped[bus.GetModel, core.Model](b, bus.GetModel{})
			if model.Provider != "anthropic" {
				return
			}
			sess.mu.Lock()
			sess.lastRunAt = time.Now()
			sess.mu.Unlock()
		}),
	)
}
