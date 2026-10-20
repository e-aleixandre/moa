package serve

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func TestReviewAuthorityFailedOwnerDeleteRevokesProjectWithdrawal(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{})
	ctx := context.Background()
	old, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tool := reviewAuthorityTool(t, e, old.SessionID)
	req, err := e.mgr.tasks.AgentAsk(ctx, tasks.Actor{SessionID: "saved-project-session", ProjectKey: old.CodebaseKey, ProjectCWD: e.root}, tasks.AgentInput{Title: "sibling request"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = e.mgr.tasks.InsertQuestion(ctx, tasks.Question{ID: tasks.NewQuestionID(), SessionID: old.SessionID, OriginMsgID: "review-message", OriginToolCallID: "review-call", OriginTool: tasks.OriginAskUser, Questions: []askuser.Question{{Text: "Continue?", Options: []string{"yes", "no"}}}, Ack: tasks.QuestionAck{Text: "open"}})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a genuine SQLite settlement write failure, not a forged actor
	// or an edited directory. DeleteOwner removes owner.json before this write.
	drop := abortTrigger(t, e.mgr.tasks.Path(), "review_authority_no_question_settle", "BEFORE UPDATE OF state ON questions")
	err = e.mgr.DeleteOwner(old.ID)
	drop()
	if err == nil || !strings.Contains(err.Error(), "refused by test") {
		t.Fatalf("fixture delete did not fail at real SQL settlement: %v", err)
	}
	if _, ok := e.mgr.Get(old.SessionID); !ok {
		t.Fatal("fixture old runtime is not live")
	}
	store, err := e.mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindByID(old.ID); err != nil || found {
		t.Fatalf("fixture registration not removed: found=%v err=%v", found, err)
	}
	fresh, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.SessionID == old.SessionID || fresh.CodebaseKey != old.CodebaseKey {
		t.Fatal("fixture owner identity was not recreated in same project")
	}
	// Existing send revalidates this exact old runtime against owner.json.
	send := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "send", "to": "Owner", "title": "must refuse"})
	if !send.IsError {
		t.Fatalf("stale send unexpectedly accepted: %+v", send)
	}
	res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "withdraw", "id": float64(req.ID), "note": "old owner withdrew it"})
	got, getErr := e.mgr.tasks.Get(ctx, req.ID)
	if !res.IsError || getErr != nil || got.Status != tasks.StatusPending {
		t.Fatalf("live stale owner retained project withdrawal after real failed delete + recreate: old=%s new=%s is_error=%v result=%+v status=%s note=%q getErr=%v", old.SessionID, fresh.SessionID, res.IsError, res.Content, got.Status, got.CompletionNote, getErr)
	}
}

func TestReviewAuthorityRecreatedOwnerDoesNotInheritCreatorRights(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{})
	ctx := context.Background()
	old, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tool := reviewAuthorityTool(t, e, old.SessionID)
	if res := reviewAuthorityExecute(t, ctx, tool, map[string]any{"action": "create", "title": "old owner's job", "when": "every day at 09:00"}); res.IsError {
		t.Fatalf("root create: %+v", res)
	}
	list, err := e.mgr.tasks.ScheduledFor(ctx, old.SessionID, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("old schedules=%+v %v", list, err)
	}
	if err := e.mgr.DeleteOwner(old.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.mgr.Get(old.SessionID); ok {
		t.Fatal("deleted old owner runtime survived")
	}
	fresh, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.SessionID == old.SessionID {
		t.Fatal("recreated owner reused old ID")
	}
	for _, action := range []string{"pause", "resume", "delete"} {
		res := reviewAuthorityExecute(t, ctx, reviewAuthorityTool(t, e, fresh.SessionID), map[string]any{"action": action, "id": float64(list[0].ID)})
		if !res.IsError {
			t.Fatalf("recreated owner inherited old creator %s: %+v", action, res)
		}
	}
}
