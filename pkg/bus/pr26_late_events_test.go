package bus

// Review PR #26 — REDs for late (already-emitted, ordered) events of a
// finished run consumed by the bridge after the next run started.
//
// The agent emitter delivers to the bridge on one ordered channel, so every
// late G1 event is bridged before any G2 event. Dropping them is not needed to
// protect G2, and it loses what clients and reconnect snapshots need.

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

type r26Provider struct {
	calls  atomic.Int32
	stream func(context.Context, int32) (<-chan core.AssistantEvent, error)
}

func (p *r26Provider) Stream(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	return p.stream(ctx, p.calls.Add(1))
}

func r26Done(msg core.Message) <-chan core.AssistantEvent {
	ch := make(chan core.AssistantEvent, 1)
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch
}

type r26HeldSubscriber struct {
	ag      *agent.Agent
	hold    func(core.AgentEvent) bool
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *r26HeldSubscriber) Subscribe(fn func(core.AgentEvent)) func() {
	return s.ag.Subscribe(func(e core.AgentEvent) {
		if s.hold(e) {
			s.once.Do(func() { close(s.entered) })
			<-s.release
		}
		fn(e)
	})
}

func r26Runtime(t *testing.T, p core.Provider, tools []core.Tool, sub func(*agent.Agent) AgentSubscriber) (*SessionRuntime, *agent.Agent) {
	t.Helper()
	reg := core.NewRegistry()
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	ag, err := agent.New(agent.AgentConfig{
		Provider: p, Model: core.Model{ID: "r26", Provider: "fixture"}, Tools: reg,
		Compaction: &core.CompactionSettings{Enabled: false},
		MaxTurns:   5, MaxRunDuration: 10 * time.Second, DrainTimeout: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: "r26", Agent: ag, Subscriber: sub(ag)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt, ag
}

func r26Await(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s not reached", label)
	}
}

func r26Ended(t *testing.T, rt *SessionRuntime, ended <-chan RunEnded) RunEnded {
	t.Helper()
	select {
	case e := <-ended:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !rt.WaitSettled(ctx) {
			t.Fatal("run did not settle")
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("run did not end")
		return RunEnded{}
	}
}

// G1's tool start is bridged; its end, turn end and agent end are bridged
// only after G2 started. Clients must still get the end of G1's tool, and a
// reconnect snapshot during G2 must not show G1's tool as running.
func TestPR26ALateToolEndOfFinishedRunStillClosesItsRow(t *testing.T) {
	g1Started, g2Started, held := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	p := &r26Provider{stream: func(_ context.Context, n int32) (<-chan core.AssistantEvent, error) {
		id, name := "old-call", "write"
		if n > 1 {
			id, name = "new-call", "new"
		}
		return r26Done(core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent(id, name, nil)}, StopReason: "tool_use"}), nil
	}}
	block := func(started chan struct{}) func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		return func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			close(started)
			<-ctx.Done()
			return core.TextResult("completed after cancellation"), nil
		}
	}
	rt, ag := r26Runtime(t, p, []core.Tool{
		{Name: "write", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell, Execute: block(g1Started)},
		{Name: "new", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell, Execute: block(g2Started)},
	}, func(ag *agent.Agent) AgentSubscriber {
		return &r26HeldSubscriber{ag: ag, entered: held, release: release, hold: func(e core.AgentEvent) bool {
			return e.Type == core.AgentEventToolExecEnd && e.ToolCallID == "old-call"
		}}
	})
	ended := make(chan RunEnded, 3)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	var mu sync.Mutex
	var oldEnds []ToolExecEnded
	var callOrder []string
	rt.Bus.SubscribeAll(func(e any) {
		mu.Lock()
		defer mu.Unlock()
		switch ev := e.(type) {
		case ToolExecEnded:
			if ev.ToolCallID == "old-call" {
				oldEnds = append(oldEnds, ev)
				callOrder = append(callOrder, "old-end")
			}
		case ToolExecStarted:
			if ev.ToolCallID == "new-call" {
				callOrder = append(callOrder, "new-start")
			}
		}
	})

	if err := rt.Bus.Execute(SendPrompt{Text: "g1"}); err != nil {
		t.Fatal(err)
	}
	r26Await(t, g1Started, "G1 tool")
	if err := rt.Bus.Execute(AbortRun{}); err != nil {
		t.Fatal(err)
	}
	r26Await(t, held, "bridge held on G1 tool end")
	oldEnd := r26Ended(t, rt, ended)
	if err := rt.Bus.Execute(SendPrompt{Text: "g2"}); err != nil {
		t.Fatal(err)
	}
	r26Await(t, g2Started, "G2 tool")
	open()
	ag.Drain(2 * time.Second)
	rt.Bus.Drain(2 * time.Second)

	mu.Lock()
	if len(oldEnds) != 1 || oldEnds[0].RunGen != oldEnd.RunGen {
		t.Errorf("G1 tool end events=%+v, want exactly one with origin generation %d", oldEnds, oldEnd.RunGen)
	}
	if !reflect.DeepEqual(callOrder, []string{"old-end", "new-start"}) {
		t.Errorf("ordered emitter bridge published tool events=%v, want old-end before new-start", callOrder)
	}
	mu.Unlock()
	if stats := rt.Context().snapshotRunStats(rt.Context().RunGenAtomic.Load()); stats.hadEdits || stats.finalText != "" || stats.costUSD != 0 {
		t.Errorf("G1 changed G2 run stats: %+v", stats)
	}
	_, live, _ := rt.Context().SnapshotInFlightWithCut()
	newRunning := 0
	for _, c := range live {
		if c.ToolCallID == "new-call" && c.Phase == LiveToolPhaseRunning {
			newRunning++
		}
		if c.ToolCallID == "old-call" && c.Phase == LiveToolPhaseRunning {
			t.Errorf("reconnect snapshot during G2 shows G1's tool as running: %+v", live)
		}
	}
	if newRunning != 1 {
		t.Errorf("late G1 terminal event removed G2 running tool: %+v", live)
	}
	if err := rt.Bus.Execute(AbortRun{}); err != nil {
		t.Fatal(err)
	}
	r26Ended(t, rt, ended)
}

// G1 answers with text; its message_end is bridged only after G2 started.
// Clients render a reply only from message_end: dropping it loses G1's answer
// from every open client until a reload.
func TestPR26ALateFinalMessageOfFinishedRunReachesClients(t *testing.T) {
	g2Started, held := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	p := &r26Provider{stream: func(ctx context.Context, n int32) (<-chan core.AssistantEvent, error) {
		if n == 1 {
			return r26Done(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("G1 final answer")}, StopReason: "end_turn"}), nil
		}
		close(g2Started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	rt, ag := r26Runtime(t, p, nil, func(ag *agent.Agent) AgentSubscriber {
		return &r26HeldSubscriber{ag: ag, entered: held, release: release, hold: func(e core.AgentEvent) bool {
			return e.Type == core.AgentEventMessageEnd && e.Message.Role == "assistant"
		}}
	})
	ended := make(chan RunEnded, 3)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	var mu sync.Mutex
	var finals []MessageEnded
	rt.Bus.Subscribe(func(e MessageEnded) {
		mu.Lock()
		finals = append(finals, e)
		mu.Unlock()
	})
	if err := rt.Bus.Execute(SendPrompt{Text: "g1"}); err != nil {
		t.Fatal(err)
	}
	r26Await(t, held, "bridge held on G1 message_end")
	oldEnd := r26Ended(t, rt, ended)
	if err := rt.Bus.Execute(SendPrompt{Text: "g2"}); err != nil {
		t.Fatal(err)
	}
	r26Await(t, g2Started, "G2 request")
	open()
	ag.Drain(2 * time.Second)
	rt.Bus.Drain(2 * time.Second)
	mu.Lock()
	got := append([]MessageEnded(nil), finals...)
	mu.Unlock()
	found := false
	for _, f := range got {
		if f.FullText == "G1 final answer" {
			found = true
			if f.RunGen != oldEnd.RunGen {
				t.Errorf("G1 final answer relabeled as generation %d, want %d", f.RunGen, oldEnd.RunGen)
			}
		}
	}
	if !found {
		t.Errorf("G1's final assistant message never reached the bus (clients): message_end events=%+v", got)
	}
	if stats := rt.Context().snapshotRunStats(rt.Context().RunGenAtomic.Load()); stats.finalText != "" || stats.costUSD != 0 {
		t.Errorf("G1 final message contaminated G2 accounting: %+v", stats)
	}
	if err := rt.Bus.Execute(AbortRun{}); err != nil {
		t.Fatal(err)
	}
	r26Ended(t, rt, ended)
}
