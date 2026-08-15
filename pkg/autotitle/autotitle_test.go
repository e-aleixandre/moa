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

func TestGenerate_UsesConfiguredModel(t *testing.T) {
	p := &capturedProvider{}
	var factoryModel core.Model
	model, _ := core.ResolveModel("luna")
	title, err := Generate(context.Background(), func(m core.Model) (core.Provider, error) { factoryModel = m; return p, nil }, model, []core.AgentMessage{{Message: core.NewUserMessage("Fix Luna title")}})
	if err != nil || title != "Fix Grok title" {
		t.Fatalf("title=%q err=%v", title, err)
	}
	if factoryModel.ID != "gpt-5.6-luna" || p.request.Model.ID != "gpt-5.6-luna" {
		t.Fatalf("model=%+v thinking=%q", factoryModel, p.request.Options.ThinkingLevel)
	}
}
