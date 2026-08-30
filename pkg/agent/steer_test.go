package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
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
	ag.Steer(core.SteerItem{ID: "steer-1", Text: "course correction"})

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
	ag.Steer(core.SteerItem{ID: "steer-1", Text: "redirect"})
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

// TestSteerEventCarriesContent covers a steer queued with attachments: the
// delivery event must carry the injected message, not just its text, or clients
// render the delivered message without its images until a reload.
func TestSteerEventCarriesContent(t *testing.T) {
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
	content := []core.Content{core.TextContent("look at this"), core.ImageContent("aW1n", "image/png")}
	ag.Steer(core.SteerItem{ID: "steer-1", Text: "look at this", Content: content})
	close(release)
	<-done

	if !waitForEvent(collector, core.AgentEventSteer, 2*time.Second) {
		t.Fatal("missing steer event")
	}
	for _, e := range collector.snapshot() {
		if e.Type != core.AgentEventSteer {
			continue
		}
		if e.Text != "look at this" {
			t.Fatalf("steer event text = %q, want 'look at this'", e.Text)
		}
		if len(e.Message.Content) != 2 {
			t.Fatalf("steer event content = %#v, want the two injected blocks", e.Message.Content)
		}
		if e.Message.Content[1].Type != "image" || e.Message.Content[1].Data != "aW1n" {
			t.Fatalf("steer event image block = %#v", e.Message.Content[1])
		}
		if e.Message.MsgID == "" || e.Message.MsgID != e.MsgID {
			t.Fatalf("steer event MsgID = %q, message MsgID = %q, want them equal and set", e.MsgID, e.Message.MsgID)
		}
		// The announced blocks must be a copy: subscribers get the event
		// asynchronously, so one mutating a block would otherwise rewrite the
		// history the next provider request replays.
		e.Message.Content[1].Data = "mutated-by-subscriber"
		for _, msg := range ag.Messages() {
			for _, c := range msg.Content {
				if c.Type == "image" && c.Data != "aW1n" {
					t.Fatalf("mutating the event corrupted history: image data = %q", c.Data)
				}
			}
		}
		return
	}
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

func TestQueuedMessageRejectedAfterFinalDrain(t *testing.T) {
	tests := []struct {
		name  string
		admit func(*Agent) bool
	}{
		{
			name: "Steer",
			admit: func(ag *Agent) bool {
				return ag.Steer(core.SteerItem{ID: "too-late", Text: "too late"})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := newTestAgent(NewMockProvider(simpleTextResponse("done")))
			endSeen := make(chan struct{})
			releaseEnd := make(chan struct{})
			var endOnce sync.Once
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(releaseEnd) })
			ag.Subscribe(func(e core.AgentEvent) {
				if e.Type != core.AgentEventEnd {
					return
				}
				endOnce.Do(func() { close(endSeen) })
				<-releaseEnd
			})

			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = ag.Run(context.Background(), "finish")
			}()

			select {
			case <-endSeen:
			case <-time.After(2 * time.Second):
				t.Fatal("agent_end was not emitted")
			}
			pollUntilAgent(t, 2*time.Second, "run to close admission after its final drain", func() bool {
				ag.steerMu.Lock()
				defer ag.steerMu.Unlock()
				return ag.runTerminal
			})

			if tt.admit(ag) {
				t.Fatalf("%s returned true after AgentEnd; want false", tt.name)
			}
			if got := ag.QueueLen(); got != 0 {
				t.Fatalf("queue length = %d after rejected %s; want 0", got, tt.name)
			}

			releaseOnce.Do(func() { close(releaseEnd) })
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("agent did not finish cleanup")
			}
		})
	}
}

func TestSteerDropsWhenChannelFull(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("ok"))
	ag := newTestAgent(provider)

	// Fill the buffer (capacity 32).
	for i := 0; i < 32; i++ {
		ag.Steer(core.SteerItem{ID: "msg", Text: "msg"})
	}
	// 33rd must not block.
	done := make(chan struct{})
	go func() {
		ag.Steer(core.SteerItem{ID: "overflow", Text: "overflow"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Steer blocked on full channel")
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
	ag.Steer(core.SteerItem{ID: "steer-1", Text: "text-only steer"})
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

// helpers

func pollUntilAgent(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

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
