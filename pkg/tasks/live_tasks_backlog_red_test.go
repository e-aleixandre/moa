package tasks

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func liveTasksIdeaInput(t *testing.T) AgentInput {
	t.Helper()
	// Place is an additive AgentInput field. Decode it so the RED suite still
	// compiles against the old input instead of failing all package tests.
	var in AgentInput
	if err := json.Unmarshal([]byte(`{"Title":"Uncommitted idea","Description":"Discussed in the owner session; not decided","Place":"backlog"}`), &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestLiveTasksBacklogIsInvisibleToChildren(t *testing.T) {
	r := newRepo(t)
	idea := backlog(t, r, "SECRET-UNDECIDED-IDEA", "p")
	child := actor("child", "p")
	t.Run("repo_list", func(t *testing.T) {
		view, err := r.AgentList(bg, child)
		if err != nil || len(view.Backlog) != 0 {
			t.Fatalf("child received project ideas: %+v, %v", view.Backlog, err)
		}
	})
	t.Run("repo_get", func(t *testing.T) {
		if got, err := r.AgentGet(bg, child, idea.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("child can read an undecided idea: %+v, %v", got, err)
		}
	})
	t.Run("tool_list", func(t *testing.T) {
		out, isErr := run(t, scopeFor(r, "child", "p"), map[string]any{"action": "list"})
		if isErr || strings.Contains(out, idea.Title) || strings.Contains(strings.ToLower(out), "backlog") {
			t.Fatalf("child's tasks tool exposed the ideas backlog: %q err=%v", out, isErr)
		}
	})
	if view, err := r.AgentList(bg, liveTasksOwner()); err != nil || len(view.Backlog) != 1 || view.Backlog[0].ID != idea.ID {
		t.Fatalf("owner lost access to its project ideas: %+v, %v", view.Backlog, err)
	}
}

func TestLiveTasksOnlyOwnerPromotesBacklog(t *testing.T) {
	r := newRepo(t)
	idea := backlog(t, r, "not decided yet", "p")
	before := mustGet(t, r, idea.ID)
	if got, err := r.AgentClaim(bg, actor("child", "p"), idea.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("non-owner claim must say not available, got %+v, %v", got, err)
	}
	if got := mustGet(t, r, idea.ID); got.Place != before.Place || got.Revision != before.Revision || got.AssigneeSessionID != "" {
		t.Fatalf("rejected child claim mutated the idea: %+v", got)
	}
	promoted, err := r.AgentClaim(bg, liveTasksOwner(), idea.ID)
	if err != nil || promoted.Place != PlaceAgent || promoted.Status != StatusPending {
		t.Fatalf("owner could not promote the decided idea: %+v, %v", promoted, err)
	}
	if rec := mustGet(t, r, idea.ID); rec.AssigneeSessionID != "owner" || rec.RequesterSessionID != "" {
		t.Fatalf("promotion must move to the owner's own checklist: %+v", rec)
	}
}

func TestLiveTasksOwnerCreatesIdeasWithoutAssigningWork(t *testing.T) {
	for _, isOwner := range []bool{true, false} {
		name := "child"
		if isOwner {
			name = "owner"
		}
		t.Run(name, func(t *testing.T) {
			r := newRepo(t)
			a := actor(name, "p")
			a.Owner = isOwner
			created, err := r.AgentCreate(bg, a, liveTasksIdeaInput(t))
			if !isOwner {
				if err == nil {
					t.Fatalf("non-owner create place=backlog must be rejected, not silently turned into work: %+v", created)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rec := mustGet(t, r, created.ID)
			if rec.Place != PlaceBacklog || rec.Status != StatusPending || rec.AssigneeSessionID != "" || rec.RequesterSessionID != "" || rec.ProjectKey != a.ProjectKey {
				t.Fatalf("owner's idea was made into assigned work: %+v", rec)
			}
			if checklist, err := r.Checklist(bg, a.SessionID); err != nil || len(checklist) != 0 {
				t.Fatalf("an undecided idea appeared on the owner's checklist: %+v, %v", checklist, err)
			}
		})
	}
}

func TestLiveTasksOwnerToolCreatesAndSummarizesIdeas(t *testing.T) {
	r := newRepo(t)
	sc := scopeFor(r, "owner", "p").WithOwners(nil, true)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(NewTool(sc).Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema.Properties["place"]), "backlog") {
		t.Error("owner's actual tool schema does not offer place=backlog")
	}
	out, isErr := run(t, sc, map[string]any{"action": "create", "title": "Idea from the conversation", "place": "backlog", "description": "Discussed here, not accepted work"})
	if isErr {
		t.Fatal(out)
	}
	list, err := r.List(bg, Filter{IncludeAgents: true})
	if err != nil || len(list.Tasks) != 1 || list.Tasks[0].Place != PlaceBacklog {
		t.Fatalf("real tasks tool ignored create place=backlog: %+v, %v", list.Tasks, err)
	}
	idea := list.Tasks[0]
	backlog(t, r, "Another undecided idea", "p")
	out, isErr = run(t, sc, map[string]any{"action": "list"})
	if isErr || !strings.Contains(out, "Backlog: 2 ideas (list place=backlog)") || strings.Contains(out, idea.Title) {
		t.Fatalf("default owner listing must summarize ideas without injecting their detail: %q err=%v", out, isErr)
	}
	out, isErr = run(t, sc, map[string]any{"action": "list", "place": "backlog"})
	if isErr || !strings.Contains(out, idea.Title) || !strings.Contains(out, "Another undecided idea") {
		t.Fatalf("owner cannot explicitly list its ideas: %q err=%v", out, isErr)
	}
	out, isErr = run(t, sc, map[string]any{"action": "claim", "id": float64(idea.ID)})
	if isErr {
		t.Fatalf("owner tool could not promote an idea: %q", out)
	}
	if rec := mustGet(t, r, idea.ID); rec.Place != PlaceAgent || rec.AssigneeSessionID != "owner" {
		t.Fatalf("owner tool promotion did not reach the checklist: %+v", rec)
	}
}
