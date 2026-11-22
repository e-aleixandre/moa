package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A message only waits by default: it is read once the batch has run, and it
// withdraws nothing. Redirect is what cuts in, either on the message itself or
// by marking the queue afterwards (RedirectQueued).

// redirectFixture is a batch of a gated shell call followed by a call that
// cannot start before it: a shell barrier, or a writer on the same path.
type redirectFixture struct {
	ag        *Agent
	provider  *MockProvider
	gate      *admissionGate
	second    atomic.Int32
	cancelled atomic.Bool
	requests  chan core.Request
}

func newRedirectFixture(t *testing.T, samePath bool) *redirectFixture {
	t.Helper()
	f := &redirectFixture{gate: newAdmissionGate(t), requests: make(chan core.Request, 1)}
	f.provider = NewMockProvider(
		toolBatchResponse(core.ToolCallContent("first", "first", nil), core.ToolCallContent("second", "second", nil)),
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			f.requests <- req
			return simpleTextResponse("read it")(req)
		})
	first := core.Tool{Name: "first", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			close(f.gate.entered)
			<-f.gate.release
			f.cancelled.Store(ctx.Err() != nil)
			return core.TextResult("first done"), nil
		}}
	second := core.Tool{Name: "second", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			f.second.Add(1)
			return core.TextResult("second done"), nil
		}}
	if samePath {
		first.Effect, second.Effect = core.EffectWritePath, core.EffectWritePath
		first.LockKey = func(map[string]any) string { return "/fixture" }
		second.LockKey = first.LockKey
	}
	f.ag = newTestAgent(f.provider, first, second)
	return f
}

func TestQueuedMessageWaitsForTheBatch(t *testing.T) {
	for _, scenario := range []string{"behind_shell", "same_path"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRedirectFixture(t, scenario == "same_path")
			done := startAdmissionRun(t, f.ag)
			awaitBarrier(t, f.gate.entered, "first call")
			if err := f.ag.TrySteer(core.SteerItem{ID: "note", Text: "when convenient"}); err != nil {
				t.Fatal(err)
			}
			f.gate.open()
			finishAdmissionRun(t, done, false)
			if f.second.Load() != 1 {
				t.Errorf("Execute(second)=%d, want 1: a queued message must not withdraw it", f.second.Load())
			}
			for _, r := range toolResultsInOrder(t, f.ag.Messages(), "first", "second") {
				if r.IsError {
					t.Errorf("%s = %q, want its real result", r.ToolCallID, messageText(r))
				}
			}
			pr26AssertSteerAfterResults(t, (<-f.requests).Messages, "when convenient", true)
			if providerCalls(f.provider) != 2 || f.ag.QueueLen() != 0 {
				t.Errorf("requests=%d queue=%d, want one delivery after the batch", providerCalls(f.provider), f.ag.QueueLen())
			}
		})
	}
}

func TestRedirectQueuedWithdrawsUnstartedCalls(t *testing.T) {
	for _, scenario := range []string{"behind_shell", "same_path"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRedirectFixture(t, scenario == "same_path")
			done := startAdmissionRun(t, f.ag)
			awaitBarrier(t, f.gate.entered, "first call")
			if err := f.ag.TrySteer(core.SteerItem{ID: "note", Text: "change direction"}); err != nil {
				t.Fatal(err)
			}
			if got := f.ag.RedirectQueued(); !eq(got, []string{"note"}) {
				t.Fatalf("RedirectQueued = %v, want [note]", got)
			}
			if pending := f.ag.PendingSteers(); len(pending) != 1 || !pending[0].Redirect {
				t.Errorf("pending = %+v, want the message marked Redirect", pending)
			}
			f.gate.open()
			finishAdmissionRun(t, done, false)
			if f.second.Load() != 0 {
				t.Errorf("Execute(second)=%d, want 0 after a redirect", f.second.Load())
			}
			if f.cancelled.Load() {
				t.Error("a redirect cancelled the call already running")
			}
			results := toolResultsInOrder(t, f.ag.Messages(), "first", "second")
			if results[0].IsError || !isNewMessageResult(results[1]) {
				t.Errorf("results = %q / %q, want first done and second withdrawn", messageText(results[0]), messageText(results[1]))
			}
			pr26AssertSteerAfterResults(t, (<-f.requests).Messages, "change direction", true)
		})
	}
}

// A message that arrives already marked behaves exactly like one marked later.
func TestRedirectMessageWithdrawsUnstartedCalls(t *testing.T) {
	f := newRedirectFixture(t, true)
	done := startAdmissionRun(t, f.ag)
	awaitBarrier(t, f.gate.entered, "first call")
	if err := f.ag.TrySteer(core.SteerItem{ID: "now", Text: "change direction", Redirect: true}); err != nil {
		t.Fatal(err)
	}
	f.gate.open()
	finishAdmissionRun(t, done, false)
	if f.second.Load() != 0 {
		t.Errorf("Execute(second)=%d, want 0", f.second.Load())
	}
	pr26AssertSteerAfterResults(t, (<-f.requests).Messages, "change direction", true)
}

// Marking the queue touches only instructions: automatic items, internal ones
// and command barriers keep their meaning and their position.
func TestRedirectQueuedMarksOnlyInstructions(t *testing.T) {
	ag := newTestAgent(NewMockProvider())
	items := []core.SteerItem{
		{ID: "notice", Text: "task notice", Custom: map[string]any{"source": "event"}},
		{ID: "internal", Text: "job finished", Internal: true, Custom: map[string]any{"source": "bash_job"}},
		{ID: "user", Text: "mine"},
		{ID: "owner", Text: "from the owner", Custom: map[string]any{"source": "owner"}},
		{ID: "voice", Text: "voice record", Custom: map[string]any{"source": "voice_call"}},
		{ID: "barrier", Command: "/compact"},
		{ID: "after", Text: "after the command"},
	}
	for _, it := range items {
		ag.steers.push(it)
	}
	if got := ag.RedirectQueued(); !eq(got, []string{"user", "owner", "after"}) {
		t.Errorf("RedirectQueued = %v, want [user owner after]", got)
	}
	marked := map[string]bool{}
	var order []string
	for _, it := range ag.steers.snapshot() {
		marked[it.ID] = it.Redirect
		order = append(order, it.ID)
	}
	if !eq(order, []string{"notice", "internal", "user", "owner", "voice", "barrier", "after"}) {
		t.Errorf("queue order changed: %v", order)
	}
	for id, want := range map[string]bool{"notice": false, "internal": false, "user": true, "owner": true, "voice": false, "barrier": false, "after": true} {
		if marked[id] != want {
			t.Errorf("%s Redirect=%v, want %v", id, marked[id], want)
		}
	}
	// The prefix before the barrier now withdraws; the barrier still stops it.
	claimed := ag.steers.claimWithdrawingPrefix()
	if got := ids(claimed); !eq(got, []string{"notice", "internal", "user", "owner", "voice"}) {
		t.Errorf("claimed = %v, want the whole prefix before the barrier", got)
	}
	ag.steers.settle(claimed)
	if got := ids(ag.steers.snapshot()); !eq(got, []string{"barrier", "after"}) {
		t.Errorf("left in queue = %v, want [barrier after]", got)
	}
}

func TestRedirectQueuedOnEmptyQueue(t *testing.T) {
	ag := newTestAgent(NewMockProvider())
	if got := ag.RedirectQueued(); len(got) != 0 {
		t.Errorf("RedirectQueued on an empty queue = %v", got)
	}
}

// RedirectQueued races the logical starts of a serial batch, once its first
// call runs. Whatever the interleaving, the executed calls are a prefix of the
// batch, every later call
// is withdrawn, nothing runs twice and the message is delivered exactly once,
// after every result.
func TestRedirectQueuedRacesLogicalStarts(t *testing.T) {
	const calls = 5 // the test agent caps a turn at five calls
	for iter := 0; iter < 40; iter++ {
		t.Run(fmt.Sprint(iter), func(t *testing.T) {
			var executed [calls]atomic.Int32
			var batch []core.Content
			var tools []core.Tool
			entered, queued := make(chan struct{}), make(chan struct{})
			for i := 0; i < calls; i++ {
				name := fmt.Sprintf("w%d", i)
				batch = append(batch, core.ToolCallContent(name, name, nil))
				i := i
				tools = append(tools, core.Tool{Name: name, Parameters: []byte(`{"type":"object"}`), Effect: core.EffectWritePath,
					LockKey: func(map[string]any) string { return "/same" },
					Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
						if i == 0 {
							close(entered)
							<-queued
						}
						executed[i].Add(1)
						return core.TextResult("ok"), nil
					}})
			}
			requests := make(chan core.Request, 1)
			provider := NewMockProvider(toolBatchResponse(batch...), func(req core.Request) (<-chan core.AssistantEvent, error) {
				requests <- req
				return simpleTextResponse("done")(req)
			})
			ag := newTestAgent(provider, tools...)
			done := startAdmissionRun(t, ag)
			awaitBarrier(t, entered, "first writer")
			if err := ag.TrySteer(core.SteerItem{ID: "note", Text: "redirect me"}); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				ag.RedirectQueued()
			}()
			close(queued)
			wg.Wait()
			finishAdmissionRun(t, done, false)
			results := toolResultsInOrder(t, ag.Messages(), func() []string {
				var ids []string
				for i := 0; i < calls; i++ {
					ids = append(ids, fmt.Sprintf("w%d", i))
				}
				return ids
			}()...)
			withdrawnSeen := false
			for i, r := range results {
				withdrawn := isNewMessageResult(r)
				n := executed[i].Load()
				switch {
				case withdrawn && n != 0:
					t.Errorf("w%d both withdrawn and executed", i)
				case !withdrawn && n != 1:
					t.Errorf("w%d executed %d times without being withdrawn: %q", i, n, messageText(r))
				case withdrawnSeen && !withdrawn:
					t.Errorf("w%d executed after an earlier call was withdrawn", i)
				}
				withdrawnSeen = withdrawnSeen || withdrawn
			}
			pr26AssertSteerAfterResults(t, (<-requests).Messages, "redirect me", true)
		})
	}
}
