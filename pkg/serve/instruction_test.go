package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

func TestVoiceInstructionPermissionHasNoEffect(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.State.ForceState(bus.StatePermission)

	if _, err := mgr.VoiceInstruction(sess.ID, "continue", "request-1"); err != ErrInstructionPermission {
		t.Fatalf("VoiceInstruction error = %v, want permission error", err)
	}
	if state := sess.runtime.State.Current(); state != bus.StatePermission {
		t.Fatalf("state = %s, want permission (instruction must have no effect)", state)
	}
}

func TestVoiceInstructionIdempotencySurvivesRestartWithoutTextPersistence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "instruction-idempotency.json")
	provider := newMockProvider(simpleResponseHandler("ok"))
	// Build managers directly so both simulated processes use the same private
	// idempotency store while the session itself remains a test runtime.
	first := NewManager(context.Background(), ManagerConfig{ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil }, DefaultModel: core.Model{ID: "test", Provider: "mock"}, WorkspaceRoot: root, MoaCfg: core.MoaConfig{DisableSandbox: true}, ConfigLoader: isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}), SessionBaseDir: filepath.Join(root, "sessions"), InstructionPath: path})
	t.Cleanup(first.Shutdown)
	sess, err := first.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if action, err := first.VoiceInstruction(sess.ID, "secret instruction text", "request-1"); err != nil || action != "send" {
		t.Fatalf("initial instruction = %q, %v", action, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("instruction store permissions = %v, want 0600", info.Mode().Perm())
	}
	if strings.Contains(string(raw), "secret instruction text") || strings.Contains(string(raw), "transcript") {
		t.Fatalf("instruction store contains raw content: %s", raw)
	}
	first.Shutdown()

	second := NewManager(context.Background(), ManagerConfig{ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil }, DefaultModel: core.Model{ID: "test", Provider: "mock"}, WorkspaceRoot: root, MoaCfg: core.MoaConfig{DisableSandbox: true}, ConfigLoader: isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}), SessionBaseDir: filepath.Join(root, "sessions"), InstructionPath: path})
	t.Cleanup(second.Shutdown)
	if _, err := second.ResumeSession(sess.ID); err != nil {
		t.Fatalf("resume after restart: %v", err)
	}
	if action, err := second.VoiceInstruction(sess.ID, "secret instruction text", "request-1"); err != nil || action != "send" {
		t.Fatalf("restart replay = %q, %v", action, err)
	}
	if _, err := second.VoiceInstruction(sess.ID, "different text", "request-1"); err != ErrInstructionConflict {
		t.Fatalf("restart mismatch = %v, want conflict", err)
	}
}

func TestVoiceInstructionIdempotencyExpiresAfterTTL(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mgr.instructionNow = func() time.Time { return now }
	if _, err := mgr.VoiceInstruction(sess.ID, "first", "request-1"); err != nil {
		t.Fatal(err)
	}
	mgr.instructionNow = func() time.Time { return now.Add(instructionTTL + time.Second) }
	if _, err := mgr.VoiceInstruction(sess.ID, "different", "request-1"); err == ErrInstructionConflict {
		t.Fatal("expired request id remained conflicted")
	}
}

func TestVoiceInstructionIdempotencyAndMismatch(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := mgr.VoiceInstruction(sess.ID, "first", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.VoiceInstruction(sess.ID, "first", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("duplicate action = %q, want original %q", second, first)
	}
	if _, err := mgr.VoiceInstruction(sess.ID, "different", "request-1"); err != ErrInstructionConflict {
		t.Fatalf("mismatched request ID error = %v, want conflict", err)
	}
}

func TestInstructionEndpointValidationAndRateLimit(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/sessions/" + sess.ID + "/instruction"

	request := func(body, contentType string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.SetPathValue("id", sess.ID)
		req.Header.Set("Content-Type", contentType)
		resp := httptest.NewRecorder()
		handleInstruction(mgr).ServeHTTP(resp, req)
		return resp.Result()
	}

	resp := request(`{"text":"hello","request_id":"one","unexpected":true}`, "application/json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = request(`{"text":"hello","request_id":"one"}`, "text/plain")
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("content type status = %d, want 415", resp.StatusCode)
	}
	resp.Body.Close()

	for i := 0; i < instructionSessionRate; i++ {
		resp = request(`{"text":"hello","request_id":"request-`+string(rune('a'+i))+`"}`, "application/json")
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("request %d status = %d, want 202", i, resp.StatusCode)
		}
		var result map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result["action"] == "" {
			t.Fatal("instruction response has no action")
		}
		resp.Body.Close()
	}
	resp = request(`{"text":"hello","request_id":"request-z"}`, "application/json")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate limited status = %d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
	resp.Body.Close()
}

func TestInstructionEndpointUsesNormalUnauthenticatedServePolicy(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID+"/instruction", strings.NewReader(`{"text":"hello","request_id":"one"}`))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	resp := httptest.NewRecorder()
	NewServer(mgr).ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.Code)
	}
}
