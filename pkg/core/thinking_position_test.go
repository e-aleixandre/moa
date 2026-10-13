package core

import "testing"

func TestThinkingPositionInvertsEffectiveLevel(t *testing.T) {
	for _, id := range []string{"gpt-6.1-sol", "gpt-6-astra", "claude-opus-5", "claude-opus-5-5"} {
		model, ok := ResolveModel(id)
		if !ok {
			t.Fatalf("%s must resolve", id)
		}
		for _, position := range ThinkingLevels {
			effort, err := EffectiveThinkingLevel(model, position)
			if err != nil {
				continue
			}
			again, err := EffectiveThinkingLevel(model, ThinkingPosition(model, effort))
			if err != nil || again != effort {
				t.Errorf("%s: %q -> %q -> %q (%v), want a stable %q", id, position, effort, again, err, effort)
			}
		}
	}
}
