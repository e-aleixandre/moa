package serve

import (
	"context"
	"reflect"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestReviewAuthorityOwnerChildCannotControlRootsOwnTasks(t *testing.T) {
	for _, action := range []string{"withdraw", "pause", "resume", "delete"} {
		t.Run(action, func(t *testing.T) {
			f1Isolate(t)
			e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{})
			ctx := context.Background()
			root, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Project Owner"})
			if err != nil {
				t.Fatal(err)
			}
			tool := reviewAuthorityTool(t, e, root.SessionID)
			var id int64
			if action == "withdraw" {
				res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "ask", "title": "root owner's request"})
				if res.IsError {
					t.Fatalf("root ask: %+v", res)
				}
				list, err := e.mgr.tasks.AgentList(ctx, tasks.Actor{SessionID: root.SessionID, ProjectKey: root.CodebaseKey})
				if err != nil || len(list.Requests) != 1 {
					t.Fatalf("root requests=%+v %v", list, err)
				}
				id = list.Requests[0].ID
			} else {
				res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "create", "title": "root owner's schedule", "when": "every day at 09:00"})
				if res.IsError {
					t.Fatalf("root create: %+v", res)
				}
				list, err := e.mgr.tasks.ScheduledFor(ctx, root.SessionID, nil)
				if err != nil || len(list) != 1 {
					t.Fatalf("root schedules=%+v %v", list, err)
				}
				id = list[0].ID
				if action == "resume" {
					if res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "pause", "id": float64(id)}); res.IsError {
						t.Fatalf("root pause: %+v", res)
					}
				}
			}
			before, err := e.mgr.tasks.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			childCtx := core.WithAgentID(ctx, "actual-child-context")
			res := reviewAuthorityExecute(t, childCtx, tool, map[string]any{"action": action, "id": float64(id), "note": "child discarded it"})
			after, getErr := e.mgr.tasks.Get(ctx, id)
			if !res.IsError || getErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("owner subagent inherited root %s authority: is_error=%v result=%+v; before requester=%q creator=%q state=%q status=%q; after state=%q status=%q note=%q getErr=%v", action, res.IsError, res.Content, before.RequesterSessionID, before.CreatedBySessionID, before.ScheduleState, before.Status, after.ScheduleState, after.Status, after.CompletionNote, getErr)
			}
		})
	}
}
