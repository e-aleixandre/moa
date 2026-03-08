package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestSteerInjectsMessageBetweenSteps(t *testing.T) {
	// A tool that signals it started, then blocks until released.
	started := make(chan struct{})
	release := make(chan struct{})
	blockTool := core.Tool{
		Name:       "block",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			close(started)
			<-release
			return core.TextResult("done"), nil
		},
	}

	provider := NewMockProvider(
		toolCallResponse("tc-1", "block", nil),
		simpleTextResponse("After steer."),
	)
	ag := newTestAgent(provider, blockTool)

	var msgs []core.AgentMessage
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		msgs, runErr = ag.Run(context.Background(), "do something")
	}()

	// Wait until the tool is executing.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}

	// Steer a message while the tool is blocked.
	ag.Steer("course correction")

	// Release the tool.
	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never finished")
	}
	if runErr != nil {
		t.Fatal(runErr)
	}

	// Expected messages: user, assistant(tool_call), tool_result, user(steer), assistant(text)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d: %v", len(msgs), roles(msgs))
	}
	if msgs[3].Role != "user" || msgs[3].Content[0].Text != "course correction" {
		t.Fatalf("expected steer message at index 3, got %s: %q", msgs[3].Role, firstText(msgs[3]))
	}
}

func TestSteerEmitsEvent(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	blockTool := core.Tool{
		Name:       "block",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			close(started)
			<-release
			return core.TextResult("done"), nil
		},
	}

	provider := NewMockProvider(
		toolCallResponse("tc-1", "block", nil),
		simpleTextResponse("OK."),
	)
	ag := newTestAgent(provider, blockTool)
	collector := collectEvents(ag)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ag.Run(context.Background(), "go") //nolint:errcheck
	}()

	<-started
	ag.Steer("redirect")
	close(release)
	<-done

	if !waitForEvent(collector, core.AgentEventSteer, 2*time.Second) {
		t.Fatal("missing steer event")
	}

	// Verify the event has the right text.
	for _, e := range collector.snapshot() {
		if e.Type == core.AgentEventSteer {
			if e.Text != "redirect" {
				t.Fatalf("steer event text = %q, want 'redirect'", e.Text)
			}
			return
		}
	}
}

func TestFollowUpTriggersNewTurn(t *testing.T) {
	provider := NewMockProvider(
		simpleTextResponse("First answer."),
		simpleTextResponse("Second answer."),
	)
	ag := newTestAgent(provider)

	// Subscribe to agent_start events to count agentLoop invocations.
	var startCount int
	var mu sync.Mutex
	ag.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.AgentEventStart {
			mu.Lock()
			startCount++
			mu.Unlock()
		}
	})

	// Enqueue before running — follow-up will be consumed after first turn.
	ag.Enqueue("follow up question")

	msgs, err := ag.Run(context.Background(), "initial question")
	if err != nil {
		t.Fatal(err)
	}

	// Expected: user(initial), assistant(first), user(follow-up), assistant(second)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d: %v", len(msgs), roles(msgs))
	}
	if msgs[2].Role != "user" || msgs[2].Content[0].Text != "follow up question" {
		t.Fatalf("expected follow-up at index 2, got %s: %q", msgs[2].Role, firstText(msgs[2]))
	}
	if msgs[3].Content[0].Text != "Second answer." {
		t.Fatalf("expected 'Second answer.' at index 3, got %q", firstText(msgs[3]))
	}

	// Verify agentLoop was called twice (two agent_start events).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := startCount
		mu.Unlock()
		if n >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	t.Fatalf("expected 2 agent_start events, got %d", startCount)
	mu.Unlock()
}

func TestFollowUpDoesNothingWhenEmpty(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("Only answer."))
	ag := newTestAgent(provider)

	msgs, err := ag.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

func TestSteerDropsWhenChannelFull(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("ok"))
	ag := newTestAgent(provider)

	// Fill the buffer (capacity 32).
	for i := 0; i < 32; i++ {
		ag.Steer("msg")
	}
	// 33rd must not block.
	done := make(chan struct{})
	go func() {
		ag.Steer("overflow")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Steer blocked on full channel")
	}
}

func TestSteerAndFollowUpCombined(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	blockTool := core.Tool{
		Name:       "block",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			close(started)
			<-release
			return core.TextResult("done"), nil
		},
	}

	provider := NewMockProvider(
		toolCallResponse("tc-1", "block", nil),
		simpleTextResponse("After steer."),   // response to steer
		simpleTextResponse("After followup."), // response to follow-up
	)
	ag := newTestAgent(provider, blockTool)

	done := make(chan struct{})
	var msgs []core.AgentMessage
	var runErr error
	go func() {
		defer close(done)
		msgs, runErr = ag.Run(context.Background(), "go")
	}()

	<-started
	ag.Steer("steer msg")
	ag.Enqueue("followup msg")
	close(release)
	<-done

	if runErr != nil {
		t.Fatal(runErr)
	}

	// Steer arrives first (inter-step), follow-up after the turn ends.
	// Expected: user(go), assistant(tc), tool_result, user(steer), assistant(after steer),
	//           user(followup), assistant(after followup)
	if len(msgs) != 7 {
		t.Fatalf("expected 7 messages, got %d: %v", len(msgs), roles(msgs))
	}
	if msgs[3].Role != "user" || msgs[3].Content[0].Text != "steer msg" {
		t.Fatalf("expected steer at index 3, got %s: %q", msgs[3].Role, firstText(msgs[3]))
	}
	if msgs[5].Role != "user" || msgs[5].Content[0].Text != "followup msg" {
		t.Fatalf("expected follow-up at index 5, got %s: %q", msgs[5].Role, firstText(msgs[5]))
	}
}

func TestSteerTextOnlyTurnStillInjected(t *testing.T) {
	// Provider streams a text-only response (no tools). Steer during streaming
	// must still be processed by the execute() outer loop.
	streaming := make(chan struct{})
	release := make(chan struct{})
	delayedTextResponse := func(text string) func(req core.Request) (<-chan core.AssistantEvent, error) {
		return func(req core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 10)
			go func() {
				defer close(ch)
				msg := core.Message{
					Role:       "assistant",
					Content:    []core.Content{core.TextContent(text)},
					StopReason: "end_turn",
				}
				ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
				close(streaming) // signal: we're streaming
				<-release        // wait for test to steer
				ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: text}
				ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			}()
			return ch, nil
		}
	}

	provider := NewMockProvider(
		delayedTextResponse("first response"),
		simpleTextResponse("after steer"),
	)
	ag := newTestAgent(provider)

	done := make(chan struct{})
	var msgs []core.AgentMessage
	var runErr error
	go func() {
		defer close(done)
		msgs, runErr = ag.Run(context.Background(), "hello")
	}()

	// Wait until streaming starts.
	select {
	case <-streaming:
	case <-time.After(2 * time.Second):
		t.Fatal("streaming never started")
	}

	// Steer while in text-only streaming (no tool calls).
	ag.Steer("text-only steer")
	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never finished")
	}
	if runErr != nil {
		t.Fatal(runErr)
	}

	// Expected: user(hello), assistant(first), user(steer), assistant(after steer)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d: %v", len(msgs), roles(msgs))
	}
	if msgs[2].Role != "user" || msgs[2].Content[0].Text != "text-only steer" {
		t.Fatalf("expected steer at index 2, got %s: %q", msgs[2].Role, firstText(msgs[2]))
	}
}

func TestExecuteDrainsBothFollowUpsAndSteer(t *testing.T) {
	// Both a follow-up and a steer are queued. Both must be drained
	// by the execute() outer loop, with follow-ups first.
	streaming := make(chan struct{})
	release := make(chan struct{})
	delayedTextResponse := func(text string) func(req core.Request) (<-chan core.AssistantEvent, error) {
		return func(req core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 10)
			go func() {
				defer close(ch)
				msg := core.Message{
					Role:       "assistant",
					Content:    []core.Content{core.TextContent(text)},
					StopReason: "end_turn",
				}
				ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
				close(streaming)
				<-release
				ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			}()
			return ch, nil
		}
	}

	provider := NewMockProvider(
		delayedTextResponse("first"),
		simpleTextResponse("after both"),
	)
	ag := newTestAgent(provider)

	done := make(chan struct{})
	var msgs []core.AgentMessage
	var runErr error
	go func() {
		defer close(done)
		msgs, runErr = ag.Run(context.Background(), "go")
	}()

	<-streaming
	ag.Enqueue("followup msg")
	ag.Steer("steer msg")
	close(release)
	<-done

	if runErr != nil {
		t.Fatal(runErr)
	}

	// Expected: user(go), assistant(first), user(followup), user(steer), assistant(after both)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d: %v", len(msgs), roles(msgs))
	}
	// Follow-ups come before steers (deterministic order).
	if msgs[2].Content[0].Text != "followup msg" {
		t.Fatalf("expected followup at index 2, got %q", firstText(msgs[2]))
	}
	if msgs[3].Content[0].Text != "steer msg" {
		t.Fatalf("expected steer at index 3, got %q", firstText(msgs[3]))
	}
}

// helpers

func roles(msgs []core.AgentMessage) []string {
	r := make([]string, len(msgs))
	for i, m := range msgs {
		r[i] = m.Role
	}
	return r
}

func firstText(m core.AgentMessage) string {
	for _, c := range m.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}
