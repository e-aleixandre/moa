package core

import "testing"

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

func TestKnownModels_HavePricing(t *testing.T) {
	for id, m := range knownModels {
		if m.Pricing == nil {
			t.Errorf("model %s has no pricing", id)
		}
	}
}
