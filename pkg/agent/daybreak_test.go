package agent

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Daybreak is read when each request is built, so turning it off while the
// agent works reaches the next request, and each response records the program
// it was asked with.
func TestLoop_DaybreakIsReadPerRequestAndStamped(t *testing.T) {
	var programs []string
	var ag *Agent
	provider := NewMockProvider(
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			programs = append(programs, req.Options.AccessProgram)
			return toolCallResponse("tc-1", "echo", map[string]any{"text": "hello"})(req)
		},
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			programs = append(programs, req.Options.AccessProgram)
			return simpleTextResponse("done")(req)
		},
	)
	echo := core.Tool{Name: "echo", Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		ag.SetDaybreak(false)
		return core.TextResult("ok"), nil
	}}
	model, _ := core.ResolveModel("gpt-6-sol")
	reg := core.NewRegistry()
	if err := reg.Register(echo); err != nil {
		t.Fatal(err)
	}
	var err error
	ag, err = New(AgentConfig{Provider: provider, Model: model, Tools: reg, Daybreak: true})
	if err != nil {
		t.Fatal(err)
	}
	if !ag.Daybreak() {
		t.Fatal("Daybreak() = false after configuring it on")
	}
	msgs, err := ag.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(programs) != 2 || programs[0] != core.DaybreakAccessProgram || programs[1] != "" {
		t.Fatalf("request programs = %q, want [daybreak_blue \"\"]", programs)
	}
	var stamps []string
	for _, m := range msgs {
		if m.Role == "assistant" {
			stamps = append(stamps, m.AccessProgram)
		}
	}
	if len(stamps) != 2 || stamps[0] != core.DaybreakAccessProgram || stamps[1] != "" {
		t.Fatalf("assistant stamps = %q", stamps)
	}
}

// Daybreak is an OpenAI program: another provider's requests never carry it.
func TestLoop_DaybreakOnlyForOpenAI(t *testing.T) {
	var program = "unset"
	provider := NewMockProvider(func(req core.Request) (<-chan core.AssistantEvent, error) {
		program = req.Options.AccessProgram
		return simpleTextResponse("done")(req)
	})
	model, _ := core.ResolveModel("opus")
	ag, err := New(AgentConfig{Provider: provider, Model: model, Daybreak: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if program != "" {
		t.Fatalf("Anthropic request carried program %q", program)
	}
}

// Reasoning signed under the standard program is not replayed once Daybreak
// is on (and the text itself stays in history).
func TestLoop_DaybreakDropsOtherProgramsThinking(t *testing.T) {
	var sent []core.Message
	provider := NewMockProvider(func(req core.Request) (<-chan core.AssistantEvent, error) {
		sent = req.Messages
		return simpleTextResponse("done")(req)
	})
	model, _ := core.ResolveModel("gpt-6-sol")
	ag, err := New(AgentConfig{Provider: provider, Model: model, Daybreak: true})
	if err != nil {
		t.Fatal(err)
	}
	prior := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
		core.WrapMessage(core.Message{Role: "assistant", Provider: "openai", Model: "gpt-6-sol", RequestedModel: "gpt-6-sol",
			Content: []core.Content{{Type: "thinking", ThinkingSignature: `{"type":"reasoning","encrypted_content":"ENC"}`}, core.TextContent("hi")}}),
	}
	if err := ag.LoadState(prior, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	for _, m := range sent {
		for _, c := range m.Content {
			if c.Type == "thinking" {
				t.Fatalf("standard thinking replayed under Daybreak: %+v", m)
			}
		}
	}
	if len(sent) < 2 || sent[1].Content[len(sent[1].Content)-1].Text != "hi" {
		t.Fatalf("history text lost: %+v", sent)
	}
}
