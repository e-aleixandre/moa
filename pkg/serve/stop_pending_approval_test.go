package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// Stop during a permission prompt must end the run without a model call and
// pair every tool call in the batch, including those never prompted yet.
func TestStopPendingApprovalDoesNotCallProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nextRequests := make(chan core.Request, 1)
	batch := func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 4)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("approve-1", "bash", map[string]any{"command": "touch one"}),
					core.ToolCallContent("approve-2", "bash", map[string]any{"command": "touch two"}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
	provider := newMockProvider(
		batch,
		func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			nextRequests <- req
			return simpleResponse("after the stop"), nil
		},
	)
	mgr := newFreshTestManager(t, ctx, provider)
	sess, err := mgr.CreateSession(CreateOpts{PermissionMode: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	shortOwnerQuiescence(t, 50*time.Millisecond)
	outcomes := make(chan runOutcome, 3)
	sess.ownerObserver = newOwnerReportObserver(sess, func(out runOutcome) { outcomes <- out })
	perms := make(chan bus.PermissionRequested, 2)
	ends := make(chan bus.RunEnded, 1)
	unsubPerm := sess.runtime.Bus.Subscribe(func(e bus.PermissionRequested) { perms <- e })
	unsubEnd := sess.runtime.Bus.Subscribe(func(e bus.RunEnded) { ends <- e })
	defer unsubPerm()
	defer unsubEnd()
	if _, _, _, err := mgr.Send(sess.ID, "run two commands", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-perms:
	case <-time.After(5 * time.Second):
		t.Fatal("approval never became pending")
	}
	if got := sessState(sess); got != StatePermission {
		t.Fatalf("pending approval state = %s, want permission", got)
	}
	select {
	case out := <-outcomes:
		if out.Status != callbackStatusNeedsInput {
			t.Fatalf("owner status while approving = %s, want needs_input", out.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not hear the approval")
	}
	before := provider.calls.Load()
	if err := mgr.Cancel(sess.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case ended := <-ends:
		if !ended.Cancelled || ended.Err != nil {
			t.Fatalf("run end = %+v, want cancelled without error", ended)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopped run did not end")
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	select {
	case out := <-outcomes:
		if out.Status != callbackStatusDone || out.Pending != nil {
			t.Fatalf("owner status after Stop = %+v, want done without pending input", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not hear the run ended")
	}
	if len(perms) != 0 {
		t.Fatal("second command was prompted after Stop")
	}
	if got := provider.calls.Load(); got != before {
		t.Fatalf("Stop called provider %d times, want 0", got-before)
	}
	info := sess.info()
	if info.State != StateIdle || info.PendingID != "" {
		t.Fatalf("after Stop: state=%s pending=%q", info.State, info.PendingID)
	}
	assertStoppedApprovalResults(t, sess.History())

	id := sess.ID
	mgr.mu.Lock()
	delete(mgr.sessions, id)
	mgr.mu.Unlock()
	sess.runtime.Close()
	resumed, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	assertStoppedApprovalResults(t, resumed.History())
	if resumed.info().State != StateIdle {
		t.Fatalf("state after restart = %s", resumed.info().State)
	}
	if _, _, _, err := mgr.Send(id, "next message", nil, "", ""); err != nil {
		t.Fatalf("next message after restart: %v", err)
	}
	pollUntil(t, 5*time.Second, "next message completed", func() bool {
		return sessState(resumed) == StateIdle && provider.calls.Load() == before+1
	})
	req := <-nextRequests
	paired := map[string]bool{}
	for _, msg := range req.Messages {
		if msg.Role == "tool_result" {
			paired[msg.ToolCallID] = true
		}
	}
	if !paired["approve-1"] || !paired["approve-2"] {
		t.Fatalf("next provider request has unpaired tool calls: %v", paired)
	}
}

func assertStoppedApprovalResults(t *testing.T, msgs []core.AgentMessage) {
	t.Helper()
	found := map[string]bool{}
	for _, msg := range msgs {
		if msg.Role != "tool_result" || len(msg.Content) == 0 {
			continue
		}
		if msg.ToolCallID == "approve-1" || msg.ToolCallID == "approve-2" {
			text := msg.Content[0].Text
			if !strings.Contains(text, "not executed") || !strings.Contains(text, "stopped the session before approving") {
				t.Fatalf("%s result not clear about the stop: %q", msg.ToolCallID, text)
			}
			found[msg.ToolCallID] = true
		}
	}
	if !found["approve-1"] || !found["approve-2"] {
		t.Fatalf("stopped approvals not paired in transcript: %v", found)
	}
}
