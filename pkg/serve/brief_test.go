package serve

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

const briefTestResponse = "ATTEMPTING: Repair the brief integration\nPROGRESS: tests are running"

type briefTestProvider struct {
	mu       sync.Mutex
	requests []core.Request

	briefStarted chan struct{}
	briefRelease <-chan struct{}
	inFlight     atomic.Int32
	maxInFlight  atomic.Int32
}

func (p *briefTestProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	if req.Model.ID != "gpt-5.4-mini" && req.Model.ID != "claude-haiku-4-5-20251001" {
		return simpleResponse("main run complete"), nil
	}
	if p.briefStarted != nil {
		select {
		case p.briefStarted <- struct{}{}:
		default:
		}
	}
	inFlight := p.inFlight.Add(1)
	for {
		max := p.maxInFlight.Load()
		if inFlight <= max || p.maxInFlight.CompareAndSwap(max, inFlight) {
			break
		}
	}
	ch := make(chan core.AssistantEvent, 1)
	go func() {
		defer close(ch)
		defer p.inFlight.Add(-1)
		if p.briefRelease != nil {
			<-p.briefRelease
		}
		msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(briefTestResponse)}}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: briefTestResponse}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}()
	return ch, nil
}

func (p *briefTestProvider) briefCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	calls := 0
	for _, req := range p.requests {
		if req.Model.ID == "gpt-5.4-mini" || req.Model.ID == "claude-haiku-4-5-20251001" {
			calls++
		}
	}
	return calls
}

func (p *briefTestProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func newBriefTestSession(t *testing.T, p *briefTestProvider, model string) (*Manager, *ManagedSession) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := newTestManager(t, ctx, p)
	sess, err := mgr.CreateSession(CreateOpts{Title: "brief test", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	return mgr, sess
}

func sendAndWaitForBrief(t *testing.T, mgr *Manager, sess *ManagedSession, p *briefTestProvider) {
	t.Helper()
	if _, _, err := mgr.Send(sess.ID, "repair the session brief", nil, ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "main run completion", func() bool { return sessState(sess) == StateIdle })
	pollUntil(t, briefDebounce+time.Second, "brief generation", func() bool { return p.briefCalls() == 1 })
}

func TestSessionBrief_DebouncesEventsAndAppliesWholeBrief(t *testing.T) {
	p := &briefTestProvider{}
	mgr, sess := newBriefTestSession(t, p, "gpt-5.3-codex")

	if _, _, err := mgr.Send(sess.ID, "repair the session brief", nil, ""); err != nil {
		t.Fatal(err)
	}
	// These arrive in the same debounce window as RunEnded. They must become
	// one cheap-model request, not one request per event.
	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID})
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID})
	pollUntil(t, briefDebounce+time.Second, "one coalesced brief", func() bool { return p.briefCalls() == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := p.briefCalls(); got != 1 {
		t.Fatalf("brief calls = %d, want 1 after coalesced events", got)
	}
	info := sess.info()
	if info.BriefAttempting != "Repair the brief integration" || info.BriefProgress != "tests are running" || info.BriefUpdated.IsZero() {
		t.Fatalf("brief DTO = %+v", info)
	}
}

func TestSessionBrief_OneFlightAndCooldown(t *testing.T) {
	p := &briefTestProvider{}
	mgr, sess := newBriefTestSession(t, p, "gpt-5.3-codex")
	sendAndWaitForBrief(t, mgr, sess, p)

	// The successful generation has just set the cooldown. A direct trigger is
	// also suppressed, which covers noisy events outside the debounce window.
	mgr.runSessionBrief(sess)
	if got := p.briefCalls(); got != 1 {
		t.Fatalf("brief calls during cooldown = %d, want 1", got)
	}

	// Expire the test session's cooldown, then keep one request in flight while
	// a second trigger arrives. The second trigger may schedule a later refresh,
	// but it must never overlap the active provider request.
	sess.mu.Lock()
	sess.briefLastAttempt = time.Now().Add(-briefCooldown)
	sess.mu.Unlock()
	release := make(chan struct{})
	p.briefRelease = release
	p.briefStarted = make(chan struct{}, 1)
	go mgr.runSessionBrief(sess)
	select {
	case <-p.briefStarted:
	case <-time.After(time.Second):
		t.Fatal("brief provider did not start")
	}
	mgr.runSessionBrief(sess)
	if got := p.maxInFlight.Load(); got != 1 {
		t.Fatalf("overlapping brief requests = %d, want 1", got)
	}
	close(release)
	pollUntil(t, time.Second, "in-flight brief finish", func() bool { return !sess.briefRunning.Load() })
}

func TestSessionBrief_DeletePreventsLateWrite(t *testing.T) {
	p := &briefTestProvider{}
	mgr, sess := newBriefTestSession(t, p, "gpt-5.3-codex")
	sendAndWaitForBrief(t, mgr, sess, p)

	sess.mu.Lock()
	sess.briefAttempting = "existing brief"
	sess.briefProgress = "existing progress"
	sess.briefLastAttempt = time.Now().Add(-briefCooldown)
	sess.mu.Unlock()
	release := make(chan struct{})
	p.briefRelease = release
	p.briefStarted = make(chan struct{}, 1)
	go mgr.runSessionBrief(sess)
	select {
	case <-p.briefStarted:
	case <-time.After(time.Second):
		t.Fatal("brief provider did not start")
	}
	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	pollUntil(t, time.Second, "late generator stop", func() bool { return !sess.briefRunning.Load() })
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.briefAttempting != "existing brief" || sess.briefProgress != "existing progress" {
		t.Fatalf("deleted session brief was overwritten: attempting=%q progress=%q", sess.briefAttempting, sess.briefProgress)
	}
}

func TestSessionInfo_BriefDTOFields(t *testing.T) {
	p := &briefTestProvider{}
	_, sess := newBriefTestSession(t, p, "gpt-5.3-codex")
	now := time.Now().UTC().Round(0)
	sess.mu.Lock()
	sess.briefAttempting = "Ship the status API"
	sess.briefProgress = "endpoint is ready"
	sess.briefUpdated = now
	sess.mu.Unlock()

	encoded, err := json.Marshal(sess.info())
	if err != nil {
		t.Fatal(err)
	}
	var dto map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &dto); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"brief_attempting", "brief_progress", "brief_updated"} {
		if _, ok := dto[field]; !ok {
			t.Fatalf("SessionInfo JSON missing %q: %s", field, encoded)
		}
	}
}

func TestSessionBrief_UpdatesAttentionRoster(t *testing.T) {
	p := &briefTestProvider{}
	mgr, sess := newBriefTestSession(t, p, "gpt-5.3-codex")
	sendAndWaitForBrief(t, mgr, sess, p)

	pollUntil(t, time.Second, "attention roster brief", func() bool {
		roster := mgr.attention.Roster()
		return len(roster) == 1 && roster[0].SessionID == sess.ID &&
			roster[0].Attempting == "Repair the brief integration" &&
			roster[0].Progress == "tests are running" && !roster[0].BriefUpdated.IsZero()
	})
}

func TestResumeSession_SchedulesMissingBrief(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	sessionBase := t.TempDir()
	store, err := session.NewFileStore(sessionBase, root)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Metadata = map[string]any{"model": "gpt-5.3-codex", "cwd": root}
	saved.Messages = []core.AgentMessage{core.WrapMessage(core.NewUserMessage("repair the resumed brief"))}
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}

	p := &briefTestProvider{}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return p, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   root,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
		SessionBaseDir:  sessionBase,
	})
	t.Cleanup(mgr.Shutdown)

	sess, err := mgr.ResumeSession(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	pollUntil(t, briefDebounce+time.Second, "resumed session brief", func() bool {
		return p.briefCalls() == 1
	})
	if info := sess.info(); info.BriefAttempting != "Repair the brief integration" || info.BriefUpdated.IsZero() {
		t.Fatalf("resumed session brief = %+v", info)
	}
}

func TestSessionBrief_UnknownProviderDoesNotGenerate(t *testing.T) {
	p := &briefTestProvider{}
	mgr, sess := newBriefTestSession(t, p, "google/gemini-test")
	if _, _, err := mgr.Send(sess.ID, "repair the session brief", nil, ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "main run completion", func() bool { return sessState(sess) == StateIdle })
	time.Sleep(briefDebounce + 150*time.Millisecond)
	if got := p.calls(); got != 1 {
		t.Fatalf("provider calls = %d, want only main session call", got)
	}
	if info := sess.info(); !info.BriefUpdated.IsZero() || info.BriefAttempting != "" || info.BriefProgress != "" {
		t.Fatalf("unknown-provider session got brief: %+v", info)
	}
}
