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

func opsInstructionRequest(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/ops/instruction", strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "token"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestOpsInstructionEndpointUsesNormalUnauthenticatedServePolicy(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	req := httptest.NewRequest(http.MethodPost, "/api/ops/instruction", strings.NewReader(`{"target":"x","text":"hello","request_id":"one"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	NewServer(mgr).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestOpsInstructionEndpointResolutionAndDelivery(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	first, err := mgr.CreateSession(CreateOpts{Title: "duplicate"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.CreateSession(CreateOpts{Title: "duplicate"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("token", false))

	ambiguous := opsInstructionRequest(t, handler, `{"target":" DUPLICATE ","text":"hello","request_id":"ambiguous"}`)
	if ambiguous.Code != http.StatusConflict {
		t.Fatalf("ambiguous status = %d, want 409", ambiguous.Code)
	}
	var candidates struct {
		Candidates []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Project string `json:"project"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(ambiguous.Body).Decode(&candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates.Candidates) != 2 || candidates.Candidates[0].ID == "" || candidates.Candidates[1].ID == "" {
		t.Fatalf("unsafe or incomplete ambiguity response: %#v", candidates)
	}
	if first.runtime.State.Current() != bus.StateIdle || second.runtime.State.Current() != bus.StateIdle {
		t.Fatal("ambiguous instruction changed a session state")
	}

	noMatch := opsInstructionRequest(t, handler, `{"target":"missing","text":"hello","request_id":"missing"}`)
	if noMatch.Code != http.StatusNotFound {
		t.Fatalf("no-match status = %d, want 404", noMatch.Code)
	}
}

func TestOpsInstructionEndpointSuccessPermissionAndReplay(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{Title: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("token", false))

	first := opsInstructionRequest(t, handler, `{"target":" deploy ","text":"continue","request_id":"replay"}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("success status = %d, want 202: %s", first.Code, first.Body.String())
	}
	var result struct {
		Action string `json:"action"`
		Target struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"target"`
	}
	if err := json.NewDecoder(first.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "send" || result.Target.ID != sess.ID || result.Target.Title != "deploy" {
		t.Fatalf("response = %#v, want action and resolved target provenance", result)
	}

	replay := opsInstructionRequest(t, handler, `{"target":"deploy","text":"continue","request_id":"replay"}`)
	if replay.Code != http.StatusAccepted {
		t.Fatalf("replay status = %d, want 202", replay.Code)
	}
	var replayResult struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(replay.Body).Decode(&replayResult); err != nil {
		t.Fatal(err)
	}
	if replayResult.Action != result.Action {
		t.Fatalf("replay action = %q, want %q", replayResult.Action, result.Action)
	}

	permission, err := mgr.CreateSession(CreateOpts{Title: "approval"})
	if err != nil {
		t.Fatal(err)
	}
	permission.runtime.State.ForceState(bus.StatePermission)
	denied := opsInstructionRequest(t, handler, `{"target":"approval","text":"continue","request_id":"permission"}`)
	if denied.Code != http.StatusConflict {
		t.Fatalf("permission status = %d, want 409", denied.Code)
	}
	if state := permission.runtime.State.Current(); state != bus.StatePermission {
		t.Fatalf("permission state = %s, instruction had an effect", state)
	}
}
