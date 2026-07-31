package serve

import (
	"context"
	"encoding/json"
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

// pendingCatcher records the first blocking request of each kind published on a
// session's bus, so a test can answer it through the API.
type pendingCatcher struct {
	mu     sync.Mutex
	askID  string
	permID string
}

func catchPending(sess *ManagedSession) *pendingCatcher {
	c := &pendingCatcher{}
	sess.runtime.Bus.Subscribe(func(e bus.AskUserRequested) {
		c.mu.Lock()
		if c.askID == "" {
			c.askID = e.ID
		}
		c.mu.Unlock()
	})
	sess.runtime.Bus.Subscribe(func(e bus.PermissionRequested) {
		c.mu.Lock()
		if c.permID == "" {
			c.permID = e.ID
		}
		c.mu.Unlock()
	})
	return c
}

func (c *pendingCatcher) ask() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.askID
}

func (c *pendingCatcher) permission() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.permID
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
	catcher := catchPending(sess)

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
	catcher := catchPending(sess)

	pollUntil(t, 5*time.Second, "permission request", func() bool { return catcher.permission() != "" })
	body := `{"id":"` + catcher.permission() + `","approved":false,"feedback":"not this time"}`
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
		{"permission feedback too long", "permission", `{"id":"p1","feedback":"` + long(maxAutomationFeedbackBytes+1) + `"}`},
		{"permission allow too long", "permission", `{"id":"p1","allow":"` + long(maxAutomationAllowBytes+1) + `"}`},
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

// An automation session that is only on disk is resumed by the endpoint, so a
// restart does not strand the caller mid-conversation.
func TestAutomationInteractionResumesSavedSession(t *testing.T) {
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true})
	id := startAutomationRun(t, mgr, "hello")
	sess, _ := mgr.Get(id)
	pollUntil(t, 5*time.Second, "first run finished", func() bool { return sessState(sess) == StateIdle })
	// Drop it from memory the way a restart would, leaving only the persisted
	// session on disk.
	mgr.mu.Lock()
	delete(mgr.sessions, id)
	mgr.mu.Unlock()

	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/reply", testAutomationToken, `{"text":"still here?"}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
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
