package serve

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
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
	// A foreign session may be named as a warning at the end, never in the roster.
	roster, _, _ := strings.Cut(listed, "Open elsewhere")
	if strings.Contains(roster, foreign.ID) {
		t.Fatalf("list put a session of another codebase in the roster:\n%s", listed)
	}
	if strings.Contains(listed, ownerSess.ID) {
		t.Fatalf("list included the owner's own conversation:\n%s", listed)
	}
}

// A session of another codebase that nobody owns is named — only as a warning,
// with its directory and no title — because otherwise it reports to no one.
func TestSessionsToolWarnsAboutUnownedSessionsElsewhere(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	strayDir := t.TempDir()
	stray, err := mgr.CreateSession(CreateOpts{CWD: strayDir, Title: "secret title"})
	if err != nil {
		t.Fatal(err)
	}

	listed := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if !strings.Contains(listed, "Open elsewhere") || !strings.Contains(listed, stray.ID) {
		t.Fatalf("list did not warn about an unowned session elsewhere:\n%s", listed)
	}
	if !strings.Contains(listed, strayDir) {
		t.Fatalf("the warning did not say where the session is:\n%s", listed)
	}
	if strings.Contains(listed, "secret title") {
		t.Fatalf("the warning leaked another project's title:\n%s", listed)
	}
	// Naming it is not reaching it.
	if res := runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": stray.ID}); !res.IsError {
		t.Fatal("the owner read a session of another codebase")
	}

	// Once it is closed it is not running any more, so it is not a warning.
	if err := mgr.CloseSession(stray.ID); err != nil {
		t.Fatal(err)
	}
	listed = toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if strings.Contains(listed, stray.ID) {
		t.Fatalf("list warned about a saved session elsewhere:\n%s", listed)
	}
}

// A codebase with its own owner is already watched: it is not a gap.
func TestSessionsToolDoesNotWarnAboutAnotherOwnersSessions(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	otherRoot := t.TempDir()
	ownerWithSession(t, mgr, otherRoot, "iREAD")
	theirs, err := mgr.CreateSession(CreateOpts{CWD: otherRoot, Title: "theirs"})
	if err != nil {
		t.Fatal(err)
	}

	listed := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "list"}))
	if strings.Contains(listed, theirs.ID) {
		t.Fatalf("list named a session another owner already watches:\n%s", listed)
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

func TestSessionsToolSendResumesASavedChild(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner", Title: "saved child"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, loaded := mgr.Get(child.ID); loaded {
		t.Fatal("child remained loaded after close")
	}

	res := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": child.ID, "text": "continue the work"})
	if res.IsError {
		t.Fatalf("send failed: %s", toolText(res))
	}
	resumed, loaded := mgr.Get(child.ID)
	if !loaded {
		t.Fatal("send did not resume the saved child")
	}
	pollUntil(t, 5*time.Second, "the message reaching the resumed child", func() bool {
		for _, msg := range resumed.History() {
			if msg.Role == "user" && strings.Contains(assistantText(msg), "continue the work") &&
				msg.Custom["source"] == "owner" && msg.Custom["owner_name"] == "Winerim" && msg.Custom["owner_id"] != "" {
				return true
			}
		}
		return false
	})
}

func TestSessionsToolSendSelectsAnAuthorizedDuplicateWithoutTouchingForeignData(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner", Title: "owned copy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(child.ID); err != nil {
		t.Fatal(err)
	}
	authorizedStore, err := session.NewFileStore(mgr.sessionBaseDir, root)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := authorizedStore.LoadReadOnly(child.ID)
	if err != nil {
		t.Fatal(err)
	}

	foreignRoot := filepath.Join(t.TempDir(), "000-foreign")
	if err := os.MkdirAll(foreignRoot, 0700); err != nil {
		t.Fatal(err)
	}
	foreignStore, err := session.NewFileStore(mgr.sessionBaseDir, foreignRoot)
	if err != nil {
		t.Fatal(err)
	}
	if foreignStore.Dir() >= authorizedStore.Dir() {
		t.Fatalf("test requires foreign store %q to sort before authorized store %q", foreignStore.Dir(), authorizedStore.Dir())
	}
	foreign := *saved
	foreign.Metadata = make(map[string]any, len(saved.Metadata))
	for key, value := range saved.Metadata {
		foreign.Metadata[key] = value
	}
	model, _, permissionMode, thinking := foreign.RuntimeMeta()
	foreign.SetRuntimeMetadata(model, foreignRoot, permissionMode, thinking)
	foreign.Title = "foreign copy"
	foreign.Version = 0
	foreign.Messages = []core.AgentMessage{core.WrapMessage(core.NewUserMessage("legacy foreign history"))}
	foreign.Entries = nil
	foreign.LeafID = ""
	if err := foreignStore.Save(&foreign); err != nil {
		t.Fatal(err)
	}
	foreignPath := filepath.Join(foreignStore.Dir(), child.ID+".json")
	foreignBefore, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	// ownerSession authorizes the newest roster record, while ResumeSession's
	// directory scan resolves the foreign duplicate first.
	if err := authorizedStore.Save(saved); err != nil {
		t.Fatal(err)
	}
	mgr.invalidateSavedCache()
	info, err := mgr.ownerSession(ownerSess.ownerOfTest(t), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.CWD != root {
		t.Fatalf("roster authorized cwd %q, want %q", info.CWD, root)
	}
	resolved, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, resolvedCWD, _, _ := resolved.RuntimeMeta()
	if resolvedCWD != foreignRoot {
		t.Fatalf("global lookup resolved cwd %q, want foreign cwd %q", resolvedCWD, foreignRoot)
	}

	res := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": child.ID, "text": "do not cross projects"})
	if res.IsError {
		t.Fatalf("send did not select the authorized duplicate: %s", toolText(res))
	}
	loaded, ok := mgr.Get(child.ID)
	if !ok {
		t.Fatal("authorized record was not resumed")
	}
	if loaded.CWD != root {
		t.Fatalf("resumed target cwd = %q, want authorized cwd %q", loaded.CWD, root)
	}
	pollUntil(t, 5*time.Second, "the owner message reaching the authorized duplicate", func() bool {
		for _, msg := range loaded.History() {
			if msg.Custom["source"] == "owner" && strings.Contains(assistantText(msg), "do not cross projects") {
				return true
			}
		}
		return false
	})
	foreignAfter, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(foreignAfter, foreignBefore) {
		t.Fatal("authorization migrated or otherwise rewrote the foreign V1 session")
	}
	if _, err := os.Stat(foreignPath + ".v1.bak"); !os.IsNotExist(err) {
		t.Fatalf("foreign V1 migration backup exists: %v", err)
	}
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
