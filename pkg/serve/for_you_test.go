package serve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// insertOpenQuestion commits an open asynchronous question for a session that
// need not be loaded, through the real question store.
func insertOpenQuestion(t *testing.T, repo *tasks.Repo, sessionID string, qs ...askuser.Question) string {
	t.Helper()
	id := tasks.NewQuestionID()
	if _, _, err := repo.InsertQuestion(context.Background(), tasks.Question{
		ID: id, SessionID: sessionID, OriginToolCallID: "tc-" + id, OriginTool: tasks.OriginAskUser, Questions: qs,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// Everything waiting, across sessions: an async question of a session that is
// not loaded (from the store), and a blocking question, a permission and a
// password from live sessions. Each appears once, what stopped a session
// first, each group oldest first; the context of both kinds of question is
// carried.
func TestForYouListsEverythingWaitingBlockingFirst(t *testing.T) {
	pinSyncAskUser(t)
	ctx := context.Background()
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(
		toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
			"questions": []any{map[string]any{"question": "Ship it?", "context": "Staging is green.", "options": []any{"yes", "no"}}},
		}),
		func(context.Context, core.Request) (<-chan core.AssistantEvent, error) { return simpleResponse("ok"), nil },
	), t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})

	asyncID := insertOpenQuestion(t, mgr.tasks, "saved-session", askuser.Question{Text: "Which plan?", Context: "Two options."})
	time.Sleep(5 * time.Millisecond)

	asker, err := mgr.CreateSession(CreateOpts{Title: "asker"})
	if err != nil {
		t.Fatal(err)
	}
	asks := make(chan bus.AskUserRequested, 1)
	unsub := asker.runtime.Bus.Subscribe(func(e bus.AskUserRequested) { asks <- e })
	defer unsub()
	if _, _, _, err := mgr.Send(asker.ID, "ask me", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var ask bus.AskUserRequested
	select {
	case ask = <-asks:
	case <-time.After(5 * time.Second):
		t.Fatal("no blocking ask")
	}
	if len(ask.Questions) != 1 || ask.Questions[0].Context != "Staging is green." {
		t.Fatalf("the ask event lost the context: %+v", ask.Questions)
	}
	time.Sleep(5 * time.Millisecond)

	permSess, err := mgr.CreateSession(CreateOpts{Title: "perm"})
	if err != nil {
		t.Fatal(err)
	}
	openPermission(t, permSess)
	time.Sleep(5 * time.Millisecond)
	secretSess, err := mgr.CreateSession(CreateOpts{Title: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	secretID, _ := askSecret(t, secretSess)

	got := getForYou(t, mgr).Items
	var kinds []string
	for _, it := range got {
		kinds = append(kinds, it.Kind+"/"+it.SessionID)
	}
	want := []string{"question/" + asker.ID, "permission/" + permSess.ID, "secret/" + secretSess.ID, "question/saved-session"}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("for you = %v, want %v", kinds, want)
	}
	if !got[0].Blocking || got[0].Questions[0].Context != "Staging is green." || got[0].ID != ask.ID {
		t.Fatalf("blocking question = %+v", got[0])
	}
	if got[2].ID != secretID || got[2].Label == "" || got[2].Command == "" {
		t.Fatalf("secret = %+v", got[2])
	}
	last := got[3]
	if last.Blocking || !last.Async || last.ID != asyncID || last.Questions[0].Context != "Two options." {
		t.Fatalf("async question = %+v", last)
	}
	for _, it := range got[:3] {
		if !it.Blocking || it.Since.IsZero() {
			t.Fatalf("live item %+v: want blocking with its age", it)
		}
	}
}

// After a restart the open asynchronous questions are still listed, still
// marked on the roster and owner rows, and keep their context; the blocking
// ones lived in memory and are gone with the process.
func TestForYouAsyncQuestionsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m1 := newRestartableManager(t, ctx, dir)
	id := insertOpenQuestion(t, m1.tasks, "sess-a", askuser.Question{Text: "Rotate the key?", Context: "It expires Friday."})
	m1.Shutdown()

	m2 := newRestartableManager(t, ctx, dir)
	got := getForYou(t, m2).Items
	if len(got) != 1 || got[0].ID != id || got[0].SessionID != "sess-a" || got[0].Questions[0].Context != "It expires Friday." {
		t.Fatalf("after restart: %+v", got)
	}
	if open := m2.openQuestionIDsBySession(); len(open["sess-a"]) != 1 || open["sess-a"][0] != id {
		t.Fatalf("roster marks after restart = %v", open)
	}
}

// A question without context is served without the field: older clients and
// older questions see nothing new.
func TestForYouOmitsAbsentContext(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	insertOpenQuestion(t, mgr.tasks, "s", askuser.Question{Text: "Plain?"})
	rec := getForYouRaw(t, mgr)
	if strings.Contains(rec, `"context"`) {
		t.Fatalf("body carries an empty context: %s", rec)
	}
}

// The asynchronous ask event (what the WebSocket sends) carries the context.
func TestAsyncAskEventCarriesContext(t *testing.T) {
	ev := bus.AskUserRequested{ID: "q_1", Async: true, Questions: busQuestions([]askuser.Question{{Text: "Ship?", Context: "Staging is green."}, {Text: "Plain?"}})}
	wsEv, ok := wsEventFromBus(ev)
	if !ok {
		t.Fatal("the ask event is not sent on the socket")
	}
	b, err := json.Marshal(wsEv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"context":"Staging is green."`) || strings.Count(string(b), `"context"`) != 1 {
		t.Fatalf("event = %s", b)
	}
}
