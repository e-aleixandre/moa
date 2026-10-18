package serve

import (
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestRecheckREDSavedCannotExceedKeptPrefix(t *testing.T) {
	h := newKAServe(t, allKA())
	model, _ := core.ResolveModel("claude-opus-5-5")
	const kept = 250000
	e := agent.KeepAliveEvent{Kind: agent.KeepAliveWake, At: time.Now(), Role: core.KeepAliveRoleSession, Model: model, ContextTokens: kept, Pings: 2, PingCostUSD: 0.1, Slept: 2 * time.Hour, Usage: core.Usage{Input: 10, CacheRead: 499990}}
	h.mgr.onKeepAliveEvent(h.sess, "", e)
	_, saved := h.mgr.keepAlive.today()
	max := model.Pricing.Cost(core.Usage{CacheWrite: kept, CacheWrite1h: kept}) - model.Pricing.Cost(core.Usage{CacheRead: kept})
	if saved > max+1e-9 {
		t.Fatalf("Saved today=$%.6f exceeds maximum $%.6f attributable to the %d-token kept prefix (wake cache_read=%d)", saved, max, kept, e.Usage.CacheRead)
	}
}

func TestRecheckSavingSleepBoundary(t *testing.T) {
	model, _ := core.ResolveModel("claude-opus-5-5")
	for _, d := range []time.Duration{time.Hour - time.Nanosecond, time.Hour, time.Hour + time.Nanosecond} {
		got := keepAliveSaving(agent.KeepAliveEvent{Model: model, ContextTokens: 250000, Slept: d, Usage: core.Usage{CacheRead: 250000}}, time.Hour)
		t.Logf("slept=%s saved=%0.8f", d, got)
		if d < time.Hour && got != 0 {
			t.Fatalf("savings within TTL=%v", got)
		}
	}
}
