package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// This provider is a deterministic fixture. It never contacts a model.
type reviewChildSendProvider struct {
	mu       sync.Mutex
	requests []core.Request
}

func (p *reviewChildSendProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	n := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	msg := core.Message{Role: "assistant", StopReason: "end_turn", Content: []core.Content{core.TextContent("finished")}}
	if n == 0 {
		msg.StopReason = "tool_use"
		msg.Content = []core.Content{core.ToolCallContent("review-child-send", "tasks", map[string]any{
			"action": "send", "to": "Infra", "title": "Child sent without the owner",
		})}
	}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func reviewOwnerPair(t *testing.T, allowed bool) (*tasks.Repo, owner.Owner, owner.Owner) {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	from, err := store.Create(t.TempDir(), "moa", "", "", true, owner.Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	to, err := store.Create(t.TempDir(), "Infra", "", "", true, owner.Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	from.SessionID, to.SessionID = "sender-owner-session", "recipient-owner-session"
	if allowed {
		from.CanSendTasksTo = []string{to.ID}
	}
	for _, own := range []owner.Owner{from, to} {
		if err := store.Save(own); err != nil {
			t.Fatal(err)
		}
	}
	r := tasks.New(filepath.Join(t.TempDir(), tasks.DatabaseName))
	t.Cleanup(func() { _ = r.Close() })
	return r, from, to
}

func reviewBuild(t *testing.T, r *tasks.Repo, own owner.Owner, id string, ownerSession bool, prov core.Provider, started func(string)) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess, err := BuildSession(SessionConfig{
		CWD: own.Root, Model: core.Model{ID: "test-model", Name: "test"}, Provider: prov,
		ProviderFactory: func(core.Model) (core.Provider, error) { return prov, nil },
		Tasks:           r, SessionID: id, OwnerSession: ownerSession, Ctx: ctx,
		MoaCfg: &core.MoaConfig{},
		OnSubagentStart: func(jobID, task, model, thinking, origin string, async bool, at time.Time, accent int) {
			if started != nil {
				started(jobID)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func reviewResultText(res core.Result) string {
	var out strings.Builder
	for _, c := range res.Content {
		out.WriteString(c.Text)
	}
	return out.String()
}

// D10 applies to subagents too, not just independently created sessions.
func TestOwnerSubagentCannotSend(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprintf("standing_permission_%t", allowed), func(t *testing.T) {
			r, from, to := reviewOwnerPair(t, allowed)
			p := &reviewChildSendProvider{}
			var jobID string
			sess := reviewBuild(t, r, from, from.SessionID, true, p, func(id string) { jobID = id })
			sub, ok := sess.ToolReg.Get("subagent")
			if !ok {
				t.Fatal("missing subagent tool")
			}
			res, err := sub.Execute(context.Background(), map[string]any{
				"task":  "Send a task directly to Infra without consulting the owner.",
				"tools": []any{"tasks"},
			}, nil)
			if err != nil || res.IsError {
				t.Fatalf("subagent fixture failed: %v, %s", err, reviewResultText(res))
			}
			if jobID == "" || jobID == from.SessionID {
				t.Fatal("fixture did not spawn an independent child")
			}
			t.Logf("child job=%q parent session=%q", jobID, from.SessionID)
			list, err := r.List(context.Background(), tasks.Filter{IncludeAgents: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, rec := range list.Tasks {
				notices, err := r.TaskNotices(context.Background(), rec.ID, 20)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("ACTUAL task=%d place=%s handoff=%s requester=%q assignee=%q notices=%+v", rec.ID, rec.Place, rec.Handoff.State,
					rec.RequesterSessionID, rec.AssigneeSessionID, notices)
				if rec.RequesterSessionID != from.SessionID || (allowed && rec.AssigneeSessionID != to.SessionID) {
					t.Fatal("unexpected fixture identity")
				}
			}
			p.mu.Lock()
			for i, req := range p.requests {
				for _, spec := range req.Tools {
					if spec.Name == "tasks" {
						t.Logf("child request %d tasks schema: %s", i, spec.Parameters)
					}
				}
				for _, m := range req.Messages {
					if m.Role == "tool_result" {
						t.Logf("child request %d result: %+v", i, m)
					}
				}
			}
			p.mu.Unlock()
			if len(list.Tasks) != 0 {
				t.Fatalf("RED D10: a subagent must not send or file a handoff as its owner; expected 0 tasks, got %d", len(list.Tasks))
			}
		})
	}
}

// Independently built children must not become owners by sharing cwd or
// supplying role/identity fields in untrusted arguments.
func TestSameCWDChildRejectsForgedSend(t *testing.T) {
	r, from, to := reviewOwnerPair(t, true)
	sess := reviewBuild(t, r, from, "child-session", false, nopProvider{}, nil)
	tool, _ := sess.ToolReg.Get("tasks")
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(schema["properties"].(map[string]any)["action"]), "send") {
		t.Fatal("send offered to child")
	}
	res, err := tool.Execute(context.Background(), map[string]any{
		"action": "send", "to": to.ID, "title": "Forged", "ownerSession": true, "owner_session": true,
		"session_id": from.SessionID, "requester_session_id": from.SessionID, "from_owner_id": from.ID,
		"project_key": from.CodebaseKey, "project_cwd": from.Root, "allowed": true,
	}, nil)
	if err != nil || !res.IsError {
		t.Fatalf("forged send accepted: %v %s", err, reviewResultText(res))
	}
	list, err := r.List(context.Background(), tasks.Filter{IncludeAgents: true})
	if err != nil || len(list.Tasks) != 0 {
		t.Fatalf("forged send wrote: %+v, %v", list, err)
	}
	t.Logf("GREEN expected refusal: %s; tasks=0", reviewResultText(res))
}
