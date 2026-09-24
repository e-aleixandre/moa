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
	// The full transcript, not the model's context: a start-fresh cut may have
	// dropped the response that last warmed the cache.
	if transcript, err := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](b, bus.GetDisplayMessages{}); err == nil {
		at, provider := lastCachedResponse(transcript, sess.cacheTTL)
		freshAt := lastFreshAt(transcript)
		sess.mu.Lock()
		if sess.lastRunAt.IsZero() && !at.IsZero() {
			sess.lastRunAt, sess.lastRunProvider = at, provider
		}
		if sess.startedFreshAt.IsZero() {
			sess.startedFreshAt = freshAt
		}
		sess.mu.Unlock()
	}
	sess.pushUnsubs = append(sess.pushUnsubs,
		b.Subscribe(func(e bus.ContextFreshStarted) {
			sess.mu.Lock()
			sess.startedFreshAt = time.Now()
			sess.mu.Unlock()
		}),
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
			// Only providers with a known window are tracked. The provider is
			// the message's own, not the session's current model: a later
			// switch must not reinterpret a write that another provider's
			// request never made (info() also requires them to match).
			if cacheWindow(e.Message.Provider, sess.cacheTTL) == 0 {
				return
			}
			sess.mu.Lock()
			sess.lastRunAt = time.Now()
			sess.lastRunProvider = e.Message.Provider
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

// cacheWindow is how long after the last request a provider's prompt cache is
// assumed warm. 0 means moa does not warn for that provider.
//
//   - anthropic: the configured TTL (5m or 1h), refreshed by every request.
//   - openai: 30m, what OpenAI guarantees ("at least 30 minutes since last
//     write or reuse" for GPT-5.6 and later; older models keep it "5 to 10
//     minutes of inactivity, up to one hour"). moa never sends
//     prompt_cache_retention, so the 24h extended retention does not apply.
//   - xai, meta and the rest: no documented TTL, no warning.
func cacheWindow(provider string, anthropicTTL time.Duration) time.Duration {
	switch provider {
	case "anthropic":
		return anthropicTTL
	case "openai":
		return 30 * time.Minute
	default:
		return 0
	}
}

// lastCachedResponse dates the last request that warmed a prompt cache, from
// the persisted history. The response's own timestamp is a little later than
// the request's, so the restored expiry errs late by at most one response's
// duration.
func lastCachedResponse(msgs []core.AgentMessage, anthropicTTL time.Duration) (time.Time, string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "assistant" && m.Timestamp > 0 && cacheWindow(m.Provider, anthropicTTL) > 0 {
			return time.Unix(m.Timestamp, 0), m.Provider
		}
	}
	return time.Time{}, ""
}

// lastFreshAt is when the conversation was last cut with start fresh, from the
// transcript's fresh markers. Zero when it never was.
func lastFreshAt(msgs []core.AgentMessage) time.Time {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "session_event" && m.Custom["type"] == "fresh_marker" && m.Timestamp > 0 {
			return time.Unix(m.Timestamp, 0)
		}
	}
	return time.Time{}
}
