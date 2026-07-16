package pulsebrief

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
)

// fakeProvider returns a scripted response as a single text delta, mirroring
// the compaction/autotitle test provider style. No real LLM is called.
type fakeProvider struct {
	response string
	err      error
}

func (p *fakeProvider) Stream(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan core.AssistantEvent, 3)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: p.response}
	ch <- core.AssistantEvent{
		Type:    core.ProviderEventDone,
		Message: &core.Message{Role: "assistant", Content: []core.Content{core.TextContent(p.response)}},
	}
	close(ch)
	return ch, nil
}

func userMsg(text string) core.AgentMessage {
	return core.AgentMessage{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent(text)}}}
}

func TestParseBrief_TwoFields(t *testing.T) {
	b := parseBrief("ATTEMPTING: Fix the auth token refresh\nPROGRESS: tests passing, waiting for review")
	if b.Attempting != "Fix the auth token refresh" {
		t.Errorf("Attempting = %q", b.Attempting)
	}
	if b.Progress != "tests passing, waiting for review" {
		t.Errorf("Progress = %q", b.Progress)
	}
	if b.IsEmpty() {
		t.Error("brief should not be empty")
	}
}

func TestParseBrief_CaseAndBulletTolerant(t *testing.T) {
	// Lower-case labels, markdown bullets and extra noise lines must still parse.
	b := parseBrief("Here you go:\n- attempting: Arreglar el refresco del token\n* Progress: bloqueada en un fallo de compilación\ndone")
	if b.Attempting != "Arreglar el refresco del token" {
		t.Errorf("Attempting = %q", b.Attempting)
	}
	if b.Progress != "bloqueada en un fallo de compilación" {
		t.Errorf("Progress = %q", b.Progress)
	}
}

func TestParseBrief_NoneSentinel(t *testing.T) {
	for _, s := range []string{"NONE", "none", "  None  "} {
		if b := parseBrief(s); !b.IsEmpty() {
			t.Errorf("parseBrief(%q) = %+v, want empty", s, b)
		}
	}
}

func TestParseBrief_MissingFieldsStayEmpty(t *testing.T) {
	// Only one field present: the other stays empty, no garbage.
	b := parseBrief("ATTEMPTING: Wire up the endpoint")
	if b.Attempting != "Wire up the endpoint" {
		t.Errorf("Attempting = %q", b.Attempting)
	}
	if b.Progress != "" {
		t.Errorf("Progress = %q, want empty", b.Progress)
	}
}

func TestCleanField_TruncatesUTF8Safe(t *testing.T) {
	// A field longer than MaxFieldLen built from 2-byte runes must be capped in
	// runes without splitting a multibyte rune.
	long := strings.Repeat("é", MaxFieldLen+50)
	got := cleanField(long)
	if utf8.RuneCountInString(got) > MaxFieldLen {
		t.Errorf("cleanField length = %d runes, want <= %d", utf8.RuneCountInString(got), MaxFieldLen)
	}
	if !utf8.ValidString(got) {
		t.Error("cleanField produced invalid UTF-8")
	}
}

func TestCleanField_StripsQuotes(t *testing.T) {
	if got := cleanField(`"Fix the bug"`); got != "Fix the bug" {
		t.Errorf("cleanField = %q", got)
	}
}

func TestBuildPrompt_RuneBoundary(t *testing.T) {
	// A single user message longer than the char budget, from 2-byte runes so
	// the byte-budget cut would land mid-rune without care.
	long := strings.Repeat("é", maxPromptChars) // 2*maxPromptChars bytes
	if got := buildPrompt([]core.AgentMessage{userMsg(long)}); !utf8.ValidString(got) {
		t.Error("buildPrompt must not split a rune at the budget boundary")
	}
}

func TestBuildPrompt_KeepsTail(t *testing.T) {
	// With many messages over budget, the newest must survive and the oldest
	// must be dropped.
	msgs := []core.AgentMessage{
		userMsg(strings.Repeat("old ", 2000)),
		userMsg("NEWEST concrete task marker"),
	}
	got := buildPrompt(msgs)
	if !strings.Contains(got, "NEWEST concrete task marker") {
		t.Error("buildPrompt must keep the newest message")
	}
	if len(got) > maxPromptChars+64 {
		t.Errorf("buildPrompt length = %d, want ~<= %d", len(got), maxPromptChars)
	}
}

func TestBuildPrompt_SkipsNonUserAssistant(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "tool_result", Content: []core.Content{core.TextContent("ignored")}}},
		userMsg("real"),
	}
	if got := buildPrompt(msgs); strings.Contains(got, "ignored") {
		t.Errorf("buildPrompt should skip non user/assistant roles, got %q", got)
	}
}

func TestGenerate_EndToEnd(t *testing.T) {
	factory := func(core.Model) (core.Provider, error) {
		return &fakeProvider{response: "ATTEMPTING: Fix login\nPROGRESS: green tests"}, nil
	}
	b, err := Generate(context.Background(), factory, core.Model{Provider: "anthropic"}, []core.AgentMessage{userMsg("please fix login")})
	if err != nil {
		t.Fatal(err)
	}
	if b.Attempting != "Fix login" || b.Progress != "green tests" {
		t.Fatalf("brief = %+v", b)
	}
}

func TestGenerate_NoneIsEmptyNoError(t *testing.T) {
	factory := func(core.Model) (core.Provider, error) {
		return &fakeProvider{response: "NONE"}, nil
	}
	b, err := Generate(context.Background(), factory, core.Model{Provider: "anthropic"}, []core.AgentMessage{userMsg("hi")})
	if err != nil {
		t.Fatalf("NONE must not error: %v", err)
	}
	if !b.IsEmpty() {
		t.Fatalf("brief = %+v, want empty", b)
	}
}

func TestGenerate_NilFactory(t *testing.T) {
	if _, err := Generate(context.Background(), nil, core.Model{}, []core.AgentMessage{userMsg("x")}); err == nil {
		t.Error("expected error for nil factory")
	}
}

func TestGenerate_NoContent(t *testing.T) {
	factory := func(core.Model) (core.Provider, error) { return &fakeProvider{response: "x"}, nil }
	if _, err := Generate(context.Background(), factory, core.Model{}, nil); err == nil {
		t.Error("expected error for empty conversation")
	}
}

func TestCheapModelSpecFor(t *testing.T) {
	if got := cheapModelSpecFor("openai"); got != "gpt-5.4-mini" {
		t.Fatalf("openai → %q, want gpt-5.4-mini", got)
	}
	for _, p := range []string{"anthropic", ""} {
		if got := cheapModelSpecFor(p); got != DefaultModelSpec {
			t.Fatalf("%q → %q, want %q", p, got, DefaultModelSpec)
		}
	}
}

func TestWrapPrompt_FramesAsData(t *testing.T) {
	got := wrapPrompt("User: hola")
	if !strings.Contains(got, "<conversation>") || !strings.Contains(got, "</conversation>") {
		t.Errorf("wrapPrompt must delimit the transcript, got:\n%s", got)
	}
	if !strings.Contains(got, "User: hola") {
		t.Errorf("wrapPrompt must include the transcript, got:\n%s", got)
	}
}
