package core

import (
	"math"
	"reflect"
	"testing"
)

func TestResolveModel_Alias(t *testing.T) {
	m, ok := ResolveModel("sonnet")
	if !ok {
		t.Fatal("expected ok")
	}
	if m.ID != "claude-sonnet-5" {
		t.Fatalf("got %s", m.ID)
	}
	if m.Provider != "anthropic" {
		t.Fatalf("provider: %s", m.Provider)
	}
}

func TestResolveModel_Grok(t *testing.T) {
	for _, spec := range []string{"grok", "xai/grok-4.5"} {
		m, ok := ResolveModel(spec)
		if !ok || m.ID != "grok-4.5" || m.Provider != "xai" {
			t.Errorf("ResolveModel(%q) = %+v, %v", spec, m, ok)
		}
	}
}

func TestGrokPricing(t *testing.T) {
	model, ok := ResolveModel("grok")
	if !ok || model.Pricing == nil {
		t.Fatal("Grok pricing missing")
	}
	p := model.Pricing
	if model.MaxInput != 500_000 || p.Input != 2 || p.Output != 6 || p.CacheRead != 0.3 {
		t.Fatalf("base Grok definition = %+v, pricing = %+v", model, p)
	}
	if len(p.Tiers) != 1 || p.Tiers[0] != (PricingTier{Threshold: 200_000, Input: 4, Output: 12, CacheRead: 0.6}) {
		t.Fatalf("Grok tiers = %+v", p.Tiers)
	}
	short := p.Cost(Usage{Input: 199_999, Output: 1_000})
	long := p.Cost(Usage{Input: 200_000, Output: 1_000})
	if math.Abs(short-0.405998) > 1e-12 || math.Abs(long-0.812) > 1e-12 {
		t.Fatalf("Grok costs = short %v, long %v", short, long)
	}
}

func TestGPT56TerraPricing(t *testing.T) {
	model, ok := ResolveModel("terra")
	if !ok || model.Pricing == nil {
		t.Fatal("Terra pricing missing")
	}
	p := model.Pricing
	if got, want := *p, (Pricing{Input: 2, Output: 12, CacheRead: 0.2, CacheWrite: 2.5, Tiers: []PricingTier{{Threshold: 272_000, Input: 4, Output: 18, CacheRead: 0.4, CacheWrite: 5}}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Terra pricing = %+v, want %+v", got, want)
	}
	if got, want := p.Cost(Usage{Input: 261_999, CacheRead: 10_000, CacheWrite: 2_000, Output: 1_000}), 0.542998; math.Abs(got-want) > 1e-12 {
		t.Fatalf("short Terra cost = %v, want %v", got, want)
	}
	if got, want := p.Cost(Usage{Input: 262_000, CacheRead: 10_000, CacheWrite: 2_000, Output: 1_000}), 1.08; math.Abs(got-want) > 1e-12 {
		t.Fatalf("long Terra cost = %v, want %v", got, want)
	}
}

func TestGPT56LunaPricing(t *testing.T) {
	model, ok := ResolveModel("luna")
	if !ok || model.Pricing == nil {
		t.Fatal("Luna pricing missing")
	}
	p := model.Pricing
	if got, want := *p, (Pricing{Input: .2, Output: 1.2, CacheRead: .02, CacheWrite: .25, Tiers: []PricingTier{{Threshold: 272_000, Input: .4, Output: 1.8, CacheRead: .04, CacheWrite: .5}}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Luna pricing = %+v, want %+v", got, want)
	}
	if got, want := p.Cost(Usage{Input: 261_999, CacheRead: 10_000, CacheWrite: 2_000, Output: 1_000}), .0542998; math.Abs(got-want) > 1e-12 {
		t.Fatalf("short Luna cost = %v, want %v", got, want)
	}
	if got, want := p.Cost(Usage{Input: 262_000, CacheRead: 10_000, CacheWrite: 2_000, Output: 1_000}), .108; math.Abs(got-want) > 1e-12 {
		t.Fatalf("long Luna cost = %v, want %v", got, want)
	}
}

func TestEffectiveThinkingLevel_Grok(t *testing.T) {
	model := Model{Provider: "xai", ID: "grok-4.5"}
	for input, want := range map[string]string{"off": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "high"} {
		got, err := EffectiveThinkingLevel(model, input)
		if err != nil || got != want {
			t.Errorf("EffectiveThinkingLevel(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := EffectiveThinkingLevel(model, "unknown"); err == nil {
		t.Fatal("unknown Grok level must fail")
	}
}

func TestResolveModel_DirectID(t *testing.T) {
	m, ok := ResolveModel("gpt-5.3-codex")
	if !ok {
		t.Fatal("expected ok")
	}
	if m.Provider != "openai" {
		t.Fatalf("provider: %s", m.Provider)
	}
}

func TestResolveModel_ProviderPrefix(t *testing.T) {
	m, ok := ResolveModel("openai/gpt-5.3-codex")
	if !ok {
		t.Fatal("expected ok")
	}
	if m.ID != "gpt-5.3-codex" {
		t.Fatalf("id: %s", m.ID)
	}
	if m.Provider != "openai" {
		t.Fatalf("provider: %s", m.Provider)
	}
}

func TestResolveModel_ProviderPrefixAlias(t *testing.T) {
	m, ok := ResolveModel("anthropic/sonnet")
	if !ok {
		t.Fatal("expected ok for provider/alias")
	}
	if m.ID != "claude-sonnet-5" {
		t.Fatalf("id: %s", m.ID)
	}
}

func TestResolveModel_Unknown(t *testing.T) {
	m, ok := ResolveModel("some-future-model")
	if ok {
		t.Fatal("expected not ok")
	}
	if m.ID != "some-future-model" {
		t.Fatalf("id should be passthrough: %s", m.ID)
	}
}

func TestResolveModel_UnknownWithProvider(t *testing.T) {
	m, ok := ResolveModel("google/gemini-2")
	if ok {
		t.Fatal("expected not ok")
	}
	if m.ID != "gemini-2" {
		t.Fatalf("id: %s", m.ID)
	}
	if m.Provider != "google" {
		t.Fatalf("provider: %s", m.Provider)
	}
}

// F16/A6: an explicit provider prefix that mismatches a *known* model's
// registered provider is rejected (ok=false), not silently resolved to the
// wrong provider's model.
func TestResolveModel_ProviderMismatchOnKnownAlias(t *testing.T) {
	m, ok := ResolveModel("openai/sonnet")
	if ok {
		t.Fatal("expected not ok for provider/model mismatch on known alias")
	}
	if m.Provider != "openai" {
		t.Fatalf("provider should be the requested one, got %s", m.Provider)
	}
	if m.ID != "sonnet" {
		t.Fatalf("id should be passthrough, got %s", m.ID)
	}
}

func TestResolveModel_ProviderMismatchOnKnownDirectID(t *testing.T) {
	m, ok := ResolveModel("openai/claude-sonnet-5")
	if ok {
		t.Fatal("expected not ok for provider/model mismatch on known direct id")
	}
	if m.Provider != "openai" {
		t.Fatalf("provider should be the requested one, got %s", m.Provider)
	}
}

// A provider/model spec that resolves to no known model at all remains a
// valid custom-model spec (still ok=false since metadata is absent, but the
// provider/id are preserved verbatim so callers can still use it).
func TestResolveModel_CustomProviderModelStillPreserved(t *testing.T) {
	m, ok := ResolveModel("openai/my-fine-tuned-model")
	if ok {
		t.Fatal("expected not ok (no pricing/context known)")
	}
	if m.Provider != "openai" || m.ID != "my-fine-tuned-model" {
		t.Fatalf("custom provider/model should be preserved verbatim, got %+v", m)
	}
}

// F16/A6: ValidateModelSpec is the entry point CLI/API use to fail fast.
func TestValidateModelSpec(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		wantErr bool
	}{
		{"known alias", "sonnet", false},
		{"known direct id", "gpt-5.3-codex", false},
		{"known provider/alias", "anthropic/sonnet", false},
		{"known provider/id", "openai/gpt-5.3-codex", false},
		{"provider mismatch on alias", "openai/sonnet", true},
		{"provider mismatch on direct id", "openai/claude-sonnet-5", true},
		{"bare unknown", "some-future-model", true},
		{"custom provider/model", "openai/my-fine-tuned-model", false},
		{"custom unknown provider", "google/gemini-2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModelSpec(tc.spec)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateModelSpec(%q): expected error, got nil", tc.spec)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateModelSpec(%q): unexpected error: %v", tc.spec, err)
			}
		})
	}
}

func TestListModels_Deduplicated(t *testing.T) {
	models := ListModels()
	if len(models) == 0 {
		t.Fatal("expected models")
	}

	// Check no duplicate IDs.
	seen := make(map[string]bool)
	for _, e := range models {
		if seen[e.Model.ID] {
			t.Fatalf("duplicate: %s", e.Model.ID)
		}
		seen[e.Model.ID] = true
	}
}

func TestListModels_HasAliases(t *testing.T) {
	models := ListModels()
	foundAlias := false
	for _, e := range models {
		if e.Alias != "" {
			foundAlias = true
			break
		}
	}
	if !foundAlias {
		t.Fatal("expected at least one alias")
	}
}

func TestListModels_SortedByProvider(t *testing.T) {
	models := ListModels()
	for i := 1; i < len(models); i++ {
		if models[i].Model.Provider < models[i-1].Model.Provider {
			t.Fatalf("not sorted by provider: %s < %s",
				models[i].Model.Provider, models[i-1].Model.Provider)
		}
	}
}

func TestPricing_Cost(t *testing.T) {
	p := &Pricing{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	u := Usage{Input: 1_000_000, Output: 500_000, CacheRead: 2_000_000, CacheWrite: 100_000}
	cost := p.Cost(u)
	// 1M * 3/1M + 500K * 15/1M + 2M * 0.3/1M + 100K * 3.75/1M
	// = 3.0 + 7.5 + 0.6 + 0.375 = 11.475
	want := 11.475
	if cost < want-0.001 || cost > want+0.001 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

func TestPricing_Cost_NilPricing(t *testing.T) {
	var p *Pricing
	cost := p.Cost(Usage{Input: 1000, Output: 500})
	if cost != 0 {
		t.Errorf("nil pricing should return 0, got %f", cost)
	}
}

func TestPricing_Cost_NoCacheFields(t *testing.T) {
	p := &Pricing{Input: 1, Output: 4}
	u := Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 500_000}
	cost := p.Cost(u)
	// CacheRead price is 0, so only Input + Output
	// 1M * 1/1M + 1M * 4/1M = 5.0
	want := 5.0
	if cost < want-0.001 || cost > want+0.001 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

func TestPricing_Cost_Tiers(t *testing.T) {
	p := &Pricing{
		Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25,
		Tiers: []PricingTier{
			{Threshold: 272_000, Input: 10, Output: 45, CacheRead: 1, CacheWrite: 12.5},
		},
	}

	t.Run("below threshold uses base tier", func(t *testing.T) {
		u := Usage{Input: 271_999, Output: 1000}
		got := p.Cost(u)
		want := float64(271_999)*5/1e6 + float64(1000)*30/1e6
		if got < want-1e-9 || got > want+1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})

	t.Run("at threshold uses upper tier", func(t *testing.T) {
		u := Usage{Input: 272_000, Output: 1000}
		got := p.Cost(u)
		want := float64(272_000)*10/1e6 + float64(1000)*45/1e6
		if got < want-1e-9 || got > want+1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})

	t.Run("above threshold uses upper tier, applies to whole request", func(t *testing.T) {
		u := Usage{Input: 300_000, Output: 5000, CacheWrite: 1000}
		got := p.Cost(u)
		want := float64(300_000)*10/1e6 + float64(5000)*45/1e6 + float64(1000)*12.5/1e6
		if got < want-1e-9 || got > want+1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})

	t.Run("cache read counts toward threshold", func(t *testing.T) {
		// Input alone is below threshold, but Input+CacheRead crosses it.
		u := Usage{Input: 100_000, CacheRead: 200_000, Output: 1000}
		got := p.Cost(u)
		want := float64(100_000)*10/1e6 + float64(1000)*45/1e6 + float64(200_000)*1/1e6
		if got < want-1e-9 || got > want+1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})

	t.Run("no tiers behaves like base pricing", func(t *testing.T) {
		base := &Pricing{Input: 1, Output: 4}
		u := Usage{Input: 1_000_000, Output: 1_000_000}
		got := base.Cost(u)
		want := 5.0
		if got < want-1e-9 || got > want+1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})
}

func TestKnownModels_PricingIsExplicit(t *testing.T) {
	for id, m := range knownModels {
		if m.Pricing == nil {
			t.Errorf("model %s has no pricing", id)
		}
	}
}

// Models name these aliases themselves when delegating ("ask Sol to review
// this"), so specs arrive capitalized or padded. Before this was folded, only
// the exact lowercase form resolved and everything else failed.
func TestResolveModel_CaseAndSpaceInsensitive(t *testing.T) {
	cases := map[string]string{
		"Sol":             "gpt-5.6-sol",
		"SOL":             "gpt-5.6-sol",
		"Terra":           "gpt-5.6-terra",
		"Luna":            "gpt-5.6-luna",
		"Opus":            "claude-opus-5",
		"Fable":           "claude-fable-5",
		" sol ":           "gpt-5.6-sol",
		"openai/Sol":      "gpt-5.6-sol",
		"ANTHROPIC/Opus":  "claude-opus-5",
		"Claude-Sonnet-5": "claude-sonnet-5",
		"Claude Sonnet 5": "claude-sonnet-5",
	}
	for spec, want := range cases {
		m, ok := ResolveModel(spec)
		if !ok {
			t.Errorf("ResolveModel(%q): expected it to resolve", spec)
			continue
		}
		if m.ID != want {
			t.Errorf("ResolveModel(%q) = %q, want %q", spec, m.ID, want)
		}
	}
}

// A custom model ID reaches a provider API verbatim, where case can be
// significant, so folding must stop at the registry lookup.
func TestResolveModel_CustomIDKeepsItsCase(t *testing.T) {
	m, ok := ResolveModel("openai/My-Custom-Model")
	if ok {
		t.Fatal("a custom model is not a known one")
	}
	if m.ID != "My-Custom-Model" {
		t.Fatalf("custom ID was rewritten: got %q", m.ID)
	}
	if m.Provider != "openai" {
		t.Fatalf("provider: %q", m.Provider)
	}
}

// The alias list an agent is shown must come from the registry, so it cannot
// drift as models are added or retired.
func TestModelAliases_ComeFromTheRegistry(t *testing.T) {
	aliases := ModelAliases()
	if len(aliases) == 0 {
		t.Fatal("no aliases returned")
	}
	seen := map[string]bool{}
	for _, alias := range aliases {
		if seen[alias] {
			t.Errorf("duplicate alias %q", alias)
		}
		seen[alias] = true
		if _, ok := ResolveModel(alias); !ok {
			t.Errorf("advertised alias %q does not resolve", alias)
		}
	}
	// Every known model reachable by alias must be advertised under one of
	// them. Synonyms ("gpt5.5" alongside "gpt5") are deliberately not listed:
	// the agent needs one working name per model, not every spelling.
	for alias, id := range modelAliases {
		if _, known := knownModels[id]; !known {
			continue
		}
		advertised := false
		for _, shown := range aliases {
			if modelAliases[shown] == id {
				advertised = true
				break
			}
		}
		if !advertised {
			t.Errorf("model %q is reachable via alias %q but never advertised", id, alias)
		}
	}
}

// A suggestion that is wrong is worse than none: it invites a second failed
// attempt. Near misses must be corrected, unrelated names left alone.
func TestSuggestModelAlias(t *testing.T) {
	for spec, want := range map[string]string{
		"sonet":       "sonnet",
		"Sonet":       "sonnet",
		"tera":        "terra",
		"lunna":       "luna",
		"grock":       "grok",
		"opeanai/sol": "sol",
		"banana":      "",
		"":            "",
		"claude":      "",
	} {
		if got := SuggestModelAlias(spec); got != want {
			t.Errorf("SuggestModelAlias(%q) = %q, want %q", spec, got, want)
		}
	}
}
