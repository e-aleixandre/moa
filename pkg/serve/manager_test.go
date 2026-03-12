package serve

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

// --- Mock provider ---

// mockProvider returns scripted responses. Implements core.Provider.
type mockHandler func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error)

type mockProvider struct {
	mu       sync.Mutex
	calls    int
	handlers []mockHandler
}

func newMockProvider(handlers ...mockHandler) *mockProvider {
	return &mockProvider{handlers: handlers}
}

func (m *mockProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.mu.Unlock()

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

// newTestManager creates a Manager with a mock provider factory and an isolated
// session base dir so tests never read real user sessions.
func newTestManager(t *testing.T, ctx context.Context, provider core.Provider) *Manager {
	t.Helper()
	return newTestManagerWithRoot(t, ctx, provider, t.TempDir())
}

func newTestManagerWithRoot(t *testing.T, ctx context.Context, provider core.Provider, root string) *Manager {
	t.Helper()
	return NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   root,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  t.TempDir(),
	})
}

// pollUntil polls fn at 10ms intervals until it returns true or timeout.
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
	if sess.State != StateIdle {
		t.Fatalf("expected idle, got %s", sess.State)
	}
	if sess.Title != "test" {
		t.Fatalf("expected title 'test', got %q", sess.Title)
	}

	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if list[0].ID != sess.ID {
		t.Fatal("list ID mismatch")
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

	// Subscribe to events to track state changes.
	ch, unsub := sess.Subscribe()
	defer unsub()

	_, err = mgr.Send(sess.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for running state broadcast.
	pollUntil(t, 2*time.Second, "state_change running", func() bool {
		for {
			select {
			case evt := <-ch:
				if evt.Type == "state_change" {
					if data, ok := evt.Data.(map[string]any); ok {
						if data["state"] == "running" {
							return true
						}
					}
				}
			default:
				return false
			}
		}
	})

	// Wait for idle state (run complete).
	pollUntil(t, 5*time.Second, "state_change idle", func() bool {
		for {
			select {
			case evt := <-ch:
				if evt.Type == "state_change" {
					if data, ok := evt.Data.(map[string]any); ok {
						if data["state"] == "idle" {
							return true
						}
					}
				}
			default:
				return false
			}
		}
	})

	sess.mu.Lock()
	state := sess.State
	sess.mu.Unlock()
	if state != StateIdle {
		t.Fatalf("expected idle after run, got %s", state)
	}
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

	// Wait for error state.
	pollUntil(t, 5*time.Second, "state becomes error", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateError
	})

	sess.mu.Lock()
	errText := sess.Error
	sess.mu.Unlock()
	if errText == "" {
		t.Fatal("expected error text to be set")
	}

	// Session should still be usable — can send again.
	// Provider will auto-reply with "done" on next call.
	_, err = mgr.Send(sess.ID, "retry")
	if err != nil {
		t.Fatalf("expected session to accept new message after error, got: %v", err)
	}

	pollUntil(t, 5*time.Second, "state becomes idle after retry", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateIdle
	})
}

func TestSend_WhileBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a slow provider that blocks.
	slowHandler := func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			time.Sleep(500 * time.Millisecond)
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

	// Wait until running.
	pollUntil(t, 2*time.Second, "state becomes running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateRunning
	})

	// Second send should steer (not error).
	action, err := mgr.Send(sess.ID, "second")
	if err != nil {
		t.Fatalf("expected steer, got error: %v", err)
	}
	if action != "steer" {
		t.Fatalf("expected action=steer, got %q", action)
	}

	// State should still be running.
	sess.mu.Lock()
	state := sess.State
	sess.mu.Unlock()
	if state != StateRunning {
		t.Fatalf("expected running after steer, got %s", state)
	}
}

func TestDelete_WhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Provider that blocks until context cancelled.
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

	pollUntil(t, 2*time.Second, "state becomes running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateRunning
	})

	err = mgr.Delete(id)
	if err != nil {
		t.Fatal(err)
	}

	// Session should be removed.
	_, ok := mgr.Get(id)
	if ok {
		t.Fatal("expected session to be removed after delete")
	}

	// List should be empty.
	if len(mgr.List()) != 0 {
		t.Fatal("expected empty list after delete")
	}
}

func TestList_MultipleSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := newTestManager(t, ctx, prov)

	s1, _ := mgr.CreateSession(CreateOpts{Title: "first"})
	s2, _ := mgr.CreateSession(CreateOpts{Title: "second"})

	// Force different Updated timestamps for deterministic sort.
	s1.mu.Lock()
	s1.Updated = time.Now().Add(-time.Second)
	s1.mu.Unlock()

	list := mgr.List()
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
	// Sorted by updated desc — second should be first.
	if list[0].ID != s2.ID {
		t.Fatalf("expected %s first, got %s", s2.ID, list[0].ID)
	}
	if list[1].ID != s1.ID {
		t.Fatalf("expected %s second, got %s", s1.ID, list[1].ID)
	}
}

func TestBroadcast_SlowConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := newTestManager(t, ctx, prov)

	sess, _ := mgr.CreateSession(CreateOpts{})

	// Subscribe but never read.
	ch, _ := sess.Subscribe()

	// Fill the channel.
	for i := 0; i < 600; i++ {
		sess.broadcast(Event{Type: "test"})
	}

	// The slow consumer should have been disconnected (channel closed).
	select {
	case _, ok := <-ch:
		if ok {
			// Got a message — that's fine, drain until closed.
			for range ch {
			}
		}
		// Channel is closed — good.
	case <-time.After(time.Second):
		t.Fatal("expected slow consumer channel to be closed")
	}

	// No subscribers left.
	sess.mu.Lock()
	count := len(sess.subscribers)
	sess.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 subscribers after slow consumer eviction, got %d", count)
	}
}

func TestSend_AutoTitle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("reply"))
	mgr := newTestManager(t, ctx, prov)

	sess, _ := mgr.CreateSession(CreateOpts{}) // no title
	if sess.Title != "" {
		t.Fatalf("expected empty title, got %q", sess.Title)
	}

	_, err := mgr.Send(sess.ID, "Refactoriza el módulo de auth")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 5*time.Second, "run complete", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateIdle
	})

	sess.mu.Lock()
	title := sess.Title
	sess.mu.Unlock()
	if title != "Refactoriza el módulo de auth" {
		t.Fatalf("expected auto-title from first message, got %q", title)
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
	// CWD should be canonicalized.
	if sess.CWD == "" {
		t.Fatal("expected non-empty CWD")
	}
	// SessionInfo should include CWD.
	sess.mu.Lock()
	info := sess.info()
	sess.mu.Unlock()
	if info.CWD == "" {
		t.Fatal("expected CWD in SessionInfo")
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

func TestCreateSession_DefaultCWD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	prov := newMockProvider()
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// Should default to WorkspaceRoot (canonicalized).
	if sess.CWD == "" {
		t.Fatal("expected CWD to default to workspace root")
	}
}

func TestDelete_CancelsSessionContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Provider that blocks until context cancelled.
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

	// Capture session context before delete.
	sessCtx := sess.sessionCtx

	_, err = mgr.Send(id, "hello")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 2*time.Second, "state becomes running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateRunning
	})

	err = mgr.Delete(id)
	if err != nil {
		t.Fatal(err)
	}

	// Session context should be cancelled (which cascades to runCtx).
	select {
	case <-sessCtx.Done():
		// Good — session context was cancelled.
	case <-time.After(2 * time.Second):
		t.Fatal("expected session context to be cancelled after delete")
	}
}

func TestCreateSession_PermissionsFromConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	prov := newMockProvider()
	// MoaCfg here is the server-level default; per-session config loads from CWD.
	// With a fresh temp dir, no .moa/config.json exists so permissions default to yolo.
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	// Default permission mode is yolo → gate should be nil.
	if sess.gate != nil {
		t.Fatal("expected nil gate for yolo mode (default)")
	}
}

func TestCreateSession_CWDInList(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	prov := newMockProvider()
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)

	_, err := mgr.CreateSession(CreateOpts{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}

	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if list[0].CWD == "" {
		t.Fatal("expected CWD in list response")
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

	// Verify persisted file was created.
	if sess.persisted == nil {
		t.Fatal("expected persisted session")
	}

	_, err = mgr.Send(sess.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for idle (run complete + auto-save).
	pollUntil(t, 5*time.Second, "state idle", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateIdle
	})

	// Load from disk and verify messages.
	loaded, _, err := session.FindSession(sessionBase, sess.ID)
	if err != nil {
		t.Fatalf("FindSession after auto-save: %v", err)
	}
	if len(loaded.Messages) == 0 {
		t.Fatal("expected saved messages, got 0")
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

	// Create a saved session on disk directly.
	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "disk-session"
	saved.Metadata = map[string]any{"model": "test", "cwd": dir}
	store.Save(saved)

	// Create manager with same base — it should see the disk session.
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
	if list[0].Title != "disk-session" {
		t.Errorf("title = %q, want 'disk-session'", list[0].Title)
	}
}

func TestResumeSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	sessionBase := t.TempDir()

	// Create a saved session on disk.
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
	store.Save(saved)

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
	if sess.Title != "resume-me" {
		t.Errorf("Title = %q, want 'resume-me'", sess.Title)
	}

	// Messages should be loaded.
	history := sess.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 message, got %d", len(history))
	}

	// Should be active now — List shows it as non-saved.
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

	// Create a saved session.
	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "active"
	saved.Metadata = map[string]any{"model": "test-model", "cwd": dir}
	store.Save(saved)

	prov := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	// Resume once.
	_, err = mgr.ResumeSession(saved.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Resume again — should fail.
	_, err = mgr.ResumeSession(saved.ID)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestResumeSession_NotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	prov := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
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

	// Verify file exists.
	_, _, findErr := session.FindSession(sessionBase, sess.ID)
	if findErr != nil {
		t.Fatalf("expected session on disk: %v", findErr)
	}

	// Delete.
	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}

	// File should be gone.
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

	// Create a saved session on disk directly (not active).
	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "disk-only"
	saved.Metadata = map[string]any{"model": "test", "cwd": dir}
	store.Save(saved)

	prov := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	// Delete by ID (not active in memory).
	if err := mgr.Delete(saved.ID); err != nil {
		t.Fatal(err)
	}

	// File should be gone.
	_, _, findErr := session.FindSession(sessionBase, saved.ID)
	if !errors.Is(findErr, session.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", findErr)
	}
}

func TestCancel_WhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()

	// Slow handler that blocks until context cancelled.
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
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateRunning
	})

	if err := mgr.Cancel(sess.ID); err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 5*time.Second, "state idle after cancel", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateIdle
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
