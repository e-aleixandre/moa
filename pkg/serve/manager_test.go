package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/attention"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/permission"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/subagent"
	"github.com/e-aleixandre/moa/pkg/tool"
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
	// Auxiliary titles now start alongside the main run. Keep legacy tests that
	// script only the main provider responses deterministic.
	if isTestAutoTitleRequest(req) {
		return simpleResponse("title"), nil
	}
	idx := int(m.calls.Add(1) - 1)
	if idx >= len(m.handlers) {
		return simpleResponse("done"), nil
	}
	return m.handlers[idx](ctx, req)
}

func isTestAutoTitleRequest(req core.Request) bool {
	return len(req.Messages) == 1 && len(req.Messages[0].Content) > 0 &&
		strings.HasPrefix(req.Messages[0].Content[0].Text, "Here is the start of the conversation, between the markers:\n\n<conversation>\n")
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
	// Keep the legacy helper's background behavior explicit. Production auto
	// selection requires real completion credentials and therefore stays off in
	// hermetic tests unless a test deliberately opts in.
	return newTestManagerWithConfig(t, ctx, provider, root, core.MoaConfig{
		DisableSandbox:    true,
		AutoTitleModel:    "off",
		SessionBriefModel: "haiku",
	})
}

func newTestManagerWithConfig(t *testing.T, ctx context.Context, provider core.Provider, root string, moaCfg core.MoaConfig) *Manager {
	t.Helper()
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		AuxiliaryModelResolver: func(spec string) (core.Model, bool, error) {
			return core.ResolveAuxiliaryModel(spec, func(string) bool { return true })
		},
		DefaultModel:   core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:  root,
		MoaCfg:         moaCfg,
		ConfigLoader:   isolatedTestConfigLoader(t, moaCfg),
		SessionBaseDir: t.TempDir(),
		SchedulePath:   filepath.Join(t.TempDir(), "schedules.json"),
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
		mgr.Shutdown()
	})
	return mgr
}

func TestExplicitAuxiliaryModelWithoutCredentialHasNoBackgroundCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := newMockProvider(simpleResponseHandler("reply"))
	// No AuxiliaryModelResolver models an application without normal completion
	// credentials. Explicit settings must not bypass that requirement.
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:   t.TempDir(),
		MoaCfg: core.MoaConfig{
			DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "haiku",
		},
		ConfigLoader:   isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "haiku"}),
		SessionBaseDir: t.TempDir(),
		SchedulePath:   filepath.Join(t.TempDir(), "schedules.json"),
	})
	t.Cleanup(mgr.Shutdown)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "one normal run", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "main run completion", func() bool { return sessState(sess) == StateIdle })
	time.Sleep(briefDebounce + 100*time.Millisecond)
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want only the main run", got)
	}
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

func TestShutdownStopsOwnedSecretReaper(t *testing.T) {
	ctx := context.Background() // deliberately remains live after Shutdown
	mgr := NewManager(ctx, ManagerConfig{
		DefaultModel:   core.Model{ID: "test", Provider: "test"},
		WorkspaceRoot:  t.TempDir(),
		SessionBaseDir: t.TempDir(),
		SchedulePath:   filepath.Join(t.TempDir(), "schedules.json"),
	})
	done := mgr.secretReaperDone
	if done == nil {
		t.Fatal("manager did not start its secret reaper")
	}
	mgr.Shutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not wait for the secret reaper")
	}
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

func TestCreateSessionKeepsSavedCache(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hello")))
	marker := []session.Summary{{ID: "saved-session"}}
	mgr.savedCache = marker
	mgr.savedCacheAt = time.Now()

	if _, err := mgr.CreateSession(CreateOpts{}); err != nil {
		t.Fatal(err)
	}

	mgr.savedCacheMu.Lock()
	defer mgr.savedCacheMu.Unlock()
	if !reflect.DeepEqual(mgr.savedCache, marker) {
		t.Fatalf("active create invalidated saved cache: got %#v, want %#v", mgr.savedCache, marker)
	}
}

func TestManagerStartupSharesRosterRead(t *testing.T) {
	baseDir := t.TempDir()
	store, err := session.NewFileStore(baseDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Metadata[session.MetaIdempotencyKey] = "startup-key"
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}

	attachments, err := attachment.New(filepath.Join(filepath.Dir(baseDir), "attachments", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := attachments.PutRef(saved.ID, []byte("attachment"), attachment.PutMeta{Name: "startup.txt", Mime: "text/plain", Kind: "document"})
	if err != nil {
		t.Fatal(err)
	}

	originalListAll := listAllSessions
	var reads atomic.Int32
	listAllSessions = func(dir string) ([]session.Summary, error) {
		reads.Add(1)
		return originalListAll(dir)
	}
	t.Cleanup(func() { listAllSessions = originalListAll })

	mgr := NewManager(context.Background(), ManagerConfig{
		DefaultModel:   core.Model{ID: "test", Provider: "test"},
		WorkspaceRoot:  t.TempDir(),
		SessionBaseDir: baseDir,
		SchedulePath:   filepath.Join(t.TempDir(), "schedules.json"),
	})
	t.Cleanup(mgr.Shutdown)

	if got := reads.Load(); got != 1 {
		t.Fatalf("startup roster reads = %d, want 1", got)
	}
	if id, ok := mgr.automation.lookup("startup-key"); !ok || id != saved.ID {
		t.Fatalf("automation index lookup = %q, %v; want %q, true", id, ok, saved.ID)
	}
	if _, ok := mgr.attachStore.Lookup(saved.ID, descriptor.ID); !ok {
		t.Fatal("attachment reconciliation removed a live session attachment")
	}
	if got := len(mgr.List()); got != 1 {
		t.Fatalf("saved sessions in List = %d, want 1", got)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("List triggered an extra startup roster read: got %d, want 1", got)
	}
}

func TestOwnedBashCompletionDoesNotStartRootNotificationRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("notification handled")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	completed := make(chan bus.BashCompleted, 2)
	runs := make(chan bus.RunStarted, 2)
	sess.runtime.Bus.Subscribe(func(e bus.BashCompleted) { completed <- e })
	sess.runtime.Bus.Subscribe(func(e bus.RunStarted) { runs <- e })

	release := make(chan struct{})
	_, err = sess.bashJobs.Start("echo child", sess.CWD, "child-1", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		<-release
		return core.TextResult("child done"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	init := buildInitData(sess, bus.StreamingAggregate{}, nil, "")
	if len(init.BashJobs) != 1 || init.BashJobs[0].OwnerAgentID != "child-1" {
		t.Fatalf("bash init snapshot = %+v", init.BashJobs)
	}
	close(release)
	select {
	case event := <-completed:
		if event.OwnerAgentID != "child-1" {
			t.Fatalf("completion owner = %q", event.OwnerAgentID)
		}
	case <-time.After(time.Second):
		t.Fatal("owned bash completion was not published")
	}
	select {
	case <-runs:
		t.Fatal("owned bash started a root notification run")
	case <-time.After(100 * time.Millisecond):
	}

	_, err = sess.bashJobs.Start("echo root", sess.CWD, "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		return core.TextResult("root done"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-completed:
		if event.OwnerAgentID != "" {
			t.Fatalf("root completion owner = %q", event.OwnerAgentID)
		}
	case <-time.After(time.Second):
		t.Fatal("root bash completion was not published")
	}
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("root bash did not start its notification run")
	}
}

func TestInitSubagentSnapshotsRetainsTerminalBashOwner(t *testing.T) {
	transcript := []core.AgentMessage{core.WrapMessage(core.Message{
		Role:    "assistant",
		Content: []core.Content{core.TextContent("Child finished its analysis.")},
	})}
	snapshots := initSubagentSnapshots(
		[]subagent.JobInfo{
			{JobID: "child-terminal", Task: "Inspect the service", Model: "test-model", Status: "completed", Async: true},
			{JobID: "unrelated-terminal", Status: "completed", Async: true},
		},
		[]tool.BashJobInfo{{JobID: "bash-1", OwnerAgentID: "child-terminal", Status: "running"}},
		func(jobID string) []core.AgentMessage {
			if jobID == "child-terminal" {
				return transcript
			}
			return nil
		},
	)
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1: %+v", len(snapshots), snapshots)
	}
	got := snapshots[0]
	if got.JobID != "child-terminal" || got.Status != "completed" || got.Task != "Inspect the service" || got.Model != "test-model" {
		t.Fatalf("terminal bash owner = %+v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "Child finished its analysis." {
		t.Fatalf("terminal owner transcript = %+v", got.Messages)
	}
}

func TestManagerAttentionTracksAndClearsSessionPermission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "deploy api"})
	if err != nil {
		t.Fatal(err)
	}

	sess.runtime.Bus.Publish(bus.PermissionRequested{
		SessionID: sess.ID,
		ID:        "perm_voice_test",
		ToolName:  "bash",
		Args:      map[string]any{"command": "git status"},
	})
	pollUntil(t, time.Second, "attention permission", func() bool {
		items := mgr.attention.Status()
		return len(items) == 1 && items[0].RefID == "perm_voice_test" && items[0].SessionID == sess.ID
	})

	sess.runtime.Bus.Publish(bus.PermissionResolved{SessionID: sess.ID, ID: "perm_voice_test"})
	pollUntil(t, time.Second, "resolved attention permission", func() bool {
		return len(mgr.attention.Status()) == 0
	})
}

func TestSessionInfoSerializesAttentionActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "activity"})
	if err != nil {
		t.Fatal(err)
	}

	sess.runtime.Bus.Publish(bus.ToolExecStarted{
		SessionID: sess.ID, ToolCallID: "tool-1", ToolName: "bash",
		Args: map[string]any{"command": "phpstan analyse"},
	})

	var info SessionInfo
	pollUntil(t, time.Second, "session activity", func() bool {
		list := mgr.List()
		if len(list) != 1 || list[0].Activity == nil {
			return false
		}
		info = list[0]
		return true
	})
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var dto struct {
		Activity       *attention.SessionActivity `json:"activity"`
		ServerInstance string                     `json:"server_instance"`
	}
	if err := json.Unmarshal(encoded, &dto); err != nil {
		t.Fatal(err)
	}
	want := &attention.SessionActivity{Kind: "tool", Tool: "bash", Detail: "phpstan analyse"}
	if !reflect.DeepEqual(dto.Activity, want) {
		t.Fatalf("activity = %+v, want %+v", dto.Activity, want)
	}
	if dto.ServerInstance == "" || dto.ServerInstance != mgr.serverInstance {
		t.Fatalf("session server_instance = %q, want %q", dto.ServerInstance, mgr.serverInstance)
	}
	if got := buildInitData(sess, bus.StreamingAggregate{}, nil, "").ServerInstance; got != mgr.serverInstance {
		t.Fatalf("init server_instance = %q, want %q", got, mgr.serverInstance)
	}
}

func TestManagerConfigLoaderIsUsedForSessionBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	// This is the one manager test that builds a real session, and a session
	// build reads — and migrates — memory from the moa config directory. Point
	// it at a scratch one so the suite cannot touch the developer's own facts.
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
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

	_, _, _, err = mgr.Send(sess.ID, "hello", nil, "", "")
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

	_, _, _, err = mgr.Send(sess.ID, "hello", nil, "", "")
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
	_, _, _, err = mgr.Send(sess.ID, "retry", nil, "", "")
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

	_, _, _, err = mgr.Send(sess.ID, "first", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 2*time.Second, "state running", func() bool {
		return sessState(sess) == StateRunning
	})

	action, steerID, _, err := mgr.Send(sess.ID, "second", nil, "c-client-123", "")
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

	_, _, _, err = mgr.Send(id, "hello", nil, "", "")
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

	prov := newEarlyAutoTitleProvider("Auth module refactor")
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})

	sess, _ := mgr.CreateSession(CreateOpts{})

	_, _, _, err := mgr.Send(sess.ID, "Refactoriza el módulo de auth", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the durable write, not for the in-memory title: generateAutoTitle
	// updates memory before it persists, so polling memory and reading disk
	// immediately is itself a race.
	pollUntil(t, 5*time.Second, "auto-title persisted", func() bool {
		saved, _, err := session.FindSession(mgr.sessionBaseDir, sess.ID)
		return err == nil && saved.Title == "Auth module refactor" && saved.TitleSource == session.TitleSourceAuto
	})
	select {
	case <-prov.mainFinished:
		t.Fatal("auto-title waited for the main provider to finish")
	default:
	}
	if got := prov.titlePrompt(); got != "User: Refactoriza el módulo de auth" {
		t.Fatalf("auto-title prompt = %q", got)
	}
	close(prov.mainRelease)
}

// autoTitleDone joins the background title goroutines of a manager, so a test
// can assert on disk state instead of polling for a write that may never come.
func autoTitleDone(t *testing.T, mgr *Manager) <-chan struct{} {
	t.Helper()
	done := make(chan struct{}, 8)
	mgr.afterAutoTitleGeneration = func(*ManagedSession) { done <- struct{}{} }
	return done
}

func waitAutoTitleDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the title goroutine to finish")
	}
}

// TestSend_AutoTitleTitlesFirstAcceptedPromptUnderConcurrency is the review's
// first reproduction: concurrent senders share the lifecycle read lock, so the
// order in which Send returns is NOT acceptance order. The generated title must
// come from the prompt that actually became the conversation's first message.
func TestSend_AutoTitleTitlesFirstAcceptedPromptUnderConcurrency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newEarlyAutoTitleProvider()
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})

	for iteration := range 30 {
		sess, err := mgr.CreateSession(CreateOpts{})
		if err != nil {
			t.Fatal(err)
		}
		before := prov.titleCount()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _, _, _ = mgr.Send(sess.ID, fmt.Sprintf("prompt-%02d-%02d", iteration, i), nil, "", "")
			}()
		}
		close(start)
		wg.Wait()
		pollUntil(t, 5*time.Second, "title request", func() bool { return prov.titleCount() == before+1 })
		var first string
		pollUntil(t, 5*time.Second, "first history prompt", func() bool {
			history := sess.History()
			if len(history) == 0 {
				return false
			}
			for _, c := range history[0].Content {
				if c.Type == "text" && c.Text != "" {
					first = c.Text
					return true
				}
			}
			return false
		})
		if got := prov.titlePromptAt(before); got != "User: "+first {
			t.Fatalf("iteration %d: first history prompt %q but title request was %q", iteration, first, got)
		}
		if err := mgr.Delete(sess.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSend_AutoTitleManualRenameWinsOnDisk is the review's second
// reproduction: a generator descheduled between committing memory and
// persisting must not write its stale title over a rename that landed meanwhile.
func TestSend_AutoTitleManualRenameWinsOnDisk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newEarlyAutoTitleProvider("Generated title")
	prov.titleRelease = make(chan struct{})
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})
	done := autoTitleDone(t, mgr)
	sess, _ := mgr.CreateSession(CreateOpts{})

	if _, _, _, err := mgr.Send(sess.ID, "rename me", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "in-flight title request", func() bool { return prov.titleCount() == 1 })
	if _, err := mgr.SetTitle(sess.ID, "Manual title"); err != nil {
		t.Fatal(err)
	}
	close(prov.titleRelease)
	waitAutoTitleDone(t, done)

	saved, _, err := session.FindSession(mgr.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "Manual title" || saved.TitleSource != session.TitleSourceManual {
		t.Fatalf("stale auto write clobbered the rename on disk: title=%q source=%q", saved.Title, saved.TitleSource)
	}
	close(prov.mainRelease)
}

// TestSend_AutoTitleClosedRuntimeCannotOverwriteResumedSession is the review's
// third reproduction: after close and resume, a title write left over from the
// old runtime must not overwrite the file the new runtime owns.
func TestSend_AutoTitleClosedRuntimeCannotOverwriteResumedSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(), t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	old, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(old.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.ResumeSession(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SetTitle(resumed.ID, "Manual after resume"); err != nil {
		t.Fatal(err)
	}

	// A title goroutine of the closed runtime resumes after cancellation.
	old.mu.Lock()
	old.Title = "Stale title from old runtime"
	old.TitleSource = session.TitleSourceAuto
	old.mu.Unlock()
	old.persister.saveTitle()

	saved, _, err := session.FindSession(mgr.sessionBaseDir, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "Manual after resume" || saved.TitleSource != session.TitleSourceManual {
		t.Fatalf("closed runtime overwrote the resumed session: title=%q source=%q", saved.Title, saved.TitleSource)
	}
}

// TestSetTitleRefusesClosingSession keeps the rename contract honest: a rename
// admitted while the runtime is being torn down would report success and never
// reach disk, since saveTitle refuses to write for a closing runtime.
func TestSetTitleRefusesClosingSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(), t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	sess, err := mgr.CreateSession(CreateOpts{Title: "before close"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.setTitle(sess, "after close"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename on a closing runtime = %v, want ErrNotFound", err)
	}
	saved, _, err := session.FindSession(mgr.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "before close" {
		t.Fatalf("closing rename reached disk: title=%q", saved.Title)
	}
}

// earlyAutoTitleProvider separates auxiliary requests from normal agent
// requests so tests don't depend on the scheduling order of their goroutines.
type earlyAutoTitleProvider struct {
	mu           sync.Mutex
	titles       []core.Request
	titleOutput  []string
	titleCalls   int
	mainRelease  chan struct{}
	mainStarted  chan struct{}
	mainFinished chan struct{}
	titleRelease chan struct{}
}

func newEarlyAutoTitleProvider(titleOutput ...string) *earlyAutoTitleProvider {
	return &earlyAutoTitleProvider{
		titleOutput: titleOutput, mainRelease: make(chan struct{}),
		mainStarted: make(chan struct{}, 8), mainFinished: make(chan struct{}, 8),
	}
}

func (p *earlyAutoTitleProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if isTestAutoTitleRequest(req) {
		p.mu.Lock()
		p.titles = append(p.titles, req)
		idx := p.titleCalls
		p.titleCalls++
		output := "title"
		if idx < len(p.titleOutput) {
			output = p.titleOutput[idx]
		}
		p.mu.Unlock()
		if p.titleRelease != nil {
			select {
			case <-p.titleRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return simpleResponse(output), nil
	}
	select {
	case p.mainStarted <- struct{}{}:
	default:
	}
	select {
	case <-p.mainRelease:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case p.mainFinished <- struct{}{}:
	default:
	}
	return simpleResponse("reply"), nil
}

func (p *earlyAutoTitleProvider) titlePrompt() string {
	return p.titlePromptAt(0)
}

func (p *earlyAutoTitleProvider) titlePromptAt(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.titles) || len(p.titles[i].Messages[0].Content) == 0 {
		return ""
	}
	text := p.titles[i].Messages[0].Content[0].Text
	start := strings.Index(text, "<conversation>\n")
	end := strings.Index(text, "\n</conversation>")
	if start < 0 || end < 0 {
		return text
	}
	return text[start+len("<conversation>\n") : end]
}

func (p *earlyAutoTitleProvider) titleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.titles)
}

func TestSend_AutoTitleUsesFirstAcceptedPromptOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newEarlyAutoTitleProvider("First task")
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})
	sess, _ := mgr.CreateSession(CreateOpts{})

	if _, _, _, err := mgr.Send(sess.ID, "first task", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "main provider started", func() bool { return len(prov.mainStarted) > 0 })
	if _, _, _, err := mgr.Send(sess.ID, "later steer", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "title request", func() bool { return prov.titleCount() == 1 })
	if got := prov.titlePrompt(); got != "User: first task" {
		t.Fatalf("title used later conversation content: %q", got)
	}
	close(prov.mainRelease)
}

func TestSend_AutoTitleFailureRetriesAndManualTitleWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newEarlyAutoTitleProvider("NONE", "Recovered title")
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})
	sess, _ := mgr.CreateSession(CreateOpts{})

	if _, _, _, err := mgr.Send(sess.ID, "greeting", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "failed title request", func() bool { return prov.titleCount() == 1 && !sess.autoTitled.Load() })
	close(prov.mainRelease)
	pollUntil(t, time.Second, "first run settled", func() bool { return sessState(sess) == StateIdle })
	// Hold the retry in flight so the manual rename races its final title
	// application rather than merely replacing an already-saved title.
	prov.titleRelease = make(chan struct{})
	if _, _, _, err := mgr.Send(sess.ID, "real task", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "retry title", func() bool { return prov.titleCount() == 2 })
	if _, err := mgr.SetTitle(sess.ID, "Manual title"); err != nil {
		t.Fatal(err)
	}
	close(prov.titleRelease)
	pollUntil(t, time.Second, "manual title preserved", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.Title == "Manual title" && sess.TitleSource == session.TitleSourceManual
	})
	if got := prov.titleCount(); got != 2 {
		t.Fatalf("title requests after manual rename = %d, want 2", got)
	}
}

func TestSend_AutoTitleRejectedAndConcurrentPrompts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newEarlyAutoTitleProvider("One title")
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})
	sess, _ := mgr.CreateSession(CreateOpts{})

	if _, _, _, err := mgr.Send(sess.ID, "", []Attachment{{Name: "bad.txt", Data: "%%%"}}, "", ""); err == nil {
		t.Fatal("rejected attachment send succeeded")
	}
	if got := prov.titleCount(); got != 0 {
		t.Fatalf("rejected send made %d title requests", got)
	}
	if _, _, _, err := mgr.Send(sess.ID, "first", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "main provider started", func() bool { return len(prov.mainStarted) > 0 })
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, _ = mgr.Send(sess.ID, "concurrent steer", nil, "", "")
		}()
	}
	wg.Wait()
	pollUntil(t, time.Second, "one title request", func() bool { return prov.titleCount() == 1 })
	close(prov.mainRelease)
}

func TestSend_AutoTitleDeleteDoesNotResurrectSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newEarlyAutoTitleProvider("Too late")
	prov.titleRelease = make(chan struct{})
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})
	sess, _ := mgr.CreateSession(CreateOpts{})
	if _, _, _, err := mgr.Send(sess.ID, "delete me", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "in-flight title request", func() bool { return prov.titleCount() == 1 })
	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.FindSession(mgr.sessionBaseDir, sess.ID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted session was recreated: %v", err)
	}
}

func TestSend_AutoTitleResumeEligibility(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newEarlyAutoTitleProvider("Initial title", "Empty restored title")
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})

	withHistory, _ := mgr.CreateSession(CreateOpts{})
	if _, _, _, err := mgr.Send(withHistory.ID, "saved prompt", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "initial title", func() bool { return prov.titleCount() == 1 })
	close(prov.mainRelease)
	pollUntil(t, time.Second, "saved run settled", func() bool { return sessState(withHistory) == StateIdle })
	if err := mgr.CloseSession(withHistory.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.ResumeSession(withHistory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(resumed.ID, "new prompt", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := prov.titleCount(); got != 1 {
		t.Fatalf("resumed history made %d title requests, want 1", got)
	}

	empty, _ := mgr.CreateSession(CreateOpts{})
	if err := mgr.CloseSession(empty.ID); err != nil {
		t.Fatal(err)
	}
	empty, err = mgr.ResumeSession(empty.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(empty.ID, "restored first prompt", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "empty restored title", func() bool { return prov.titleCount() == 2 })
}

// TestSend_AutoTitleResumeEmptyAlreadyAutoTitled is the review's fourth
// reproduction: an empty session that already carries an automatic title (the
// early write beat the first durable transcript snapshot) has had its one shot.
func TestSend_AutoTitleResumeEmptyAlreadyAutoTitled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newEarlyAutoTitleProvider("Second title")
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "haiku", SessionBriefModel: "off"})
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.Title = "Already generated"
	sess.TitleSource = session.TitleSourceAuto
	sess.mu.Unlock()
	sess.persister.saveTitle()
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.autoTitled.Load() {
		t.Fatal("an already auto-titled session is still eligible for a second title")
	}
	if _, _, _, err := mgr.Send(resumed.ID, "later prompt", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "main provider started", func() bool { return len(prov.mainStarted) > 0 })
	if got := prov.titleCount(); got != 0 {
		t.Fatalf("already auto-titled session generated %d more title(s)", got)
	}
	close(prov.mainRelease)
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

func TestManagerAttentionUsesConfiguredSTTLanguage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(), t.TempDir(), core.MoaConfig{DisableSandbox: true, STTLanguage: "es"})
	sess, err := mgr.CreateSession(CreateOpts{Title: "facturas"})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "ask_es", Questions: []bus.AskQuestion{{Text: "¿Continuamos?"}}})
	pollUntil(t, time.Second, "Spanish attention", func() bool {
		items := mgr.attention.Status()
		return len(items) == 1 && strings.HasPrefix(items[0].Spoken, "En facturas")
	})
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

	_, _, _, err = mgr.Send(id, "hello", nil, "", "")
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

	_, _, _, err = mgr.Send(sess.ID, "hello", nil, "", "")
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

// Closing a session unloads its runtime but must never lose the conversation:
// it stays in the list as "saved" and reopens with everything intact.
func TestCloseSession_UnloadsButKeepsSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("reply"))
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID
	if _, _, _, err := mgr.Send(id, "remember this", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "state idle", func() bool {
		return sessState(sess) == StateIdle
	})

	if err := mgr.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if _, ok := mgr.Get(id); ok {
		t.Fatal("expected the session to be unloaded from memory")
	}

	var found bool
	for _, si := range mgr.List() {
		if si.ID == id {
			found = true
			if si.State != StateSaved {
				t.Errorf("closed session state = %q, want %q", si.State, StateSaved)
			}
			if si.Title != "test" {
				t.Errorf("closed session title = %q, want \"test\"", si.Title)
			}
		}
	}
	if !found {
		t.Fatal("a closed session must stay in List() — closing is not deleting")
	}

	// It reopens as a live session again, with the conversation intact: the
	// whole promise of close-vs-delete is that nothing is lost.
	reopened, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatalf("ResumeSession after close: %v", err)
	}
	if _, ok := mgr.Get(id); !ok {
		t.Fatal("expected the session to be loaded again after resume")
	}
	msgs, _ := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](reopened.runtime.Bus, bus.GetMessages{})
	var sawPrompt bool
	for _, msg := range msgs {
		for _, c := range msg.Content {
			if c.Type == "text" && strings.Contains(c.Text, "remember this") {
				sawPrompt = true
			}
		}
	}
	if !sawPrompt {
		t.Fatalf("the conversation must survive a close/reopen; got %d messages", len(msgs))
	}
}

func TestCloseSession_FlushFailureKeepsSessionLoaded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := newTestManager(t, ctx, newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "unsaved"})
	if err != nil {
		t.Fatal(err)
	}
	storeDir := sess.persister.store.Dir()
	if err := os.Chmod(storeDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0700) })

	err = mgr.CloseSession(sess.ID)
	if err == nil {
		t.Fatal("CloseSession succeeded after its final flush failed")
	}
	if _, ok := mgr.Get(sess.ID); !ok {
		t.Fatal("CloseSession unloaded the session after its final flush failed")
	}
}

// Closing is refused while the session is still working: the teardown cancels
// the session context, which would kill an in-flight run or the background work
// (async subagents, bash jobs) whose output the user is waiting for.
func TestCloseSession_RefusedWhileNotQuiescent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	prov := newMockProvider(func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return simpleResponse("reply"), nil
	})
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "state running", func() bool {
		return sessState(sess) == StateRunning
	})

	if err := mgr.CloseSession(sess.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("CloseSession during a run: got %v, want ErrBusy", err)
	}
	if _, ok := mgr.Get(sess.ID); !ok {
		t.Fatal("a refused close must leave the session loaded")
	}

	close(release)
	pollUntil(t, 5*time.Second, "state idle", func() bool {
		return sessState(sess) == StateIdle
	})
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatalf("CloseSession once idle: %v", err)
	}
}

// Closing a session that is only on disk is a no-op, so any client can close
// idempotently; a session that does not exist at all is still a 404.
// A /send racing a close must not start a run into a runtime being torn down:
// either the send wins (and the close is refused as busy) or the close wins
// (and the send reports the session as gone). Never a run on a dead runtime.
func TestCloseSession_RacesSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockingResponse := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	// Keep an accepted send running for the whole assertion. A start barrier only
	// schedules both callers; with an immediate response the complete turn can
	// finish before CloseSession establishes its boundary, making both operations
	// correctly succeed in sequence rather than exercise the lifecycle race.
	prov := newMockProvider(blockingResponse)
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var sendErr, closeErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, _, sendErr = mgr.Send(sess.ID, "hello", nil, "", "")
	}()
	go func() {
		defer wg.Done()
		<-start
		closeErr = mgr.CloseSession(sess.ID)
	}()
	close(start)
	wg.Wait()

	// Exactly one operation wins: either the send is accepted and the close is
	// refused while its run remains active, or close removes the runtime before
	// send can use it.
	if sendErr == nil {
		if !errors.Is(closeErr, ErrBusy) {
			t.Fatalf("send won the race: close got %v, want ErrBusy", closeErr)
		}
		if _, ok := mgr.Get(sess.ID); !ok {
			t.Fatal("send succeeded but the session was closed under it")
		}
		return
	}
	if !errors.Is(sendErr, ErrNotFound) {
		t.Fatalf("send lost the race: got %v, want ErrNotFound", sendErr)
	}
	if closeErr != nil {
		t.Fatalf("close won the race: got %v, want nil", closeErr)
	}
	if _, ok := mgr.Get(sess.ID); ok {
		t.Fatal("close succeeded but the session remained loaded")
	}
}

func TestCloseSession_NotLoadedAndNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider()
	mgr := newTestManager(t, ctx, prov)

	sess, err := mgr.CreateSession(CreateOpts{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatalf("first CloseSession: %v", err)
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatalf("closing an already-closed session should be a no-op: %v", err)
	}

	if err := mgr.CloseSession("does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("CloseSession on a missing session: got %v, want ErrNotFound", err)
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

// TestResumeSession_KeepsSystemPrompt verifies that resuming a legacy session
// with removed plan-mode metadata preserves its usable session data.
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
	// The run reaching idle only schedules its persistence snapshot. Shut down
	// before TempDir cleanup so the persistence reactor has drained its save.
	t.Cleanup(mgr.Shutdown)

	sess, err := mgr.ResumeSession(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != saved.Title || sess.CWD != dir {
		t.Fatalf("legacy session fields changed on resume: title=%q cwd=%q", sess.Title, sess.CWD)
	}
	if history := sess.History(); len(history) != 1 || history[0].Content[0].Text != "prior message" {
		t.Fatalf("legacy session history changed on resume: %+v", history)
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

func TestDelete_ReturnsPersistedFileRemovalFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := newTestManager(t, ctx, newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "cannot-delete"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sess.persister.store.Dir(), sess.ID+".json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "remaining"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Delete(sess.ID); err == nil {
		t.Fatal("Delete succeeded after the persisted session file could not be removed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persisted session path was unexpectedly removed: %v", err)
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

	if _, _, _, err := mgr.Send(sess.ID, "block", nil, "", ""); err != nil {
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

func TestCancelWithDiscardedSteers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	started := make(chan struct{})
	prov := newMockProvider(func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent)
		go func() {
			defer close(ch)
			close(started)
			<-ctx.Done()
		}()
		return ch, nil
	})
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "first", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	<-started
	action, steerID, _, err := mgr.Send(sess.ID, "queued", nil, "client-steer", "")
	if err != nil || action != "steer" || steerID != "client-steer" {
		t.Fatalf("queued send = %q, %q, %v", action, steerID, err)
	}
	discarded, err := mgr.CancelWithDiscardedSteers(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(discarded) != 1 || discarded[0].ID != "client-steer" {
		t.Fatalf("discarded = %+v, want client steer", discarded)
	}
}

func TestCancelAndSendUseAgentOccupancyWhenBusIsIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	started := make(chan struct{}, 2)
	blockingHandler := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent)
		go func() {
			started <- struct{}{}
			<-ctx.Done()
			close(ch)
		}()
		return ch, nil
	}
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(blockingHandler, blockingHandler), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	runDirectly := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := sess.runtime.Context().Agent.Send(context.Background(), "occupy agent")
			done <- err
		}()
		<-started
		if sess.runtime.State.Current() != bus.StateIdle {
			t.Fatalf("bus state = %q, want idle", sess.runtime.State.Current())
		}
		if !sess.runtime.Context().Agent.IsRunning() {
			t.Fatal("agent is not occupied")
		}
		return done
	}

	firstDone := runDirectly()
	if _, err := mgr.CancelWithDiscardedSteers(sess.ID); err != nil {
		t.Fatalf("cancel with occupied agent and idle bus = %v", err)
	}
	<-firstDone

	secondDone := runDirectly()
	action, _, _, err := mgr.Send(sess.ID, "queued instead of direct", nil, "steer-id", "")
	if err != nil {
		t.Fatalf("send with occupied agent and idle bus = %v", err)
	}
	if action != "steer" {
		t.Fatalf("send action = %q, want steer", action)
	}
	if _, err := mgr.CancelWithDiscardedSteers(sess.ID); err != nil {
		t.Fatalf("cleanup cancel = %v", err)
	}
	<-secondDone
}

func TestSendRetriesAsNewRunWhenQueueAdmissionCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(
		simpleResponseHandler("first"),
		simpleResponseHandler("second"),
	), dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	admissionClosed := make(chan struct{})
	releaseEnd := make(chan struct{})
	var endOnce sync.Once
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseEnd) })
	sess.runtime.Context().Agent.(bus.AgentSubscriber).Subscribe(func(e core.AgentEvent) {
		if e.Type != core.AgentEventEnd {
			return
		}
		endOnce.Do(func() {
			// Force the same closed-admission state as the final-drain window and
			// hold run settlement so the concurrent Send must take its retry path.
			sess.runtime.Context().Agent.Abort()
			close(admissionClosed)
			<-releaseEnd
		})
	})

	if _, _, _, err := mgr.Send(sess.ID, "first", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-admissionClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not close queue admission")
	}

	type result struct {
		action string
		err    error
	}
	second := make(chan result, 1)
	go func() {
		action, _, _, err := mgr.Send(sess.ID, "second", nil, "", "")
		second <- result{action: action, err: err}
	}()
	select {
	case got := <-second:
		t.Fatalf("send returned before the terminal run settled: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(releaseEnd) })

	select {
	case got := <-second:
		if got.err != nil || got.action != "send" {
			t.Fatalf("retried send = action %q, err %v; want send, nil", got.action, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send was not retried after the terminal run settled")
	}
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

	_, _, _, _ = mgr.Send(sess.ID, "hi", nil, "", "")
	pollUntil(t, 5*time.Second, "run complete", func() bool {
		return sessState(sess) == StateIdle
	})

	if len(sess.History()) == 0 {
		t.Fatal("expected messages after send")
	}

	result, err := mgr.ExecCommand(sess.ID, "/clear", "")
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

	result, err := mgr.ExecCommand(sess.ID, "/nope", "")
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

	_, err := mgr.ExecCommand("nonexistent", "/clear", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ===========================================================================
// Client-supplied msg_id — validation, uniqueness and re-minting
// ===========================================================================

// sendAndWaitIdle performs a direct send and waits for the run to settle, so
// the message is in history before the next assertion reads it.
func sendAndWaitIdle(t *testing.T, mgr *Manager, sess *ManagedSession, text, msgID string) string {
	t.Helper()
	action, id, _, err := mgr.Send(sess.ID, text, nil, "", msgID)
	if err != nil {
		t.Fatal(err)
	}
	if action != "send" {
		t.Fatalf("action = %q, want send", action)
	}
	pollUntil(t, 5*time.Second, "run settles", func() bool { return sessState(sess) == StateIdle })
	return id
}

func historyMsgIDs(sess *ManagedSession) []string {
	msgs, _ := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetDisplayMessages{})
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" {
			ids = append(ids, m.MsgID)
		}
	}
	return ids
}

func TestSend_ClientMsgID_HonoredWhenValidAndFree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("reply")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	got := sendAndWaitIdle(t, mgr, sess, "hola", "c-abc_123")
	if got != "c-abc_123" {
		t.Fatalf("effective msg ID = %q, want the client-supplied one", got)
	}
	if ids := historyMsgIDs(sess); len(ids) != 1 || ids[0] != "c-abc_123" {
		t.Fatalf("history user msg IDs = %v, want [c-abc_123]", ids)
	}
}

func TestSend_ClientMsgID_RemintedWhenInvalidOrHuge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, tc := range []struct {
		name  string
		msgID string
	}{
		{"too long", strings.Repeat("a", 65)},
		{"gigantic", strings.Repeat("z", 100_000)},
		{"illegal characters", "c-abc/../etc"},
		{"whitespace and newlines", "id with\nnewline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("reply")))
			sess, err := mgr.CreateSession(CreateOpts{})
			if err != nil {
				t.Fatal(err)
			}
			got := sendAndWaitIdle(t, mgr, sess, "hola", tc.msgID)
			if got == tc.msgID {
				t.Fatal("a malformed client msg_id was accepted verbatim")
			}
			if !clientIDPattern.MatchString(got) {
				t.Fatalf("re-minted msg ID %q does not satisfy the ID shape", got)
			}
			// The prompt must still have reached the conversation: rejecting the
			// ID must never cost the message.
			ids := historyMsgIDs(sess)
			if len(ids) != 1 || ids[0] != got {
				t.Fatalf("history user msg IDs = %v, want [%s]", ids, got)
			}
		})
	}
}

func TestSend_ClientMsgID_RemintedWhenAlreadyInHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("reply"), simpleResponseHandler("reply2")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	first := sendAndWaitIdle(t, mgr, sess, "primero", "c-reused")
	if first != "c-reused" {
		t.Fatalf("first msg ID = %q, want c-reused", first)
	}

	// Replaying the same ID must not make the second message invisible: every
	// client dedups by ID, so reusing one would suppress the new message
	// everywhere. The server re-mints instead, and the prompt still lands.
	second := sendAndWaitIdle(t, mgr, sess, "segundo", "c-reused")
	if second == "c-reused" {
		t.Fatal("a msg_id already present in history was reused")
	}
	ids := historyMsgIDs(sess)
	if len(ids) != 2 || ids[0] != first || ids[1] != second {
		t.Fatalf("history user msg IDs = %v, want [%s %s]", ids, first, second)
	}
}

// Two POSTs racing with the same client msg_id on one idle session. Whatever
// each one ends up doing (starting the run, being queued as a steer, or losing
// the run slot), the session must never end up with two messages sharing that
// identity: clients dedup by it, so the duplicate is invisible until a reload
// and then shows twice. The barrier makes both requests enter Send together.
func TestSend_ConcurrentSameClientMsgID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(
		simpleResponseHandler("reply"), simpleResponseHandler("reply2")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	var start, done sync.WaitGroup
	start.Add(1)
	accepted := make([]string, 2)
	actions := make([]string, 2)
	for i := range accepted {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			// An error is a legitimate outcome for the loser of the race (e.g.
			// it lost the run slot); a duplicated identity is not.
			actions[i], accepted[i], _, _ = mgr.Send(sess.ID, "hola", nil, "", "c-race")
		}()
	}
	start.Done()
	done.Wait()
	pollUntil(t, 5*time.Second, "runs settle", func() bool { return sessState(sess) == StateIdle })

	dupes := 0
	for _, id := range historyMsgIDs(sess) {
		if id == "c-race" {
			dupes++
		}
	}
	if dupes > 1 {
		t.Fatalf("history holds %d messages under c-race, want at most 1", dupes)
	}
	// Whoever was accepted for a direct send must have been told the identity
	// its message actually landed under.
	for i, act := range actions {
		if act == "send" && accepted[i] == "" {
			t.Fatalf("send %d was accepted without an effective msg ID", i)
		}
	}
}

// Concurrent POSTs to an idle session: some start the run, the rest are
// converted into queued steers — possibly inside the bus handler, after
// Manager.Send already classified them as direct sends (the queue rail can
// fill in between). Whatever happens, the response must describe the rail the
// message ACTUALLY landed on: a "send" answers with a msg_id that really
// identifies a message in history, a "steer" answers with a chip ID and no
// msg_id. Announcing a chip ID as a msg_id makes the client reconcile the wrong
// rail: the chip is dropped and the message renders as a phantom that vanishes
// on reload.
func TestSendHTTP_RaceReportsTheRailItLandedOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(
		delayedResponseHandler(150*time.Millisecond, "reply"),
		delayedResponseHandler(150*time.Millisecond, "reply2"),
		delayedResponseHandler(150*time.Millisecond, "reply3"),
	))
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	const senders = 4
	var start, done sync.WaitGroup
	start.Add(1)
	type sendResp struct {
		Action  string `json:"action"`
		SteerID string `json:"steer_id"`
		MsgID   string `json:"msg_id"`
	}
	out := make([]sendResp, senders)
	for i := range out {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send",
				sendBody(t, fmt.Sprintf("msg %d", i), nil))
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != 202 {
				return
			}
			_ = json.NewDecoder(resp.Body).Decode(&out[i])
		}()
	}
	start.Done()
	done.Wait()
	pollUntil(t, 10*time.Second, "runs settle", func() bool { return sessState(sess) == StateIdle })

	inHistory := map[string]bool{}
	for _, id := range historyMsgIDs(sess) {
		inHistory[id] = true
	}
	for i, r := range out {
		switch r.Action {
		case "send":
			if r.SteerID != "" {
				t.Fatalf("send %d answered with both a msg_id and a steer_id (%+v)", i, r)
			}
			if !inHistory[r.MsgID] {
				t.Fatalf("send %d answered msg_id %q, which identifies no message in history", i, r.MsgID)
			}
		case "steer":
			if r.MsgID != "" {
				t.Fatalf("steer %d answered with a msg_id (%+v): a chip is not a message", i, r)
			}
			if r.SteerID == "" {
				t.Fatalf("steer %d answered without a chip ID", i)
			}
		case "":
			// Rejected (e.g. lost the run slot): nothing to assert.
		default:
			t.Fatalf("send %d answered an unknown action %q", i, r.Action)
		}
	}
}
