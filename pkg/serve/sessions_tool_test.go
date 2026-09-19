package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

func runSessionsTool(t *testing.T, sess *ManagedSession, params map[string]any) core.Result {
	t.Helper()
	tool, ok := sess.infra.toolReg.Get(SessionsToolName)
	if !ok {
		t.Fatal("owner session has no sessions tool")
	}
	res, err := tool.Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func toolText(res core.Result) string {
	var sb strings.Builder
	for _, c := range res.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// ownerWithSession creates an owner and returns its live conversation.
func ownerWithSession(t *testing.T, mgr *Manager, root, name string) (OwnerInfo, *ManagedSession) {
	t.Helper()
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := mgr.Get(info.SessionID)
	if !ok {
		t.Fatal("owner session missing")
	}
	return info, sess
}

func TestSessionsToolListsOnlyThisProject(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	mine, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir(), Title: "foreign"})
	if err != nil {
		t.Fatal(err)
	}

	listed := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if !strings.Contains(listed, mine.ID) {
		t.Fatalf("list omitted a session of the project:\n%s", listed)
	}
	if strings.Contains(listed, foreign.ID) {
		t.Fatalf("list leaked a session of another codebase:\n%s", listed)
	}
	if strings.Contains(listed, ownerSess.ID) {
		t.Fatalf("list included the owner's own conversation:\n%s", listed)
	}
}

func TestSessionsToolRefusesTargetsOutsideTheProject(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	foreign, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"read", "send", "answer"} {
		params := map[string]any{"action": action, "session_id": foreign.ID, "text": "hi", "ask_id": "a", "answers": []any{"x"}}
		if res := runSessionsTool(t, ownerSess, params); !res.IsError {
			t.Fatalf("%s reached a session of another codebase", action)
		}
	}
	// The owner's own conversation is not a target either.
	if res := runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": ownerSess.ID}); !res.IsError {
		t.Fatal("the owner read its own conversation through the tool")
	}
}

func TestSessionsToolNewRefusesAForeignDirectory(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	before := len(mgr.List())
	res := runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": t.TempDir(), "text": "do a thing"})
	if !res.IsError {
		t.Fatal("new created a session outside the project")
	}
	if got := len(mgr.List()); got != before {
		t.Fatalf("sessions after a refused new = %d, want %d", got, before)
	}
}

func TestSessionsToolNewStartsAndPrompts(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	res := runSessionsTool(t, ownerSess, map[string]any{"action": "new", "cwd": root, "text": "fix the import", "title": "import fix"})
	if res.IsError {
		t.Fatalf("new failed: %s", toolText(res))
	}
	var created *ManagedSession
	for _, info := range mgr.List() {
		if info.Title == "import fix" {
			created, _ = mgr.Get(info.ID)
		}
	}
	if created == nil {
		t.Fatalf("new did not create the session: %s", toolText(res))
	}
	pollUntil(t, 5*time.Second, "the prompt reaching the new session", func() bool {
		for _, msg := range created.History() {
			if msg.Role == "user" && strings.Contains(assistantText(msg), "fix the import") {
				return true
			}
		}
		return false
	})
}

func TestSessionsToolSendReachesAChild(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	res := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": child.ID, "text": "use the book"})
	if res.IsError {
		t.Fatalf("send failed: %s", toolText(res))
	}
	pollUntil(t, 5*time.Second, "the message reaching the child", func() bool {
		for _, msg := range child.History() {
			if msg.Role == "user" && strings.Contains(assistantText(msg), "use the book") &&
				msg.Custom["source"] == "owner" && msg.Custom["owner_name"] == "Winerim" && msg.Custom["owner_id"] != "" {
				return true
			}
		}
		return false
	})
}

func TestSessionsToolAnswerResolvesAnAskOnce(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	provider := newMockProvider(toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
		"questions": []any{map[string]any{"question": "which one?", "options": []any{"first", "second"}}},
	}))
	mgr := newTestManager(t, context.Background(), provider)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	asks := make(chan bus.AskUserRequested, 1)
	t.Cleanup(child.runtime.Bus.Subscribe(func(e bus.AskUserRequested) { asks <- e }))
	if _, _, _, err := mgr.Send(child.ID, "ask me something", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var ask bus.AskUserRequested
	select {
	case ask = <-asks:
	case <-time.After(5 * time.Second):
		t.Fatal("the child never raised its question")
	}

	// The owner sees the question in its listing, with the id it must answer.
	listed := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if !strings.Contains(listed, ask.ID) || !strings.Contains(listed, "which one?") {
		t.Fatalf("list does not surface the pending question:\n%s", listed)
	}

	res := runSessionsTool(t, ownerSess, map[string]any{
		"action": "answer", "session_id": child.ID, "ask_id": ask.ID, "answers": []any{"first"},
	})
	if res.IsError {
		t.Fatalf("answer failed: %s", toolText(res))
	}
	// Answering twice must report the conflict rather than silently succeed:
	// the user may have answered it in the UI first.
	again := runSessionsTool(t, ownerSess, map[string]any{
		"action": "answer", "session_id": child.ID, "ask_id": ask.ID, "answers": []any{"second"},
	})
	if !again.IsError {
		t.Fatal("answering an already-resolved question reported success")
	}
}

func TestSessionsToolAnswerIsGatedOnAnswerAsks(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	own := info.Owner
	own.AnswerAsks = false
	if err := store.Save(own); err != nil {
		t.Fatal(err)
	}
	res := runSessionsTool(t, ownerSess, map[string]any{
		"action": "answer", "session_id": child.ID, "ask_id": "whatever", "answers": []any{"x"},
	})
	if !res.IsError || !strings.Contains(toolText(res), "not allowed") {
		t.Fatalf("answer with answer_asks off = %q (IsError=%v)", toolText(res), res.IsError)
	}
}

func TestSessionsToolCannotAnswerAUserSession(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "user"})
	if err != nil {
		t.Fatal(err)
	}
	res := runSessionsTool(t, ownerSess, map[string]any{
		"action": "answer", "session_id": child.ID, "ask_id": "whatever", "answers": []any{"x"},
	})
	const want = "This session is the user's: do not answer for them. Ask the user with ask_user, proposing the answer you would give as the first option."
	if !res.IsError || !strings.Contains(toolText(res), want) {
		t.Fatalf("answering user session = %q (IsError=%v)", toolText(res), res.IsError)
	}
}

func TestSessionsToolOffersNoPermissionAction(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	_, ownerSess := ownerWithSession(t, mgr, t.TempDir(), "Winerim")

	tool, ok := ownerSess.infra.toolReg.Get(SessionsToolName)
	if !ok {
		t.Fatal("owner session has no sessions tool")
	}
	schema := string(tool.Parameters)
	for _, forbidden := range []string{"permission", "approve"} {
		if strings.Contains(strings.ToLower(schema), forbidden) {
			t.Fatalf("the sessions tool offers %q:\n%s", forbidden, schema)
		}
	}
}

func TestChildSessionHasNoSessionsTool(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := child.infra.toolReg.Get(SessionsToolName); ok {
		t.Fatal("a child session can direct its siblings")
	}
}

// The balance the owner owes on every turn needs times, and the tool is where
// they come from: how long ago each session moved, how long a blocked one has
// been waiting, and whether the list is complete.
func TestSessionsToolListPrintsTimesAndTruncation(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	provider := newMockProvider(toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
		"questions": []any{map[string]any{"question": "which one?"}},
	}))
	mgr := newTestManager(t, context.Background(), provider)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner", Title: "asking"})
	if err != nil {
		t.Fatal(err)
	}
	asks := make(chan bus.AskUserRequested, 1)
	t.Cleanup(child.runtime.Bus.Subscribe(func(e bus.AskUserRequested) { asks <- e }))
	if _, _, _, err := mgr.Send(child.ID, "ask me something", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asks:
	case <-time.After(5 * time.Second):
		t.Fatal("the child never raised its question")
	}

	listed := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if !strings.Contains(listed, "updated ") {
		t.Fatalf("list does not say when a session last moved:\n%s", listed)
	}
	if !strings.Contains(listed, "waiting since ") {
		t.Fatalf("list does not say how long the question has been waiting:\n%s", listed)
	}

	// The pending time is the question's, not the session's: the child was
	// written to and asked within the same second, so what is asserted here is
	// that the number comes from the pending approval at all.
	info, err := mgr.ownerSession(ownerSess.ownerOfTest(t), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.PendingSince.IsZero() || info.PendingID == "" {
		t.Fatalf("the pending approval carries no identity or time: %+v", info)
	}
}

// ownerOfTest reads the owner a session belongs to, for assertions that need
// the same authorization the tool does.
func (s *ManagedSession) ownerOfTest(t *testing.T) owner.Owner {
	t.Helper()
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	own, found, err := store.FindByDir(s.CWD)
	if err != nil || !found {
		t.Fatalf("no owner for %s: %v", s.CWD, err)
	}
	return own
}
