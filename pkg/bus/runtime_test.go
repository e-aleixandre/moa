package bus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/tool"
)

// fakeAgentSubscriber wraps fakeAgent to also implement AgentSubscriber.
type fakeAgentSubscriber struct {
	*fakeAgent
	fakeSubscriber
}

func newFakeAgentSubscriber() *fakeAgentSubscriber {
	return &fakeAgentSubscriber{
		fakeAgent: &fakeAgent{},
	}
}

func TestNewSessionRuntime_Works(t *testing.T) {
	fas := newFakeAgentSubscriber()
	fas.model = core.Model{ID: "claude-4", Name: "Claude 4"}

	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Query model via bus.
	m, err := QueryTyped[GetModel, core.Model](rt.Bus, GetModel{})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude-4" {
		t.Fatalf("Model.ID = %q", m.ID)
	}
}

func TestNewSessionRuntime_NilAgent(t *testing.T) {
	_, err := NewSessionRuntime(RuntimeConfig{})
	if err == nil {
		t.Fatal("expected error for nil Agent")
	}
}

func TestNewSessionRuntime_AutoSubscriber(t *testing.T) {
	// fakeAgentSubscriber implements both AgentController and AgentSubscriber.
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent: fas, // implements both interfaces
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
}

func TestNewSessionRestoreStateAcceptsExplicitCustomModel(t *testing.T) {
	state := NewSessionRestoreState(&session.Session{Metadata: map[string]any{
		session.MetaModel: "openai/internal-model",
	}})
	if !state.HasModel {
		t.Fatal("custom provider/model was discarded")
	}
	if state.Model.Provider != "openai" || state.Model.ID != "internal-model" {
		t.Fatalf("custom model = %#v", state.Model)
	}
}

func TestNewSessionRuntime_NoSubscriber(t *testing.T) {
	// fakeAgent does NOT implement AgentSubscriber.
	fa := &fakeAgent{}
	_, err := NewSessionRuntime(RuntimeConfig{
		Agent: fa,
	})
	if err == nil {
		t.Fatal("expected error when Agent doesn't implement AgentSubscriber and no Subscriber provided")
	}
}

func TestSessionRuntime_StateInitiallyIdle(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.State.Current() != StateIdle {
		t.Fatalf("state = %q, want idle", rt.State.Current())
	}
}

func TestSessionRuntime_Close_Idempotent(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}

	rt.Close()
	rt.Close() // should not panic
}

func TestSessionRuntime_Close_AbortsAgent(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}

	rt.Close()
	if !fas.wasAborted() {
		t.Fatal("Abort not called on Close")
	}
}

func TestSessionRuntime_DefaultSessionID(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.ID != "default" {
		t.Fatalf("ID = %q, want 'default'", rt.ID)
	}
}

func TestSessionRuntime_CustomSessionID(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		SessionID:  "custom-123",
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.ID != "custom-123" {
		t.Fatalf("ID = %q", rt.ID)
	}
}

func TestSessionRuntime_FullLifecycle(t *testing.T) {
	fas := newFakeAgentSubscriber()
	fas.sendResult = []core.AgentMessage{
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "text", Text: "hello from runtime"},
		}}},
	}

	fp := &fakePersister{}
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
		Persister:  fp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Subscribe to RunEnded.
	gotRunEnded := make(chan RunEnded, 1)
	rt.Bus.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	// Send prompt.
	if err := rt.Bus.Execute(SendPrompt{Text: "hello"}); err != nil {
		t.Fatal(err)
	}

	// Wait for completion.
	re := waitForRunEnded(t, gotRunEnded, rt.Bus)
	if re.FinalText != "hello from runtime" {
		t.Fatalf("FinalText = %q", re.FinalText)
	}
	if re.Err != nil {
		t.Fatalf("Err = %v", re.Err)
	}

	// State back to idle.
	if rt.State.Current() != StateIdle {
		t.Fatalf("state = %q", rt.State.Current())
	}

	// Persister should have been called.
	// Give persistence reactor time to process.
	rt.Bus.Drain(time.Second)
	time.Sleep(50 * time.Millisecond)
	rt.Bus.Drain(time.Second)
	if fp.count() == 0 {
		t.Fatal("persister was not called")
	}
}

func TestSessionRuntime_FullLifecycle_Error(t *testing.T) {
	fas := newFakeAgentSubscriber()
	fas.sendErr = errors.New("boom")

	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	gotRunEnded := make(chan RunEnded, 1)
	rt.Bus.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	if err := rt.Bus.Execute(SendPrompt{Text: "fail"}); err != nil {
		t.Fatal(err)
	}

	re := waitForRunEnded(t, gotRunEnded, rt.Bus)
	if re.Err == nil || re.Err.Error() != "boom" {
		t.Fatalf("Err = %v", re.Err)
	}
	if rt.State.Current() != StateError {
		t.Fatalf("state = %q, want error", rt.State.Current())
	}
}

func TestSessionRuntime_BridgeForwards(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Verify bridge forwards agent events to bus.
	got := make(chan AgentStarted, 1)
	rt.Bus.Subscribe(func(e AgentStarted) { got <- e })

	fas.emit(core.AgentEvent{Type: core.AgentEventStart})
	rt.Bus.Drain(time.Second)
	select {
	case e := <-got:
		if e.SessionID != "default" {
			t.Fatalf("SessionID = %q", e.SessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for bridged event")
	}
}

// TestSessionRuntime_Flush_PersistsLastTurn demonstrates the lost-last-turn
// shutdown fix: a turn that completed just before shutdown must reach disk even
// if the async RunEnded→TreeSynced→save chain never ran. Here RunEnded is never
// published, so the only path to disk is the synchronous Flush.
func TestSessionRuntime_Flush_PersistsLastTurn(t *testing.T) {
	fas := newFakeAgentSubscriber()
	fp := &fakeTreePersister{}
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
		Persister:  fp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Simulate a turn that just completed: the agent gained messages but no
	// RunEnded (and thus no TreeSynced→save) has fired yet.
	if err := fas.LoadState([]core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hi")}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	if fp.treeSnapCount() != 0 {
		t.Fatalf("expected no snapshot before Flush, got %d", fp.treeSnapCount())
	}

	// Flush must fold the turn into the tree and persist it synchronously.
	if err := rt.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if fp.treeSnapCount() != 1 {
		t.Fatalf("expected 1 snapshot after Flush, got %d", fp.treeSnapCount())
	}
	if got := len(fp.lastTree()); got != 2 {
		t.Fatalf("persisted tree = %d entries, want 2 (last turn must be included)", got)
	}
}

func TestSessionRuntime_Flush_NoPersister(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if err := rt.Flush(); err != nil {
		t.Fatalf("Flush with no persister should return nil, got %v", err)
	}
}

func TestSessionRuntime_Context(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.Context() == nil {
		t.Fatal("Context() returned nil")
	}
	if rt.Context().Bus != rt.Bus {
		t.Fatal("Context().Bus != rt.Bus")
	}
}

func newTestRuntime(t *testing.T) *SessionRuntime {
	t.Helper()
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt
}

func TestWaitSettled_ReturnsWhenRunEnds(t *testing.T) {
	rt := newTestRuntime(t)
	if err := rt.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	// Simulate the run goroutine settling shortly after shutdown begins.
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = rt.State.Transition(StateIdle)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	if !rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = false, want true (run should have settled)")
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("WaitSettled returned too early (%v); it did not wait for the transition", elapsed)
	}
	if s := rt.State.Current(); s != StateIdle {
		t.Fatalf("state = %s, want idle", s)
	}
}

func TestWaitSettled_ReturnsImmediatelyWhenIdle(t *testing.T) {
	rt := newTestRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = false for an already-idle session")
	}
}

func TestWaitQuiescent_WaitsForAutonomousBackgroundWork(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{Agent: fas.fakeAgent, Subscriber: &fas.fakeSubscriber})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// These are the three independent sources of follow-up work that may outlive
	// a foreground RunEnded: auto-verify, goal verification, and an async child.
	rt.sctx.beginAutoVerify()
	rt.sctx.beginGoalVerify()
	rt.Bus.Publish(SubagentStarted{JobID: "child"})

	go func() {
		time.Sleep(20 * time.Millisecond)
		rt.sctx.endAutoVerify()
		time.Sleep(20 * time.Millisecond)
		rt.sctx.endGoalVerify()
		time.Sleep(20 * time.Millisecond)
		rt.Bus.Publish(SubagentEnded{JobID: "child", Status: "completed"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if !rt.WaitQuiescent(ctx) {
		t.Fatal("WaitQuiescent = false, want true")
	}
	if elapsed := time.Since(started); elapsed < 45*time.Millisecond {
		t.Fatalf("WaitQuiescent returned after %v, before background work ended", elapsed)
	}
}

// rebindablePersister is a fake SessionPersister that also implements
// SessionRebinder, for testing LoadSession's rebind behavior.
type rebindablePersister struct {
	snapshots int
	rebindTo  *session.Session
}

func (p *rebindablePersister) Snapshot(_ []core.AgentMessage, _ int, _ map[string]any) error {
	p.snapshots++
	return nil
}
func (p *rebindablePersister) RebindSession(sess *session.Session) { p.rebindTo = sess }

func TestSessionRuntime_LoadSession_RestoresHistoryAndRebindsPersister(t *testing.T) {
	fas := newFakeAgentSubscriber()
	persister := &rebindablePersister{}
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
		Persister:  persister,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Agent starts empty.
	if len(fas.Messages()) != 0 {
		t.Fatalf("expected empty agent, got %d messages", len(fas.Messages()))
	}

	// Build a v2 session with two message entries.
	tree := session.NewTree()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hola")}}}})
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("qué tal")}}}})
	entries, leafID := tree.Snapshot()
	sess := &session.Session{ID: "s2", Version: session.SessionVersion, Entries: entries, LeafID: leafID}

	if err := rt.LoadSession(sess); err != nil {
		t.Fatal(err)
	}

	// Agent history is now loaded (not amnesiac).
	if got := len(fas.Messages()); got != 2 {
		t.Fatalf("expected 2 messages loaded into agent, got %d", got)
	}
	// Persister was re-pointed at the new session.
	if persister.rebindTo != sess {
		t.Fatalf("expected persister rebound to sess, got %v", persister.rebindTo)
	}
	// The runtime's tree reflects the loaded session.
	if got := len(rt.Context().Tree.AllMessages()); got != 2 {
		t.Fatalf("expected tree with 2 messages, got %d", got)
	}
}

func TestWaitSettled_TimesOutWhileRunning(t *testing.T) {
	rt := newTestRuntime(t)
	if err := rt.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = true, want false (run never settled)")
	}
	if s := rt.State.Current(); s != StateRunning {
		t.Fatalf("state = %s, want running", s)
	}
}

// fileSessionPersister is a TUI-shaped persister backed by real session JSON.
// It lets the switching regression test verify disk state, not merely a fake
// Snapshot call's arguments.
type fileSessionPersister struct {
	mu        sync.Mutex
	store     *session.FileStore
	session   *session.Session
	snapshots int
}

func (p *fileSessionPersister) RebindSession(sess *session.Session) {
	p.mu.Lock()
	p.session = sess
	p.mu.Unlock()
}

func (p *fileSessionPersister) Snapshot(messages []core.AgentMessage, epoch int, metadata map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		return nil
	}
	p.session.Messages = append([]core.AgentMessage(nil), messages...)
	p.session.CompactionEpoch = epoch
	p.session.Metadata = metadata
	p.snapshots++
	return p.store.Save(p.session)
}

func (p *fileSessionPersister) SnapshotTree(entries []session.Entry, leafID string, metadata map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		return nil
	}
	p.session.Version = session.SessionVersion
	p.session.Entries = append([]session.Entry(nil), entries...)
	p.session.LeafID = leafID
	p.session.Messages = nil
	p.session.CompactionEpoch = 0
	p.session.Metadata = metadata
	p.snapshots++
	return p.store.Save(p.session)
}

func (p *fileSessionPersister) snapshotCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshots
}

func TestSessionRuntime_SwitchSession_RestoresMetadataTransactionally(t *testing.T) {
	base := t.TempDir()
	store, err := session.NewFileStore(base, "")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	extraPath := t.TempDir()
	planPath := filepath.Join(t.TempDir(), "A-plan.md")
	if err := os.WriteFile(planPath, []byte("# A plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	makeEntries := func(text string) ([]session.Entry, string) {
		tree := session.NewTree()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{
			Role: "user", Content: []core.Content{core.TextContent(text)},
		})})
		entries, leafID := tree.Snapshot()
		return entries, leafID
	}

	modelDefault, ok := core.ResolveModel("claude-fable-5")
	if !ok {
		t.Fatal("default model not found")
	}
	modelA, ok := core.ResolveModel("gpt-5.6-sol")
	if !ok {
		t.Fatal("session A model not found")
	}

	tasksA := tasks.NewStore()
	tasksA.Create("A-only task", "must never leak into B", nil)
	metadataA := map[string]any{
		session.MetaModel:          "openai/gpt-5.6-sol",
		session.MetaThinking:       "high",
		session.MetaPermissionMode: "yolo",
		session.MetaPathScope:      "unrestricted",
		session.MetaAllowedPaths:   []any{extraPath},
		"planmode": map[string]any{
			"mode":      "planning",
			"plan_file": planPath,
		},
	}
	for key, value := range tasksA.SaveToMetadata() {
		metadataA[key] = value
	}

	sessA := store.Create()
	sessA.Metadata = metadataA
	sessA.Entries, sessA.LeafID = makeEntries("session A history")
	if err := store.Save(sessA); err != nil {
		t.Fatal(err)
	}

	// B intentionally has no metadata. It must receive startup defaults for
	// model/thinking/permission/tasks/plan, and workspace-only paths rather
	// than inheriting A's unrestricted policy.
	sessB := store.Create()
	sessB.Metadata = nil
	sessB.Entries, sessB.LeafID = makeEntries("session B history")
	if err := store.Save(sessB); err != nil {
		t.Fatal(err)
	}

	fas := newFakeAgentSubscriber()
	fas.model = modelDefault
	fas.thinkingLevel = "medium"
	taskStore := tasks.NewStore()
	registry := core.NewRegistry()
	pm := planmode.New(planmode.Config{
		Registry:   registry,
		SessionDir: t.TempDir(),
		TaskStore:  taskStore,
	})
	gate := permission.New(permission.ModeAsk, permission.Config{Allow: []string{"read"}})
	// This deliberately differs from the secure legacy fallback expected for B.
	pathPolicy := tool.NewPathPolicy(workspace, nil, true)
	persister := &fileSessionPersister{store: store}
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
		TaskStore:  taskStore,
		PlanMode:   pm,
		Gate:       gate,
		GateConfig: gate.SnapshotConfig(),
		PathPolicy: pathPolicy,
		ProviderFactory: func(core.Model) (core.Provider, error) {
			return nil, nil
		},
		Persister: persister,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	configChanges := make(chan ConfigChanged, 4)
	loaded := make(chan SessionLoaded, 4)
	rt.Bus.Subscribe(func(e ConfigChanged) { configChanges <- e })
	rt.Bus.Subscribe(func(e SessionLoaded) { loaded <- e })

	assertRuntime := func(wantModel core.Model, wantThinking, wantPermission, wantScope, wantTask, wantPlan, wantHistory string) {
		t.Helper()
		model, err := QueryTyped[GetModel, core.Model](rt.Bus, GetModel{})
		if err != nil || !sameModel(model, wantModel) {
			t.Fatalf("model = %+v, %v; want %+v", model, err, wantModel)
		}
		thinking, err := QueryTyped[GetThinkingLevel, string](rt.Bus, GetThinkingLevel{})
		if err != nil || thinking != wantThinking {
			t.Fatalf("thinking = %q, %v; want %q", thinking, err, wantThinking)
		}
		permissionMode, err := QueryTyped[GetPermissionMode, string](rt.Bus, GetPermissionMode{})
		if err != nil || permissionMode != wantPermission {
			t.Fatalf("permission = %q, %v; want %q", permissionMode, err, wantPermission)
		}
		path, err := QueryTyped[GetPathPolicy, PathPolicyInfo](rt.Bus, GetPathPolicy{})
		if err != nil || path.Scope != wantScope {
			t.Fatalf("path scope = %+v, %v; want %q", path, err, wantScope)
		}
		taskList, err := QueryTyped[GetTasks, []tasks.Task](rt.Bus, GetTasks{})
		if err != nil {
			t.Fatal(err)
		}
		if wantTask == "" {
			if len(taskList) != 0 {
				t.Fatalf("tasks = %+v, want none", taskList)
			}
		} else if len(taskList) != 1 || taskList[0].Title != wantTask {
			t.Fatalf("tasks = %+v, want %q", taskList, wantTask)
		}
		plan, err := QueryTyped[GetPlanMode, PlanModeInfo](rt.Bus, GetPlanMode{})
		if err != nil || plan.Mode != wantPlan {
			t.Fatalf("plan = %+v, %v; want %q", plan, err, wantPlan)
		}
		messages := fas.Messages()
		if len(messages) != 1 || len(messages[0].Content) != 1 || messages[0].Content[0].Text != wantHistory {
			t.Fatalf("history = %+v, want %q", messages, wantHistory)
		}
	}
	loadJSON := func(id string) *session.Session {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(store.Dir(), id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var saved session.Session
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		return &saved
	}

	if err := rt.SwitchSession(sessA); err != nil {
		t.Fatal(err)
	}
	rt.Bus.Drain(time.Second)
	assertRuntime(modelA, "high", "yolo", "unrestricted", "A-only task", "planning", "session A history")

	// A non-zero cost must be reset as part of the switch, without a separate
	// SessionCostUpdated event/config save.
	rt.Context().addSessionCost(12.34)
	if err := rt.SwitchSession(sessB); err != nil {
		t.Fatal(err)
	}
	rt.Bus.Drain(time.Second)
	assertRuntime(modelDefault, "medium", "ask", "workspace", "", "off", "session B history")
	if cost, err := QueryTyped[GetSessionCost, float64](rt.Bus, GetSessionCost{}); err != nil || cost != 0 {
		t.Fatalf("session cost = %v, %v; want 0", cost, err)
	}
	if paths := pathPolicy.AllowedPaths(); len(paths) != 0 {
		t.Fatalf("B inherited allowed paths: %v", paths)
	}
	savedB := loadJSON(sessB.ID)
	if savedB.Metadata[session.MetaModel] != "anthropic/claude-fable-5" ||
		savedB.Metadata[session.MetaThinking] != "medium" ||
		savedB.Metadata[session.MetaPermissionMode] != "ask" ||
		savedB.Metadata[session.MetaPathScope] != "workspace" {
		t.Fatalf("B JSON metadata = %#v", savedB.Metadata)
	}
	if _, ok := savedB.Metadata[session.MetaAllowedPaths]; ok {
		t.Fatalf("B JSON inherited allowed_paths: %#v", savedB.Metadata)
	}

	if err := rt.SwitchSession(sessA); err != nil {
		t.Fatal(err)
	}
	rt.Bus.Drain(time.Second)
	assertRuntime(modelA, "high", "yolo", "unrestricted", "A-only task", "planning", "session A history")
	savedA := loadJSON(sessA.ID)
	if savedA.Metadata[session.MetaModel] != "openai/gpt-5.6-sol" ||
		savedA.Metadata[session.MetaThinking] != "high" ||
		savedA.Metadata[session.MetaPermissionMode] != "yolo" ||
		savedA.Metadata[session.MetaPathScope] != "unrestricted" {
		t.Fatalf("A JSON metadata = %#v", savedA.Metadata)
	}
	if got, _ := savedA.PathMeta(); got != "unrestricted" {
		t.Fatalf("A JSON path scope = %q", got)
	}
	if paths := pathPolicy.AllowedPaths(); len(paths) != 1 || paths[0] != extraPath {
		t.Fatalf("A allowed paths = %v, want [%s]", paths, extraPath)
	}

	if got := persister.snapshotCount(); got != 3 {
		t.Fatalf("snapshots = %d, want one final snapshot per switch", got)
	}
	if got := len(configChanges); got != 0 {
		t.Fatalf("ConfigChanged events during switch = %d, want 0", got)
	}
	if got := len(loaded); got != 3 {
		t.Fatalf("SessionLoaded events = %d, want 3", got)
	}
}
