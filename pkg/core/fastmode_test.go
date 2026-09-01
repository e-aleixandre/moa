package core

import (
	"math"
	"testing"
)

func TestSupportsFast(t *testing.T) {
	cases := []struct {
		model string
		want  bool
		why   string
	}{
		{"claude-opus-5", true, "Anthropic serves fast mode on Opus"},
		{"claude-opus-4-8", true, "Anthropic serves fast mode on Opus"},
		{"claude-fable-5-1", false, "Fable answers: does not support the `speed` parameter"},
		{"claude-sonnet-5", false, "Sonnet answers: does not support the `speed` parameter"},
		{"claude-haiku-4-5-20251001", false, "Haiku answers: does not support the `speed` parameter"},
		{"gpt-5.6", true, "OpenAI prices a priority tier on the 5.4 generation onwards"},
		{"grok-4.5", true, "xAI accepts the priority tier across its catalogue"},
		{"no-such-model", false, "an unknown model must not be offered a switch the API would reject"},
	}
	for _, c := range cases {
		if got := SupportsFast(c.model); got != c.want {
			t.Errorf("SupportsFast(%q) = %v, want %v — %s", c.model, got, c.want, c.why)
		}
	}
}

func TestFastNoteDiffersPerProvider(t *testing.T) {
	// The trade-off is genuinely different: OpenAI bills fast mode against the
	// plan's credits, Anthropic against usage credits outside the
	// subscription. One shared sentence would misstate one of them.
	opus := FastNote("claude-opus-5")
	gpt := FastNote("gpt-5.6")
	if opus == "" || gpt == "" {
		t.Fatalf("every supported provider needs a note: opus=%q gpt=%q", opus, gpt)
	}
	if opus == gpt {
		t.Errorf("Anthropic and OpenAI share the note %q, but only Anthropic bills separate usage credits", opus)
	}
}

func TestPricingCostFastMultiplier(t *testing.T) {
	usage := Usage{Input: 1_000, Output: 2_000, CacheRead: 3_000, CacheWrite: 4_000}
	cases := []struct {
		model      string
		multiplier float64
	}{
		{"claude-opus-5", 2},
		{"gpt-5.6-terra", 2.5},
		{"claude-sonnet-5", 1},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			model, ok := ResolveModel(tc.model)
			if !ok || model.Pricing == nil {
				t.Fatalf("model %q has no pricing", tc.model)
			}
			standard := model.Pricing.Cost(usage)
			fast := model.Pricing.Cost(Usage{
				Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite, Fast: true,
			})
			if want := standard * tc.multiplier; math.Abs(fast-want) > 1e-12 {
				t.Errorf("fast cost = %v, want %v (standard %v × %v)", fast, want, standard, tc.multiplier)
			}
		})
	}
}
