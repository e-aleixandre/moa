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
		update, ok := event.(bus.RateLimitUpdated)
		if !ok || sess.providerName() != "openai" {
			return
		}
		m.usagePoller.ObserveRateLimit("openai", "oauth", update.RateLimit.FiveHourUtil, update.RateLimit.SevenDayUtil)
	})
}
