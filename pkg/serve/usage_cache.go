package serve

import "github.com/e-aleixandre/moa/pkg/bus"

// subscribeUsageCache keeps header-only provider usage available to every web
// session for this process. The cache belongs to usage, while serve only
// observes its own session buses and exposes the existing HTTP representation.
func (m *Manager) subscribeUsageCache(sess *ManagedSession) {
	if m.usagePoller == nil {
		return
	}
	sess.usageUnsub = sess.runtime.Bus.SubscribeAll(func(event any) {
		switch event := event.(type) {
		case bus.RunStarted:
			sess.mu.Lock()
			sess.runProvider = sess.modelProvider
			sess.mu.Unlock()
		case bus.RateLimitUpdated:
			sess.mu.Lock()
			provider := sess.runProvider
			sess.mu.Unlock()
			if provider == "openai" {
				m.usagePoller.ObserveRateLimit("openai", "oauth", event.RateLimit.FiveHourUtil, event.RateLimit.SevenDayUtil)
			}
		}
	})
}
