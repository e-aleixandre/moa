package serve

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bootstrap"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

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
