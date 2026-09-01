package core

import "testing"

func TestSameModelIdentityNormalizesAliases(t *testing.T) {
	if !SameModelIdentity("fable", "claude-fable-5-1") {
		t.Fatal("alias and effective ID should match")
	}
	if SameModelIdentity("fable", "claude-opus-5") {
		t.Fatal("different effective model should not match")
	}
}

// TestCanonicalModelIDFoldsBackendSpellings covers xAI naming one model two
// ways: the subscription backend answers "grok-4.6-build", the API backend
// "grok-4.6". Same model and same pricing, so both must land on one entry —
// otherwise cost attribution splits and the response badge reports a safety
// fallback that never happened.
func TestCanonicalModelIDFoldsBackendSpellings(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"grok-4.6-build", "grok-4.6"},
		{"grok-4.5-build", "grok-4.5"},
		{"grok-4.6", "grok-4.6"},
		{"grok", "grok-4.6"},
		{"some-custom-model", "some-custom-model"}, // unknown ids pass through
	} {
		if got := CanonicalModelID(tc.id); got != tc.want {
			t.Errorf("CanonicalModelID(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

// A "-build" response must carry the catalogued model's pricing: billing it as
// an unknown model silently costs zero.
func TestBackendSpellingResolvesToPricing(t *testing.T) {
	m, ok := ResolveModel("grok-4.6-build")
	if !ok {
		t.Fatal("grok-4.6-build must resolve")
	}
	if m.Pricing == nil {
		t.Fatal("grok-4.6-build resolved without pricing")
	}
	canon, _ := ResolveModel("grok-4.6")
	if m.ID != canon.ID || m.Pricing.Input != canon.Pricing.Input {
		t.Errorf("grok-4.6-build = %s (input %.2f), want same as grok-4.6 (input %.2f)",
			m.ID, m.Pricing.Input, canon.Pricing.Input)
	}
}

// The two spellings are the same model; two different models are not.
func TestSameModelIdentityAcrossBackends(t *testing.T) {
	if !SameModelIdentity("grok-4.6", "grok-4.6-build") {
		t.Error("same model spelled per backend should match")
	}
	if SameModelIdentity("grok-4.6", "grok-4.5-build") {
		t.Error("different Grok versions must not match")
	}
}
