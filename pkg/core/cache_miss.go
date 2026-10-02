package core

import (
	"strconv"
	"strings"
	"time"
)

// Why a turn read less cache than the previous one had left behind.
const (
	// CacheMissExpired: the gap since the previous request outlasted the
	// provider's cache window.
	CacheMissExpired = "expired"
	// CacheMissModelChanged: another provider or model served the request, and
	// a cache is per model.
	CacheMissModelChanged = "model_changed"
	// CacheMissCompaction: the history was rewritten by a compaction.
	CacheMissCompaction = "compaction"
	// CacheMissContextCut: the context shrank (start fresh or trimmed tool
	// results), so the prefix no longer matches.
	CacheMissContextCut = "context_cut"
	// CacheMissUnknown: nothing the transcript records explains it. A gap
	// inside the window lands here on purpose: a cause is only named when it
	// is demonstrated.
	CacheMissUnknown = "unknown"
)

// CacheMiss is one turn that missed the prompt cache.
type CacheMiss struct {
	Cause      string
	GapSeconds int64   // since the previous request; 0 when a timestamp is missing
	Tokens     int     // prefix tokens that had to be processed again
	CostUSD    float64 // extra cost over reading them from cache; 0 for an unpriced model
	At         int64   // Unix seconds of the missing turn
	Provider   string
	Model      string
}

const (
	// A prefix shorter than this is below every provider's minimum cacheable
	// length, so reading nothing from it is not a miss.
	minCacheablePrefix = 2048
	// A turn is a miss when it read less than this share of the previous
	// turn's context. Partial reads are noise (rounding to the provider's
	// block size), a collapse is the event worth reporting.
	missReadShare = 0.5
	// A context this much smaller than the previous one was cut, not grown.
	cutShrinkShare = 0.9
	// OpenAI documents "at least 30 minutes since the last write or reuse"
	// for GPT-5.6 and later, and 5 to 10 minutes for earlier models.
	openAICacheWindow       = 30 * time.Minute
	openAILegacyCacheWindow = 10 * time.Minute
)

type cacheMissTracker struct {
	seen     bool
	ctx      int
	at       int64
	provider string
	model    string
	epoch    int
	// window1h remembers the TTL of the last request that wrote the Anthropic
	// cache: a pure read does not say which window the entry lives in.
	window1h bool
}

// observe folds one valid assistant turn, in transcript order, and returns the
// miss it represents, if any.
func (t *cacheMissTracker) observe(m *AgentMessage) *CacheMiss {
	u := m.Usage
	ctx := u.Input + u.CacheRead + u.CacheWrite
	epoch := customInt(m.Custom, "compaction_epoch")
	prev := *t
	if u.CacheWrite > 0 {
		t.window1h = u.CacheWrite1h > 0
	}
	t.seen, t.ctx, t.at, t.provider, t.model, t.epoch = true, ctx, m.Timestamp, m.Provider, m.Model, epoch
	if !prev.seen || prev.ctx < minCacheablePrefix {
		return nil
	}
	if float64(u.CacheRead) >= missReadShare*float64(prev.ctx) {
		return nil
	}

	lost := prev.ctx - u.CacheRead
	// A shrunk context cannot have re-cached more than it holds beyond what it
	// read: tokens that were read were hits.
	if fresh := ctx - u.CacheRead; lost > fresh {
		lost = fresh
	}
	miss := &CacheMiss{Tokens: lost, At: m.Timestamp, Provider: m.Provider, Model: m.Model}
	if m.Timestamp > 0 && prev.at > 0 && m.Timestamp >= prev.at {
		miss.GapSeconds = m.Timestamp - prev.at
	}
	window := cacheWindowFor(m.Provider, m.Model, prev.window1h)
	switch {
	case epoch != prev.epoch:
		miss.Cause = CacheMissCompaction
	case m.Provider != prev.provider || !sameServedModel(m.Model, prev.model):
		miss.Cause = CacheMissModelChanged
	case float64(ctx) < cutShrinkShare*float64(prev.ctx):
		miss.Cause = CacheMissContextCut
	case window > 0 && time.Duration(miss.GapSeconds)*time.Second > window:
		miss.Cause = CacheMissExpired
	default:
		miss.Cause = CacheMissUnknown
	}
	miss.CostUSD = recacheCost(m.Provider, m.Model, *u, lost)
	return miss
}

func customInt(c map[string]any, key string) int {
	switch v := c[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// sameServedModel compares what the providers answered. xAI suffixes its
// answers ("grok-4.6-build"); that is the same model.
func sameServedModel(a, b string) bool {
	return SameModelIdentity(strings.TrimSuffix(a, "-build"), strings.TrimSuffix(b, "-build"))
}

// cacheWindowFor is how long after a request the provider keeps its cache.
// Zero means there is no documented window, so a gap is never blamed.
func cacheWindowFor(provider, model string, anthropic1h bool) time.Duration {
	switch provider {
	case "anthropic":
		if anthropic1h {
			return time.Hour
		}
		return 5 * time.Minute
	case "openai":
		if modernOpenAICache(model) {
			return openAICacheWindow
		}
		return openAILegacyCacheWindow
	}
	return 0
}

// modernOpenAICache reports GPT-5.6 and later, whose cache is guaranteed 30
// minutes. The release names carry the generation; anything older is legacy.
func modernOpenAICache(model string) bool {
	if strings.HasPrefix(model, "gpt-6") || strings.HasPrefix(model, "gpt-daybreak") {
		return true
	}
	rest, ok := strings.CutPrefix(model, "gpt-5.")
	if !ok {
		return false
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	minor, err := strconv.Atoi(rest[:end])
	return err == nil && minor >= 6
}

// recacheCost is what the turn paid beyond a hit: the same usage with the lost
// tokens read from cache instead of written (or, where the provider does not
// report writes, billed as plain input).
func recacheCost(provider, model string, u Usage, lost int) float64 {
	pricing := pricingFor(provider, model)
	if pricing == nil || lost <= 0 {
		return 0
	}
	hit := u
	rem := lost
	n := min(rem, hit.CacheWrite1h) // the 1h writes are a subset of CacheWrite
	hit.CacheWrite1h -= n
	hit.CacheWrite -= n
	rem -= n
	n = min(rem, hit.CacheWrite)
	hit.CacheWrite -= n
	rem -= n
	n = min(rem, hit.Input)
	hit.Input -= n
	rem -= n
	hit.CacheRead += lost - rem
	return max(0, pricing.Cost(u)-pricing.Cost(hit))
}

func pricingFor(provider, model string) *Pricing {
	model = strings.TrimSuffix(model, "-build")
	if m, ok := ResolveModel(provider + "/" + model); ok && m.Pricing != nil {
		return m.Pricing
	}
	if m, ok := ResolveModel(model); ok && m.Provider == provider {
		return m.Pricing
	}
	return nil
}
