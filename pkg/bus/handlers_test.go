package bus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/session"
)

type busyGoalRestoreAgent struct{ *fakeAgent }

func (a *busyGoalRestoreAgent) SetCompactAt(int) error { return errors.New("busy") }
func (a *busyGoalRestoreAgent) SetMaxBudget(float64) error {
	return errors.New("busy")
}

type runEndedDuringSubscribeBus struct {
	*LocalBus
	fire       atomic.Bool
	unsubCalls atomic.Int32
}

func (b *runEndedDuringSubscribeBus) Subscribe(handler any) func() {
	unsub := b.LocalBus.Subscribe(handler)
	wrapped := func() {
		b.unsubCalls.Add(1)
		unsub()
	}
	if b.fire.CompareAndSwap(true, false) {
		b.Publish(RunEnded{})
		b.Drain(time.Second)
	}
	return wrapped
}

func TestStopGoalRunEndedDuringSubscribeTearsDown(t *testing.T) {
	b := &runEndedDuringSubscribeBus{LocalBus: NewLocalBus()}
	defer b.Close()
	agent := &busyGoalRestoreAgent{fakeAgent: &fakeAgent{}}
	sctx := newTestSessionContextWithState(b, agent)
	sctx.Goal = goal.New()
	if err := sctx.Goal.Enter(goal.Options{
		Objective: "finish",
		StatePath: filepath.Join(t.TempDir(), "STATE.md"),
	}); err != nil {
		t.Fatal(err)
	}

	b.fire.Store(true)
	stopGoal(sctx, "stopped")

	if got := b.unsubCalls.Load(); got != 1 {
		t.Fatalf("deferred goal restore unsubscribe calls = %d, want 1", got)
	}
}

func TestAutoVerifyCanceledBeforeGoroutineStartsDoesNotExecute(t *testing.T) {
	deferred := make(chan func(), 1)

	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".moa"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"checks":[{"name":"marker","command":"touch verify-started"}]}`
	if err := os.WriteFile(filepath.Join(cwd, ".moa", "verify.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContextWithState(b, &fakeAgent{})
	sctx.AutoVerify = true
	sctx.CWD = cwd
	sctx.RunGenAtomic.Store(7)
	registerHandlers(sctx, func(fn func()) { deferred <- fn })

	b.Publish(RunEnded{RunGen: 7, HadEdits: true})
	var run func()
	select {
	case run = <-deferred:
	case <-time.After(time.Second):
		t.Fatal("auto-verify goroutine was not launched")
	}

	// Force startRun to reject after SendPrompt has synchronously canceled the
	// pending auto-verify, keeping the run generation unchanged for this test.
	sctx.State.ForceState(StateRunning)
	if err := b.Execute(SendPrompt{Text: "new turn"}); err == nil {
		t.Fatal("SendPrompt unexpectedly started while state was running")
	}
	run()

	if _, err := os.Stat(filepath.Join(cwd, "verify-started")); !os.IsNotExist(err) {
		t.Fatalf("stale auto-verify executed after user prompt; marker stat: %v", err)
	}
}

// TestCleanRunError guards the user-facing rendering of run errors: a usage
// limit shows its typed, actionable message, while a generic error is stripped
// of the internal "stream: provider:" plumbing prefixes.
func TestCleanRunError(t *testing.T) {
	quota := &core.QuotaExceededError{Provider: "openai", Message: "The usage limit has been reached"}
	wrapped := fmt.Errorf("stream: %w", fmt.Errorf("provider: %w", quota))
	if got := cleanRunError(wrapped); got != "openai quota exceeded: The usage limit has been reached" {
		t.Fatalf("quota clean = %q", got)
	}
	generic := errors.New("stream: provider: openai: HTTP 500: boom")
	if got := cleanRunError(generic); got != "openai: HTTP 500: boom" {
		t.Fatalf("generic clean = %q", got)
	}
	if got := cleanRunError(nil); got != "" {
		t.Fatalf("nil clean = %q, want empty", got)
	}
}

func TestHasSuccessfulEdits_True(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewToolResultMessage("tc1", "read", []core.Content{core.TextContent("ok")}, false)),
		core.WrapMessage(core.NewToolResultMessage("tc2", "edit", []core.Content{core.TextContent("ok")}, false)),
	}
	if !hasSuccessfulEdits(msgs) {
		t.Error("expected true: edit tool succeeded")
	}
}

func TestHasSuccessfulEdits_False_NoEdits(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewToolResultMessage("tc1", "read", []core.Content{core.TextContent("ok")}, false)),
		core.WrapMessage(core.NewToolResultMessage("tc2", "grep", []core.Content{core.TextContent("ok")}, false)),
	}
	if hasSuccessfulEdits(msgs) {
		t.Error("expected false: no edit tools")
	}
}

func TestHasSuccessfulEdits_False_FailedEdit(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewToolResultMessage("tc1", "edit", []core.Content{core.TextContent("err")}, true)),
	}
	if hasSuccessfulEdits(msgs) {
		t.Error("expected false: edit failed (IsError)")
	}
}

func TestHasSuccessfulEdits_AllEditTools(t *testing.T) {
	for _, tool := range []string{"edit", "write", "multiedit", "apply_patch"} {
		msgs := []core.AgentMessage{
			core.WrapMessage(core.NewToolResultMessage("tc1", tool, []core.Content{core.TextContent("ok")}, false)),
		}
		if !hasSuccessfulEdits(msgs) {
			t.Errorf("expected true for tool %s", tool)
		}
	}
}

func TestHasSuccessfulEdits_Empty(t *testing.T) {
	if hasSuccessfulEdits(nil) {
		t.Error("expected false for empty messages")
	}
}

func TestHasSuccessfulEdits_SkipsAssistantMessages(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.ToolCallContent("tc1", "edit", map[string]any{"path": "foo"})},
		}),
	}
	// tool_call messages have role "assistant", not "tool_result" — should be ignored.
	if hasSuccessfulEdits(msgs) {
		t.Error("expected false: assistant messages should be skipped")
	}
}

// TestHandler_BranchTo_RejectsWhileNotIdle verifies BranchTo refuses to run
// while a run is in flight (running) or a permission is pending, and — crucially
// — does NOT mutate the tree's leaf. The permission case regressed: the old
// guard only rejected StateRunning, so a pending permission moved the leaf
// before LoadState failed, leaving the tree inconsistent.
func TestHandler_BranchTo_RejectsWhileNotIdle(t *testing.T) {
	for _, st := range []SessionState{StateRunning, StatePermission} {
		t.Run(string(st), func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			fa := &fakeAgent{}
			sctx := newTestSessionContextWithState(b, fa)

			// Two message entries so there is a valid branch target.
			tree := session.NewTree()
			firstID := tree.Append(session.Entry{
				Type:    session.EntryMessage,
				Message: core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent("first")}}),
			})
			tree.Append(session.Entry{
				Type:    session.EntryMessage,
				Message: core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("answer")}}),
			})
			sctx.Tree = tree
			RegisterHandlers(sctx)

			leafBefore := tree.LeafID()
			sctx.State.ForceState(st)

			if err := b.Execute(BranchTo{EntryID: firstID}); err == nil {
				t.Fatalf("expected BranchTo to be rejected in state %s", st)
			}
			if got := tree.LeafID(); got != leafBefore {
				t.Fatalf("tree leaf changed despite rejected branch: %s → %s", leafBefore, got)
			}
		})
	}
}

// TestHandler_BranchTo_AllowedWhenIdle confirms the guard still permits
// branching from a terminal state.
func TestHandler_BranchTo_AllowedWhenIdle(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)

	tree := session.NewTree()
	firstID := tree.Append(session.Entry{
		Type:    session.EntryMessage,
		Message: core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent("first")}}),
	})
	tree.Append(session.Entry{
		Type:    session.EntryMessage,
		Message: core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("answer")}}),
	})
	sctx.Tree = tree
	RegisterHandlers(sctx)

	if err := b.Execute(BranchTo{EntryID: firstID}); err != nil {
		t.Fatalf("BranchTo while idle: %v", err)
	}
	if got := tree.LeafID(); got != firstID {
		t.Fatalf("leaf = %s, want %s", got, firstID)
	}
}

// TestHandler_BranchTo_RejectsDanglingToolCall verifies the F15 guard: an
// assistant turn with an unresolved tool_call must not become the new leaf,
// even while idle, and GetBranchPoints must not offer it as a target.
func TestHandler_BranchTo_RejectsDanglingToolCall(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)

	tree := session.NewTree()
	tree.Append(session.Entry{
		Type:    session.EntryMessage,
		Message: core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent("run a command")}}),
	})
	acID := tree.Append(session.Entry{
		Type: session.EntryMessage,
		Message: core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.ToolCallContent("tc1", "bash", map[string]any{"command": "echo hi"})},
		}),
	})
	sctx.Tree = tree
	RegisterHandlers(sctx)

	leafBefore := tree.LeafID()
	if err := b.Execute(BranchTo{EntryID: acID}); err == nil {
		t.Fatal("expected BranchTo to reject an assistant entry with an unresolved tool call")
	}
	if got := tree.LeafID(); got != leafBefore {
		t.Fatalf("tree leaf changed despite rejected branch: %s → %s", leafBefore, got)
	}

	points, err := QueryTyped[GetBranchPoints, []BranchPoint](b, GetBranchPoints{})
	if err != nil {
		t.Fatalf("GetBranchPoints: %v", err)
	}
	for _, p := range points {
		if p.EntryID == acID {
			t.Fatalf("GetBranchPoints must not offer an entry with a dangling tool call: %+v", p)
		}
	}
}

func rewindTestTree() (*session.Tree, map[string]string) {
	tree := session.NewTree()
	ids := map[string]string{}
	add := func(name, role, text string) {
		ids[name] = tree.Append(session.Entry{
			Type:    session.EntryMessage,
			Message: core.WrapMessage(core.Message{Role: role, Content: []core.Content{core.TextContent(text)}}),
		})
	}
	add("u1", "user", "first")
	add("a1", "assistant", "answer one")
	add("u2", "user", "second")
	add("a2", "assistant", "answer two")
	return tree, ids
}

// TestHandler_BranchTo_Before rewinds to just before a user message: the
// previous answer stays and the agent is rehydrated from that branch.
func TestHandler_BranchTo_Before(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	tree, ids := rewindTestTree()
	sctx.Tree = tree
	RegisterHandlers(sctx)

	if err := b.Execute(BranchTo{EntryID: ids["u2"], Before: true}); err != nil {
		t.Fatalf("BranchTo before: %v", err)
	}
	if got := tree.LeafID(); got != ids["a1"] {
		t.Fatalf("leaf = %s, want %s", got, ids["a1"])
	}
	if len(fa.messages) != 2 {
		t.Fatalf("agent was rehydrated with %d messages, want 2", len(fa.messages))
	}
}

// TestHandler_BranchTo_BeforeFirstMessage leaves an empty branch and an empty
// agent, without deleting the old branch.
func TestHandler_BranchTo_BeforeFirstMessage(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	tree, ids := rewindTestTree()
	sctx.Tree = tree
	RegisterHandlers(sctx)

	if err := b.Execute(BranchTo{EntryID: ids["u1"], Before: true}); err != nil {
		t.Fatalf("BranchTo before first: %v", err)
	}
	if tree.LeafID() != "" || len(fa.messages) != 0 || tree.Len() != 4 {
		t.Fatalf("leaf=%q agent=%d entries=%d", tree.LeafID(), len(fa.messages), tree.Len())
	}
}

// TestHandler_BranchTo_BeforeRejectedWhileBusy keeps the existing guard.
func TestHandler_BranchTo_BeforeRejectedWhileBusy(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	tree, ids := rewindTestTree()
	sctx.Tree = tree
	RegisterHandlers(sctx)
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}
	leaf := tree.LeafID()
	if err := b.Execute(BranchTo{EntryID: ids["u2"], Before: true}); err == nil {
		t.Fatal("expected rejection while running")
	}
	if tree.LeafID() != leaf {
		t.Fatal("leaf moved despite the rejection")
	}
}

// TestQuery_GetBranchMessage returns the original message a rewind puts back
// in the composer, and only for user messages.
func TestQuery_GetBranchMessage(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContextWithState(b, &fakeAgent{})
	tree, ids := rewindTestTree()
	sctx.Tree = tree
	RegisterHandlers(sctx)

	msg, err := QueryTyped[GetBranchMessage, core.Message](b, GetBranchMessage{EntryID: ids["u2"]})
	if err != nil || len(msg.Content) != 1 || msg.Content[0].Text != "second" {
		t.Fatalf("msg=%+v err=%v", msg, err)
	}
	if _, err := QueryTyped[GetBranchMessage, core.Message](b, GetBranchMessage{EntryID: ids["a1"]}); err == nil {
		t.Fatal("an assistant entry must be rejected")
	}
}

// The timeline must know which user points are the owner's or the parent
// agent's: they are not the user's own text to edit (review finding 4).
func TestQuery_GetBranchPoints_CarriesSourceOfInjectedMessages(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContextWithState(b, &fakeAgent{})
	tree := session.NewTree()
	own := tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent("mine")}})})
	owner := tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{
		Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("task")}},
		Custom:  map[string]any{"source": "owner"},
	}})
	sctx.Tree = tree
	RegisterHandlers(sctx)

	points, err := QueryTyped[GetBranchPoints, []BranchPoint](b, GetBranchPoints{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range points {
		got[p.EntryID] = p.Source
	}
	if got[own] != "" || got[owner] != "owner" {
		t.Fatalf("sources = %v", got)
	}
}

// A rewind on an invoked skill hands back what the user typed, never the
// SKILL.md, and the timeline labels it the same way. Legacy skill messages
// (custom.skill and nothing else) behave identically.
func TestRewind_SkillInvocationReturnsTheTypedCommand(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContextWithState(b, &fakeAgent{})
	tree := session.NewTree()
	body := core.TextContent("# Deploy\n\nShip it.")
	modern := tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{
		Message: core.Message{Role: "user", Content: []core.Content{body}},
		Custom:  map[string]any{"source": "skill", "skill": "deploy", "skill_args": "staging --fast", "skill_command": "/skill:deploy staging --fast"},
	}})
	legacy := tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{
		Message: core.Message{Role: "user", Content: []core.Content{body}},
		Custom:  map[string]any{"skill": "deploy"},
	}})
	sctx.Tree = tree
	RegisterHandlers(sctx)

	for id, want := range map[string]string{modern: "/skill:deploy staging --fast", legacy: "/deploy"} {
		msg, err := QueryTyped[GetBranchMessage, core.Message](b, GetBranchMessage{EntryID: id})
		if err != nil || len(msg.Content) != 1 || msg.Content[0].Text != want {
			t.Fatalf("rewind message = %+v err=%v, want %q", msg.Content, err, want)
		}
	}
	points, err := QueryTyped[GetBranchPoints, []BranchPoint](b, GetBranchPoints{})
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]BranchPoint{}
	for _, p := range points {
		labels[p.EntryID] = p
	}
	if got := labels[modern]; got.Label != "/skill:deploy staging --fast" || got.Source != "skill" {
		t.Fatalf("modern point = %+v", got)
	}
	if got := labels[legacy]; got.Label != "/deploy" || got.Source != "skill" {
		t.Fatalf("legacy point = %+v", got)
	}
}
