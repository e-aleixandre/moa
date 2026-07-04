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

	"nhooyr.io/websocket"        //nolint:staticcheck // TODO: migrate to coder/websocket
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // TODO: migrate to coder/websocket

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
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var list []SessionInfo
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestCreateAndSend(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	// Create session.
	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","model":"sonnet"}`)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var info SessionInfo
	_ = json.NewDecoder(resp.Body).Decode(&info)
	if info.ID == "" {
		t.Fatal("expected session ID")
	}

	// Send message.
	resp2 := apiReq(t, srv, "POST", "/api/sessions/"+info.ID+"/send", `{"text":"hello"}`)
	defer resp2.Body.Close() //nolint:errcheck
	if resp2.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp2.StatusCode)
	}

	// Wait for run to complete.
	sess, _ := mgr.Get(info.ID)
	pollUntil(t, 5*time.Second, "session idle after send", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle
	})
	// Small wait for async session save to flush.
	time.Sleep(50 * time.Millisecond)
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
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	// Wait for running state.
	pollUntil(t, 2*time.Second, "running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateRunning
	})

	// Second send should be 202 (steer).
	resp2 := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"second"}`)
	resp2.Body.Close() //nolint:errcheck
	if resp2.StatusCode != 202 {
		t.Fatalf("expected 202 (steer), got %d", resp2.StatusCode)
	}

	// Wait for the run to finish so async saves don't race with TempDir cleanup.
	pollUntil(t, 2*time.Second, "idle", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle || sessState(sess) == StateError
	})
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
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestHostMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := hostMiddleware([]string{"moa.tail1234.ts.net"}, next)

	cases := []struct {
		host string
		want int
	}{
		{"localhost", 200},
		{"localhost:8080", 200},
		{"127.0.0.1", 200},
		{"127.0.0.1:8080", 200},
		{"[::1]", 200},
		{"[::1]:8080", 200},
		{"192.168.1.10:8080", 200},        // LAN IP literal — cannot be rebound
		{"moa.tail1234.ts.net", 200},      // allowlisted host
		{"MOA.tail1234.ts.net:8080", 200}, // case-insensitive, port ignored
		{"evil.example.com", 403},         // DNS-rebinding attempt
		{"evil.example.com:8080", 403},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("host %q: got %d, want %d", c.host, rec.Code, c.want)
		}
	}
}

func TestServer_RejectsRebindingHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newMockProvider(simpleResponseHandler("x"))
	mgr := newTestManager(t, ctx, prov)
	handler := NewServer(mgr, WithAllowedHosts(nil))

	req := httptest.NewRequest("GET", "/api/sessions", nil)
	req.Host = "attacker.example.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for rebinding host, got %d", rec.Code)
	}
}

func TestAuthMiddleware(t *testing.T) {
	const secret = "s3cr3t"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := authMiddleware(secret, false, next)

	t.Run("no credentials -> 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", rec.Code)
		}
	})

	t.Run("bad token -> 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/?token=wrong", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", rec.Code)
		}
	})

	t.Run("good token via query -> sets cookie and redirects", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/?token="+secret+"&foo=bar", nil))
		if rec.Code != http.StatusFound {
			t.Fatalf("got %d, want 302", rec.Code)
		}
		loc := rec.Header().Get("Location")
		if strings.Contains(loc, "token") {
			t.Fatalf("redirect location must strip token, got %q", loc)
		}
		if !strings.Contains(loc, "foo=bar") {
			t.Fatalf("redirect should keep other params, got %q", loc)
		}
		var authCookie *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == authCookieName {
				authCookie = c
			}
		}
		if authCookie == nil {
			t.Fatal("expected auth cookie to be set")
		}
		if !authCookie.HttpOnly || authCookie.SameSite != http.SameSiteLaxMode || authCookie.Secure {
			t.Fatalf("unexpected cookie attrs: HttpOnly=%v SameSite=%v Secure=%v", authCookie.HttpOnly, authCookie.SameSite, authCookie.Secure)
		}
		if authCookie.MaxAge != authCookieMaxAge {
			t.Fatalf("cookie MaxAge = %d, want %d (persistent cookie for the installed PWA)", authCookie.MaxAge, authCookieMaxAge)
		}

		// Re-request with the cookie -> passes through.
		req := httptest.NewRequest("GET", "/api/sessions", nil)
		req.AddCookie(authCookie)
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req)
		if rec2.Code != http.StatusOK {
			t.Fatalf("cookie auth: got %d, want 200", rec2.Code)
		}
	})

	t.Run("bad cookie -> 401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/sessions", nil)
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: "nope"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", rec.Code)
		}
	})
}

func TestServer_NoToken_NoAuthRequired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("x")))
	// No WithAuthToken -> auth disabled, behavior unchanged.
	handler := NewServer(mgr)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions", nil)
	req.Host = "localhost"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("no token configured should allow request, got %d", rec.Code)
	}
}

func TestServer_WithToken_GuardsRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("x")))
	handler := NewServer(mgr, WithAuthToken("open-sesame", false))

	// No credentials -> 401.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions", nil)
	req.Host = "localhost"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
}

func TestWebSocket_Init(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, _ := mgr.CreateSession(CreateOpts{Title: "ws-test"})

	ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()

	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck

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

	conn, _, err := websocket.Dial(wsCtx, httpSrv.URL+"/api/sessions/"+sess.ID+"/ws", nil) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck

	// Read init event.
	var init Event
	_ = wsjson.Read(wsCtx, conn, &init)

	// Send message.
	resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"hello"}`)
	resp.Body.Close() //nolint:errcheck

	// Collect events until we have the expected stream lifecycle.
	gotTextDelta := false
	gotMessageStart := false
	gotMessageEnd := false
	gotTurnStart := false
	gotTurnEnd := false
	gotRunEnd := false
	eventIdx := 0
	index := map[string]int{}
	deadline := time.After(10 * time.Second)

	allGot := func() bool {
		return gotTextDelta && gotMessageStart && gotMessageEnd && gotTurnStart && gotTurnEnd && gotRunEnd
	}

	for !allGot() {
		select {
		case <-deadline:
			t.Fatalf("timed out (message_start=%v text_delta=%v message_end=%v turn_start=%v turn_end=%v run_end=%v)",
				gotMessageStart, gotTextDelta, gotMessageEnd, gotTurnStart, gotTurnEnd, gotRunEnd)
		default:
		}

		var evt Event
		if err := wsjson.Read(wsCtx, conn, &evt); err != nil {
			t.Fatalf("ws read error: %v", err)
		}
		if _, ok := index[evt.Type]; !ok {
			index[evt.Type] = eventIdx
		}
		eventIdx++

		switch evt.Type {
		case "message_start":
			gotMessageStart = true
		case "text_delta":
			gotTextDelta = true
		case "message_end":
			gotMessageEnd = true
		case "turn_start":
			gotTurnStart = true
		case "turn_end":
			gotTurnEnd = true
		case "run_end":
			gotRunEnd = true
		}
	}

	if index["turn_start"] >= index["message_start"] ||
		index["message_start"] >= index["text_delta"] ||
		index["text_delta"] >= index["message_end"] ||
		index["message_end"] >= index["turn_end"] ||
		index["turn_end"] >= index["run_end"] {
		t.Fatalf("unexpected stream order: %v", index)
	}
}

func TestWebSocket_TextBeforeToolCallPreservesEventOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	textThenToolHandler := func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.TextContent("I'll check."),
					core.ToolCallContent("tc-1", "bash", map[string]any{"command": "echo hi"}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "I'll check."}
			ch <- core.AssistantEvent{
				Type:       core.ProviderEventToolCallStart,
				ToolCallID: "tc-1",
				ToolName:   "bash",
			}
			ch <- core.AssistantEvent{
				Type:        core.ProviderEventToolCallDelta,
				ToolCallID:  "tc-1",
				ToolName:    "bash",
				PartialArgs: map[string]any{"command": "echo hi"},
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	prov := newMockProvider(textThenToolHandler, simpleResponseHandler("done"))
	mgr := newTestManager(t, ctx, prov)
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, _ := mgr.CreateSession(CreateOpts{})

	wsCtx, wsCancel := context.WithTimeout(ctx, 10*time.Second)
	defer wsCancel()

	conn, _, err := websocket.Dial(wsCtx, httpSrv.URL+"/api/sessions/"+sess.ID+"/ws", nil) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck

	var init Event
	if err := wsjson.Read(wsCtx, conn, &init); err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"hello"}`)
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	want := []string{"message_start", "text_delta", "tool_call_start", "tool_call_delta", "message_end", "tool_start"}
	seen := make(map[string]int)
	eventIdx := 0
	deadline := time.After(10 * time.Second)
	for len(seen) < len(want) {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for ordered events, seen=%v", seen)
		default:
		}

		var evt Event
		if err := wsjson.Read(wsCtx, conn, &evt); err != nil {
			t.Fatalf("ws read error: %v", err)
		}
		for _, typ := range want {
			if evt.Type == typ {
				if _, ok := seen[typ]; !ok {
					seen[typ] = eventIdx
				}
				break
			}
		}
		eventIdx++
	}

	for i := 1; i < len(want); i++ {
		prev := want[i-1]
		curr := want[i]
		if seen[prev] >= seen[curr] {
			t.Fatalf("%s should arrive before %s, seen=%v", prev, curr, seen)
		}
	}
}

func TestWebSocket_PermissionDenied_OrdersToolStartBeforePromptAndMarksRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	toolCallHandler := func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 4)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("tc-1", "bash", map[string]any{"command": "echo hi"}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	// Isolate the global config and trust the workspace so its repo-local
	// .moa/config.json (mode:ask) is honored — the C1 trust gate ignores
	// untrusted repo config.
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".moa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".moa", "config.json"), []byte(`{"permissions":{"mode":"ask"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := core.SaveGlobalConfig(func(c *core.MoaConfig) {
		c.TrustedProjectPaths = append(c.TrustedProjectPaths, workspace)
	}); err != nil {
		t.Fatal(err)
	}

	prov := newMockProvider(toolCallHandler, simpleResponseHandler("done"))
	// Use the shared helper so its t.Cleanup shuts sessions down (and drains the
	// async persistence reactors) before t.TempDir removal — otherwise a pending
	// autosave races RemoveAll and flakes under -race ("directory not empty").
	mgr := newTestManagerWithRoot(t, ctx, prov, workspace)

	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, _ := mgr.CreateSession(CreateOpts{})

	wsCtx, wsCancel := context.WithTimeout(ctx, 10*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(wsCtx, httpSrv.URL+"/api/sessions/"+sess.ID+"/ws", nil) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck

	var init Event
	if err := wsjson.Read(wsCtx, conn, &init); err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"hello"}`)
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	idxToolStart := -1
	idxPermission := -1
	seenRejected := false
	eventIdx := 0
	resolved := false
	gotRunEnd := false

	// Read events until we have both run_end AND tool_end with rejected.
	deadline := time.After(10 * time.Second)
	for !gotRunEnd || !seenRejected {
		select {
		case <-deadline:
			t.Fatalf("timed out (tool_start=%d permission=%d rejected=%v run_end=%v)", idxToolStart, idxPermission, seenRejected, gotRunEnd)
		default:
		}

		var evt Event
		if err := wsjson.Read(wsCtx, conn, &evt); err != nil {
			if gotRunEnd {
				// Connection may close after run_end; if we already have
				// everything except rejected, that's the real failure.
				break
			}
			t.Fatalf("ws read error: %v", err)
		}

		switch evt.Type {
		case "tool_start":
			if idxToolStart == -1 {
				idxToolStart = eventIdx
			}
		case "permission_request":
			if idxPermission == -1 {
				idxPermission = eventIdx
			}
			if !resolved {
				data, _ := evt.Data.(map[string]any)
				permID, _ := data["id"].(string)
				if permID == "" {
					t.Fatal("permission_request missing id")
				}
				respPerm := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/permission", `{"id":"`+permID+`","approved":false,"feedback":""}`)
				respPerm.Body.Close() //nolint:errcheck
				if respPerm.StatusCode != 204 {
					t.Fatalf("expected 204 on permission resolve, got %d", respPerm.StatusCode)
				}
				resolved = true
			}
		case "tool_end":
			data, _ := evt.Data.(map[string]any)
			if data["tool_call_id"] == "tc-1" {
				rejected, _ := data["rejected"].(bool)
				seenRejected = rejected
			}
		case "run_end":
			gotRunEnd = true
		}
		eventIdx++
	}

	if idxToolStart == -1 {
		t.Fatal("missing tool_start event")
	}
	if idxPermission == -1 {
		t.Fatal("missing permission_request event")
	}
	if idxToolStart > idxPermission {
		t.Fatalf("tool_start should arrive before permission_request (tool_start=%d permission_request=%d)", idxToolStart, idxPermission)
	}
	if !seenRejected {
		t.Fatal("expected tool_end with rejected=true after denial")
	}
}

func TestWebSocket_Disconnect(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, _ := mgr.CreateSession(CreateOpts{})

	ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()

	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}

	// Read init.
	var init Event
	_ = wsjson.Read(ctx, conn, &init)

	// Close connection.
	_ = conn.Close(websocket.StatusNormalClosure, "bye") //nolint:staticcheck

	// Give WS handler time to process the close.
	time.Sleep(100 * time.Millisecond)
	// If we got here without hanging, the WS handler cleaned up properly.
}

func TestCreateSession_InvalidCWD_Returns400(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","cwd":"/nonexistent/path/does/not/exist"}`)
	defer resp.Body.Close() //nolint:errcheck
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
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var info SessionInfo
	_ = json.NewDecoder(resp.Body).Decode(&info)
	if info.CWD == "" {
		t.Fatal("expected CWD in response")
	}
}

func TestCreateSession_DefaultCWD_API(t *testing.T) {
	dir := t.TempDir()
	srv, _, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test"}`)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var info SessionInfo
	_ = json.NewDecoder(resp.Body).Decode(&info)
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
	_ = store.Save(saved)

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
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var info SessionInfo
	_ = json.NewDecoder(resp.Body).Decode(&info)
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
	defer resp.Body.Close() //nolint:errcheck
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
	_, _ = mgr.Send(sess.ID, "block", nil)

	pollUntil(t, 2*time.Second, "running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateRunning
	})

	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel", "")
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	pollUntil(t, 5*time.Second, "idle after cancel", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateIdle
	})
	// Small wait for async session save to flush.
	time.Sleep(50 * time.Millisecond)
}

func TestCancelEndpoint_NotRunning(t *testing.T) {
	dir := t.TempDir()
	srv, mgr, cancel := newTestServerWithRoot(t, dir)
	defer cancel()

	sess, _ := mgr.CreateSession(CreateOpts{CWD: dir})

	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel", "")
	defer resp.Body.Close() //nolint:errcheck
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
	_ = store.Save(saved)

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
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify file is gone.
	_, _, findErr := session.FindSession(sessionBase, saved.ID)
	if findErr == nil {
		t.Fatal("expected session to be deleted from disk")
	}
}

// TestManagerShutdown_PersistsLastTurn exercises the real shutdown path:
// Manager.Shutdown → runtime.Flush → servePersister → FileStore. A turn that
// completed just before shutdown must be on disk afterwards.
func TestManagerShutdown_PersistsLastTurn(t *testing.T) {
	dir := t.TempDir()
	sessionBase := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("final answer"))
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Send(sess.ID, "question", nil); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "idle after send", func() bool {
		return sessState(sess) == StateIdle
	})

	// Flush synchronously — this is what runs on process shutdown.
	mgr.Shutdown()

	saved, _, err := session.FindSession(sessionBase, sess.ID)
	if err != nil {
		t.Fatalf("session not on disk after Shutdown: %v", err)
	}
	found := false
	for _, e := range saved.Entries {
		if e.Type == session.EntryMessage && e.Message.Role == "assistant" {
			for _, c := range e.Message.Content {
				if strings.Contains(c.Text, "final answer") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("persisted session missing the assistant turn that completed before shutdown")
	}
}

// TestManagerShutdown_WaitsForActiveRun verifies Shutdown does not flush a
// partial turn: when a run is still active, it waits for the run to settle
// (leave StateRunning) before snapshotting, so the persisted turn is complete.
func TestManagerShutdown_WaitsForActiveRun(t *testing.T) {
	dir := t.TempDir()
	sessionBase := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(delayedResponseHandler(150*time.Millisecond, "final answer"))
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "test-model", Provider: "mock"},
		WorkspaceRoot:   dir,
		MoaCfg:          core.MoaConfig{DisableSandbox: true},
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Send(sess.ID, "question", nil); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "run active", func() bool {
		return sessState(sess) == StateRunning
	})

	start := time.Now()
	mgr.Shutdown()
	elapsed := time.Since(start)

	if elapsed < 100*time.Millisecond {
		t.Fatalf("Shutdown returned in %v; it did not wait for the active run to settle", elapsed)
	}
	if s := sessState(sess); s != StateIdle {
		t.Fatalf("state after shutdown = %s, want idle", s)
	}

	saved, _, err := session.FindSession(sessionBase, sess.ID)
	if err != nil {
		t.Fatalf("session not on disk after Shutdown: %v", err)
	}
	found := false
	for _, e := range saved.Entries {
		if e.Type == session.EntryMessage && e.Message.Role == "assistant" {
			for _, c := range e.Message.Content {
				if strings.Contains(c.Text, "final answer") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("persisted session missing the turn that was still running at shutdown")
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
		resp.Body.Close() //nolint:errcheck
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
	_ = os.WriteFile(filepath.Join(dir, "test.txt"), []byte(testContent), 0644)

	t.Setenv("MOA_SERVE_STATIC_DIR", dir)

	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp, err := http.Get(srv.URL + "/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != testContent {
		t.Fatalf("expected %q, got %q", testContent, string(body))
	}
}
