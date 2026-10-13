package bus

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
)

type heldProviderFunc func(context.Context, core.Request) (<-chan core.AssistantEvent, error)

func (p heldProviderFunc) Stream(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
	return p(ctx, r)
}

func heldMessage(m core.Message) <-chan core.AssistantEvent {
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch
}

type heldUsageBridge struct {
	a       *agent.Agent
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *heldUsageBridge) Subscribe(fn func(core.AgentEvent)) func() {
	return s.a.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.AgentEventCompactionUsage {
			s.once.Do(func() { close(s.entered) })
			<-s.release
		}
		fn(e)
	})
}

// A background summary charged to the run's pool reaches the bridge only after
// RunEnded, so it is reported as CompactionUsage instead of in RunEnded.Cost.
// Each spend lands once in the goal total: the run's own, the summary and the
// async child the run launched, which finishes within the run's pool.
func TestGoalLateSummaryAndAsyncChildCountOnce(t *testing.T) {
	bridgeRelease := make(chan struct{})
	childRelease := make(chan struct{})
	var bridgeOnce, childOnce sync.Once
	openBridge := func() { bridgeOnce.Do(func() { close(bridgeRelease) }) }
	openChild := func() { childOnce.Do(func() { close(childRelease) }) }
	defer openBridge()
	defer openChild()
	sumRelease := make(chan struct{})
	childResult := make(chan error, 1)
	price := &core.Pricing{Input: 10_000}
	g := goal.New()
	if err := g.Enter(goal.Options{Objective: "x", TotalBudget: .10, StatePath: t.TempDir() + "/STATE.md"}); err != nil {
		t.Fatal(err)
	}
	reg := core.NewRegistry()
	var root *agent.Agent
	var bridge *heldUsageBridge
	if err := reg.Register(core.Tool{Name: "spawn", Effect: core.EffectReadOnly, Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
		child, err := agent.New(agent.AgentConfig{
			Model:        core.Model{ID: "child", Provider: "mock", Pricing: price},
			ParentBudget: agent.RunBudgetFrom(ctx),
			Provider: heldProviderFunc(func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
				select {
				case <-childRelease:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return heldMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("child done")}, StopReason: "end_turn", Usage: &core.Usage{Input: 1}}), nil
			}),
		})
		if err != nil {
			return core.Result{}, err
		}
		go func() { _, err := child.Run(context.Background(), "work"); childResult <- err }()
		close(sumRelease)
		select {
		case <-bridge.entered:
		case <-time.After(3 * time.Second):
			return core.Result{}, errors.New("summary usage did not enter bridge")
		}
		root.WaitBackgroundCompaction()
		return core.TextResult("child launched; summary settled"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := heldProviderFunc(func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		calls++
		if calls == 1 {
			return heldMessage(core.Message{Role: "assistant", StopReason: "tool_use", Content: []core.Content{core.ToolCallContent("s", "spawn", map[string]any{})}, Usage: &core.Usage{Input: 1}}), nil
		}
		return nil, errors.New("pause goal maker after its summary")
	})
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecent: 8000, CompactAt: 40000, TrimDisabled: true}
	var err error
	root, err = agent.New(agent.AgentConfig{Provider: provider, Model: core.Model{ID: "root", Provider: "mock", MaxInput: 100000, Pricing: price}, Tools: reg, MaxBudget: .10, DrainTimeout: 30 * time.Millisecond, Compaction: &settings,
		CompactSummarizer: func(core.Model) (core.Provider, core.Model, string) {
			return heldProviderFunc(func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
				select {
				case <-sumRelease:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return heldMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("compact summary")}, StopReason: "end_turn", Usage: &core.Usage{Input: 6}}), nil
			}), core.Model{ID: "summary", Provider: "mock", Pricing: price}, ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge = &heldUsageBridge{a: root, entered: make(chan struct{}), release: bridgeRelease}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: "bg-budget", Agent: root, Subscriber: bridge, Goal: g, InitialMessages: bgPrefix()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { openBridge(); openChild(); rt.Close() })
	ended := make(chan RunEnded, 1)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	if err := rt.Bus.Execute(SendPrompt{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	var e RunEnded
	select {
	case e = <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("maker did not end")
	}
	if e.RunGen == 0 {
		t.Fatal("this probe must use the real bus generation")
	}
	if math.Abs(root.RunCost()-.07) > 1e-9 || math.Abs(e.Cost-.01) > 1e-9 {
		t.Fatalf("invalid schedule: pool own=%v RunEnded=%v, want .07/.01", root.RunCost(), e.Cost)
	}
	openBridge()
	root.Drain(time.Second)
	rt.Bus.Drain(time.Second)
	if math.Abs(g.Spent()-.07) > 1e-9 {
		t.Fatalf("goal spent=%v, want .07 (own .01 + summary .06)", g.Spent())
	}
	openChild()
	select {
	case err := <-childResult:
		if err != nil {
			t.Fatalf("child=%v, want it to finish: the pool holds .08 of .10", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not settle")
	}
	// The child ran outside the subagent tool, so charge its spend as the
	// tool's end callback would.
	rt.Bus.Publish(SubagentEnded{SessionID: "bg-budget", JobID: "c", Status: "completed", CostUSD: .01, GoalActivation: g.Activation()})
	rt.Bus.Drain(time.Second)
	if math.Abs(g.Spent()-.08) > 1e-9 {
		t.Fatalf("goal spent=%v, want .08: own, summary and child once each", g.Spent())
	}
	if total, _ := QueryTyped[GetSessionCost, float64](rt.Bus, GetSessionCost{}); math.Abs(total-.08) > 1e-9 {
		t.Fatalf("session cost=%v, want .08", total)
	}
}
