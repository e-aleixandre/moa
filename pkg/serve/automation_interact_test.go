package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// automationInteractServer builds a server whose sessions run the given
// provider handlers under the supplied config.
func automationInteractServer(t *testing.T, moaCfg core.MoaConfig, handlers ...mockHandler) (*httptest.Server, *Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if len(handlers) == 0 {
		handlers = []mockHandler{simpleResponseHandler("test reply")}
	}
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(handlers...), t.TempDir(), moaCfg)
	srv := httptest.NewServer(NewServer(mgr, WithAutomationToken(testAutomationToken)))
	t.Cleanup(srv.Close)
	return srv, mgr
}

// startAutomationRun creates an automation session the way the API does.
func startAutomationRun(t *testing.T, mgr *Manager, prompt string) string {
	t.Helper()
	id, _, err := mgr.CreateAutomationRun(AutomationRunRequest{Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// unloadSession drops a session from memory the way a restart would, leaving
// only the persisted session on disk.
func unloadSession(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	if _, ok := mgr.Get(id); !ok {
		t.Fatalf("session %s is not loaded", id)
	}
	mgr.mu.Lock()
	delete(mgr.sessions, id)
	mgr.mu.Unlock()
}

// pendingAskCatcher records the first ask request published on a session's
// bus, so a test can answer it through the API.
type pendingAskCatcher struct {
	mu    sync.Mutex
	askID string
}

func catchPendingAsk(sess *ManagedSession) *pendingAskCatcher {
	c := &pendingAskCatcher{}
	sess.runtime.Bus.Subscribe(func(e bus.AskUserRequested) {
		c.mu.Lock()
		if c.askID == "" {
			c.askID = e.ID
		}
		c.mu.Unlock()
	})
	return c
}

func (c *pendingAskCatcher) ask() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.askID
}

// toolCallHandlerFor emits a single tool call, which is what makes a session
// block on an ask_user prompt or a permission request.
func toolCallHandlerFor(id, tool string, args map[string]any) mockHandler {
	return func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 4)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.ToolCallContent(id, tool, args)},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func TestAutomationReplySendsMessage(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	id := startAutomationRun(t, mgr, "first prompt")
	sess, _ := mgr.Get(id)
	pollUntil(t, 5*time.Second, "first run finished", func() bool { return sessState(sess) == StateIdle })

	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/reply", testAutomationToken, `{"text":"and now this"}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["action"] == "" {
		t.Errorf("response has no action: %#v", out)
	}

	pollUntil(t, 5*time.Second, "reply reached the transcript", func() bool {
		for _, msg := range sess.History() {
			if msg.Role != "user" {
				continue
			}
			for _, c := range msg.Content {
				if strings.Contains(c.Text, "and now this") {
					return true
				}
			}
		}
		return false
	})
}

func TestAutomationAskResponseAnswersPendingQuestion(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true},
		toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
			"questions": []any{map[string]any{"question": "Ship it?", "options": []any{"yes", "no"}}},
		}),
		simpleResponseHandler("shipped"))
	id := startAutomationRun(t, mgr, "ask me something")
	sess, _ := mgr.Get(id)
	catcher := catchPendingAsk(sess)

	pollUntil(t, 5*time.Second, "ask_user request", func() bool { return catcher.ask() != "" })
	body := `{"id":"` + catcher.ask() + `","answers":["yes"]}`
	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/ask-response", testAutomationToken, body, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	pollUntil(t, 5*time.Second, "run resumed after the answer", func() bool { return sessState(sess) == StateIdle })
}

func TestAutomationPermissionDecidesPendingRequest(t *testing.T) {
	srv, mgr := automationInteractServer(t,
		core.MoaConfig{DisableSandbox: true, Permissions: core.PermissionsConfig{Mode: "ask"}},
		toolCallHandlerFor("tc-bash", "bash", map[string]any{"command": "echo hi"}),
		simpleResponseHandler("denied then"))
	id := startAutomationRun(t, mgr, "run something")
	sess, _ := mgr.Get(id)

	// The run starts asynchronously before this test receives the session, so
	// subscribing now can miss the event. Read the pending source of truth that
	// late production consumers also use instead.
	var permissionID string
	pollUntil(t, 5*time.Second, "permission request", func() bool {
		pending, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](
			sess.runtime.Bus, bus.GetPendingApproval{SessionID: id},
		)
		if err != nil || pending.Permission == nil {
			return false
		}
		permissionID = pending.Permission.ID
		return true
	})
	body := `{"id":"` + permissionID + `","approved":false,"feedback":"not this time"}`
	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/permission", testAutomationToken, body, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	pollUntil(t, 5*time.Second, "run resumed after the decision", func() bool { return sessState(sess) == StateIdle })
}

// The marker is not the origin label: a session created through the ordinary
// create endpoint can claim any origin it likes and still must not be
// reachable with the automation token.
func TestAutomationInteractionRejectsSpoofedOriginSession(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	sess, err := mgr.CreateSession(CreateOpts{Origin: automationOriginDefault})
	if err != nil {
		t.Fatal(err)
	}
	if sess.automationCreated {
		t.Fatal("a normally created session claims to be automation-created")
	}
	for _, path := range []string{"reply", "ask-response", "permission"} {
		t.Run(path, func(t *testing.T) {
			resp := automationReq(t, srv, "/api/automation/sessions/"+sess.ID+"/"+path, testAutomationToken,
				`{"text":"hi","id":"x","answers":["a"]}`, false)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", resp.StatusCode)
			}
		})
	}
}

func TestAutomationInteractionUnknownSession(t *testing.T) {
	srv, _ := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	for _, path := range []string{"reply", "ask-response", "permission"} {
		t.Run(path, func(t *testing.T) {
			resp := automationReq(t, srv, "/api/automation/sessions/does-not-exist/"+path, testAutomationToken,
				`{"text":"hi","id":"x"}`, false)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", resp.StatusCode)
			}
		})
	}
}

func TestAutomationInteractionValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	id := startAutomationRun(t, mgr, "hello")
	sess, _ := mgr.Get(id)
	pollUntil(t, 5*time.Second, "first run finished", func() bool { return sessState(sess) == StateIdle })

	tests := []struct {
		name string
		path string
		body string
	}{
		{"reply invalid JSON", "reply", `{`},
		{"reply missing text", "reply", `{}`},
		{"reply blank text", "reply", `{"text":"   "}`},
		{"reply text too large", "reply", `{"text":"` + long(maxAutomationPromptBytes+1) + `"}`},
		{"ask invalid JSON", "ask-response", `{`},
		{"ask missing id", "ask-response", `{"answers":["yes"]}`},
		{"ask id too long", "ask-response", `{"id":"` + long(maxAutomationRequestIDBytes+1) + `"}`},
		{"ask answer too long", "ask-response", `{"id":"a1","answers":["` + long(maxAutomationAnswerBytes+1) + `"]}`},
		{"ask unknown request", "ask-response", `{"id":"nope","answers":["yes"]}`},
		{"permission invalid JSON", "permission", `{`},
		{"permission missing id", "permission", `{"approved":true}`},
		{"permission missing approved", "permission", `{"id":"p1"}`},
		{"permission feedback too long", "permission", `{"id":"p1","approved":true,"feedback":"` + long(maxAutomationFeedbackBytes+1) + `"}`},
		{"permission allow too long", "permission", `{"id":"p1","approved":true,"allow":"` + long(maxAutomationAllowBytes+1) + `"}`},
		{"permission unknown request", "permission", `{"id":"nope","approved":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/"+tt.path, testAutomationToken, tt.body, false)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

// The interaction endpoints live behind the same bearer as the rest of the
// Automation API, and nothing else opens them.
func TestAutomationInteractionRequiresBearer(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	id := startAutomationRun(t, mgr, "hello")
	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/reply", "", `{"text":"hi"}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// An automation session that is only on disk is resumed by /reply, so a
// restart does not strand the caller mid-conversation.
func TestAutomationReplyResumesSavedSession(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	id := startAutomationRun(t, mgr, "hello")
	sess, _ := mgr.Get(id)
	pollUntil(t, 5*time.Second, "first run finished", func() bool { return sessState(sess) == StateIdle })
	unloadSession(t, mgr, id)

	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/reply", testAutomationToken, `{"text":"still here?"}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if _, ok := mgr.Get(id); !ok {
		t.Fatal("reply did not resume the session")
	}
}

// The ask/permission endpoints never resume: a pending prompt or permission
// only exists inside a live runtime, so a saved-only session has nothing to
// answer and must not cost a full runtime build.
func TestAutomationInteractionDoesNotResumeSavedSession(t *testing.T) {
	for _, path := range []string{"ask-response", "permission"} {
		t.Run(path, func(t *testing.T) {
			srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
			id := startAutomationRun(t, mgr, "hello")
			sess, _ := mgr.Get(id)
			pollUntil(t, 5*time.Second, "first run finished", func() bool { return sessState(sess) == StateIdle })
			unloadSession(t, mgr, id)

			resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/"+path, testAutomationToken,
				`{"id":"whatever","approved":true,"answers":["yes"]}`, false)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if _, ok := mgr.Get(id); ok {
				t.Fatal("the endpoint resumed the session")
			}
		})
	}
}

// Resume-via-reply is bounded: once the resident set is at the cap, a reply to
// a saved session is refused instead of building yet another runtime.
func TestAutomationReplyRespectsLoadedSessionCap(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	id := startAutomationRun(t, mgr, "hello")
	sess, _ := mgr.Get(id)
	pollUntil(t, 5*time.Second, "first run finished", func() bool { return sessState(sess) == StateIdle })
	unloadSession(t, mgr, id)

	// Fill the resident set up to the cap with ordinary sessions.
	loadedCount := func() int {
		mgr.mu.RLock()
		defer mgr.mu.RUnlock()
		return len(mgr.sessions) + len(mgr.resuming)
	}
	for loadedCount() < maxAutomationLoadedSessions {
		if _, err := mgr.CreateSession(CreateOpts{}); err != nil {
			t.Fatal(err)
		}
	}

	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/reply", testAutomationToken, `{"text":"hi"}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if _, ok := mgr.Get(id); ok {
		t.Fatal("the capped reply resumed the session anyway")
	}
}

// A session created through the ordinary, public HTTP create endpoint is never
// reachable by the automation token, and is never loaded from disk by the
// interaction endpoints — even when the create body tries to claim the
// automation marker fields directly.
func TestAutomationInteractionIgnoresPublicSessionOnDisk(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	body := `{"title":"mine","origin":"automation","automation_created":true,"callback_url":"https://evil.example/cb","idempotency_key":"k"}`
	created := apiReq(t, srv, "POST", "/api/sessions", body)
	defer created.Body.Close() //nolint:errcheck
	if created.StatusCode != http.StatusOK && created.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", created.StatusCode)
	}
	var info SessionInfo
	if err := json.NewDecoder(created.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	// The unexported automation metadata is not part of the public create body,
	// so none of the claimed fields were stored.
	saved, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if automationCreatedMeta(saved.Metadata) {
		t.Fatalf("public create wrote automation metadata: %v", saved.Metadata)
	}
	unloadSession(t, mgr, info.ID)

	for _, path := range []string{"reply", "ask-response", "permission"} {
		t.Run(path, func(t *testing.T) {
			resp := automationReq(t, srv, "/api/automation/sessions/"+info.ID+"/"+path, testAutomationToken,
				`{"text":"hi","id":"x","approved":true,"answers":["a"]}`, false)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", resp.StatusCode)
			}
			if _, ok := mgr.Get(info.ID); ok {
				t.Fatal("the endpoint loaded a session it may not talk to")
			}
		})
	}
}

// The marker survives a restart: it is a preserved metadata key, so a resumed
// automation session is still reachable by the token.
func TestAutomationCreatedMarkerIsPersisted(t *testing.T) {
	_, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	id := startAutomationRun(t, mgr, "hello")
	saved, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if created, _ := saved.Metadata[session.MetaAutomationCreated].(bool); !created {
		t.Fatalf("metadata = %v, want %s true", saved.Metadata, session.MetaAutomationCreated)
	}
}

// Sessions created by the Automation API before the marker existed still carry
// bookkeeping only that path could write, and stay reachable.
func TestAutomationCreatedFallbacks(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want bool
	}{
		{"marker", map[string]any{session.MetaAutomationCreated: true}, true},
		{"legacy callback url", map[string]any{session.MetaCallbackURL: "https://x/y"}, true},
		{"legacy idempotency key", map[string]any{session.MetaIdempotencyKey: "k"}, true},
		{"origin alone is not enough", map[string]any{session.MetaOrigin: "automation"}, false},
		{"nothing", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := automationCreatedMeta(tt.meta); got != tt.want {
				t.Fatalf("automationCreatedMeta(%v) = %v, want %v", tt.meta, got, tt.want)
			}
		})
	}
}

// Concurrent replies to different saved sessions cannot race past the resident
// cap: the check is atomic with the resume reservation. (Regression for the
// TOCTOU found in review: count-then-reserve used to be two critical sections.)
func TestAutomationReplyCapAdmissionIsAtomic(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})

	// Two saved automation sessions, both unloaded.
	ids := make([]string, 2)
	for i := range ids {
		ids[i] = startAutomationRun(t, mgr, "hello")
		sess, _ := mgr.Get(ids[i])
		pollUntil(t, 5*time.Second, "run finished", func() bool { return sessState(sess) == StateIdle })
		unloadSession(t, mgr, ids[i])
	}

	// Fill the resident set to one below the cap: only one slot left.
	loadedCount := func() int {
		mgr.mu.RLock()
		defer mgr.mu.RUnlock()
		return len(mgr.sessions) + len(mgr.resuming)
	}
	for loadedCount() < maxAutomationLoadedSessions-1 {
		if _, err := mgr.CreateSession(CreateOpts{}); err != nil {
			t.Fatal(err)
		}
	}

	// Race both replies for the single remaining slot.
	results := make(chan int, len(ids))
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/reply", testAutomationToken, `{"text":"hi"}`, false)
			defer resp.Body.Close() //nolint:errcheck
			results <- resp.StatusCode
		}(id)
	}
	wg.Wait()
	close(results)

	var accepted, refused int
	for code := range results {
		switch code {
		case http.StatusAccepted:
			accepted++
		case http.StatusServiceUnavailable:
			refused++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("accepted=%d refused=%d, want exactly one of each", accepted, refused)
	}
	if got := loadedCount(); got > maxAutomationLoadedSessions {
		t.Fatalf("resident set %d exceeds cap %d", got, maxAutomationLoadedSessions)
	}
}

// Direct admission-primitive test: many concurrent resumeSession calls for
// distinct on-disk sessions competing for one remaining slot admit exactly one.
// Wider window than the HTTP test above: the contenders enter simultaneously.
func TestResumeSessionCapAdmitsExactlyOne(t *testing.T) {
	_, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})

	// Five ordinary sessions saved to disk and unloaded: resumable contenders.
	const contenders = 5
	ids := make([]string, contenders)
	for i := range ids {
		sess, err := mgr.CreateSession(CreateOpts{})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = sess.ID
		unloadSession(t, mgr, sess.ID)
	}

	loadedCount := func() int {
		mgr.mu.RLock()
		defer mgr.mu.RUnlock()
		return len(mgr.sessions) + len(mgr.resuming)
	}
	cap := loadedCount() + 1 // exactly one free slot

	start := make(chan struct{})
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start // all contenders enter the admission check together
			_, err := mgr.resumeSession(id, cap)
			results <- err
		}(id)
	}
	close(start)
	wg.Wait()
	close(results)

	var admitted, refused int
	for err := range results {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ErrAutomationTooManySessions):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if admitted != 1 || refused != contenders-1 {
		t.Fatalf("admitted=%d refused=%d, want 1/%d", admitted, refused, contenders-1)
	}
	if got := loadedCount(); got > cap {
		t.Fatalf("resident set %d exceeds cap %d", got, cap)
	}
}
