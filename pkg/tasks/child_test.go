package tasks

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func runChildTool(t *testing.T, tool core.Tool, params map[string]any) (string, bool) {
	t.Helper()
	ctx := core.WithAgentID(context.Background(), "job-1")
	res, err := tool.Execute(ctx, params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].Text, res.IsError
}

// A subagent runs inside its owner's session but is not the owner: what acts
// in the owner's name (sending, answering or closing a task between owners)
// is refused for it, and not even offered.
func TestSubagentCannotActAsOwner(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	dir := &fakeOwners{owners: []OwnerRef{moa, infra}, allow: map[[2]string]bool{{moa.ID, infra.ID}: true}}
	sender := NewScope(r, moa.SessionID, "").WithOwners(dir, true)
	recipient := NewScope(r, infra.SessionID, "").WithOwners(dir, true)

	rec := mustSend(t, r, moa, infra, true)
	before := len(notices(t, r, rec.ID))

	for _, tool := range []core.Tool{NewTool(sender), mustChild(t, NewTool(sender))} {
		text, isErr := runChildTool(t, tool, map[string]any{"action": "send", "to": "Infra", "title": "forged"})
		if !isErr {
			t.Fatalf("child send accepted: %s", text)
		}
	}
	ls, _ := r.List(bg, Filter{IncludeAgents: true})
	if len(ls.Tasks) != 1 {
		t.Fatalf("a child created %d tasks", len(ls.Tasks)-1)
	}

	// The recipient's child neither answers nor closes the handoff it received.
	for _, params := range []map[string]any{
		{"action": "comment", "id": float64(rec.ID), "body": "hi"},
		{"action": "done", "id": float64(rec.ID), "outcome": "cant_do", "note": "no"},
	} {
		text, isErr := runChildTool(t, NewTool(recipient), params)
		if !isErr {
			t.Fatalf("child %v accepted: %s", params["action"], text)
		}
	}
	got, _ := r.Get(bg, rec.ID)
	if got.Status == StatusDone || got.CommentCount != 0 || len(notices(t, r, rec.ID)) != before {
		t.Fatalf("child changed the handoff: %+v, notices %d->%d", got, before, len(notices(t, r, rec.ID)))
	}

	// What does not speak for the owner still works for a child: its session's
	// own request to the user takes a comment.
	req, err := r.AgentAsk(bg, Actor{SessionID: infra.SessionID}, AgentInput{Title: "need the key"})
	if err != nil {
		t.Fatal(err)
	}
	if text, isErr := runChildTool(t, NewTool(recipient), map[string]any{"action": "comment", "id": float64(req.ID), "body": "details"}); isErr {
		t.Fatalf("child comment on its session's request: %s", text)
	}
	// The root conversation (no agent id) is unaffected.
	if text, isErr := runTool(t, recipient, map[string]any{"action": "comment", "id": float64(rec.ID), "body": "2 GB?"}); isErr {
		t.Fatalf("owner comment: %s", text)
	}
}

func mustChild(t *testing.T, tool core.Tool) core.Tool {
	t.Helper()
	child, ok := ChildVariant(tool)
	if !ok {
		t.Fatal("no child variant for an owner's tasks tool")
	}
	if strings.Contains(string(child.Parameters), `"send"`) || strings.Contains(string(child.Parameters), `"to"`) ||
		strings.Contains(child.Description, "send gives") {
		t.Fatalf("child variant still offers send: %s / %s", child.Parameters, child.Description)
	}
	return child
}

func TestChildVariantOfNonOwnerToolIsUnchanged(t *testing.T) {
	r := newRepo(t)
	if _, ok := ChildVariant(NewTool(NewScope(r, "s1", ""))); ok {
		t.Fatal("a tool that never offered send was downgraded")
	}
	if _, ok := ChildVariant(core.Tool{Name: "read"}); ok {
		t.Fatal("another tool was downgraded")
	}
}
