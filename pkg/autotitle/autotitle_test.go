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

func TestWrapPrompt_FramesAsData(t *testing.T) {
	got := wrapPrompt("User: hola\nAssistant: hey")
	if !strings.Contains(got, "<conversation>") || !strings.Contains(got, "</conversation>") {
		t.Errorf("wrapPrompt must delimit the transcript, got:\n%s", got)
	}
	if !strings.Contains(got, "User: hola") {
		t.Errorf("wrapPrompt must include the transcript, got:\n%s", got)
	}
}

func TestIsNoConcreteTask(t *testing.T) {
	// The NONE sentinel (any case, after clean strips punctuation) means the
	// greeting-only session keeps its first-message title instead of a bad one.
	for _, s := range []string{"NONE", "none", "None."} {
		if !isNoConcreteTask(clean(s)) {
			t.Errorf("isNoConcreteTask(clean(%q)) = false, want true", s)
		}
	}
	for _, s := range []string{"Fix login bug", "None of your business"} {
		if isNoConcreteTask(clean(s)) {
			t.Errorf("isNoConcreteTask(clean(%q)) = true, want false", s)
		}
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
