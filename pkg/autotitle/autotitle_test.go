package autotitle

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/e-aleixandre/moa/pkg/core"
)

type capturedProvider struct{ request core.Request }

func (p *capturedProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.request = req
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "Fix Grok title"}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone}
	close(ch)
	return ch, nil
}

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
	if got := cheapModelSpecFor("xai"); got != "grok" {
		t.Fatalf("xai → %q, want grok", got)
	}
	if got := cheapModelSpecFor("anthropic"); got != DefaultModelSpec {
		t.Fatalf("anthropic → %q, want %q", got, DefaultModelSpec)
	}
	if got := cheapModelSpecFor(""); got != "" {
		t.Fatalf("unknown provider → %q, want no fallback", got)
	}
}

func TestGenerate_XAIUsesGrokAndLowThinking(t *testing.T) {
	p := &capturedProvider{}
	var factoryModel core.Model
	title, err := Generate(context.Background(), func(m core.Model) (core.Provider, error) { factoryModel = m; return p, nil }, core.Model{Provider: "xai"}, []core.AgentMessage{{Message: core.NewUserMessage("Fix Grok title")}})
	if err != nil || title != "Fix Grok title" {
		t.Fatalf("title=%q err=%v", title, err)
	}
	if factoryModel.Provider != "xai" || factoryModel.ID != "grok-4.5" || p.request.Options.ThinkingLevel != "low" {
		t.Fatalf("model=%+v thinking=%q", factoryModel, p.request.Options.ThinkingLevel)
	}
}

func TestGenerate_UnknownProviderDoesNotFallbackToAnthropic(t *testing.T) {
	called := false
	_, err := Generate(context.Background(), func(core.Model) (core.Provider, error) { called = true; return nil, nil }, core.Model{Provider: "unknown"}, []core.AgentMessage{{Message: core.NewUserMessage("task")}})
	if err == nil || called {
		t.Fatalf("err=%v factory called=%v", err, called)
	}
}
