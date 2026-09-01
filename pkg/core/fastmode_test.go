package core

import "testing"

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
