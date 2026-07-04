package autotitle

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestBuildPrompt_RuneBoundary(t *testing.T) {
	// A single user message longer than the 4000-char budget, built from 2-byte
	// runes so the byte-budget cut lands mid-rune.
	long := strings.Repeat("é", 3000) // 6000 bytes
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent(long)}}},
	}
	if got := buildPrompt(msgs); !utf8.ValidString(got) {
		t.Errorf("buildPrompt must not split a rune at the budget boundary, got invalid UTF-8")
	}
}

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
