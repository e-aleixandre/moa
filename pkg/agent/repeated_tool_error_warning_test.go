package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
)

const repeatedToolErrorWarningText = "\n\nRepeated tool error.\nChange the arguments or use a different\napproach; the next identical tool-call\nbatch will stop this run."

func repeatedWarningCount(content []core.Content) int {
	count := 0
	for _, c := range content {
		count += strings.Count(c.Text, repeatedToolErrorWarningText)
	}
	return count
}

func assertRepeatedWarningIDs(t *testing.T, msgs []core.AgentMessage, wantIDs ...string) {
	t.Helper()
	want := make(map[string]bool, len(wantIDs))
	for _, id := range wantIDs {
		want[id] = true
	}
	seen := make(map[string]bool)
	for _, msg := range msgs {
		count := repeatedWarningCount(msg.Content)
		if want[msg.ToolCallID] && msg.Role == "tool_result" {
			seen[msg.ToolCallID] = true
			if count != 1 {
				t.Errorf("%s warning count=%d, want exactly 1", msg.ToolCallID, count)
			}
		} else if count != 0 {
			t.Errorf("unexpected repeated-error warning in role=%s id=%s", msg.Role, msg.ToolCallID)
		}
	}
	for _, id := range wantIDs {
		if !seen[id] {
			t.Errorf("missing warned tool result %s", id)
		}
	}
}

func assertRepeatedWarningRequest(t *testing.T, req core.Request, wantIDs ...string) {
	t.Helper()
	msgs := make([]core.AgentMessage, len(req.Messages))
	for i, msg := range req.Messages {
		msgs[i] = core.WrapMessage(msg)
	}
	assertRepeatedWarningIDs(t, msgs, wantIDs...)
}

func assertRepeatedWarningContent(t *testing.T, msg core.AgentMessage, original []core.Content, warned bool) {
	t.Helper()
	want := core.CloneContent(original)
	if warned {
		want = append(want, core.TextContent(repeatedToolErrorWarningText))
	}
	if !reflect.DeepEqual(msg.Content, want) {
		t.Errorf("%s content=%+v, want original content plus warning=%v", msg.ToolCallID, msg.Content, warned)
	}
}

func assertRepeatedWarningEndEvents(t *testing.T, ag *Agent, events *eventCollector, wantIDs ...string) {
	t.Helper()
	ag.Drain(2 * time.Second)
	results := make(map[string]core.AgentMessage)
	for _, msg := range ag.Messages() {
		if msg.Role == "tool_result" {
			results[msg.ToolCallID] = msg
		}
	}
	want := make(map[string]bool, len(wantIDs))
	for _, id := range wantIDs {
		want[id] = true
	}
	seen := make(map[string]bool)
	for _, e := range events.snapshot() {
		if e.Type != core.AgentEventToolExecEnd {
			continue
		}
		if !want[e.ToolCallID] || seen[e.ToolCallID] {
			t.Errorf("unexpected/duplicate tool end event %s", e.ToolCallID)
		}
		seen[e.ToolCallID] = true
		msg, ok := results[e.ToolCallID]
		if !ok || e.Result == nil {
			t.Errorf("tool end %s has no matching final result", e.ToolCallID)
			continue
		}
		if e.ToolName != msg.ToolName || e.IsError != msg.IsError || e.Result.IsError != msg.IsError || !reflect.DeepEqual(e.Result.Content, msg.Content) {
			t.Errorf("tool end %s disagrees with transcript: event=%+v result=%+v message=%+v", e.ToolCallID, e, e.Result, msg)
		}
		var custom map[string]any
		if e.Result.Custom != nil || e.Rejected {
			custom = make(map[string]any)
			for key, value := range e.Result.Custom {
				custom[key] = value
			}
			if e.Rejected {
				custom["rejected"] = true
			}
		}
		if !reflect.DeepEqual(msg.Custom, custom) {
			t.Errorf("tool end %s custom/rejected flags disagree: event=%+v transcript=%+v", e.ToolCallID, custom, msg.Custom)
		}
	}
	for _, id := range wantIDs {
		if !seen[id] {
			t.Errorf("missing tool end event %s", id)
		}
	}
}

func TestLoop_RepeatedToolErrorWarning_SecondResultThirdStops(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	for _, tc := range []struct {
		name       string
		args       map[string]any
		returned   bool
		validation bool
	}{
		{name: "structured_error", args: map[string]any{"path": "missing"}},
		{name: "returned_error", args: map[string]any{"path": "missing"}, returned: true},
		{name: "invalid_args_nil", validation: true},
		{name: "invalid_args_empty", args: map[string]any{}, validation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var executions atomic.Int32
			handlers := make([]func(core.Request) (<-chan core.AssistantEvent, error), 3)
			for i := range handlers {
				handlers[i] = toolBatchResponse(core.ToolCallContent(fmt.Sprintf("call-%d", i+1), "read", tc.args))
			}
			third := handlers[2]
			handlers[2] = func(req core.Request) (<-chan core.AssistantEvent, error) {
				assertRepeatedWarningRequest(t, req, "call-2")
				return third(req)
			}
			provider := NewMockProvider(handlers...)
			ag := newTestAgent(provider, core.Tool{
				Name: "read", Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`), Effect: core.EffectReadOnly,
				Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
					executions.Add(1)
					if tc.returned {
						return core.Result{}, errors.New("missing file")
					}
					return core.ErrorResult("missing file"), nil
				},
			})
			events := collectEvents(ag)
			_, err := ag.Run(context.Background(), "read the file")
			if !errors.Is(err, ErrDoomLoop) {
				t.Fatalf("run error=%v, want doom-loop stop on third batch", err)
			}
			wantExecutions := int32(2)
			original := core.ErrorResult("missing file")
			if tc.validation {
				wantExecutions = 0
				original = core.ErrorResult("Parameter validation error: missing required parameter: path")
			}
			if executions.Load() != wantExecutions || providerCalls(provider) != 3 {
				t.Errorf("executions=%d provider calls=%d, want %d/3", executions.Load(), providerCalls(provider), wantExecutions)
			}
			results := toolResultsInOrder(t, ag.Messages(), "call-1", "call-2", "call-3")
			assertRepeatedWarningIDs(t, ag.Messages(), "call-2")
			for i, msg := range results[:2] {
				assertRepeatedWarningContent(t, msg, original.Content, i == 1)
				if !msg.IsError || msg.Custom["rejected"] == true {
					t.Errorf("%s lost error/non-permission classification: %+v", msg.ToolCallID, msg)
				}
			}
			if !results[2].IsError || !strings.Contains(messageText(results[2]), "doom loop detected") {
				t.Errorf("third unexecuted call lacks its original doom-loop result: %+v", results[2])
			}
			assertRepeatedWarningEndEvents(t, ag, events, "call-1", "call-2")
		})
	}
}

func TestLoop_RepeatedToolErrorWarning_FinalResultOnePerBatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	for _, scenario := range []string{"executed_errors", "mixed_validation_and_execution"} {
		t.Run(scenario, func(t *testing.T) {
			names := []string{"repaired", "failed-a", "failed-b"}
			if scenario == "mixed_validation_and_execution" {
				names = append(names, "invalid")
			}
			finalError := core.Result{
				Content: []core.Content{
					core.TextContent("Error: final hook diagnostic"),
					core.ImageContent("Zm9v", "image/png"),
					{Type: "text", Text: "Permission feedback: keep scope", TextSignature: "opaque-signature"},
				},
				IsError: true,
				Custom:  map[string]any{"subagent_job_id": "sa-fixture", "fixture_flag": true},
			}
			originalContent := core.CloneContent(finalError.Content)
			var executions, hookCalls atomic.Int32
			reg := core.NewRegistry()
			for _, name := range names {
				schema := []byte(`{"type":"object"}`)
				if name == "invalid" {
					schema = []byte(`{"type":"object","required":["count"]}`)
				}
				if err := reg.Register(core.Tool{
					Name: name, Parameters: schema, Effect: core.EffectReadOnly,
					Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
						executions.Add(1)
						return core.ErrorResult("raw diagnostic"), nil
					},
				}); err != nil {
					t.Fatal(err)
				}
			}
			handlers := make([]func(core.Request) (<-chan core.AssistantEvent, error), 3)
			var resultIDs []string
			for i := range handlers {
				var calls []core.Content
				for _, name := range names {
					id := fmt.Sprintf("batch-%d-%s", i+1, name)
					resultIDs = append(resultIDs, id)
					calls = append(calls, core.ToolCallContent(id, name, map[string]any{"path": "missing"}))
				}
				handlers[i] = toolBatchResponse(calls...)
			}
			third := handlers[2]
			handlers[2] = func(req core.Request) (<-chan core.AssistantEvent, error) {
				count := 0
				for _, msg := range req.Messages {
					count += repeatedWarningCount(msg.Content)
				}
				if count != 1 {
					t.Errorf("next provider request has %d warnings, want exactly 1", count)
				}
				return third(req)
			}
			provider := NewMockProvider(handlers...)
			ag, err := New(AgentConfig{
				Provider: provider, Model: core.Model{ID: "test"}, Tools: reg, MaxTurns: 10, MaxToolCallsPerTurn: 5,
				PermissionCheck: func(_ context.Context, _ string, args map[string]any) *core.ToolCallDecision {
					args["__permission_feedback"] = "keep scope"
					return nil
				},
				Extensions: []extension.Extension{&testExtension{initFunc: func(api extension.API) error {
					api.OnToolResult(func(_ context.Context, name string, result core.Result, isError bool) (*core.Result, error) {
						hookCalls.Add(1)
						if repeatedWarningCount(result.Content) != 0 || !isError || !strings.Contains(messageText(core.WrapMessage(core.Message{Content: result.Content})), "Permission feedback: keep scope") {
							t.Errorf("hook must receive original error and approval feedback, not the warning: %+v", result)
						}
						if name == "repaired" {
							repaired := core.TextResult("hook repaired the error")
							return &repaired, nil
						}
						return &finalError, nil
					})
					return nil
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			events := collectEvents(ag)
			_, err = ag.Run(context.Background(), "try the batch")
			if !errors.Is(err, ErrDoomLoop) || executions.Load() != 6 || hookCalls.Load() != 6 {
				t.Fatalf("error=%v executions=%d hooks=%d, want doom loop/6/6", err, executions.Load(), hookCalls.Load())
			}
			toolResultsInOrder(t, ag.Messages(), resultIDs...)
			var endIDs []string
			count := 0
			for _, msg := range ag.Messages() {
				if msg.Role != "tool_result" {
					continue
				}
				count += repeatedWarningCount(msg.Content)
				if strings.HasPrefix(msg.ToolCallID, "batch-3-") {
					continue
				}
				endIDs = append(endIDs, msg.ToolCallID)
				warned := repeatedWarningCount(msg.Content) != 0
				if msg.ToolName == "repaired" {
					assertRepeatedWarningContent(t, msg, core.TextResult("hook repaired the error").Content, false)
					if msg.IsError {
						t.Error("final hook success still marked as an error")
					}
					continue
				}
				if warned && !strings.HasPrefix(msg.ToolCallID, "batch-2-") {
					t.Errorf("warning placed before the second batch: %s", msg.ToolCallID)
				}
				if msg.ToolName == "invalid" {
					assertRepeatedWarningContent(t, msg, core.ErrorResult("Parameter validation error: missing required parameter: count").Content, warned)
					if !msg.IsError || msg.Custom["rejected"] == true {
						t.Errorf("validation result lost non-permission error classification: %+v", msg)
					}
					continue
				}
				assertRepeatedWarningContent(t, msg, originalContent, warned)
				if !msg.IsError || !reflect.DeepEqual(msg.Custom, finalError.Custom) {
					t.Errorf("final result lost flags/custom metadata: %+v", msg)
				}
			}
			if count != 1 {
				t.Errorf("batch warning count=%d, want exactly 1 across eligible final errors", count)
			}
			if !reflect.DeepEqual(finalError.Content, originalContent) {
				t.Error("warning mutated the hook's original content")
			}
			assertRepeatedWarningEndEvents(t, ag, events, endIDs...)
		})
	}
}

func TestLoop_RepeatedToolErrorWarning_NoWarn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	type noWarnCase struct {
		name     string
		toolName string
		result   core.Result
		decision *core.ToolCallDecision
		hook     bool
		exempt   bool
		skipped  bool
	}
	cases := []noWarnCase{
		{name: "polling_success", toolName: "poll", result: core.TextResult("still running")},
		{name: "bash_exit_code_text", toolName: "bash", result: core.TextResult("Exit code: 1\nexpected RED test failure")},
		{name: "denied_default_kind", toolName: "read", decision: &core.ToolCallDecision{Block: true, Reason: "not allowed"}},
		{name: "denied_permission_kind", toolName: "read", decision: &core.ToolCallDecision{Block: true, Reason: "not allowed", Kind: core.ToolCallDecisionKindPermission}},
		{name: "policy_blocked", toolName: "read", decision: &core.ToolCallDecision{Block: true, Reason: "not allowed", Kind: core.ToolCallDecisionKindPolicy}},
		{name: "hook_blocked", toolName: "read", decision: &core.ToolCallDecision{Block: true, Reason: "not allowed"}, hook: true},
		{name: "max_calls_skipped", toolName: "read", skipped: true},
	}
	for _, name := range []string{"bash_status", "subagent_status", "bash_wait", "subagent_wait"} {
		cases = append(cases, noWarnCase{name: name + "_structured_error", toolName: name, result: core.ErrorResult("job not found"), exempt: true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var executions atomic.Int32
			reg := core.NewRegistry()
			schema := []byte(`{"type":"object"}`)
			if tc.decision != nil || tc.skipped {
				// Admission rejections win even if the arguments also fail validation.
				schema = []byte(`{"type":"object","required":["path"]}`)
			}
			if err := reg.Register(core.Tool{Name: tc.toolName, Parameters: schema, Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
				executions.Add(1)
				return tc.result, nil
			}}); err != nil {
				t.Fatal(err)
			}
			if tc.skipped {
				if err := reg.Register(readOnlyTool("allowed")); err != nil {
					t.Fatal(err)
				}
			}
			var handlers []func(core.Request) (<-chan core.AssistantEvent, error)
			var resultIDs []string
			for i := range 3 {
				id := fmt.Sprintf("call-%d", i+1)
				calls := []core.Content{core.ToolCallContent(id, tc.toolName, map[string]any{})}
				if tc.skipped {
					allowedID := fmt.Sprintf("allowed-%d", i+1)
					calls = append([]core.Content{core.ToolCallContent(allowedID, "allowed", nil)}, calls...)
					resultIDs = append(resultIDs, allowedID)
				}
				resultIDs = append(resultIDs, id)
				handlers = append(handlers, toolBatchResponse(calls...))
			}
			third := handlers[2]
			handlers[2] = func(req core.Request) (<-chan core.AssistantEvent, error) {
				assertRepeatedWarningRequest(t, req)
				return third(req)
			}
			handlers = append(handlers, func(req core.Request) (<-chan core.AssistantEvent, error) {
				assertRepeatedWarningRequest(t, req)
				return simpleTextResponse("done")(req)
			})
			provider := NewMockProvider(handlers...)
			cfg := AgentConfig{Provider: provider, Model: core.Model{ID: "test"}, Tools: reg, MaxTurns: 10}
			if tc.skipped {
				cfg.MaxToolCallsPerTurn = 1
			}
			if tc.hook {
				cfg.Extensions = []extension.Extension{&testExtension{initFunc: func(api extension.API) error {
					api.OnToolCall(func(context.Context, string, map[string]any) *core.ToolCallDecision { return tc.decision })
					return nil
				}}}
			} else if tc.decision != nil {
				cfg.PermissionCheck = func(context.Context, string, map[string]any) *core.ToolCallDecision { return tc.decision }
			}
			ag, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			events := collectEvents(ag)
			_, err = ag.Run(context.Background(), "repeat the call")
			wantExecutions, wantRequests := int32(2), 3
			endIDs := []string{"call-1", "call-2"}
			if tc.exempt {
				wantExecutions, wantRequests = 3, 4
				endIDs = append(endIDs, "call-3")
				if err != nil {
					t.Errorf("exempt polling failed: %v", err)
				}
			} else if !errors.Is(err, ErrDoomLoop) {
				t.Errorf("non-exempt repetition must still stop: %v", err)
			}
			if tc.decision != nil || tc.skipped {
				wantExecutions = 0
			}
			if tc.skipped {
				endIDs = []string{"allowed-1", "call-1", "allowed-2", "call-2"}
			}
			if executions.Load() != wantExecutions || providerCalls(provider) != wantRequests {
				t.Errorf("executions=%d provider calls=%d, want %d/%d", executions.Load(), providerCalls(provider), wantExecutions, wantRequests)
			}
			results := toolResultsInOrder(t, ag.Messages(), resultIDs...)
			assertRepeatedWarningIDs(t, ag.Messages())
			for _, msg := range results[:len(endIDs)] {
				if msg.ToolName != tc.toolName {
					continue
				}
				wantRejected := tc.decision != nil && !tc.hook && tc.decision.Kind != core.ToolCallDecisionKindPolicy
				if (msg.Custom["rejected"] == true) != wantRejected {
					t.Errorf("%s permission classification changed: %+v", msg.ToolCallID, msg.Custom)
				}
				original := tc.result
				if tc.skipped {
					original = core.ErrorResult("Tool call skipped: max tool calls per turn exceeded")
				} else if tc.decision != nil {
					prefix := "Tool call blocked: "
					if wantRejected {
						prefix = "Permission denied: "
					}
					original = core.ErrorResult(prefix + tc.decision.Reason)
				}
				assertRepeatedWarningContent(t, msg, original.Content, false)
				if msg.IsError != original.IsError {
					t.Errorf("%s lost its original error flag", msg.ToolCallID)
				}
			}
			assertRepeatedWarningEndEvents(t, ag, events, endIDs...)
		})
	}
}

func TestLoop_RepeatedToolErrorWarning_ExemptTurnsTransparent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	var failures, polls atomic.Int32
	fail := func(id string) core.Content {
		return core.ToolCallContent(id, "read", map[string]any{"path": "missing"})
	}
	poll := func(id, name string) core.Content {
		return core.ToolCallContent(id, name, map[string]any{"job_id": "job"})
	}
	provider := NewMockProvider(
		toolBatchResponse(fail("failure-1")),
		toolBatchResponse(poll("poll-1", "bash_status")),
		toolBatchResponse(poll("poll-2", "subagent_status"), fail("failure-2"), poll("poll-3", "bash_wait")),
		toolBatchResponse(poll("poll-4", "subagent_wait")),
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			assertRepeatedWarningRequest(t, req, "failure-2")
			return toolBatchResponse(fail("failure-3"))(req)
		},
	)
	tools := []core.Tool{{Name: "read", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		failures.Add(1)
		return core.ErrorResult("missing file"), nil
	}}}
	for _, name := range []string{"bash_status", "subagent_status", "bash_wait", "subagent_wait"} {
		tools = append(tools, core.Tool{Name: name, Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
			Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
				polls.Add(1)
				return core.ErrorResult("job not found"), nil
			},
		})
	}
	ag := newTestAgent(provider, tools...)
	events := collectEvents(ag)
	_, err := ag.Run(context.Background(), "retry across polls")
	if !errors.Is(err, ErrDoomLoop) || failures.Load() != 2 || polls.Load() != 4 || providerCalls(provider) != 5 {
		t.Fatalf("error=%v failures=%d polls=%d requests=%d, want doom loop/2/4/5", err, failures.Load(), polls.Load(), providerCalls(provider))
	}
	toolResultsInOrder(t, ag.Messages(), "failure-1", "poll-1", "poll-2", "failure-2", "poll-3", "poll-4", "failure-3")
	assertRepeatedWarningIDs(t, ag.Messages(), "failure-2")
	assertRepeatedWarningEndEvents(t, ag, events, "failure-1", "poll-1", "poll-2", "failure-2", "poll-3", "poll-4")
}

func TestLoop_RepeatedToolErrorWarning_ArgsChangeResets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	var executions atomic.Int32
	var handlers []func(core.Request) (<-chan core.AssistantEvent, error)
	for i, path := range []string{"a", "a", "b", "b", "a", "a", "a"} {
		handlers = append(handlers, toolBatchResponse(core.ToolCallContent(fmt.Sprintf("call-%d", i+1), "read", map[string]any{"path": path})))
	}
	last := handlers[6]
	handlers[6] = func(req core.Request) (<-chan core.AssistantEvent, error) {
		assertRepeatedWarningRequest(t, req, "call-2", "call-4", "call-6")
		return last(req)
	}
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider, core.Tool{Name: "read", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		executions.Add(1)
		return core.ErrorResult("missing file"), nil
	}})
	events := collectEvents(ag)
	_, err := ag.Run(context.Background(), "change the path")
	if !errors.Is(err, ErrDoomLoop) || executions.Load() != 6 || providerCalls(provider) != 7 {
		t.Fatalf("error=%v executions=%d requests=%d, want doom loop/6/7", err, executions.Load(), providerCalls(provider))
	}
	results := toolResultsInOrder(t, ag.Messages(), "call-1", "call-2", "call-3", "call-4", "call-5", "call-6", "call-7")
	assertRepeatedWarningIDs(t, ag.Messages(), "call-2", "call-4", "call-6")
	for i, msg := range results[:6] {
		assertRepeatedWarningContent(t, msg, core.ErrorResult("missing file").Content, i%2 == 1)
	}
	assertRepeatedWarningEndEvents(t, ag, events, "call-1", "call-2", "call-3", "call-4", "call-5", "call-6")
}

func TestLoop_RepeatedToolErrorWarning_StopOrSteerBeforeStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	for _, action := range []string{"stop", "steer"} {
		t.Run(action, func(t *testing.T) {
			gate := newAdmissionGate(t)
			var executions, approvals, lockCalls atomic.Int32
			batch := func(prefix string) func(core.Request) (<-chan core.AssistantEvent, error) {
				return toolBatchResponse(
					core.ToolCallContent(prefix+"-a", "writer", map[string]any{"path": "a"}),
					core.ToolCallContent(prefix+"-b", "writer", map[string]any{"path": "b"}),
				)
			}
			provider := NewMockProvider(batch("first"), batch("second"), func(req core.Request) (<-chan core.AssistantEvent, error) {
				assertRepeatedWarningRequest(t, req)
				lastResult, steerIndex := -1, -1
				for i, msg := range req.Messages {
					if msg.Role == "tool_result" {
						lastResult = i
					}
					if msg.Role == "user" && len(msg.Content) > 0 && msg.Content[0].Text == "change direction" {
						steerIndex = i
					}
				}
				if steerIndex <= lastResult {
					t.Errorf("steer must follow balanced tool results: steer=%d last result=%d", steerIndex, lastResult)
				}
				return simpleTextResponse("model decides anew")(req)
			})
			ag := newTestAgent(provider, core.Tool{
				Name: "writer", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectWritePath,
				LockKey: func(map[string]any) string {
					if lockCalls.Add(1) == 3 {
						close(gate.entered)
						<-gate.release
					}
					return "/fixture"
				},
				Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
					executions.Add(1)
					return core.ErrorResult("original tool failure"), nil
				},
			})
			if err := ag.SetPermissionCheck(func(context.Context, string, map[string]any) *core.ToolCallDecision {
				approvals.Add(1)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			events := collectEvents(ag)
			done := startAdmissionRun(t, ag)
			awaitBarrier(t, gate.entered, "second identical batch approved, before logical start")
			if approvals.Load() != 4 || executions.Load() != 2 {
				t.Fatalf("fixture approvals=%d executions=%d, want 4/2 before Stop/steer", approvals.Load(), executions.Load())
			}
			if action == "stop" {
				ag.Abort()
			} else if err := ag.TrySteer(core.SteerItem{ID: "new-direction", Text: "change direction"}); err != nil {
				t.Fatal(err)
			}
			gate.open()
			finishAdmissionRun(t, done, action == "stop")
			results := toolResultsInOrder(t, ag.Messages(), "first-a", "first-b", "second-a", "second-b")
			assertRepeatedWarningIDs(t, ag.Messages())
			for _, msg := range results[2:] {
				original := core.ErrorResult("Tool result unavailable: the run was cancelled before a result was recorded.")
				if action == "steer" {
					original = core.ErrorResult("Tool call not executed: a new user message arrived.")
					if !isNewMessageResult(msg) {
						t.Errorf("withdrawal result changed: %+v", msg)
					}
				} else if !msg.IsError || !strings.Contains(messageText(msg), "cancelled") {
					t.Errorf("pending call lost Stop result: %+v", msg)
				}
				assertRepeatedWarningContent(t, msg, original.Content, false)
			}
			wantRequests := 3
			if action == "stop" {
				wantRequests = 2
			}
			if executions.Load() != 2 || providerCalls(provider) != wantRequests || len(ag.PendingSteers()) != 0 {
				t.Errorf("pending execution/steer changed: executions=%d requests=%d queued=%d", executions.Load(), providerCalls(provider), len(ag.PendingSteers()))
			}
			assertRepeatedWarningEndEvents(t, ag, events, "first-a", "first-b", "second-a", "second-b")
		})
	}
}
