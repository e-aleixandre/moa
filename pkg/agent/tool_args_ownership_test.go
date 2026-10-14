package agent

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
	"github.com/e-aleixandre/moa/pkg/permission"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func toolCallArgs(t *testing.T, msgs []core.AgentMessage, role string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range msgs {
		if m.Role != role {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "tool_call" {
				out = append(out, c.Arguments)
			}
		}
	}
	return out
}

// The assistant message is published (history, MessageEnd, provider replay)
// before its tool call runs. The real tasks tool normalizes its params map in
// place, so that map must not be the published one.
func TestToolArgsOwnershipTasksNormalizationVsMessageEnd(t *testing.T) {
	cases := []struct {
		name   string
		seed   bool
		args   func(id int64) map[string]any
		verify func(t *testing.T, view tasks.AgentView)
	}{
		{
			name: "create drops null optionals",
			args: func(int64) map[string]any {
				return map[string]any{
					"action": "create", "title": "race create", "when": nil, "target": nil,
					"subtasks": []any{map[string]any{"title": "child", "done": false}},
				}
			},
			verify: func(t *testing.T, view tasks.AgentView) {
				if len(view.Checklist) != 1 || view.Checklist[0].Title != "race create" ||
					len(view.Checklist[0].Subtasks) != 1 || view.Checklist[0].Subtasks[0].Title != "child" {
					t.Fatalf("task not created as asked: %+v", view.Checklist)
				}
			},
		},
		{
			name: "update drops empty when and own target",
			seed: true,
			args: func(id int64) map[string]any {
				return map[string]any{"action": "update", "id": float64(id), "title": "renamed", "when": "", "target": "this session"}
			},
			verify: func(t *testing.T, view tasks.AgentView) {
				if len(view.Checklist) != 1 || view.Checklist[0].Title != "renamed" {
					t.Fatalf("task not updated: %+v", view.Checklist)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := tasks.New(filepath.Join(t.TempDir(), tasks.DatabaseName))
			t.Cleanup(func() { _ = repo.Close() })
			scope := tasks.NewScope(repo, "race-session", "")
			ctx := context.Background()
			var id int64
			if tc.seed {
				seeded, err := repo.AgentCreate(ctx, scope.Actor(), tasks.AgentInput{Title: "seed"})
				if err != nil {
					t.Fatal(err)
				}
				id = seeded.ID
			}
			sent := tc.args(id)
			want := tc.args(id)

			readerReady := make(chan struct{})
			var once sync.Once
			var mu sync.Mutex
			var ended []core.AgentMessage

			real := tasks.NewTool(scope)
			wrapped := real
			var result core.Result
			wrapped.Execute = func(c context.Context, p map[string]any, u func(core.Result)) (core.Result, error) {
				select {
				case <-readerReady:
				case <-time.After(10 * time.Second):
					t.Error("MessageEnd reader never became ready")
				}
				r, err := real.Execute(c, p, u)
				mu.Lock()
				result = r
				mu.Unlock()
				return r, err
			}

			var replay []map[string]any
			provider := NewMockProvider(
				toolCallResponse("tc-tasks", "tasks", sent),
				func(req core.Request) (<-chan core.AssistantEvent, error) {
					replay = toolCallArgs(t, agentMessages(req.Messages), "assistant")
					return simpleTextResponse("done")(req)
				},
			)
			ag := newTestAgent(provider, wrapped)

			ag.Subscribe(func(e core.AgentEvent) {
				if e.Type != core.AgentEventMessageEnd || e.Message.Role != "assistant" {
					return
				}
				mu.Lock()
				ended = append(ended, e.Message)
				mu.Unlock()
				for _, m := range ag.Messages() {
					if m.Role != "assistant" || len(toolCallArgs(t, []core.AgentMessage{m}, "assistant")) == 0 {
						continue
					}
					once.Do(func() { close(readerReady) })
					core.EstimateOutputTokens(m.Message)
				}
			})

			ctxRun, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if _, err := ag.Send(ctxRun, "go"); err != nil {
				t.Fatal(err)
			}

			mu.Lock()
			defer mu.Unlock()
			if result.IsError {
				t.Fatalf("real tasks call failed: %+v", result.Content)
			}
			history := toolCallArgs(t, ag.Messages(), "assistant")
			if len(history) != 1 || !reflect.DeepEqual(history[0], want) {
				t.Fatalf("history args changed: %#v", history)
			}
			if got := toolCallArgs(t, ended, "assistant"); len(got) == 0 || !reflect.DeepEqual(got[0], want) {
				t.Fatalf("MessageEnd args changed: %#v", got)
			}
			if len(replay) != 1 || !reflect.DeepEqual(replay[0], want) {
				t.Fatalf("provider replay args changed: %#v", replay)
			}
			view, err := repo.AgentList(ctx, scope.Actor())
			if err != nil {
				t.Fatal(err)
			}
			tc.verify(t, view)
		})
	}
}

func agentMessages(in []core.Message) []core.AgentMessage {
	out := make([]core.AgentMessage, len(in))
	for i, m := range in {
		out[i] = core.WrapMessage(m)
	}
	return out
}

// Start events are retained by async consumers while the call is still being
// adjusted (hook) and executed: they must not alias the working args, nested
// values included, nor the published history.
func TestToolArgsOwnershipStartEventSnapshots(t *testing.T) {
	newArgs := func() map[string]any {
		return map[string]any{"items": []any{map[string]any{"title": "orig"}}, "drop": "x"}
	}
	var seen map[string]any
	probe := core.Tool{
		Name:       "probe",
		Parameters: []byte(`{"type":"object","properties":{"items":{"type":"array"},"drop":{"type":"string"}}}`),
		Execute: func(_ context.Context, p map[string]any, _ func(core.Result)) (core.Result, error) {
			seen = core.CloneArgs(p)
			return core.TextResult("ok"), nil
		},
	}
	ext := &testExtension{initFunc: func(api extension.API) error {
		api.OnToolCall(func(_ context.Context, _ string, args map[string]any) *core.ToolCallDecision {
			args["items"].([]any)[0].(map[string]any)["title"] = "adjusted"
			delete(args, "drop")
			return nil
		})
		return nil
	}}
	run := func(t *testing.T, calls ...map[string]any) (*Agent, []core.AgentEvent) {
		var handlers []func(core.Request) (<-chan core.AssistantEvent, error)
		handlers = append(handlers, func(req core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 4)
			var content []core.Content
			for i, a := range calls {
				content = append(content, core.ToolCallContent(string(rune('a'+i)), "probe", a))
			}
			msg := core.Message{Role: "assistant", Content: content, StopReason: "tool_use"}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			close(ch)
			return ch, nil
		}, simpleTextResponse("done"))
		reg := core.NewRegistry()
		_ = reg.Register(probe)
		ag, err := New(AgentConfig{
			Provider: NewMockProvider(handlers...), Model: core.Model{ID: "test"}, Tools: reg,
			Extensions: []extension.Extension{ext}, MaxTurns: 10, MaxToolCallsPerTurn: 1, MaxRunDuration: 30 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var starts []core.AgentEvent
		ag.Subscribe(func(e core.AgentEvent) {
			if e.Type == core.AgentEventToolExecStart {
				mu.Lock()
				starts = append(starts, e)
				mu.Unlock()
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := ag.Send(ctx, "go"); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return ag, append([]core.AgentEvent(nil), starts...)
	}

	t.Run("executed call", func(t *testing.T) {
		ag, starts := run(t, newArgs())
		if len(starts) != 1 {
			t.Fatalf("starts = %d", len(starts))
		}
		adjusted := newArgs()
		adjusted["items"].([]any)[0].(map[string]any)["title"] = "adjusted"
		delete(adjusted, "drop")
		if !reflect.DeepEqual(seen, adjusted) {
			t.Fatalf("Execute did not see the hook adjustments: %#v", seen)
		}
		if got := toolCallArgs(t, ag.Messages(), "assistant"); len(got) != 1 || !reflect.DeepEqual(got[0], newArgs()) {
			t.Fatalf("history args changed: %#v", got)
		}
		if !reflect.DeepEqual(starts[0].Args, newArgs()) {
			t.Fatalf("start args were adjusted in place: %#v", starts[0].Args)
		}
	})

	t.Run("rejected call", func(t *testing.T) {
		// MaxToolCallsPerTurn is 1: the second call is skipped, and its start is
		// emitted by the rejection path.
		ag, starts := run(t, newArgs(), newArgs())
		if len(starts) != 2 {
			t.Fatalf("starts = %d", len(starts))
		}
		starts[1].Args["items"].([]any)[0].(map[string]any)["title"] = "mutated by consumer"
		starts[1].Args["extra"] = true
		got := toolCallArgs(t, ag.Messages(), "assistant")
		if len(got) != 2 || !reflect.DeepEqual(got[1], newArgs()) {
			t.Fatalf("history shares state with the start event: %#v", got)
		}
	})
}

// The permission callback and the hook run on the call's private args: the
// feedback key is added and popped there, never in published args, and a
// permission request retained by its consumer is a snapshot.
func TestToolArgsOwnershipPermissionFeedbackStaysPrivate(t *testing.T) {
	var seen map[string]any
	probe := core.Tool{
		Name:       "probe",
		Parameters: []byte(`{"type":"object","properties":{"v":{"type":"object"}}}`),
		Execute: func(_ context.Context, p map[string]any, _ func(core.Result)) (core.Result, error) {
			seen = core.CloneArgs(p)
			return core.TextResult("ok"), nil
		},
	}
	gate := permission.New(permission.ModeAsk, permission.Config{})
	args := map[string]any{"v": map[string]any{"a": "orig"}}
	want := map[string]any{"v": map[string]any{"a": "orig"}}

	paused := make(chan struct{})
	release := make(chan struct{})
	var inCallback map[string]any
	ag := newTestAgent(NewMockProvider(toolCallResponse("tc", "probe", args), simpleTextResponse("done")), probe)
	_ = ag.SetPermissionCheck(func(ctx context.Context, name string, a map[string]any) *core.ToolCallDecision {
		d := gate.Check(ctx, name, a)
		inCallback = core.CloneArgs(a)
		close(paused)
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		return d
	})

	var requested permission.Request
	go func() {
		select {
		case requested = <-gate.Requests():
			requested.Response <- permission.Response{Approved: true, Feedback: "careful"}
		case <-time.After(10 * time.Second):
		}
	}()

	done := make(chan error, 1)
	var msgs []core.AgentMessage
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		var err error
		msgs, err = ag.Send(ctx, "go")
		done <- err
	}()

	select {
	case <-paused:
	case <-time.After(10 * time.Second):
		t.Fatal("permission check never ran")
	}
	if _, ok := inCallback["__permission_feedback"]; !ok {
		t.Fatalf("working args lack the feedback: %#v", inCallback)
	}
	if got := toolCallArgs(t, ag.Messages(), "assistant"); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("history args carry the feedback: %#v", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("Execute saw internal key or lost args: %#v", seen)
	}
	if !reflect.DeepEqual(requested.Args, want) {
		t.Fatalf("retained request args changed: %#v", requested.Args)
	}
	feedbackFound := false
	for _, msg := range msgs {
		if msg.Role == "tool_result" && strings.Contains(messageText(msg), "Permission feedback: careful") {
			feedbackFound = true
		}
	}
	if !feedbackFound {
		t.Fatal("tool result lost the permission feedback")
	}
}
