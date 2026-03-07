package core

import "testing"

func TestResolveModel_Alias(t *testing.T) {
	m, ok := ResolveModel("sonnet")
	if !ok {
		t.Fatal("expected ok")
	}
	if m.ID != "claude-sonnet-4-6" {
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
	if m.ID != "claude-sonnet-4-6" {
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
