package serve

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// The script drives the real parent -> subagent -> inherited tasks tool path;
// task storage, scope bootstrap and child context tagging are production code.
type reviewAuthorityDelegateScript struct {
	id       int64
	selected bool
	mu       sync.Mutex
	results  []core.Message
}

func (p *reviewAuthorityDelegateScript) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	for _, m := range req.Messages {
		if m.Role == "tool_result" && m.ToolCallID == "review-authority-child-withdraw" {
			p.results = append(p.results, m)
		}
	}
	p.mu.Unlock()
	role, text := lastText(req)
	if role == "user" && text == "REVIEW_AUTHORITY_DELEGATE" {
		params := map[string]any{"task": "REVIEW_AUTHORITY_CHILD_WITHDRAW"}
		if p.selected {
			params["tools"] = []any{"tasks"}
		}
		return toolCallHandlerFor("review-authority-parent-delegates", "subagent", params)(ctx, req)
	}
	if role == "user" && text == "REVIEW_AUTHORITY_CHILD_WITHDRAW" {
		return toolCallHandlerFor("review-authority-child-withdraw", "tasks", map[string]any{"action": "withdraw", "id": float64(p.id), "note": "not needed"})(ctx, req)
	}
	return simpleResponse("authority review complete"), nil
}

func TestReviewAuthorityActualSubagentWithdrawalAuthority(t *testing.T) {
	for _, tc := range []struct{ selected, rootRequest bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("selected_tasks_%v/root_request_%v", tc.selected, tc.rootRequest), func(t *testing.T) {
			p := &reviewAuthorityDelegateScript{selected: tc.selected}
			_, mgr := newNoticeTestServer(t, p)
			root, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Moa"})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			requester := "other-project-session"
			if tc.rootRequest {
				requester = root.SessionID
			}
			req, err := mgr.tasks.AgentAsk(ctx, tasks.Actor{SessionID: requester, ProjectKey: root.CodebaseKey, ProjectCWD: root.Root}, tasks.AgentInput{Title: "PRIVATE project request"})
			if err != nil {
				t.Fatal(err)
			}
			p.id = req.ID
			parent, ok := mgr.Get(root.SessionID)
			if !ok {
				t.Fatal("parent not loaded")
			}
			started := make(chan bus.SubagentStarted, 1)
			unsub := parent.runtime.Bus.Subscribe(func(e bus.SubagentStarted) { started <- e })
			defer unsub()
			if _, _, _, err := mgr.Send(parent.ID, "REVIEW_AUTHORITY_DELEGATE", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			select {
			case child := <-started:
				if child.JobID == "" || child.JobID == parent.ID {
					t.Fatal("child has no independent agent identity")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("parent did not delegate")
			}
			pollUntil(t, 10*time.Second, "delegated child and parent idle", func() bool { return sessState(parent) == StateIdle })
			p.mu.Lock()
			results := append([]core.Message(nil), p.results...)
			p.mu.Unlock()
			if len(results) == 0 {
				t.Fatal("real child never called tasks withdraw")
			}
			for _, result := range results {
				if !result.IsError || strings.Contains(fmt.Sprint(result.Content), "PRIVATE") {
					t.Fatalf("actual child was authorized/leaked request: %+v", result)
				}
			}
			got, err := mgr.tasks.Get(ctx, req.ID)
			if err != nil || got.Status != tasks.StatusPending || got.CompletionNote != "" {
				t.Fatalf("actual child changed request: %+v %v", got, err)
			}
			t.Logf("actual child tasks withdraw refused; selected=%v root_request=%v request #%d remains pending", tc.selected, tc.rootRequest, req.ID)
		})
	}
}
