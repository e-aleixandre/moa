package core

import (
	"math"
	"strconv"
	"strings"
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
		{"gpt-5.6", true, "alias of gpt-5.6-sol"},
		{"gpt-6", true, "alias of gpt-6-astra"},
		{"openai/gpt-5.4-nano", false, "not in the catalogue, and OpenAI offers no fast tier for it"},
		{"openai/gpt-5.5-pro", false, "not in the catalogue, and OpenAI offers no fast tier for it"},
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
		{"gpt-5.6-terra", 2},
		{"gpt-5.5", 2.5},
		{"gpt-6-astra", 2},
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

// TestOpenAIFastCatalogue pins every OpenAI model in the catalogue against the
// "Fast mode" tab of OpenAI's pricing page: support and multiplier over the
// Standard price. A new OpenAI model fails here until it is classified.
func TestOpenAIFastCatalogue(t *testing.T) {
	cases := []struct {
		model      string
		fast       bool
		multiplier float64
	}{
		{"gpt-6-astra", true, 2},
		{"gpt-6-sol", true, 2},
		{"gpt-6-luna", true, 2},
		{"gpt-5.6-sol", true, 2},
		{"gpt-5.6-terra", true, 2},
		{"gpt-5.6-luna", true, 2},
		{"gpt-5.5", true, 2.5},
		{"gpt-5.4-mini", true, 2},
		// Not verified against the pricing page's multiplier: no fast tier.
		{"gpt-5.3-codex", false, 1},
		{"gpt-5.3-codex-spark", false, 1},
		{"gpt-5.2-codex", false, 1},
		{"gpt-daybreak-blue-latest", false, 1},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.model] = true
		t.Run(c.model, func(t *testing.T) {
			m, ok := ResolveModel(c.model)
			if !ok || m.Provider != "openai" {
				t.Fatalf("ResolveModel(%q) = %+v, %v; want a catalogue OpenAI model", c.model, m, ok)
			}
			if got := SupportsFast(c.model); got != c.fast {
				t.Errorf("SupportsFast = %v, want %v", got, c.fast)
			}
			if got := FastCostMultiplier(m); got != c.multiplier {
				t.Errorf("FastCostMultiplier = %v, want %v", got, c.multiplier)
			}
			wantPriced := 0.0
			if c.fast {
				wantPriced = c.multiplier
			}
			if got := m.Pricing.FastMultiplier; got != wantPriced {
				t.Errorf("Pricing.FastMultiplier = %v, want %v", got, wantPriced)
			}
			note := FastNote(c.model)
			if !c.fast {
				if note != "" {
					t.Errorf("FastNote = %q, want empty for a model without fast mode", note)
				}
				return
			}
			rate := strconv.FormatFloat(c.multiplier, 'f', -1, 64) + "×"
			if !strings.Contains(note, rate) {
				t.Errorf("FastNote = %q, want it to quote %s", note, rate)
			}
		})
	}
	for id, m := range knownModels {
		if m.Provider == "openai" && !covered[id] {
			t.Errorf("OpenAI catalogue model %q is not classified here", id)
		}
	}
}
