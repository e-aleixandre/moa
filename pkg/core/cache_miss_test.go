package core

import (
	"math"
	"testing"
)

func missTurn(provider, model string, ts int64, input, read, written, written1h int, custom map[string]any) AgentMessage {
	return AgentMessage{
		Message: Message{
			Role: "assistant", Provider: provider, Model: model, Timestamp: ts,
			Usage: &Usage{Input: input, CacheRead: read, CacheWrite: written, CacheWrite1h: written1h},
		},
		Custom: custom,
	}
}

func lastMiss(t *testing.T, msgs ...AgentMessage) CacheUsageSummary {
	t.Helper()
	return SummarizeCacheUsage(msgs)
}

func TestCacheMiss_AnthropicExpiredAfterFiveMinuteWindow(t *testing.T) {
	got := lastMiss(t,
		missTurn("anthropic", "claude-opus-5", 1000, 10, 0, 100_000, 0, nil),
		missTurn("anthropic", "claude-opus-5", 1000+301, 10, 0, 100_500, 0, nil),
	)
	if got.Misses != 1 || got.LastMiss == nil {
		t.Fatalf("want one miss, got %+v", got)
	}
	m := got.LastMiss
	if m.Cause != CacheMissExpired || m.GapSeconds != 301 || m.Tokens != 100_010 {
		t.Fatalf("unexpected miss %+v", m)
	}
	// 100,010 tokens written at 6.25 instead of read at 0.5 per million.
	want := 100_010 * (6.25 - 0.5) / 1e6
	if math.Abs(m.CostUSD-want) > 1e-9 || math.Abs(got.MissCostUSD-want) > 1e-9 {
		t.Fatalf("cost %v / %v, want %v", m.CostUSD, got.MissCostUSD, want)
	}
}

func TestCacheMiss_OneHourWindowIsRememberedAcrossPureReads(t *testing.T) {
	msgs := []AgentMessage{
		missTurn("anthropic", "claude-opus-5", 0, 10, 0, 100_000, 100_000, nil),
		missTurn("anthropic", "claude-opus-5", 10, 10, 100_000, 0, 0, nil), // pure read
		missTurn("anthropic", "claude-opus-5", 10+1800, 10, 0, 100_010, 100_010, nil),
	}
	got := SummarizeCacheUsage(msgs)
	if got.LastMiss == nil || got.LastMiss.Cause != CacheMissUnknown {
		t.Fatalf("a 30m gap inside a 1h window must not be blamed on the gap: %+v", got.LastMiss)
	}
	// The 1h write rate (10) is what the miss cost over a read (0.5).
	want := 100_010 * (10 - 0.5) / 1e6
	if math.Abs(got.LastMiss.CostUSD-want) > 1e-9 {
		t.Fatalf("cost %v, want %v", got.LastMiss.CostUSD, want)
	}

	msgs = append(msgs, missTurn("anthropic", "claude-opus-5", 10+1800+3601, 10, 0, 100_020, 100_020, nil))
	if got := SummarizeCacheUsage(msgs); got.Misses != 2 || got.LastMiss.Cause != CacheMissExpired {
		t.Fatalf("a 1h+ gap is expiry: %+v", got)
	}
}

func TestCacheMiss_CausesOtherThanGap(t *testing.T) {
	base := missTurn("anthropic", "claude-opus-5", 0, 10, 0, 100_000, 0, map[string]any{"compaction_epoch": 0})
	cases := []struct {
		name string
		next AgentMessage
		want string
	}{
		{"compaction", missTurn("anthropic", "claude-opus-5", 5, 10, 0, 40_000, 0, map[string]any{"compaction_epoch": float64(1)}), CacheMissCompaction},
		{"model", missTurn("anthropic", "claude-sonnet-5", 5, 10, 0, 100_500, 0, map[string]any{"compaction_epoch": 0}), CacheMissModelChanged},
		{"provider", missTurn("openai", "gpt-6-sol", 5, 100_500, 0, 0, 0, map[string]any{"compaction_epoch": 0}), CacheMissModelChanged},
		{"cut", missTurn("anthropic", "claude-opus-5", 5, 10, 0, 30_000, 0, map[string]any{"compaction_epoch": 0}), CacheMissContextCut},
		{"unknown", missTurn("anthropic", "claude-opus-5", 5, 10, 0, 100_500, 0, map[string]any{"compaction_epoch": 0}), CacheMissUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizeCacheUsage([]AgentMessage{base, tc.next})
			if got.LastMiss == nil || got.LastMiss.Cause != tc.want {
				t.Fatalf("want %s, got %+v", tc.want, got.LastMiss)
			}
		})
	}
}

func TestCacheMiss_OpenAIWindowAndUnreportedWrites(t *testing.T) {
	// OpenAI's ChatGPT backend reports no cache writes: lost tokens come back
	// as plain input, and the miss costs input - read on them.
	a := missTurn("openai", "gpt-6-sol", 1000, 100_000, 0, 0, 0, nil)
	within := missTurn("openai", "gpt-6-sol", 2700, 100_500, 0, 0, 0, nil)
	got := SummarizeCacheUsage([]AgentMessage{a, within})
	if got.LastMiss == nil || got.LastMiss.Cause != CacheMissUnknown {
		t.Fatalf("a miss inside 30m is unknown: %+v", got.LastMiss)
	}
	want := 100_000 * (2.0 - 0.2) / 1e6
	if math.Abs(got.LastMiss.CostUSD-want) > 1e-9 {
		t.Fatalf("cost %v, want %v", got.LastMiss.CostUSD, want)
	}
	late := missTurn("openai", "gpt-6-sol", 2801, 100_500, 0, 0, 0, nil)
	if got := SummarizeCacheUsage([]AgentMessage{a, late}); got.LastMiss == nil || got.LastMiss.Cause != CacheMissExpired {
		t.Fatalf("a miss after 30m is expiry: %+v", got.LastMiss)
	}
	// Before GPT-5.6 the documented window is shorter.
	old := missTurn("openai", "gpt-5.5", 1000, 100_000, 0, 0, 0, nil)
	oldLate := missTurn("openai", "gpt-5.5", 1601, 100_500, 0, 0, 0, nil)
	if got := SummarizeCacheUsage([]AgentMessage{old, oldLate}); got.LastMiss == nil || got.LastMiss.Cause != CacheMissExpired {
		t.Fatalf("legacy window: %+v", got.LastMiss)
	}
}

func TestCacheMiss_NotAMiss(t *testing.T) {
	cases := map[string][]AgentMessage{
		"first turn":      {missTurn("anthropic", "claude-opus-5", 0, 10, 0, 100_000, 0, nil)},
		"hit":             {missTurn("anthropic", "claude-opus-5", 0, 10, 0, 100_000, 0, nil), missTurn("anthropic", "claude-opus-5", 5, 10, 100_000, 400, 0, nil)},
		"tiny prefix":     {missTurn("anthropic", "claude-opus-5", 0, 10, 0, 1_000, 0, nil), missTurn("anthropic", "claude-opus-5", 4000, 10, 0, 1_500, 0, nil)},
		"partial read ok": {missTurn("anthropic", "claude-opus-5", 0, 10, 0, 100_000, 0, nil), missTurn("anthropic", "claude-opus-5", 5, 10, 60_000, 40_000, 0, nil)},
	}
	for name, msgs := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SummarizeCacheUsage(msgs); got.Misses != 0 || got.LastMiss != nil || got.MissCostUSD != 0 {
				t.Fatalf("not a miss: %+v", got)
			}
		})
	}
}

func TestCacheMiss_UnpricedModelHasNoCost(t *testing.T) {
	got := SummarizeCacheUsage([]AgentMessage{
		missTurn("anthropic", "claude-not-in-catalog", 1000, 10, 0, 100_000, 0, nil),
		missTurn("anthropic", "claude-not-in-catalog", 9999, 10, 0, 100_000, 0, nil),
	})
	if got.LastMiss == nil || got.LastMiss.CostUSD != 0 || got.LastMiss.Cause != CacheMissExpired {
		t.Fatalf("unpriced miss keeps its cause and costs nothing: %+v", got.LastMiss)
	}
}

func TestModernOpenAICache(t *testing.T) {
	for model, want := range map[string]bool{
		"gpt-6-sol": true, "gpt-6.1-sol": true, "gpt-5.6-sol": true, "gpt-5.10-x": true, "gpt-daybreak-blue-latest": true,
		"gpt-5.5": false, "gpt-5.4-mini": false, "gpt-5.3-codex": false, "gpt-4.1": false,
	} {
		if got := modernOpenAICache(model); got != want {
			t.Errorf("%s: got %v, want %v", model, got, want)
		}
	}
}
