package bus

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

type genProvider struct {
	calls  atomic.Int32
	stream func(context.Context, core.Request, int32) (<-chan core.AssistantEvent, error)
}

func (p *genProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	return p.stream(ctx, req, p.calls.Add(1))
}

func genToolResponse(calls ...core.Content) <-chan core.AssistantEvent {
	msg := core.Message{Role: "assistant", Content: calls, StopReason: "tool_use"}
	ch := make(chan core.AssistantEvent, 1)
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch
}

func awaitGenBarrier(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not reach its barrier", label)
	}
}

func newGenRuntime(t *testing.T, provider core.Provider, tools []core.Tool, drain time.Duration, sub func(*agent.Agent) AgentSubscriber) (*SessionRuntime, *agent.Agent) {
	t.Helper()
	reg := core.NewRegistry()
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	ag, err := agent.New(agent.AgentConfig{
		Provider: provider, Model: core.Model{ID: "run-gen", Provider: "fixture"}, Tools: reg,
		Compaction: &core.CompactionSettings{Enabled: false},
		MaxTurns:   5, MaxRunDuration: 10 * time.Second, DrainTimeout: drain,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := RuntimeConfig{SessionID: "run-gen", Agent: ag}
	if sub != nil {
		cfg.Subscriber = sub(ag)
	}
	rt, err := NewSessionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt, ag
}

func waitGenRunEnded(t *testing.T, rt *SessionRuntime, ended <-chan RunEnded) RunEnded {
	t.Helper()
	select {
	case e := <-ended:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !rt.WaitSettled(ctx) {
			t.Fatal("RunEnded was published but the run did not settle")
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("run did not end")
		return RunEnded{}
	}
}

func execGen(t *testing.T, b EventBus, cmd any) {
	t.Helper()
	if err := b.Execute(cmd); err != nil {
		t.Fatal(err)
	}
}

// Both queue-admission orders are forced through real bus commands. Stop owns
// the discard IDs and stop_id; the unwind cannot deliver them.
func TestStopAndSteerOrdersOwnDiscardAndStopID(t *testing.T) {
	for _, order := range []string{"steer_then_stop", "stop_then_steer"} {
		t.Run(order, func(t *testing.T) {
			started := make(chan struct{})
			provider := &genProvider{stream: func(context.Context, core.Request, int32) (<-chan core.AssistantEvent, error) {
				return genToolResponse(core.ToolCallContent("active", "external", nil)), nil
			}}
			rt, ag := newGenRuntime(t, provider, []core.Tool{{
				Name: "external", Parameters: []byte(`{"type":"object"}`),
				Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
					close(started)
					<-ctx.Done()
					return core.Result{}, ctx.Err()
				},
			}}, 2*time.Second, nil)
			ended := make(chan RunEnded, 2)
			rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
			var mu sync.Mutex
			var canceled []SteersCanceled
			var delivered []string
			rt.Bus.SubscribeAll(func(e any) {
				mu.Lock()
				defer mu.Unlock()
				switch ev := e.(type) {
				case SteersCanceled:
					canceled = append(canceled, ev)
				case Steered:
					delivered = append(delivered, ev.ID)
				}
			})
			execGen(t, rt.Bus, SendPrompt{Text: "start"})
			awaitGenBarrier(t, started, "active tool")
			var discarded []core.SteerItem
			steer := SteerAgent{ID: "ordered", Text: "do not lose this"}
			if order == "steer_then_stop" {
				execGen(t, rt.Bus, steer)
			}
			execGen(t, rt.Bus, AbortAndRecall{StopID: "own-stop", DiscardedSteers: &discarded})
			if order == "stop_then_steer" {
				if err := rt.Bus.Execute(steer); !errors.Is(err, agent.ErrSteerAdmissionClosed) {
					t.Errorf("steer admitted after confirmed Stop: %v", err)
				}
			}
			e := waitGenRunEnded(t, rt, ended)
			ag.Drain(2 * time.Second)
			rt.Bus.Drain(2 * time.Second)
			if !e.Cancelled || e.Err != nil || provider.calls.Load() != 1 {
				t.Errorf("stopped run = %+v; provider calls=%d", e, provider.calls.Load())
			}
			wantIDs := []string{}
			if order == "steer_then_stop" {
				wantIDs = []string{"ordered"}
			}
			if got := visibleSteerIDs(discarded); !reflect.DeepEqual(got, wantIDs) {
				t.Errorf("authoritative discard=%v, want %v", got, wantIDs)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(delivered) != 0 || len(canceled) != 1 || canceled[0].StopID != "own-stop" || !reflect.DeepEqual(canceled[0].SteerIDs, wantIDs) {
				t.Errorf("delivered=%v; cancellation events=%+v", delivered, canceled)
			}
			if len(ag.PendingSteers()) != 0 {
				t.Error("old steer survived Stop")
			}
		})
	}
}

func TestSteerDeliveredBeforeStopIsNotRecalled(t *testing.T) {
	firstStarted, nextRequest := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	provider := &genProvider{stream: func(ctx context.Context, _ core.Request, n int32) (<-chan core.AssistantEvent, error) {
		if n == 1 {
			return genToolResponse(core.ToolCallContent("active", "external", nil)), nil
		}
		close(nextRequest)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	rt, ag := newGenRuntime(t, provider, []core.Tool{{Name: "external", Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			close(firstStarted)
			<-release
			return core.TextResult("done"), nil
		}}}, 2*time.Second, nil)
	ended := make(chan RunEnded, 2)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	execGen(t, rt.Bus, SendPrompt{Text: "start"})
	awaitGenBarrier(t, firstStarted, "first tool")
	execGen(t, rt.Bus, SteerAgent{ID: "delivered", Text: "already in history"})
	open()
	awaitGenBarrier(t, nextRequest, "request after steer delivery")
	var discarded []core.SteerItem
	execGen(t, rt.Bus, AbortAndRecall{StopID: "after-delivery", DiscardedSteers: &discarded})
	waitGenRunEnded(t, rt, ended)
	if len(discarded) != 0 {
		t.Errorf("Stop recalled delivered steer: %+v", discarded)
	}
	count := 0
	for _, m := range ag.Messages() {
		for _, c := range m.Content {
			if m.Role == "user" && c.Text == "already in history" {
				count++
			}
		}
	}
	if count != 1 || provider.calls.Load() != 2 {
		t.Errorf("delivered message count=%d; provider requests=%d", count, provider.calls.Load())
	}
}

// A non-cooperative tool retains its callback and calls it after its run
// settled and a new generation started: nothing may be published, under
// either generation.
func TestLateToolUpdateCannotReachNextGeneration(t *testing.T) {
	firstStarted, nextStarted := make(chan struct{}), make(chan struct{})
	oldRelease := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(oldRelease) }) }
	t.Cleanup(open)
	callback := make(chan func(core.Result), 1)
	provider := &genProvider{stream: func(_ context.Context, _ core.Request, n int32) (<-chan core.AssistantEvent, error) {
		if n == 1 {
			return genToolResponse(core.ToolCallContent("old-call", "old", nil)), nil
		}
		return genToolResponse(core.ToolCallContent("new-call", "new", nil)), nil
	}}
	rt, ag := newGenRuntime(t, provider, []core.Tool{
		{Name: "old", Parameters: []byte(`{"type":"object"}`), Execute: func(_ context.Context, _ map[string]any, update func(core.Result)) (core.Result, error) {
			callback <- update
			close(firstStarted)
			<-oldRelease
			return core.TextResult("late old result"), nil
		}},
		{Name: "new", Parameters: []byte(`{"type":"object"}`), Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			close(nextStarted)
			<-ctx.Done()
			return core.Result{}, ctx.Err()
		}},
	}, 2*time.Second, nil)
	ended := make(chan RunEnded, 3)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	var mu sync.Mutex
	var late []ToolExecUpdate
	rt.Bus.Subscribe(func(e ToolExecUpdate) {
		mu.Lock()
		defer mu.Unlock()
		late = append(late, e)
	})
	execGen(t, rt.Bus, SendPrompt{Text: "old run"})
	awaitGenBarrier(t, firstStarted, "old tool")
	update := <-callback
	execGen(t, rt.Bus, AbortRun{})
	open()
	oldEnd := waitGenRunEnded(t, rt, ended)
	execGen(t, rt.Bus, SendPrompt{Text: "explicit new run"})
	awaitGenBarrier(t, nextStarted, "new tool")
	newGen := rt.Context().RunGenAtomic.Load()
	before := ag.Messages()
	update(core.TextResult("old callback after G2 started"))
	ag.Drain(2 * time.Second)
	rt.Bus.Drain(2 * time.Second)
	mu.Lock()
	if len(late) != 0 {
		t.Errorf("late callback escaped its cancelled token: oldGen=%d newGen=%d events=%+v", oldEnd.RunGen, newGen, late)
	}
	mu.Unlock()
	if !reflect.DeepEqual(before, ag.Messages()) || provider.calls.Load() != 2 {
		t.Error("late callback mutated transcript or started another provider request")
	}
	execGen(t, rt.Bus, AbortRun{})
	waitGenRunEnded(t, rt, ended)
}

// An early user steer through the real bus withdraws the unstarted call and is
// delivered exactly once under its steer ID, after the balanced results.
func TestEarlySteerIsDeliveredOnceAfterWithdrawnCalls(t *testing.T) {
	firstStarted, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	var secondExecutions atomic.Int32
	provider := &genProvider{stream: func(_ context.Context, _ core.Request, n int32) (<-chan core.AssistantEvent, error) {
		if n == 1 {
			return genToolResponse(core.ToolCallContent("first", "first", nil), core.ToolCallContent("second", "second", nil)), nil
		}
		msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("new turn")}, StopReason: "end_turn"}
		ch := make(chan core.AssistantEvent, 1)
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}}
	rt, ag := newGenRuntime(t, provider, []core.Tool{
		{Name: "first", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
			Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
				close(firstStarted)
				<-release
				return core.TextResult("first done"), nil
			}},
		{Name: "second", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
			Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
				secondExecutions.Add(1)
				return core.TextResult("stale effect"), nil
			}},
	}, 2*time.Second, nil)
	ended := make(chan RunEnded, 2)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	var mu sync.Mutex
	var delivered []string
	var canceled int
	rt.Bus.SubscribeAll(func(e any) {
		mu.Lock()
		defer mu.Unlock()
		switch ev := e.(type) {
		case Steered:
			delivered = append(delivered, ev.ID)
		case SteersCanceled:
			canceled++
		}
	})
	execGen(t, rt.Bus, SendPrompt{Text: "start"})
	awaitGenBarrier(t, firstStarted, "first tool")
	execGen(t, rt.Bus, SteerAgent{ID: "early", Text: "change direction"})
	open()
	e := waitGenRunEnded(t, rt, ended)
	rt.Bus.Drain(2 * time.Second)
	if e.Cancelled || e.Err != nil || provider.calls.Load() != 2 || secondExecutions.Load() != 0 {
		t.Errorf("run=%+v provider calls=%d second executions=%d", e, provider.calls.Load(), secondExecutions.Load())
	}
	var roles []string
	for _, m := range ag.Messages() {
		switch {
		case m.Role == "tool_result":
			roles = append(roles, m.ToolCallID)
		case m.Role == "user" && len(m.Content) > 0 && m.Content[0].Text == "change direction":
			roles = append(roles, "steer")
		}
	}
	if want := []string{"first", "second", "steer"}; !reflect.DeepEqual(roles, want) {
		t.Errorf("history order = %v, want %v", roles, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(delivered, []string{"early"}) || canceled != 0 {
		t.Errorf("delivered=%v canceled=%d, want the steer delivered once and never discarded", delivered, canceled)
	}
}
