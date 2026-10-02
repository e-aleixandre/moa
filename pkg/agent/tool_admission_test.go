package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// These tests pin a tool call's logical start: the boundary right before
// Execute. Stop and a deliverable user steer are decided against that
// boundary, not against tool_execution_start or permission approval. Every
// ordering below is forced through channel barriers; timeouts only bound a
// failure.

type admissionGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newAdmissionGate(t *testing.T) *admissionGate {
	t.Helper()
	g := &admissionGate{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *admissionGate) open() { g.once.Do(func() { close(g.release) }) }

func awaitBarrier(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not reach its barrier", label)
	}
}

func startAdmissionRun(t *testing.T, ag *Agent) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := ag.Run(context.Background(), "initial prompt")
		done <- err
	}()
	t.Cleanup(ag.Abort)
	return done
}

func finishAdmissionRun(t *testing.T, done <-chan error, cancelled bool) {
	t.Helper()
	select {
	case err := <-done:
		if cancelled && !errors.Is(err, context.Canceled) {
			t.Errorf("run error = %v, want cancellation", err)
		}
		if !cancelled && err != nil {
			t.Errorf("run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish after releasing its barriers")
	}
}

func toolBatchResponse(calls ...core.Content) func(core.Request) (<-chan core.AssistantEvent, error) {
	return func(core.Request) (<-chan core.AssistantEvent, error) {
		msg := core.Message{Role: "assistant", Content: calls, StopReason: "tool_use"}
		ch := make(chan core.AssistantEvent, 1)
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}
}

func toolResultsInOrder(t *testing.T, msgs []core.AgentMessage, ids ...string) []core.AgentMessage {
	t.Helper()
	var results []core.AgentMessage
	var got []string
	for _, msg := range msgs {
		if msg.Role == "tool_result" {
			results = append(results, msg)
			got = append(got, msg.ToolCallID)
		}
	}
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("result IDs/order = %v, want exactly %v", got, ids)
	}
	return results
}

func messageText(msg core.AgentMessage) string {
	var text string
	for _, c := range msg.Content {
		text += c.Text
	}
	return text
}

func providerCalls(p *MockProvider) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func isNewMessageResult(r core.AgentMessage) bool {
	// ErrorResult prefixes every error text with "Error: ".
	return r.IsError && strings.HasSuffix(messageText(r), "Tool call not executed: a new user message arrived.")
}

// The wait registration is the last preparation step before Execute. The
// registration seam parks the call there, after any earlier ctx check, and a
// Stop confirmed in that gap must still keep Execute from being entered.
func TestStopDuringWaitRegistrationNeverEntersExecute(t *testing.T) {
	for _, name := range []string{"bash_wait", "subagent_wait"} {
		t.Run(name, func(t *testing.T) {
			var executions atomic.Int32
			reg := core.NewRegistry()
			_ = reg.Register(core.Tool{
				Name: name, Parameters: []byte(`{"type":"object"}`),
				Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
					executions.Add(1)
					return core.TextResult("external effect despite Stop"), nil
				},
			})
			gate := newAdmissionGate(t)
			cfg := makeCfg(reg)
			cfg.steerMu = &sync.Mutex{}
			cfg.registerSteerWait = func(context.CancelCauseFunc, bool) func() {
				close(gate.entered)
				<-gate.release
				return func() {}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result  core.Result
				isError bool
			}
			done := make(chan outcome, 1)
			go func() {
				r, isErr := runTool(ctx, cfg, makeToolCall("pending", name, nil))
				done <- outcome{r, isErr}
			}()
			awaitBarrier(t, gate.entered, "wait registration")
			// Stop is confirmed under the same lock Agent.Abort holds.
			cfg.steerMu.Lock()
			cancel()
			cfg.steerMu.Unlock()
			gate.open()
			got := <-done
			if n := executions.Load(); n != 0 {
				t.Errorf("Execute entered %d time(s) after a confirmed Stop", n)
			}
			if !got.isError || !strings.Contains(got.result.Content[0].Text, "cancelled") {
				t.Errorf("result = %+v, want the cancellation error", got.result)
			}
		})
	}
}

// LockKey runs during scheduling, before logical start.
func TestStopDuringLockKeyNeverEntersExecute(t *testing.T) {
	gate := newAdmissionGate(t)
	var executions atomic.Int32
	provider := NewMockProvider(toolBatchResponse(core.ToolCallContent("pending", "writer", nil)))
	ag := newTestAgent(provider, core.Tool{
		Name: "writer", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectWritePath,
		LockKey: func(map[string]any) string {
			close(gate.entered)
			<-gate.release
			return "/fixture"
		},
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			executions.Add(1)
			return core.TextResult("effect"), nil
		},
	})
	done := startAdmissionRun(t, ag)
	awaitBarrier(t, gate.entered, "LockKey after approval")
	ag.Abort()
	gate.open()
	finishAdmissionRun(t, done, true)
	if executions.Load() != 0 {
		t.Error("tool entered Execute after Stop during LockKey")
	}
	if !toolResultsInOrder(t, ag.Messages(), "pending")[0].IsError {
		t.Error("missing synthetic cancellation result")
	}
}

// Both admission orders of Stop and a user steer while the batch has not
// started: Stop decides the calls, and the steer is never delivered by the
// stopped run.
func TestStopAndSteerBeforeLogicalStartInEitherOrder(t *testing.T) {
	for _, order := range []string{"steer_then_stop", "stop_then_steer"} {
		t.Run(order, func(t *testing.T) {
			gate := newAdmissionGate(t)
			var executions atomic.Int32
			provider := NewMockProvider(toolBatchResponse(
				core.ToolCallContent("first", "writer", nil),
				core.ToolCallContent("second", "writer", nil),
			))
			var lockCalls atomic.Int32
			ag := newTestAgent(provider, core.Tool{
				Name: "writer", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectWritePath,
				LockKey: func(map[string]any) string {
					if lockCalls.Add(1) == 1 {
						close(gate.entered)
						<-gate.release
					}
					return "/fixture"
				},
				Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
					executions.Add(1)
					return core.TextResult("effect"), nil
				},
			})
			done := startAdmissionRun(t, ag)
			awaitBarrier(t, gate.entered, "scheduling")
			steer := core.SteerItem{ID: "early", Text: "change direction"}
			if order == "steer_then_stop" {
				if err := ag.TrySteer(steer); err != nil {
					t.Fatal(err)
				}
				ag.Abort()
			} else {
				ag.Abort()
				if err := ag.TrySteer(steer); !errors.Is(err, ErrSteerAdmissionClosed) {
					t.Errorf("steer admitted after a confirmed Stop: %v", err)
				}
			}
			gate.open()
			finishAdmissionRun(t, done, true)
			if executions.Load() != 0 {
				t.Errorf("Execute entered %d time(s) after Stop", executions.Load())
			}
			for _, r := range toolResultsInOrder(t, ag.Messages(), "first", "second") {
				if !r.IsError || !strings.Contains(messageText(r), "cancelled") {
					t.Errorf("%s = %q, want the Stop cancellation result", r.ToolCallID, messageText(r))
				}
			}
			for _, m := range ag.Messages() {
				if m.Role == "user" && messageText(m) == "change direction" {
					t.Error("stopped run delivered the steer into history")
				}
			}
			// The stopped run's unwind discards whatever is still queued.
			if got := ids(ag.PendingSteers()); len(got) != 0 {
				t.Errorf("pending steers = %v, want none after the stopped unwind", got)
			}
			if providerCalls(provider) != 1 {
				t.Error("stopped run made another provider request")
			}
		})
	}
}

func TestToolStartedBeforeStopIsCancelledCooperatively(t *testing.T) {
	started := make(chan struct{})
	provider := NewMockProvider(toolBatchResponse(core.ToolCallContent("active", "external", nil)))
	ag := newTestAgent(provider, core.Tool{
		Name: "external", Parameters: []byte(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			close(started)
			<-ctx.Done()
			return core.Result{}, ctx.Err()
		},
	})
	done := startAdmissionRun(t, ag)
	awaitBarrier(t, started, "external tool")
	ag.Abort()
	finishAdmissionRun(t, done, true)
	if !toolResultsInOrder(t, ag.Messages(), "active")[0].IsError || providerCalls(provider) != 1 {
		t.Error("active cancelled call was not balanced without a continuation")
	}
}

// A non-cooperative tool keeps the run slot until it returns: its late result
// settles the old transcript, and no new Agent execution overlaps it.
func TestLateToolReturnKeepsRunSlotUntilJoined(t *testing.T) {
	gate := newAdmissionGate(t)
	provider := NewMockProvider(
		toolBatchResponse(core.ToolCallContent("old", "external", nil)),
		simpleTextResponse("fresh answer"),
	)
	ag := newTestAgent(provider, core.Tool{
		Name: "external", Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			close(gate.entered)
			<-gate.release
			return core.TextResult("old completed effect"), nil
		},
	})
	done := startAdmissionRun(t, ag)
	awaitBarrier(t, gate.entered, "non-cooperative tool")
	ag.Abort()
	before := ag.Messages()
	if _, err := ag.Send(context.Background(), "must not enter"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("new Agent run admitted before old tool joined: %v", err)
	}
	if !reflect.DeepEqual(ag.Messages(), before) {
		t.Error("rejected run mutated state")
	}
	gate.open()
	finishAdmissionRun(t, done, true)
	toolResultsInOrder(t, ag.Messages(), "old")
	if providerCalls(provider) != 1 {
		t.Fatal("late return started a provider continuation")
	}
	if _, err := ag.Send(context.Background(), "explicit new prompt"); err != nil {
		t.Fatal(err)
	}
	if providerCalls(provider) != 2 {
		t.Error("explicit new run must make exactly one fresh provider request")
	}
}

// Owner option A: approval is not logical start. Once a deliverable user
// steer wins, every unstarted call of the batch is withdrawn, including calls
// behind a shell barrier or a same-path writer; started calls finish.
func TestUserSteerWithdrawsUnstartedCallsOfTheBatch(t *testing.T) {
	for _, scenario := range []string{"steer_before_any_start", "tool_before_steer_shell", "tool_before_steer_same_path", "wait_behind_shell"} {
		t.Run(scenario, func(t *testing.T) {
			gate := newAdmissionGate(t)
			var firstExecutions, secondExecutions, approvals atomic.Int32
			var activeWasCancelled atomic.Bool
			secondName := "second"
			if scenario == "wait_behind_shell" {
				secondName = "bash_wait"
			}
			calls := []core.Content{
				core.ToolCallContent("first", "first", nil),
				core.ToolCallContent("second", secondName, nil),
			}
			provider := NewMockProvider(toolBatchResponse(calls...), func(req core.Request) (<-chan core.AssistantEvent, error) {
				wrapped := make([]core.AgentMessage, len(req.Messages))
				for i, msg := range req.Messages {
					wrapped[i] = core.WrapMessage(msg)
				}
				results := toolResultsInOrder(t, wrapped, "first", "second")
				for i, r := range results {
					withdrawn := i == 1 || scenario == "steer_before_any_start"
					if withdrawn != isNewMessageResult(r) {
						t.Errorf("%s = %q (error=%v), withdrawn=%v", r.ToolCallID, messageText(r), r.IsError, withdrawn)
					}
				}
				steerIndex, lastResult := -1, -1
				for i, m := range req.Messages {
					if m.Role == "tool_result" {
						lastResult = i
					}
					if m.Role == "user" && len(m.Content) > 0 && m.Content[0].Text == "change direction" {
						steerIndex = i
					}
				}
				if steerIndex <= lastResult {
					t.Errorf("next request must hold balanced results before the steer: steer=%d lastResult=%d", steerIndex, lastResult)
				}
				return simpleTextResponse("model decides anew")(req)
			})
			first := core.Tool{
				Name: "first", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
				Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
					firstExecutions.Add(1)
					if scenario != "steer_before_any_start" {
						close(gate.entered)
						<-gate.release
						activeWasCancelled.Store(ctx.Err() != nil)
					}
					return core.TextResult("first completed"), nil
				},
			}
			second := core.Tool{
				Name: secondName, Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
				Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
					secondExecutions.Add(1)
					return core.TextResult("stale second effect"), nil
				},
			}
			switch scenario {
			case "steer_before_any_start":
				first.Effect = core.EffectWritePath
				first.LockKey = func(map[string]any) string {
					close(gate.entered)
					<-gate.release
					return "/fixture"
				}
			case "tool_before_steer_same_path":
				first.Effect, second.Effect = core.EffectWritePath, core.EffectWritePath
				first.LockKey = func(map[string]any) string { return "/fixture" }
				second.LockKey = first.LockKey
			}
			ag := newTestAgent(provider, first, second)
			if err := ag.SetPermissionCheck(func(context.Context, string, map[string]any) *core.ToolCallDecision {
				approvals.Add(1)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			done := startAdmissionRun(t, ag)
			awaitBarrier(t, gate.entered, scenario)
			if approvals.Load() != 2 {
				t.Fatalf("fixture did not approve the entire batch: %d", approvals.Load())
			}
			if err := ag.TrySteer(core.SteerItem{ID: "early", Text: "change direction"}); err != nil {
				t.Fatal(err)
			}
			gate.open()
			finishAdmissionRun(t, done, false)
			wantFirst := int32(1)
			if scenario == "steer_before_any_start" {
				wantFirst = 0
			}
			if firstExecutions.Load() != wantFirst || secondExecutions.Load() != 0 {
				t.Errorf("Execute counts first=%d second=%d, want %d/0", firstExecutions.Load(), secondExecutions.Load(), wantFirst)
			}
			if activeWasCancelled.Load() {
				t.Error("steer cancelled a started non-wait tool")
			}
			toolResultsInOrder(t, ag.Messages(), "first", "second")
			if providerCalls(provider) != 2 {
				t.Errorf("provider calls=%d, want exactly one new turn after balanced results + steer", providerCalls(provider))
			}
			if len(ag.PendingSteers()) != 0 {
				t.Error("steer was not delivered by the run that withdrew the batch")
			}
		})
	}
}

func TestUserSteerLetsStartedParallelToolsFinish(t *testing.T) {
	first, second := newAdmissionGate(t), newAdmissionGate(t)
	var cancelled atomic.Bool
	toolFor := func(name string, g *admissionGate) core.Tool {
		return core.Tool{Name: name, Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
			Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
				close(g.entered)
				<-g.release
				if ctx.Err() != nil {
					cancelled.Store(true)
				}
				return core.TextResult(name + " complete"), nil
			},
		}
	}
	provider := NewMockProvider(toolBatchResponse(core.ToolCallContent("a", "a", nil), core.ToolCallContent("b", "b", nil)), simpleTextResponse("new turn"))
	ag := newTestAgent(provider, toolFor("a", first), toolFor("b", second))
	done := startAdmissionRun(t, ag)
	awaitBarrier(t, first.entered, "first parallel tool")
	awaitBarrier(t, second.entered, "second parallel tool")
	if err := ag.TrySteer(core.SteerItem{ID: "early", Text: "new instructions"}); err != nil {
		t.Fatal(err)
	}
	first.open()
	second.open()
	finishAdmissionRun(t, done, false)
	results := toolResultsInOrder(t, ag.Messages(), "a", "b")
	if cancelled.Load() || results[0].IsError || results[1].IsError {
		t.Error("steer must let both already-started parallel tools finish")
	}
}

// Internal messages, and user messages queued behind a command barrier, are
// not deliverable at the batch boundary, so they withdraw nothing.
func TestNonDeliverableSteerKeepsTheBatch(t *testing.T) {
	for _, scenario := range []string{"internal", "command_barrier_before_user"} {
		t.Run(scenario, func(t *testing.T) {
			gate := newAdmissionGate(t)
			var secondExecutions atomic.Int32
			provider := NewMockProvider(toolBatchResponse(core.ToolCallContent("a", "a", nil), core.ToolCallContent("b", "b", nil)), simpleTextResponse("finished"))
			ag := newTestAgent(provider,
				core.Tool{Name: "a", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
					Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
						close(gate.entered)
						<-gate.release
						return core.TextResult("a finished"), nil
					}},
				core.Tool{Name: "b", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
					Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
						secondExecutions.Add(1)
						return core.TextResult("b finished"), nil
					}},
			)
			done := startAdmissionRun(t, ag)
			awaitBarrier(t, gate.entered, "first shell tool")
			if scenario == "internal" {
				if err := ag.TrySteer(core.SteerItem{ID: "internal", Text: "completion notice", Internal: true}); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, it := range []core.SteerItem{{ID: "command", Command: "/compact"}, {ID: "user", Text: "after command"}} {
					if err := ag.TrySteer(it); err != nil {
						t.Fatal(err)
					}
				}
			}
			gate.open()
			finishAdmissionRun(t, done, false)
			if secondExecutions.Load() != 1 {
				t.Error("an internal steer or a user message behind a command withdrew the pending tool")
			}
			if scenario == "command_barrier_before_user" && !eq(ids(ag.PendingSteers()), []string{"command", "user"}) {
				t.Errorf("command barrier or trailing user changed FIFO position: %v", ids(ag.PendingSteers()))
			}
		})
	}
}

// A retained onUpdate is closed once its Execute returns, even while the run
// that owned it is still alive, and once that run is stopped.
func TestRetainedToolUpdateIsClosedAfterItsInvocation(t *testing.T) {
	for _, scenario := range []string{"after_return_same_run", "after_stop"} {
		t.Run(scenario, func(t *testing.T) {
			retained := make(chan func(core.Result), 1)
			holderStarted := newAdmissionGate(t)
			provider := NewMockProvider(
				toolBatchResponse(core.ToolCallContent("leaky", "leaky", nil), core.ToolCallContent("holder", "holder", nil)),
				simpleTextResponse("done"),
			)
			var leakyReturned atomic.Bool
			ag := newTestAgent(provider,
				core.Tool{Name: "leaky", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
					Execute: func(_ context.Context, _ map[string]any, update func(core.Result)) (core.Result, error) {
						update(core.TextResult("live progress"))
						retained <- update
						if scenario == "after_stop" {
							<-holderStarted.release
						}
						leakyReturned.Store(true)
						return core.TextResult("leaky done"), nil
					}},
				core.Tool{Name: "holder", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
					Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
						close(holderStarted.entered)
						<-holderStarted.release
						return core.TextResult("holder done"), nil
					}},
			)
			var mu sync.Mutex
			var updates []string
			leakyEnded := make(chan struct{})
			ag.Subscribe(func(e core.AgentEvent) {
				switch {
				case e.Type == core.AgentEventToolExecUpdate:
					mu.Lock()
					updates = append(updates, e.Result.Content[0].Text)
					mu.Unlock()
				case e.Type == core.AgentEventToolExecEnd && e.ToolCallID == "leaky":
					close(leakyEnded)
				}
			})
			done := startAdmissionRun(t, ag)
			update := <-retained
			awaitBarrier(t, holderStarted.entered, "holder tool")
			cancelled := scenario == "after_stop"
			if cancelled {
				ag.Abort()
			} else {
				// The end event is emitted after Execute returned.
				awaitBarrier(t, leakyEnded, "leaky tool end")
				if !leakyReturned.Load() {
					t.Fatal("end event preceded the return")
				}
				// Execute has returned, but its batch is still running.
				ag.mu.Lock()
				running := ag.cancel != nil
				ag.mu.Unlock()
				if !running {
					t.Fatal("fixture lost the run before the late callback")
				}
			}
			update(core.TextResult("stale progress"))
			holderStarted.open()
			finishAdmissionRun(t, done, cancelled)
			ag.Drain(2 * time.Second)
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(updates, []string{"live progress"}) {
				t.Errorf("published updates = %v, want only the live one", updates)
			}
		})
	}
}
