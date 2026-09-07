package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bootstrap"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestAskUserAsyncSubagentTimeoutKeepsRunState(t *testing.T) {
	childStarted := make(chan struct{})
	parentResumed := make(chan struct{})
	releaseParent := make(chan struct{})

	childHandler := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		close(childStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	parentFollowUp := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		close(parentResumed)
		select {
		case <-releaseParent:
			return simpleResponse("parent finished"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(
		toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
			"questions": []any{map[string]any{"question": "Continue?", "options": []any{"yes", "no"}}},
		}),
		childHandler,
		parentFollowUp,
	), t.TempDir(), core.MoaConfig{
		DisableSandbox:         true,
		AutoTitleModel:         "off",
		SessionBriefModel:      "off",
		SubagentMaxRunDuration: "25ms",
	})
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	asks := make(chan bus.AskUserRequested, 1)
	ends := make(chan bus.SubagentEnded, 1)
	unsubAsk := sess.runtime.Bus.Subscribe(func(event bus.AskUserRequested) { asks <- event })
	unsubEnd := sess.runtime.Bus.Subscribe(func(event bus.SubagentEnded) { ends <- event })
	t.Cleanup(unsubAsk)
	t.Cleanup(unsubEnd)

	if _, _, _, err := mgr.Send(sess.ID, "ask before continuing", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var ask bus.AskUserRequested
	select {
	case ask = <-asks:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ask_user")
	}
	if got := sess.runtime.State.Current(); got != bus.StatePermission {
		t.Fatalf("state with ask_user pending = %q, want %q", got, bus.StatePermission)
	}

	subagentTool, ok := sess.infra.toolReg.Get("subagent")
	if !ok {
		t.Fatal("subagent tool is not registered")
	}
	result, err := subagentTool.Execute(context.Background(), map[string]any{
		"task":  "expire while parent is asking",
		"async": true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := result.Custom["subagent_job_id"].(string)
	if jobID == "" {
		t.Fatal("subagent result has no job ID")
	}
	select {
	case <-childStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for subagent to start")
	}

	var ended bus.SubagentEnded
	select {
	case ended = <-ends:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for subagent timeout")
	}
	if ended.JobID != jobID || ended.Status != "failed" || !strings.Contains(ended.Error, "timed out after 25ms") {
		t.Fatalf("subagent end = %+v, want job %q failed by timeout", ended, jobID)
	}

	// Before the fix, the notification starts a second run from StatePermission.
	// Wait for that deterministically-created run to settle so the assertion below
	// observes the same false-idle/error state the client receives in production.
	if sess.runtime.Context().RunGenAtomic.Load() != ask.RunGen {
		pollUntil(t, 5*time.Second, "spurious notification run settlement", func() bool {
			return sess.runtime.State.Current() == bus.StateError
		})
	}
	if err := sess.runtime.Bus.Execute(bus.ResolveAskUser{
		SessionID: sess.ID,
		AskID:     ask.ID,
		Answers:   []string{"yes"},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-parentResumed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for parent to resume after ask_user")
	}
	if got := sess.runtime.State.Current(); got != bus.StateRunning {
		t.Errorf("state after ask_user response = %q, want %q while the agent is still executing", got, bus.StateRunning)
	}
	close(releaseParent)
}

func TestAskUserRootBashCompletionKeepsRunState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		timeout int
		status  string
	}{
		{name: "success", command: "printf root-bash-complete", status: "completed"},
		{name: "explicit timeout", command: "sleep 2", timeout: 1, status: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parentResumed := make(chan struct{}, 1)
			releaseParent := make(chan struct{})
			capturedRequests := make(chan core.Request, 2)
			provider := newMockProvider(
				toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
					"questions": []any{map[string]any{"question": "Continue?"}},
				}),
				func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
					select {
					case capturedRequests <- req:
					default:
					}
					select {
					case parentResumed <- struct{}{}:
					default:
					}
					select {
					case <-releaseParent:
						return simpleResponse("parent finished"), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
				func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
					select {
					case capturedRequests <- req:
					default:
					}
					return simpleResponse("steers handled"), nil
				},
			)
			mgr := newTestManagerWithConfig(t, context.Background(), provider, t.TempDir(), core.MoaConfig{
				DisableSandbox:    true,
				AutoTitleModel:    "off",
				SessionBriefModel: "off",
			})
			sess, err := mgr.CreateSession(CreateOpts{})
			if err != nil {
				t.Fatal(err)
			}

			asks := make(chan bus.AskUserRequested, 1)
			completed := make(chan bus.BashCompleted, 1)
			runs := make(chan bus.RunStarted, 2)
			errors := make(chan bus.AgentError, 1)
			ends := make(chan bus.RunEnded, 2)
			unsubAsk := sess.runtime.Bus.Subscribe(func(event bus.AskUserRequested) {
				select {
				case asks <- event:
				default:
				}
			})
			unsubCompleted := sess.runtime.Bus.Subscribe(func(event bus.BashCompleted) {
				select {
				case completed <- event:
				default:
				}
			})
			unsubRun := sess.runtime.Bus.Subscribe(func(event bus.RunStarted) {
				select {
				case runs <- event:
				default:
				}
			})
			unsubError := sess.runtime.Bus.Subscribe(func(event bus.AgentError) {
				select {
				case errors <- event:
				default:
				}
			})
			unsubEnd := sess.runtime.Bus.Subscribe(func(event bus.RunEnded) {
				select {
				case ends <- event:
				default:
				}
			})
			t.Cleanup(unsubAsk)
			t.Cleanup(unsubCompleted)
			t.Cleanup(unsubRun)
			t.Cleanup(unsubError)
			t.Cleanup(unsubEnd)

			if _, _, _, err := mgr.Send(sess.ID, "ask", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			var ask bus.AskUserRequested
			select {
			case ask = <-asks:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for ask_user")
			}
			if got := sess.runtime.State.Current(); got != bus.StatePermission {
				t.Fatalf("state with ask_user pending = %q, want %q", got, bus.StatePermission)
			}
			select {
			case <-runs: // Initial foreground run.
			default:
				t.Fatal("missing initial RunStarted")
			}

			bashTool, ok := sess.infra.toolReg.Get("bash")
			if !ok {
				t.Fatal("bash tool is not registered")
			}
			params := map[string]any{"command": tc.command, "async": true}
			if tc.timeout != 0 {
				params["timeout"] = tc.timeout
			}
			result, err := bashTool.Execute(context.Background(), params, nil)
			if err != nil || result.IsError {
				t.Fatalf("start background bash: result=%+v err=%v", result, err)
			}
			var completion bus.BashCompleted
			select {
			case completion = <-completed:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for root bash completion")
			}
			if completion.Status != tc.status {
				t.Fatalf("root bash completion status = %q, want %q", completion.Status, tc.status)
			}
			pollUntil(t, 5*time.Second, "root bash completion steer", func() bool {
				queueLen, err := bus.QueryTyped[bus.GetQueueLen, int](sess.runtime.Bus, bus.GetQueueLen{})
				return err == nil && queueLen == 1
			})
			if got := sess.runtime.State.Current(); got != bus.StatePermission {
				t.Fatalf("state after root bash completion = %q, want %q", got, bus.StatePermission)
			}
			if got := sess.runtime.Context().RunGenAtomic.Load(); got != ask.RunGen {
				t.Fatalf("run generation after root bash completion = %d, want %d", got, ask.RunGen)
			}
			select {
			case event := <-runs:
				t.Fatalf("unexpected notification run start: %+v", event)
			default:
			}
			select {
			case event := <-errors:
				t.Fatalf("unexpected agent error: %+v", event)
			default:
			}
			select {
			case event := <-ends:
				t.Fatalf("unexpected run end: %+v", event)
			default:
			}
			if got := provider.calls.Load(); got != 1 {
				t.Fatalf("provider calls before resolving ask_user = %d, want 1", got)
			}

			if err := sess.runtime.Bus.Execute(bus.ResolveAskUser{SessionID: sess.ID, AskID: ask.ID, Answers: []string{"yes"}}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-parentResumed:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for parent to resume after ask_user")
			}
			if got := sess.runtime.State.Current(); got != bus.StateRunning {
				t.Fatalf("state after ask_user response = %q, want %q", got, bus.StateRunning)
			}
			if action, _, _, err := mgr.Send(sess.ID, "follow up", nil, "", ""); err != nil || action != "steer" {
				t.Fatalf("follow-up send = action %q, err %v; want accepted steer", action, err)
			}
			close(releaseParent)
			pollUntil(t, 5*time.Second, "session to become idle", func() bool {
				return sess.runtime.State.Current() == bus.StateIdle
			})

			firstDeliveryOccurrences := 0
			for {
				select {
				case req := <-capturedRequests:
					occurrences := 0
					for _, message := range req.Messages {
						for _, content := range message.Content {
							occurrences += strings.Count(content.Text, completion.Text)
						}
					}
					if occurrences > 1 {
						t.Fatalf("BashCompleted.Text occurrences in one original run request = %d, want at most 1", occurrences)
					}
					if occurrences == 1 && firstDeliveryOccurrences == 0 {
						firstDeliveryOccurrences = occurrences
					}
				default:
					if firstDeliveryOccurrences != 1 {
						t.Fatalf("BashCompleted.Text occurrences in first original run request that received it = %d, want 1", firstDeliveryOccurrences)
					}
					return
				}
			}
		})
	}
}

func TestCancelledSubagentNotificationDoesNotRetainSteerFilter(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         string
		resultTail     string
		wantSteerEvent bool
	}{
		{
			name:           "cancelled notification is released",
			status:         "cancelled",
			wantSteerEvent: true,
		},
		{
			name:           "completed notification remains filtered",
			status:         "completed",
			resultTail:     "child result",
			wantSteerEvent: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parentStarted := make(chan struct{})
			releaseParent := make(chan struct{})
			childStarted := make(chan struct{})

			parentHandler := func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
				close(parentStarted)
				<-releaseParent
				return simpleResponse("parent result"), nil
			}
			childHandler := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
				close(childStarted)
				if tc.status == "cancelled" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return simpleResponse(tc.resultTail), nil
			}

			mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(
				parentHandler,
				childHandler,
				simpleResponseHandler("parent follow-up"),
			), t.TempDir(), core.MoaConfig{
				DisableSandbox:    true,
				AutoTitleModel:    "off",
				SessionBriefModel: "off",
			})
			sess, err := mgr.CreateSession(CreateOpts{})
			if err != nil {
				t.Fatal(err)
			}

			events := make(chan any, 128)
			unsubEvents := sess.runtime.Bus.SubscribeAll(func(event any) { events <- event })
			t.Cleanup(unsubEvents)
			subagentEnded := make(chan bus.SubagentEnded, 1)
			unsubEnd := sess.runtime.Bus.Subscribe(func(event bus.SubagentEnded) { subagentEnded <- event })
			t.Cleanup(unsubEnd)

			if _, _, _, err := mgr.Send(sess.ID, "hold the parent run", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			<-parentStarted

			subagentTool, ok := sess.infra.toolReg.Get("subagent")
			if !ok {
				t.Fatal("subagent tool is not registered")
			}
			result, err := subagentTool.Execute(context.Background(), map[string]any{
				"task":  "regression child",
				"async": true,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			jobID, _ := result.Custom["subagent_job_id"].(string)
			if jobID == "" {
				t.Fatalf("subagent job ID = %q", jobID)
			}
			<-childStarted

			if tc.status == "cancelled" && !sess.subagents.Cancel(jobID) {
				t.Fatal("cancelling subagent returned false")
			}
			ended := <-subagentEnded
			if ended.JobID != jobID || ended.Status != tc.status {
				t.Fatalf("subagent end = %+v, want job %q with status %q", ended, jobID, tc.status)
			}

			// Remove the completion steer before it can be delivered. This leaves
			// SteerFilter as the only observer of whether its text was retained.
			discarded := sess.runtime.Context().Agent.CancelSteer()
			text := bootstrap.FormatSubagentNotification(jobID, "regression child", tc.status, tc.resultTail, false)
			if tc.status == "completed" && (len(discarded) != 1 || discarded[0].Text != text) {
				t.Fatalf("completed notification steer = %+v, want %q", discarded, text)
			}
			if err := sess.runtime.Bus.Execute(bus.SteerAgent{ID: "steer-filter-probe", Text: text, Internal: true}); err != nil {
				t.Fatal(err)
			}
			close(releaseParent)

			gotSteerEvent := false
			for {
				event := <-events
				switch event := event.(type) {
				case bus.Steered:
					if event.Text == text {
						gotSteerEvent = true
					}
				case bus.RunEnded:
					if event.SessionID == sess.ID {
						if gotSteerEvent != tc.wantSteerEvent {
							t.Fatalf("SteerFilter(%q) emitted event = %t, want %t", text, gotSteerEvent, tc.wantSteerEvent)
						}
						return
					}
				}
			}
		})
	}
}
