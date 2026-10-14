package serve

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// This is an in-process script, not a model connection. It exercises both
// actual agents and the production subagent tool without any external API.
type reviewDelegateScript struct {
	mu       sync.Mutex
	requests []core.Request
	selected bool
}

func (p *reviewDelegateScript) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	role, text := lastText(req)
	if role == "user" && text == "REVIEW_ROOT_DELEGATE" {
		params := map[string]any{"task": "REVIEW_CHILD_SEND"}
		if p.selected {
			params["tools"] = []any{"tasks"}
		}
		return toolCallHandlerFor("review-parent-delegates", "subagent", params)(ctx, req)
	}
	if role == "user" && text == "REVIEW_CHILD_SEND" {
		return toolCallHandlerFor("review-child-sends", "tasks", map[string]any{
			"action": "send", "to": "Infra", "title": "Unauthorized child handoff",
		})(ctx, req)
	}
	return simpleResponse("review finished"), nil
}

func TestServedOwnerSubagentCannotSend(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		allowed  bool
		selected bool
	}{
		{"permission_inherit_all_tools", true, false},
		{"permission_select_tasks", true, true},
		{"no_permission_select_tasks", false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := &reviewDelegateScript{selected: scenario.selected}
			srv, mgr := newNoticeTestServer(t, p)
			from, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "moa"})
			if err != nil {
				t.Fatal(err)
			}
			to, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Infra"})
			if err != nil {
				t.Fatal(err)
			}
			if scenario.allowed {
				setCanSend(t, srv.URL, from, to.ID)
			}
			parent, _ := mgr.Get(from.SessionID)
			if parent.Kind != session.KindOwner {
				t.Fatal("fixture sender is not an owner session")
			}
			// This recipient can only run if the unauthorized child generated
			// a real notice through the normal shared outbox/dispatcher.
			closeSession(t, mgr, to.SessionID)
			started := make(chan bus.SubagentStarted, 2)
			unsub := parent.runtime.Bus.Subscribe(func(e bus.SubagentStarted) { started <- e })
			defer unsub()
			if _, _, _, err := mgr.Send(from.SessionID, "REVIEW_ROOT_DELEGATE", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			var child bus.SubagentStarted
			select {
			case child = <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("parent did not launch a subagent through its actual tool call")
			}
			if child.JobID == "" || child.JobID == from.SessionID {
				t.Fatal("no independently identified child")
			}
			pollUntil(t, 10*time.Second, "parent completed child", func() bool { return sessState(parent) == StateIdle })
			var sawParentCall, sawChildCall bool
			for _, m := range parent.History() {
				if m.Role == "tool_result" && m.ToolName == "subagent" {
					sawParentCall = true
					t.Logf("parent subagent result: %+v", m.Content)
				}
			}
			p.mu.Lock()
			for _, req := range p.requests {
				for _, m := range req.Messages {
					if m.Role == "tool_result" && m.ToolCallID == "review-child-sends" {
						sawChildCall = true
						t.Logf("actual child tasks result: is_error=%t content=%+v", m.IsError, m.Content)
					}
				}
			}
			p.mu.Unlock()
			if !sawParentCall || !sawChildCall {
				t.Fatal("fixture did not exercise both real agent/tool entry points")
			}
			list, err := mgr.tasks.List(context.Background(), tasks.Filter{IncludeAgents: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, rec := range list.Tasks {
				if rec.Handoff == nil {
					t.Fatal("fixture produced an unrelated task")
				}
				t.Logf("ACTUAL child=%s parent=%s task=%d place=%s state=%s requester=%s assignee=%s",
					child.JobID, from.SessionID, rec.ID, rec.Place, rec.Handoff.State, rec.RequesterSessionID, rec.AssigneeSessionID)
				if scenario.allowed {
					n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
					receiver, loaded := mgr.Get(to.SessionID)
					if !loaded || n.RecipientSessionID != to.SessionID || n.Deliver != tasks.DeliverWake {
						t.Fatalf("unexpected delivery: loaded=%t notice=%+v", loaded, n)
					}
					pollUntil(t, 10*time.Second, "recipient turn ran", func() bool { return hasAssistantAfter(receiver.History(), n.ID) })
					t.Logf("ACTUAL recipient woke and replied; notice=%s kind=%s state=%s deliver=%s", n.ID, n.Kind, n.State, n.Deliver)
				} else {
					ns, err := mgr.tasks.TaskNotices(context.Background(), rec.ID, 20)
					if err != nil || len(ns) != 0 {
						t.Fatalf("unexpected pending-request notices: %+v (%v)", ns, err)
					}
					t.Logf("ACTUAL unapproved request filed as owner, no recipient notice")
				}
			}
			if len(list.Tasks) != 0 {
				t.Fatalf("RED D10 served entry point: want 0 tasks from non-owner child, got %d (%s)", len(list.Tasks), fmt.Sprint(scenario))
			}
		})
	}
}
