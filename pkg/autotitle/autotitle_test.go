package autotitle

import "testing"

func TestCheapModelSpecFor(t *testing.T) {
	// OpenAI sessions must title with an OpenAI model — never ship the
	// transcript to a different vendor (Anthropic) just for a title.
	if got := cheapModelSpecFor("openai"); got != "gpt-5.4-mini" {
		t.Fatalf("openai → %q, want gpt-5.4-mini", got)
	}
	// Anthropic and unknown/empty providers fall back to the cheap Anthropic model.
	for _, p := range []string{"anthropic", ""} {
		if got := cheapModelSpecFor(p); got != DefaultModelSpec {
			t.Fatalf("%q → %q, want %q", p, got, DefaultModelSpec)
		}
	}
}
