package serve

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

// --- Mock provider ---

type mockHandler func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error)

type mockProvider struct {
	calls    int
	handlers []mockHandler
}

func newMockProvider(handlers ...mockHandler) *mockProvider {
	return &mockProvider{handlers: handlers}
}

func (m *mockProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	idx := m.calls
	m.calls++
	if idx >= len(m.handlers) {
		return simpleResponse("done"), nil
	}
	return m.handlers[idx](ctx, req)
}

func simpleResponse(text string) <-chan core.AssistantEvent {
	ch := make(chan core.AssistantEvent, 10)
	go func() {
		defer close(ch)
		msg := core.Message{
			Role:       "assistant",
			Content:    []core.Content{core.TextContent(text)},
			StopReason: "end_turn",
			Timestamp:  time.Now().Unix(),
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextStart, ContentIndex: 0}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: text}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextEnd, ContentIndex: 0}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}()
	return ch
}

func simpleResponseHandler(text string) mockHandler {
	return func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		return simpleResponse(text), nil
	}
}

func errorHandler(err error) mockHandler {
	return func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		go func() {
			defer close(ch)
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: err}
		}()
		return ch, nil
	}
}

// --- Helpers ---

func newTestManager(t *testing.T, ctx context.Context, provider core.Provider) *Manager {
	t.Helper()
	return newTestManagerWithRoot(t, ctx, provider, t.TempDir())
}

func newTestManagerWithRoot(t *testing.T, ctx context.Context, provider core.Provider, root string) *Manager {
	t.Helper()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   root,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  t.TempDir(),
	})
	// Ensure all sessions are properly shut down before TempDir cleanup.
	// Without this, async persistence reactors can race with directory removal.
	t.Cleanup(func() {
		mgr.mu.RLock()
		ids := make([]string, 0, len(mgr.sessions))
		for id := range mgr.sessions {
			ids = append(ids, id)
		}
		mgr.mu.RUnlock()
		for _, id := range ids {
			_ = mgr.Delete(id)
		}
	})
	return mgr
}

// sessState returns the current session state via bus query.
func sessState(sess *ManagedSession) SessionState {
	return SessionState(sess.runtime.State.Current())
}

// sessError returns the last error via bus query.
func sessError(sess *ManagedSession) string {
	e, _ := bus.QueryTyped[bus.GetSessionError, string](sess.runtime.Bus, bus.GetSessionError{})
	return e
}

func pollUntil(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

// ===========================================================================
// Tests
// ===========================================================================

func TestCreateSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("hello"))
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" {
		t.Fatal("expected non-empty session ID")
	}
	if sessState(sess) != StateIdle {
		t.Fatalf("expected idle, got %s", sessState(sess))
	}
	sess.mu.Lock()
	title := sess.Title
	sess.mu.Unlock()
	if title != "test" {
		t.Fatalf("expected title 'test', got %q", title)
	}

	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
}

func TestSend_StateTransitions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("reply"))
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.Send(sess.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for idle state (run complete).
	pollUntil(t, 5*time.Second, "state idle", func() bool {
		return sessState(sess) == StateIdle
	})
}

func TestSend_Error(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(errorHandler(fmt.Errorf("provider error")))
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.Send(sess.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 5*time.Second, "state becomes error", func() bool {
		return sessState(sess) == StateError
	})

	if sessError(sess) == "" {
		t.Fatal("expected error text to be set")
	}

	// Session should still be usable.
	_, err = mgr.Send(sess.ID, "retry")
	if err != nil {
		t.Fatalf("expected session to accept new message after error, got: %v", err)
	}

	pollUntil(t, 5*time.Second, "state idle after retry", func() bool {
		return sessState(sess) == StateIdle
	})
}

func TestSend_WhileBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slowHandler := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return
			}
			msg := core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.TextContent("slow")},
				StopReason: "end_turn",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	prov := newMockProvider(slowHandler)
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.Send(sess.ID, "first")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 2*time.Second, "state running", func() bool {
		return sessState(sess) == StateRunning
	})

	action, err := mgr.Send(sess.ID, "second")
	if err != nil {
		t.Fatalf("expected steer, got error: %v", err)
	}
	if action != "steer" {
		t.Fatalf("expected action=steer, got %q", action)
	}
}

func TestDelete_WhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockingHandler := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			<-ctx.Done()
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
		}()
		return ch, nil
	}

	prov := newMockProvider(blockingHandler)
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID

	_, err = mgr.Send(id, "hello")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 2*time.Second, "state running", func() bool {
		return sessState(sess) == StateRunning
	})

	err = mgr.Delete(id)
	if err != nil {
		t.Fatal(err)
	}

	_, ok := mgr.Get(id)
	if ok {
		t.Fatal("expected session to be removed")
	}
}

func TestList_MultipleSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := newTestManager(t, ctx, prov)

	s1, _ := mgr.CreateSession(CreateOpts{Title: "first"})
	s2, _ := mgr.CreateSession(CreateOpts{Title: "second"})

	s1.mu.Lock()
	s1.Updated = time.Now().Add(-time.Second)
	s1.mu.Unlock()

	list := mgr.List()
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
	if list[0].ID != s2.ID {
		t.Fatalf("expected %s first, got %s", s2.ID, list[0].ID)
	}
}

func TestSend_AutoTitle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("reply"))
	mgr := newTestManager(t, ctx, prov)

	sess, _ := mgr.CreateSession(CreateOpts{})

	_, err := mgr.Send(sess.ID, "Refactoriza el módulo de auth")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 5*time.Second, "run complete", func() bool {
		return sessState(sess) == StateIdle
	})

	sess.mu.Lock()
	title := sess.Title
	sess.mu.Unlock()
	if title != "Refactoriza el módulo de auth" {
		t.Fatalf("expected auto-title, got %q", title)
	}
}

func TestCreateSession_WithCWD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	prov := newMockProvider()
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if sess.CWD == "" {
		t.Fatal("expected non-empty CWD")
	}
}

func TestCreateSession_InvalidCWD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := newTestManager(t, ctx, prov)

	_, err := mgr.CreateSession(CreateOpts{CWD: "/nonexistent/path/does/not/exist"})
	if err == nil {
		t.Fatal("expected error for invalid CWD")
	}
	if !errors.Is(err, ErrInvalidCWD) {
		t.Fatalf("expected ErrInvalidCWD, got: %v", err)
	}
}

func TestDelete_CancelsSessionContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockingHandler := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			<-ctx.Done()
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
		}()
		return ch, nil
	}

	prov := newMockProvider(blockingHandler)
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID
	sessCtx := sess.infra.sessionCtx

	_, err = mgr.Send(id, "hello")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 2*time.Second, "state running", func() bool {
		return sessState(sess) == StateRunning
	})

	err = mgr.Delete(id)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-sessCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("expected session context to be cancelled")
	}
}

func TestCreateSession_PermissionsFromConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	prov := newMockProvider()
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	// Default permission mode is yolo → gate should be nil.
	if sess.runtime.Context().GetGate() != nil {
		t.Fatal("expected nil gate for yolo mode")
	}
}

// --- Persistence tests ---

func TestAutoSave_AfterRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	sessionBase := t.TempDir()
	prov := newMockProvider(simpleResponseHandler("hello back"))
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.CreateSession(CreateOpts{Title: "persist-test"})
	if err != nil {
		t.Fatal(err)
	}

	if sess.persister == nil {
		t.Fatal("expected persister to be attached")
	}

	_, err = mgr.Send(sess.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 5*time.Second, "state idle", func() bool {
		return sessState(sess) == StateIdle
	})

	// Give persistence reactor time to fire.
	time.Sleep(100 * time.Millisecond)

	loaded, _, err := session.FindSession(sessionBase, sess.ID)
	if err != nil {
		t.Fatalf("FindSession after auto-save: %v", err)
	}
	if len(loaded.Messages) == 0 {
		t.Fatal("expected saved messages")
	}
	if loaded.Title != "persist-test" {
		t.Errorf("saved title = %q, want 'persist-test'", loaded.Title)
	}
}

func TestList_IncludesSavedSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	sessionBase := t.TempDir()

	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "disk-session"
	saved.Metadata = map[string]any{"model": "test", "cwd": dir}
	_ = store.Save(saved)

	prov := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 saved session, got %d", len(list))
	}
	if list[0].State != StateSaved {
		t.Errorf("state = %q, want 'saved'", list[0].State)
	}
}

func TestResumeSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	sessionBase := t.TempDir()

	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "resume-me"
	saved.Metadata = map[string]any{"model": "test-model", "cwd": dir}
	saved.Messages = []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("prior message")),
	}
	_ = store.Save(saved)

	prov := newMockProvider(simpleResponseHandler("resumed reply"))
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.ResumeSession(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != saved.ID {
		t.Errorf("ID = %q, want %q", sess.ID, saved.ID)
	}

	history := sess.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 message, got %d", len(history))
	}

	list := mgr.List()
	found := false
	for _, info := range list {
		if info.ID == saved.ID && info.State == StateIdle {
			found = true
		}
	}
	if !found {
		t.Error("resumed session not found as idle in list")
	}
}

func TestResumeSession_AlreadyActive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	sessionBase := t.TempDir()

	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "active"
	saved.Metadata = map[string]any{"model": "test-model", "cwd": dir}
	_ = store.Save(saved)

	prov := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	_, err = mgr.ResumeSession(saved.ID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.ResumeSession(saved.ID)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestResumeSession_NotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   t.TempDir(),
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  t.TempDir(),
	})

	_, err := mgr.ResumeSession("nonexistent")
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_RemovesSavedFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	sessionBase := t.TempDir()
	prov := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.CreateSession(CreateOpts{Title: "to-delete"})
	if err != nil {
		t.Fatal(err)
	}

	_, _, findErr := session.FindSession(sessionBase, sess.ID)
	if findErr != nil {
		t.Fatalf("expected session on disk: %v", findErr)
	}

	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}

	_, _, findErr = session.FindSession(sessionBase, sess.ID)
	if !errors.Is(findErr, session.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", findErr)
	}
}

func TestDelete_SavedOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	sessionBase := t.TempDir()

	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "disk-only"
	saved.Metadata = map[string]any{"model": "test", "cwd": dir}
	_ = store.Save(saved)

	prov := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	if err := mgr.Delete(saved.ID); err != nil {
		t.Fatal(err)
	}

	_, _, findErr := session.FindSession(sessionBase, saved.ID)
	if !errors.Is(findErr, session.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", findErr)
	}
}

func TestCancel_WhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()

	blockingHandler := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			<-ctx.Done()
			msg := core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.TextContent("cancelled")},
				StopReason: "end_turn",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	prov := newMockProvider(blockingHandler)
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.Send(sess.ID, "block"); err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 2*time.Second, "state running", func() bool {
		return sessState(sess) == StateRunning
	})

	if err := mgr.Cancel(sess.ID); err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 5*time.Second, "state idle after cancel", func() bool {
		return sessState(sess) == StateIdle
	})
}

func TestCancel_WhileIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	prov := newMockProvider()
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	err = mgr.Cancel(sess.ID)
	if err == nil {
		t.Fatal("expected error cancelling idle session")
	}
}

func TestExecCommand_Clear(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	prov := newMockProvider(simpleResponseHandler("hello"))
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	_, _ = mgr.Send(sess.ID, "hi")
	pollUntil(t, 5*time.Second, "run complete", func() bool {
		return sessState(sess) == StateIdle
	})

	if len(sess.History()) == 0 {
		t.Fatal("expected messages after send")
	}

	result, err := mgr.ExecCommand(sess.ID, "/clear")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("expected OK, got: %s", result.Message)
	}
	if len(sess.History()) != 0 {
		t.Fatalf("expected 0 messages after clear, got %d", len(sess.History()))
	}
}

func TestExecCommand_UnknownCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := newTestManager(t, ctx, newMockProvider())
	sess, _ := mgr.CreateSession(CreateOpts{})

	result, err := mgr.ExecCommand(sess.ID, "/nope")
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("expected not OK for unknown command")
	}
}

func TestExecCommand_NotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider())

	_, err := mgr.ExecCommand("nonexistent", "/clear")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
