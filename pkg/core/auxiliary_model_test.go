package core

import "testing"

func TestResolveAuxiliaryModel_AutoPriorityAndAvailability(t *testing.T) {
	cases := []struct {
		name      string
		available map[string]bool
		want      string
		enabled   bool
	}{
		{"OpenAI wins", map[string]bool{"openai": true, "anthropic": true}, "gpt-5.6-luna", true},
		{"Anthropic fallback", map[string]bool{"anthropic": true}, "claude-haiku-4-5-20251001", true},
		{"xAI is never automatic", map[string]bool{"xai": true}, "", false},
		{"none", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, enabled, err := ResolveAuxiliaryModel("auto", func(provider string) bool { return tc.available[provider] })
			if err != nil || enabled != tc.enabled || model.ID != tc.want {
				t.Fatalf("ResolveAuxiliaryModel(auto) = %+v, %v, %v", model, enabled, err)
			}
		})
	}
}

func TestResolveAuxiliaryModel_ExplicitAndOff(t *testing.T) {
	model, enabled, err := ResolveAuxiliaryModel("grok", func(provider string) bool { return provider == "xai" })
	if err != nil || !enabled || model.ID != "grok-4.6" {
		t.Fatalf("explicit Grok = %+v, %v, %v", model, enabled, err)
	}
	if model, enabled, err := ResolveAuxiliaryModel("grok", func(string) bool { return false }); err != nil || enabled || model.ID != "" {
		t.Fatalf("explicit unavailable Grok = %+v, %v, %v", model, enabled, err)
	}
	if model, enabled, err := ResolveAuxiliaryModel("openai/a-custom-model", func(string) bool { return false }); err != nil || enabled || model.ID != "" {
		t.Fatalf("explicit unavailable custom model = %+v, %v, %v", model, enabled, err)
	}
	if _, enabled, err := ResolveAuxiliaryModel("off", nil); err != nil || enabled {
		t.Fatalf("off enabled=%v err=%v", enabled, err)
	}
	if _, _, err := ResolveAuxiliaryModel("not-a-model", nil); err == nil {
		t.Fatal("invalid explicit model must fail validation")
	}
}
