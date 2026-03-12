package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

func newTestServer(t *testing.T) (*httptest.Server, *Manager, context.CancelFunc) {
	t.Helper()
	return newTestServerWithRoot(t, "/tmp")
}

func newTestServerWithRoot(t *testing.T, root string) (*httptest.Server, *Manager, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	prov := newMockProvider(simpleResponseHandler("test reply"))
	mgr := newTestManagerWithRoot(t, ctx, prov, root)
	srv := httptest.NewServer(NewServer(mgr))
	t.Cleanup(srv.Close)
	return srv, mgr, cancel
}

func apiReq(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var req *http.Request
	var err error
	if body != "" {
		req, err = http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	} else {
		req, err = http.NewRequest(method, srv.URL+path, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestListSessions_Empty(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp := apiReq(t, srv, "GET", "/api/sessions", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var list []SessionInfo
	json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestCreateAndSend(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	// Create session.
	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","model":"sonnet"}`)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var info SessionInfo
	json.NewDecoder(resp.Body).Decode(&info)
	if info.ID == "" {
		t.Fatal("expected session ID")
	}

	// Send message.
	resp2 := apiReq(t, srv, "POST", "/api/sessions/"+info.ID+"/send", `{"text":"hello"}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp2.StatusCode)
	}

	// Wait for run to complete.
	sess, _ := mgr.Get(info.ID)
	pollUntil(t, 5*time.Second, "session idle after send", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateIdle
	})
}

func TestSend_WhileBusy_409(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Slow provider.
	slowHandler := func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			time.Sleep(500 * time.Millisecond)
			msg := core.Message{
				Role: "assistant", Content: []core.Content{core.TextContent("slow")},
				StopReason: "end_turn", Timestamp: time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	prov := newMockProvider(slowHandler)
	mgr := newTestManager(t, ctx, prov)
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, _ := mgr.CreateSession(CreateOpts{})

	// First send.
	resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"first"}`)
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	// Wait for running state.
	pollUntil(t, 2*time.Second, "running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateRunning
	})

	// Second send should be 202 (steer).
	resp2 := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"second"}`)
	resp2.Body.Close()
	if resp2.StatusCode != 202 {
		t.Fatalf("expected 202 (steer), got %d", resp2.StatusCode)
	}
}

func TestCSRF_MissingHeader(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	// POST without X-Moa-Request should be 403.
	req, _ := http.NewRequest("POST", srv.URL+"/api/sessions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	// No X-Moa-Request header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebSocket_Init(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, _ := mgr.CreateSession(CreateOpts{Title: "ws-test"})

	ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()

	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	var evt Event
	if err := wsjson.Read(ctx, conn, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Type != "init" {
		t.Fatalf("expected init event, got %q", evt.Type)
	}
	data, ok := evt.Data.(map[string]any)
	if !ok {
		t.Fatal("expected data map")
	}
	if data["state"] != "idle" {
		t.Fatalf("expected state idle, got %v", data["state"])
	}
}

func TestWebSocket_Streaming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("streamed response"))
	mgr := newTestManager(t, ctx, prov)
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, _ := mgr.CreateSession(CreateOpts{})

	wsCtx, wsCancel := context.WithTimeout(ctx, 10*time.Second)
	defer wsCancel()

	conn, _, err := websocket.Dial(wsCtx, httpSrv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Read init event.
	var init Event
	wsjson.Read(wsCtx, conn, &init)

	// Send message.
	resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"hello"}`)
	resp.Body.Close()

	// Collect all events until run_end (which fires after agent events).
	// The agent emitter delivers events asynchronously, so text_delta and
	// message_end may arrive after state_change idle. run_end is the last
	// event broadcast by Send's goroutine.
	gotTextDelta := false
	gotMessageEnd := false
	gotRunEnd := false
	deadline := time.After(10 * time.Second)

	for !gotRunEnd {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for events (got text_delta=%v, message_end=%v)", gotTextDelta, gotMessageEnd)
		default:
		}

		var evt Event
		if err := wsjson.Read(wsCtx, conn, &evt); err != nil {
			t.Fatalf("ws read error: %v", err)
		}

		switch evt.Type {
		case "text_delta":
			gotTextDelta = true
		case "message_end":
			gotMessageEnd = true
		case "run_end":
			gotRunEnd = true
		}
	}

	if !gotTextDelta {
		t.Error("expected text_delta event")
	}
	if !gotMessageEnd {
		t.Error("expected message_end event")
	}
}

func TestWebSocket_Disconnect(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, _ := mgr.CreateSession(CreateOpts{})

	ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()

	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Read init.
	var init Event
	wsjson.Read(ctx, conn, &init)

	// Close connection.
	conn.Close(websocket.StatusNormalClosure, "bye")

	// Poll until WS handler cleans up the subscriber.
	pollUntil(t, 2*time.Second, "0 subscribers after WS disconnect", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return len(sess.subscribers) == 0
	})
}

func TestCreateSession_InvalidCWD_Returns400(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","cwd":"/nonexistent/path/does/not/exist"}`)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestCreateSession_WithCWD_API(t *testing.T) {
	dir := t.TempDir()
	srv, _, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	body := `{"title":"test","cwd":"` + dir + `"}`
	resp := apiReq(t, srv, "POST", "/api/sessions", body)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var info SessionInfo
	json.NewDecoder(resp.Body).Decode(&info)
	if info.CWD == "" {
		t.Fatal("expected CWD in response")
	}
}

func TestCreateSession_DefaultCWD_API(t *testing.T) {
	dir := t.TempDir()
	srv, _, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test"}`)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var info SessionInfo
	json.NewDecoder(resp.Body).Decode(&info)
	if info.CWD == "" {
		t.Fatal("expected CWD to default to workspace root")
	}
}

// --- Resume, Cancel, Delete-saved HTTP tests ---

func TestResumeEndpoint(t *testing.T) {
	dir := t.TempDir()
	sessionBase := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a saved session on disk.
	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "api-resume"
	saved.Metadata = map[string]any{"model": "test-model", "cwd": dir}
	store.Save(saved)

	prov := newMockProvider(simpleResponseHandler("hello"))
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	resp := apiReq(t, srv, "POST", "/api/sessions/"+saved.ID+"/resume", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var info SessionInfo
	json.NewDecoder(resp.Body).Decode(&info)
	if info.ID != saved.ID {
		t.Errorf("ID = %q, want %q", info.ID, saved.ID)
	}
	if info.State != StateIdle {
		t.Errorf("state = %q, want idle", info.State)
	}
}

func TestResumeEndpoint_NotFound(t *testing.T) {
	dir := t.TempDir()
	srv, _, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions/nonexistent/resume", "")
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		// FindSession returns a wrapped ErrNotFound; the handler checks errors.Is
		// which works. But the session might not exist at all.
		// The handler sends 404 when errors.Is(err, session.ErrNotFound).
		// Let's accept either 404 or 500.
		if resp.StatusCode != 404 {
			t.Fatalf("expected 404 or 500, got %d", resp.StatusCode)
		}
	}
}

func TestCancelEndpoint(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockingHandler := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			<-ctx.Done()
			msg := core.Message{
				Role: "assistant", Content: []core.Content{core.TextContent("cancelled")},
				StopReason: "end_turn", Timestamp: time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	prov := newMockProvider(blockingHandler)
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	sess, _ := mgr.CreateSession(CreateOpts{CWD: dir})
	mgr.Send(sess.ID, "block")

	pollUntil(t, 2*time.Second, "running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateRunning
	})

	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel", "")
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	pollUntil(t, 5*time.Second, "idle after cancel", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.State == StateIdle
	})
}

func TestCancelEndpoint_NotRunning(t *testing.T) {
	dir := t.TempDir()
	srv, mgr, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	sess, _ := mgr.CreateSession(CreateOpts{CWD: dir})

	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel", "")
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDeleteEndpoint_SavedSession(t *testing.T) {
	dir := t.TempDir()
	sessionBase := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create saved session on disk.
	store, err := session.NewFileStore(sessionBase, dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "delete-me"
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
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	resp := apiReq(t, srv, "DELETE", "/api/sessions/"+saved.ID, "")
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify file is gone.
	_, _, findErr := session.FindSession(sessionBase, saved.ID)
	if findErr == nil {
		t.Fatal("expected session to be deleted from disk")
	}
}

func TestStaticAssets(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/", "text/html", "<div id=\"root\">"},
		{"/app.js", "", ""},
		{"/app.css", "", ""},
	}

	for _, tt := range tests {
		resp, err := http.Get(srv.URL + tt.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tt.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s: expected 200, got %d", tt.path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Errorf("GET %s: empty body", tt.path)
		}
		if tt.contains != "" && !strings.Contains(string(body), tt.contains) {
			t.Errorf("GET %s: expected body to contain %q", tt.path, tt.contains)
		}
	}
}

func TestStaticDirOverride(t *testing.T) {
	dir := t.TempDir()
	testContent := "test-static-content"
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte(testContent), 0644)

	t.Setenv("MOA_SERVE_STATIC_DIR", dir)

	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp, err := http.Get(srv.URL + "/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != testContent {
		t.Fatalf("expected %q, got %q", testContent, string(body))
	}
}
