package serve

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

const costChildTask = "COST-CHILD-TASK"

// costProvider answers the parent and its subagent by who is asking, so an
// async child may run alongside the parent's next turn.
type costProvider struct {
	mu         sync.Mutex
	parent     []core.Message
	child      core.Message
	childGate  <-chan struct{}
	parentCall int
}

func (p *costProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if isTestAutoTitleRequest(req) {
		return simpleResponse("title"), nil
	}
	// A child's conversation starts with its task; the parent's with "start".
	isChild := false
	if len(req.Messages) > 0 {
		for _, c := range req.Messages[0].Content {
			if strings.Contains(c.Text, costChildTask) {
				isChild = true
			}
		}
	}
	var msg core.Message
	if isChild {
		if p.childGate != nil {
			select {
			case <-p.childGate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		msg = p.child
	} else {
		p.mu.Lock()
		i := p.parentCall
		p.parentCall++
		p.mu.Unlock()
		if i >= len(p.parent) {
			return simpleResponse("done"), nil
		}
		msg = p.parent[i]
	}
	msg.Timestamp = time.Now().Unix()
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func costText(text string, input int) core.Message {
	return core.Message{Role: "assistant", Content: []core.Content{core.TextContent(text)}, StopReason: "end_turn", Usage: &core.Usage{Input: input}}
}

func costSpawn(async bool, input int) core.Message {
	return core.Message{
		Role:       "assistant",
		Content:    []core.Content{core.ToolCallContent("tc-sub", "subagent", map[string]any{"task": costChildTask, "async": async})},
		StopReason: "tool_use", Usage: &core.Usage{Input: input},
	}
}

func newCostManager(t *testing.T, prov core.Provider) (*Manager, string) {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	mgr := newTestManagerWithConfig(t, context.Background(), prov, root, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	return mgr, root
}

func sessionCostOf(t *testing.T, s *ManagedSession) float64 {
	t.Helper()
	cost, err := bus.QueryTyped[bus.GetSessionCost, float64](s.runtime.Bus, bus.GetSessionCost{})
	if err != nil {
		t.Fatal(err)
	}
	return cost
}

// diskSessionCost reads session_cost_usd straight from the saved JSON.
func diskSessionCost(t *testing.T, mgr *Manager, id string) (any, bool) {
	t.Helper()
	saved, _, err := session.FindSession(mgr.sessionBaseDir, id)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := saved.Metadata["session_cost_usd"]
	return v, ok
}

// closeWhenQuiet closes a session once its background bookkeeping settles.
func closeWhenQuiet(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	var err error
	pollUntil(t, 5*time.Second, "session closable", func() bool {
		err = mgr.CloseSession(id)
		return !errors.Is(err, ErrBusy)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func haikuCost(t *testing.T, input int) float64 {
	t.Helper()
	m, ok := core.ResolveModel("haiku")
	if !ok || m.Pricing == nil {
		t.Fatal("test model needs catalog pricing")
	}
	return m.Pricing.Cost(core.Usage{Input: input})
}

// The review's probe: a session's total spend reset to $0 after closing and
// reopening it.
func TestSessionCostSurvivesResume(t *testing.T) {
	prov := &costProvider{
		parent: []core.Message{costSpawn(false, 2_000), costText("parent done", 3_000)},
		child:  costText("child done", 1_000),
	}
	mgr, _ := newCostManager(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	want := haikuCost(t, 6_000)
	pollUntil(t, 5*time.Second, "parent idle", func() bool { return sessState(sess) == StateIdle && prov.calls() >= 2 })
	sess.runtime.Bus.Drain(time.Second)
	if got := sessionCostOf(t, sess); math.Abs(got-want) > 1e-9 {
		t.Fatalf("session cost before close = %v, want %v (run + child)", got, want)
	}

	closeWhenQuiet(t, mgr, sess.ID)
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionCostOf(t, resumed); math.Abs(got-want) > 1e-9 {
		t.Fatalf("session cost after close/resume = %v, want %v", got, want)
	}
}

// An async child that ends while the session is idle is on disk without
// waiting for the next turn or a flush.
func TestSessionCostPersistedAfterIdleSubagentEnd(t *testing.T) {
	gate := make(chan struct{})
	prov := &costProvider{
		parent:    []core.Message{costSpawn(true, 0), costText("parent done", 0)},
		child:     costText("child done", 1_000),
		childGate: gate,
	}
	mgr, _ := newCostManager(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "parent idle", func() bool { return sessState(sess) == StateIdle && prov.calls() >= 2 })
	time.Sleep(100 * time.Millisecond) // let the run's own saves settle
	close(gate)

	want := haikuCost(t, 1_000)
	pollUntil(t, 5*time.Second, "child cost on disk", func() bool {
		v, ok := diskSessionCost(t, mgr, sess.ID)
		f, isNum := v.(float64)
		return ok && isNum && math.Abs(f-want) < 1e-9
	})
}

func (p *costProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.parentCall
}

func TestSessionCostClearPersistsZero(t *testing.T) {
	prov := &costProvider{parent: []core.Message{costText("done", 1_000)}}
	mgr, _ := newCostManager(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "cost on disk", func() bool {
		_, ok := diskSessionCost(t, mgr, sess.ID)
		return sessState(sess) == StateIdle && ok
	})
	if err := sess.runtime.Bus.Execute(bus.ClearSession{SessionID: sess.ID}); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "cleared cost on disk", func() bool {
		v, ok := diskSessionCost(t, mgr, sess.ID)
		return !ok || v == float64(0)
	})
	closeWhenQuiet(t, mgr, sess.ID)
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionCostOf(t, resumed); got != 0 {
		t.Fatalf("session cost after clear and resume = %v, want 0", got)
	}
}

// Sessions saved before the key existed, or with a value no build writes,
// start at $0; a valid saved total is restored.
func TestSessionCostLegacySessionStartsAtZero(t *testing.T) {
	for name, tc := range map[string]struct {
		value any
		want  float64
	}{
		"valid":    {1.25, 1.25}, // control: a saved total is restored
		"missing":  {nil, 0},
		"string":   {"1.5", 0},
		"negative": {-2.0, 0},
		"bool":     {true, 0},
		"object":   {map[string]any{"usd": 1}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			mgr, root := newCostManager(t, &costProvider{})
			store, err := session.NewFileStore(mgr.sessionBaseDir, root)
			if err != nil {
				t.Fatal(err)
			}
			saved := store.Create()
			saved.SetRuntimeMetadata("anthropic/claude-haiku-4-5-20251001", root, "yolo", "")
			if tc.value != nil {
				saved.Metadata["session_cost_usd"] = tc.value
			}
			if err := store.Save(saved); err != nil {
				t.Fatal(err)
			}
			resumed, err := mgr.ResumeSession(saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := sessionCostOf(t, resumed); got != tc.want {
				t.Fatalf("session cost = %v, want %v", got, tc.want)
			}
		})
	}
}
