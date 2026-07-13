package serve

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/session"
)

// --- Mock provider ---

type mockHandler func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error)

type mockProvider struct {
	calls    atomic.Int32 // atomic: background auto-titling may call Stream concurrently
	handlers []mockHandler
}

func newMockProvider(handlers ...mockHandler) *mockProvider {
	return &mockProvider{handlers: handlers}
}

func (m *mockProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	idx := int(m.calls.Add(1) - 1)
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

// delayedResponseHandler emits a full response after delay, unless ctx is
// cancelled first. Used to hold a run in StateRunning for a bounded window.
func delayedResponseHandler(delay time.Duration, text string) mockHandler {
	return func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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

func isolatedTestConfigLoader(t *testing.T, cfg core.MoaConfig) func(string) core.MoaConfig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return func(string) core.MoaConfig { return cfg }
}

func newTestManager(t *testing.T, ctx context.Context, provider core.Provider) *Manager {
	t.Helper()
	return newTestManagerWithRoot(t, ctx, provider, t.TempDir())
}

func newTestManagerWithRoot(t *testing.T, ctx context.Context, provider core.Provider, root string) *Manager {
	t.Helper()
	return newTestManagerWithConfig(t, ctx, provider, root, core.MoaConfig{DisableSandbox: true})
}

func newTestManagerWithConfig(t *testing.T, ctx context.Context, provider core.Provider, root string, moaCfg core.MoaConfig) *Manager {
	t.Helper()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   root,
		MoaCfg:          moaCfg,
		ConfigLoader:    isolatedTestConfigLoader(t, moaCfg),
		SessionBaseDir:  t.TempDir(),
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
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

func TestManagerConfigLoaderIsUsedForSessionBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	var loadedCWD string
	loadedCfg := core.MoaConfig{
		CacheTTL: "1h",
		Permissions: core.PermissionsConfig{
			Mode: "ask",
		},
	}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return newMockProvider(), nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{Permissions: core.PermissionsConfig{Mode: "yolo"}},
		ConfigLoader: func(cwd string) core.MoaConfig {
			loadedCWD = cwd
			return loadedCfg
		},
		SessionBaseDir: t.TempDir(),
	})
	defer mgr.Shutdown()

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if loadedCWD != sess.CWD {
		t.Errorf("loader CWD = %q, want %q", loadedCWD, sess.CWD)
	}
	if gate := sess.runtime.Context().GetGate(); gate == nil || gate.Mode() != permission.ModeAsk {
		t.Fatal("session did not use the injected permission config")
	}
	if sess.cacheTTL != time.Hour {
		t.Errorf("cache TTL = %v, want %v", sess.cacheTTL, time.Hour)
	}
	if sess.infra.mcpMgr != nil {
		t.Fatal("injected config without MCP servers started an MCP manager")
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

	_, _, err = mgr.Send(sess.ID, "hello", nil, "")
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

	_, _, err = mgr.Send(sess.ID, "hello", nil, "")
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
	_, _, err = mgr.Send(sess.ID, "retry", nil, "")
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

	_, _, err = mgr.Send(sess.ID, "first", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 2*time.Second, "state running", func() bool {
		return sessState(sess) == StateRunning
	})

	action, steerID, err := mgr.Send(sess.ID, "second", nil, "c-client-123")
	if err != nil {
		t.Fatalf("expected steer, got error: %v", err)
	}
	if action != "steer" {
		t.Fatalf("expected action=steer, got %q", action)
	}
	// The client-minted ID must be honored verbatim so the optimistic chip
	// reconciles by the same identity it was created with.
	if steerID != "c-client-123" {
		t.Fatalf("expected steer ID to echo the client-supplied ID, got %q", steerID)
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

	_, _, err = mgr.Send(id, "hello", nil, "")
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

	// First call answers the run; the second answers the auto-title request.
	prov := newMockProvider(
		simpleResponseHandler("reply"),
		simpleResponseHandler("Auth module refactor"),
	)
	mgr := newTestManager(t, ctx, prov)

	sess, _ := mgr.CreateSession(CreateOpts{})

	_, _, err := mgr.Send(sess.ID, "Refactoriza el módulo de auth", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	// After the first run, auto-titling replaces the crude first-message title
	// with the LLM-generated one.
	pollUntil(t, 5*time.Second, "auto-title generated", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.Title == "Auth module refactor"
	})
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

// TestCreateSession_InvalidModel is the F16/A6 regression: a model spec that
// mismatches a known model's provider (or is a bare unknown name) must be
// rejected at creation with a clear, immediate error — not accepted and left
// to fail opaquely later at the provider factory.
func TestCreateSession_InvalidModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := newTestManager(t, ctx, prov)

	_, err := mgr.CreateSession(CreateOpts{Model: "openai/sonnet"})
	if err == nil {
		t.Fatal("expected error for provider/model mismatch")
	}
	if !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("expected ErrInvalidModel, got: %v", err)
	}

	_, err = mgr.CreateSession(CreateOpts{Model: "totally-unknown-model"})
	if !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("expected ErrInvalidModel for unknown bare model, got: %v", err)
	}
}

// TestCreateSession_CustomProviderModelStillAllowed ensures F16/A6 doesn't
// regress support for genuine custom models expressed as "provider/model"
// that simply aren't in the registry.
func TestCreateSession_CustomProviderModelStillAllowed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{Model: "mock/my-custom-finetune"})
	if err != nil {
		t.Fatalf("custom provider/model should be accepted, got: %v", err)
	}
	model, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
	if model.Provider != "mock" || model.ID != "my-custom-finetune" {
		t.Fatalf("custom model not preserved, got: %+v", model)
	}
}

// TestReconfigureSession_InvalidModel is the manager-level F16/A6 regression:
// switching to a model spec ValidateModelSpec rejects must fail with
// ErrInvalidModel, without touching the session's current model.
func TestReconfigureSession_InvalidModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("hi"))
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})

	_, err = mgr.ReconfigureSession(sess.ID, "openai/sonnet", "")
	if !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("expected ErrInvalidModel, got: %v", err)
	}

	after, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
	if after != before {
		t.Fatalf("model should be unchanged after rejected reconfigure: before=%+v after=%+v", before, after)
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

	_, _, err = mgr.Send(id, "hello", nil, "")
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
	// Default permission mode is yolo, but its gate stays active to enforce
	// hard-coded download-and-execute confirmations.
	if gate := sess.runtime.Context().GetGate(); gate == nil || gate.Mode() != permission.ModeYolo {
		t.Fatal("expected active yolo gate")
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.CreateSession(CreateOpts{Title: "persist-test"})
	if err != nil {
		t.Fatal(err)
	}

	if sess.persister == nil {
		t.Fatal("expected persister to be attached")
	}

	_, _, err = mgr.Send(sess.ID, "hello", nil, "")
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
	if len(loaded.Entries) == 0 && len(loaded.Messages) == 0 {
		t.Fatal("expected saved messages or entries")
	}
	if loaded.Title != "persist-test" {
		t.Errorf("saved title = %q, want 'persist-test'", loaded.Title)
	}
}

func TestArchiveSession_ActiveSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("reply"))
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}

	if info := sess.info(); info.Archived {
		t.Fatal("expected new session to be unarchived")
	}

	if err := mgr.ArchiveSession(sess.ID, true); err != nil {
		t.Fatalf("ArchiveSession(true): %v", err)
	}
	if info := sess.info(); !info.Archived {
		t.Fatal("expected session to be archived after ArchiveSession(true)")
	}

	var found bool
	for _, si := range mgr.List() {
		if si.ID == sess.ID {
			found = true
			if !si.Archived {
				t.Error("expected List() entry to report Archived = true")
			}
		}
	}
	if !found {
		t.Fatal("session missing from List()")
	}

	if err := mgr.ArchiveSession(sess.ID, false); err != nil {
		t.Fatalf("ArchiveSession(false): %v", err)
	}
	if info := sess.info(); info.Archived {
		t.Fatal("expected session to be unarchived after ArchiveSession(false)")
	}
}

func TestArchiveSession_NotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := newTestManager(t, ctx, prov)

	err := mgr.ArchiveSession("does-not-exist", true)
	if !errors.Is(err, session.ErrNotFound) {
		t.Errorf("ArchiveSession on missing session: got %v, want session.ErrNotFound", err)
	}
}

func TestSend_UnarchivesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("reply"))
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.ArchiveSession(sess.ID, true); err != nil {
		t.Fatalf("ArchiveSession(true): %v", err)
	}
	if info := sess.info(); !info.Archived {
		t.Fatal("expected session to be archived before Send")
	}

	if _, _, err := mgr.Send(sess.ID, "hello", nil, ""); err != nil {
		t.Fatal(err)
	}
	if info := sess.info(); info.Archived {
		t.Error("expected Send to auto-unarchive the session")
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
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

// TestResumeSession_KeepsSystemPrompt guards against the serve regression where
// ResumeSession wiped the agent's system prompt to "" (via SyncPlanMode →
// rebuildSystemPrompt with an empty BaseSystemPrompt), leaving resumed serve
// sessions running with no identity / tool guidance / Persistence section. That
// made OpenAI (and other) models behave erratically and stall. The base prompt
// must survive a resume and reach the provider.
func TestResumeSession_KeepsSystemPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	sessionBase := t.TempDir()

	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "resume-prompt"
	// planmode present + mode "off" is the common case that triggered the wipe.
	saved.Metadata = map[string]any{
		"model":    "test-model",
		"cwd":      dir,
		"planmode": map[string]any{"mode": "off"},
	}
	saved.Messages = []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("prior message")),
	}
	_ = store.Save(saved)

	// Capture the system prompt the provider actually receives on the next run.
	var gotSystem atomic.Value // string
	captureHandler := func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		gotSystem.Store(req.System)
		return simpleResponse("ok"), nil
	}
	prov := newMockProvider(captureHandler)

	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.ResumeSession(saved.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Drive one run so the provider is invoked and records req.System.
	done := make(chan struct{})
	unsub := sess.runtime.Bus.Subscribe(func(e bus.StateChanged) {
		if e.State == string(bus.StateIdle) {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})
	defer unsub()

	if err := sess.runtime.Bus.Execute(bus.SendPrompt{Text: "go"}); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not reach idle in time")
	}

	sys, _ := gotSystem.Load().(string)
	if sys == "" {
		t.Fatal("resumed session ran with an EMPTY system prompt (wipe regression)")
	}
	if !strings.Contains(sys, "# Persistence") {
		t.Errorf("system prompt missing Persistence section after resume; got %d bytes:\n%s", len(sys), sys)
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
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

	if _, _, err := mgr.Send(sess.ID, "block", nil, ""); err != nil {
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

	_, _, _ = mgr.Send(sess.ID, "hi", nil, "")
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
	// /clear must not destroy data: it starts a new session (returned via
	// NewSessionID) and leaves the original session's history intact.
	if result.NewSessionID == "" || result.NewSessionID == sess.ID {
		t.Fatalf("expected a new session ID, got %q", result.NewSessionID)
	}
	if len(sess.History()) == 0 {
		t.Fatal("expected original session's messages to survive /clear")
	}
	newSess, ok := mgr.Get(result.NewSessionID)
	if !ok {
		t.Fatal("new session should exist after /clear")
	}
	if len(newSess.History()) != 0 {
		t.Fatalf("expected 0 messages in the new session, got %d", len(newSess.History()))
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
