package serve

import "github.com/e-aleixandre/moa/pkg/bus"

// subscribeUsageCache keeps header-only provider usage available to every web
// session for this process. The cache belongs to usage, while serve only
// observes its own session buses and exposes the existing HTTP representation.
//
// Each RateLimitUpdated event carries the provider that actually served that
// request (bus.RateLimitUpdated.Provider, stamped by the provider itself at
// the point of emission). This must be used instead of the session's current
// model provider: the model can change mid-run (see
// fix/config-while-running), so a response that started under one provider
// can arrive after the session has already switched to another, and
// attributing it to the session's now-current provider would misfile the
// reading.
//
// Provider is empty only for a caller that predates this field (in-tree, that
// is a test double emitting a bare core.AssistantEvent); real providers
// (anthropic, openai) always stamp it. For that legacy shape, fall back to
// the session's provider at RunStarted, matching the old behavior exactly.
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
			provider := event.Provider
			if provider == "" {
				sess.mu.Lock()
				provider = sess.runProvider
				sess.mu.Unlock()
			}
			if provider == "openai" {
				m.usagePoller.ObserveRateLimit("openai", "oauth", event.RateLimit.FiveHourUtil, event.RateLimit.SevenDayUtil)
			}
		}
	})
}
