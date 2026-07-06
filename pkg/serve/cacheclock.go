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
		b.Subscribe(func(e bus.RunEnded) {
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
