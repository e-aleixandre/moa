package agent

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The caller's provider has already reported .09 when the tool launches a
// child. After the child's first .02 call the shared spend is .11, so its next
// checkpoint must stop it before another paid call under a .10 ceiling.
func TestRunBudgetParentUsageVisibleToChildCheckpoint(t *testing.T) {
	var childErr error
	var childCalls int
	var childCost float64
	spawn := spawnTool("spawn", func(ctx context.Context) string {
		child, provider := newBudgetChild(t, ctx, testPricing(),
			toolCallWithUsage("c1", "noop", map[string]any{}, usageOf(.02)),
			textResponseWithUsage("child finished", usageOf(.02)),
		)
		_, childErr = child.Run(context.Background(), "work")
		childCalls = provider.calls
		childCost = child.RunCost()
		return "child returned"
	})
	parent := newBudgetParent(t, .10, NewMockProvider(
		toolCallWithUsage("p1", "spawn", map[string]any{}, usageOf(.09)),
		textResponseWithUsage("parent finished", nil),
	), spawn)
	_, err := parent.Run(context.Background(), "go")
	if be := budgetError(err); be == nil {
		t.Fatalf("parent error=%v, want budget error", err)
	}
	if childCalls != 1 || budgetError(childErr) == nil {
		t.Fatalf("child error=%v calls=%d own cost=%.2f; parent own=%.2f, final parent error=%v; child passed checkpoint at real .11 > .10 and made an extra paid call", childErr, childCalls, childCost, parent.RunCost(), err)
	}
}

func TestRunBudgetUnpricedChildChecksAfterFinalNoUsage(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	result := make(chan error, 1)
	spawn := spawnTool("spawn", func(ctx context.Context) string {
		child, _ := newBudgetChild(t, ctx, nil,
			func(req core.Request) (<-chan core.AssistantEvent, error) {
				close(started)
				<-release
				return textResponseWithUsage("unpriced child finished", nil)(req)
			},
		)
		go func() { _, err := child.Run(context.Background(), "work"); result <- err }()
		return "child started"
	})
	parent := newBudgetParent(t, .10, NewMockProvider(
		toolCallWithUsage("p1", "spawn", map[string]any{}, nil),
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Error("child did not enter provider")
			}
			return textResponseWithUsage("expensive parent", usageOf(.20))(req)
		},
	), spawn)
	_, err := parent.Run(context.Background(), "go")
	unblock()
	if budgetError(err) == nil {
		t.Fatalf("parent error=%v, want budget error", err)
	}
	select {
	case err := <-result:
		if budgetError(err) == nil {
			t.Fatalf("unpriced child returned %v after caller exhausted the shared pool (.20 > .10); final no-usage response skipped its budget checkpoint", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not settle")
	}
}

// Cancellation at the tool-return boundary must not erase the caller's paid
// response: an async child still charges that launching run after it ends.
func TestRunBudgetCancelledToolStillChargesParent(t *testing.T) {
	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	result := make(chan error, 1)
	spawn := spawnTool("spawn", func(ctx context.Context) string {
		child, _ := newBudgetChild(t, ctx, testPricing(),
			func(req core.Request) (<-chan core.AssistantEvent, error) {
				<-release
				return toolCallWithUsage("c1", "noop", map[string]any{}, usageOf(.02))(req)
			},
			textResponseWithUsage("child finished", nil),
		)
		go func() { _, err := child.Run(context.Background(), "work"); result <- err }()
		cancel()
		return "child started; root cancelled"
	})
	parent := newBudgetParent(t, .10, NewMockProvider(
		toolCallWithUsage("p1", "spawn", map[string]any{}, usageOf(.09)),
	), spawn)
	_, err := parent.Run(rootCtx, "go")
	close(release)
	var childErr error
	select {
	case childErr = <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not settle")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent error=%v, want cancelled", err)
	}
	if math.Abs(parent.RunCost()-.09) > 1e-9 || budgetError(childErr) == nil {
		t.Fatalf("parent paid .09, but RunCost=%.2f; async child error=%v after .02 new spend; cancellation erased paid usage from the launching pool", parent.RunCost(), childErr)
	}
}
