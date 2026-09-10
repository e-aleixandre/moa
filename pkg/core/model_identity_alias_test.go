package core

import "testing"

func TestSameResponseOrigin(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		effective string
		want      bool
	}{
		{"identical", "gpt-5.6-sol", "gpt-5.6-sol", true},
		{"alias accepts its target", "gpt-daybreak-blue-latest", "gpt-5.6-sol", true},
		{"alias accepts its own id", "gpt-daybreak-blue-latest", "gpt-daybreak-blue-latest", true},
		{"target rejects the alias", "gpt-5.6-sol", "gpt-daybreak-blue-latest", false},
		{"unrelated models", "gpt-6-astra", "gpt-5.6-sol", false},
		{"alias vs unrelated", "gpt-daybreak-blue-latest", "gpt-6-astra", false},
		{"legacy empty effective", "gpt-daybreak-blue-latest", "", false},
		{"unknown ids compare literally", "custom-x", "custom-x", true},
		{"unknown ids differ", "custom-x", "custom-y", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameResponseOrigin(tt.requested, tt.effective); got != tt.want {
				t.Errorf("SameResponseOrigin(%q, %q) = %v, want %v",
					tt.requested, tt.effective, got, tt.want)
			}
		})
	}
}

// The registry's alias target must name a real model: a typo would silently
// restore the bug this relation exists to fix.
func TestAliasTargetsResolve(t *testing.T) {
	for id, model := range knownModels {
		if model.AliasOf == "" {
			continue
		}
		target, ok := ResolveModel(model.AliasOf)
		if !ok {
			t.Errorf("model %q aliases unknown model %q", id, model.AliasOf)
			continue
		}
		if target.Provider != model.Provider {
			t.Errorf("model %q aliases %q from another provider (%s vs %s)",
				id, model.AliasOf, target.Provider, model.Provider)
		}
		if target.AliasOf != "" {
			t.Errorf("model %q aliases %q, which is itself an alias", id, model.AliasOf)
		}
	}
}
