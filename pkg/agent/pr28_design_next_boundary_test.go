package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

type pr28DesignBoundarySummary struct {
	calls   atomic.Int32
	entered [2]chan struct{}
	release [2]chan struct{}
}

func (s *pr28DesignBoundarySummary) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	i := int(s.calls.Add(1)) - 1
	if i >= len(s.entered) {
		return nil, errors.New("unexpected third summary attempt")
	}
	close(s.entered[i])
	select {
	case <-s.release[i]:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("")}, StopReason: "end_turn", Usage: &core.Usage{Input: 1000, Output: 100}}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

// The held worker finishes its failure while an ordinary request is already
// in flight. A real tool turn then supplies the next ordinary request boundary;
// cleanup of the first failure must not disable compaction for that boundary.
func TestPR28Design_FailureAllowsExactlyOneAttemptAtNextBoundary(t *testing.T) {
	sum := &pr28DesignBoundarySummary{}
	for i := range sum.entered {
		sum.entered[i] = make(chan struct{})
		sum.release[i] = make(chan struct{})
	}
	prov := &bgtProvider{}
	ag := pr28Agent(t, sum, prov, 40000)
	if err := ag.tools.Register(core.Tool{Name: "next_boundary", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		return core.TextResult("next"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var usage, starts, ends atomic.Int32
	ag.Subscribe(func(e core.AgentEvent) {
		switch e.Type {
		case core.AgentEventCompactionUsage:
			usage.Add(1)
		case core.AgentEventStart:
			starts.Add(1)
		case core.AgentEventEnd:
			ends.Add(1)
		}
	})
	prov.script = func(call int) *core.Message {
		if call > 2 {
			t.Errorf("unexpected ordinary request %d", call)
			return nil
		}
		select {
		case <-sum.entered[call-1]:
		case <-ctx.Done():
			t.Errorf("boundary %d did not launch its own summary attempt", call)
			return nil
		}
		close(sum.release[call-1])
		ag.WaitBackgroundCompaction()
		if s := ag.BackgroundCompaction(); s.Active || s.Waiting {
			t.Errorf("finished failure %d kept background state active: %+v", call, s)
		}
		if call == 1 {
			return &core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("next-boundary", "next_boundary", map[string]any{})}, StopReason: "tool_use"}
		}
		return nil
	}
	if _, err := ag.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	ag.WaitBackgroundCompaction()
	ag.Drain(bgtWait)
	if sum.calls.Load() != 2 || prov.calls.Load() != 2 || usage.Load() != 2 {
		t.Fatalf("summary=%d ordinary=%d usage=%d, want 2 each", sum.calls.Load(), prov.calls.Load(), usage.Load())
	}
	if starts.Load() != 1 || ends.Load() != 1 || hasSummary(ag.Messages()) {
		t.Fatalf("synthetic/adopted work: starts=%d ends=%d summary=%v", starts.Load(), ends.Load(), hasSummary(ag.Messages()))
	}
}
