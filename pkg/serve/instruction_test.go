package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/bus"
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
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/sessions/" + sess.ID + "/instruction"

	request := func(body, contentType string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-Moa-Request", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
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

func TestInstructionEndpointRequiresCSRFMiddleware(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID+"/instruction", strings.NewReader(`{"text":"hello","request_id":"one"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	NewServer(mgr).ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
}
