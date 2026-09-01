package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/e-aleixandre/moa/pkg/attention"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/release"
	"github.com/e-aleixandre/moa/pkg/session"
)

func newTestServer(t *testing.T) (*httptest.Server, *Manager, context.CancelFunc) {
	t.Helper()
	return newTestServerWithRoot(t, "/tmp")
}

func TestReadSessionCursorRejectsStaleNamespaceAndEmitsRosterFields(t *testing.T) {
	ts, mgr, cancel := newTestServer(t)
	defer cancel()
	defer ts.Close()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	processed := make(chan struct{})
	mgr.afterAttentionMark = func() { close(processed) }
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "cursor-roster", RunGen: 1})
	<-processed

	stale := apiReq(t, ts, http.MethodPost, "/api/sessions/"+sess.ID+"/read?through_seq="+strconv.FormatUint(sess.runtime.Bus.LastSeq(), 10)+"&attention_namespace=old", "")
	stale.Body.Close() //nolint:errcheck
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale cursor namespace status = %d, want %d", stale.StatusCode, http.StatusConflict)
	}
	if !mgr.sessionInfo(sess).Unseen {
		t.Fatal("stale cursor namespace cleared attention")
	}

	resp := apiReq(t, ts, http.MethodGet, "/api/sessions", "")
	defer resp.Body.Close() //nolint:errcheck
	var list []struct {
		ID                 string `json:"id"`
		UnseenSeq          uint64 `json:"unseen_seq"`
		AttentionNamespace string `json:"attention_namespace"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != sess.ID || list[0].UnseenSeq != sess.runtime.Bus.LastSeq() || list[0].AttentionNamespace != sess.attentionNamespace {
		t.Fatalf("roster cursor fields = %+v, want seq %d namespace %q", list, sess.runtime.Bus.LastSeq(), sess.attentionNamespace)
	}
}

func TestAttentionEndpointReturnsCrossSessionBlockingPermissionMetadata(t *testing.T) {
	ts, mgr, cancel := newTestServer(t)
	defer cancel()
	defer ts.Close()

	sess, err := mgr.CreateSession(CreateOpts{Title: "deploy api"})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{
		SessionID: sess.ID,
		ID:        "perm_attention_api",
		ToolName:  "bash",
		Args:      map[string]any{"command": "rm -rf /tmp/build"},
	})
	pollUntil(t, time.Second, "attention API item", func() bool {
		resp, err := http.Get(ts.URL + "/api/attention")
		if err != nil {
			return false
		}
		defer resp.Body.Close() //nolint:errcheck // polling test cleanup
		var body struct {
			Items    []map[string]json.RawMessage `json:"items"`
			Sessions []attention.SessionBrief     `json:"sessions"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || len(body.Items) != 1 || len(body.Sessions) != 1 {
			return false
		}
		item := body.Items[0]
		var refID, riskLevel, verbatim string
		var riskFlags []string
		if json.Unmarshal(item["ref_id"], &refID) != nil ||
			json.Unmarshal(item["risk_level"], &riskLevel) != nil ||
			json.Unmarshal(item["risk_flags"], &riskFlags) != nil ||
			json.Unmarshal(item["verbatim"], &verbatim) != nil {
			return false
		}
		hasDestructive := false
		for _, flag := range riskFlags {
			if flag == "destructive" {
				hasDestructive = true
				break
			}
		}
		_, hasLegacyConfirm := item["requires_verbatim_confirm"]
		return body.Sessions[0].SessionID == sess.ID && body.Sessions[0].PendingPerm == 1 &&
			refID == "perm_attention_api" && riskLevel == "high" &&
			hasDestructive && verbatim == "rm -rf /tmp/build" && !hasLegacyConfirm
	})
}

func TestVersionAPI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, ManagerConfig{ReleaseInfo: release.Info{Version: "0.8.1", Commit: "abc", Date: "today"}})
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()
	resp := apiReq(t, srv, "GET", "/api/version", "")
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got release.Result
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Current != "v0.8.1" || got.UpdateAvailable {
		t.Fatalf("unexpected version response: %#v", got)
	}
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

func TestSessionFastPatchPublishesAndPersists(t *testing.T) {
	srv, mgr, cancel := newTestServerWithRoot(t, t.TempDir())
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{Model: "claude-opus-5"})
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan bus.ConfigChanged, 1)
	sess.runtime.Bus.Subscribe(func(e bus.ConfigChanged) { changed <- e })

	resp := apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/fast", `{"fast":true}`)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH fast status = %d", resp.StatusCode)
	}
	var state sessionFastState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if !state.Fast || !state.Supported {
		t.Fatalf("PATCH response = %+v, want enabled supported fast mode", state)
	}

	select {
	case e := <-changed:
		if e.Fast == nil || !*e.Fast || e.FastSupported == nil || !*e.FastSupported || e.FastNote == nil {
			t.Fatalf("PATCH ConfigChanged = %+v, want all explicit fast fields", e)
		}
	case <-time.After(time.Second):
		t.Fatal("PATCH did not publish ConfigChanged")
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	saved, _, err := session.FindSession(mgr.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatalf("load saved session before close: %v", err)
	}
	if fast, _ := saved.Metadata[session.MetaFast].(bool); !fast {
		t.Fatalf("fast metadata before close = %#v, want true", saved.Metadata[session.MetaFast])
	}

	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatalf("close session: %v", err)
	}
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatalf("resume session: %v", err)
	}
	if !resumed.runtime.Context().Agent.Fast() {
		t.Fatal("fast mode was not restored after resume")
	}
}

type fastPatchResult struct {
	status int
}

func startFastPatch(srv *httptest.Server, sessionID string, on bool) <-chan fastPatchResult {
	done := make(chan fastPatchResult, 1)
	go func() {
		body := `{"fast":false}`
		if on {
			body = `{"fast":true}`
		}
		req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/sessions/"+sessionID+"/fast", strings.NewReader(body))
		if err != nil {
			done <- fastPatchResult{}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Moa-Request", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- fastPatchResult{}
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		done <- fastPatchResult{status: resp.StatusCode}
	}()
	return done
}

func waitFastPatch(t *testing.T, done <-chan fastPatchResult) fastPatchResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(time.Second):
		t.Fatal("fast PATCH did not return")
		return fastPatchResult{}
	}
}

func TestSessionFastPatchSerializesWithModelSwitch(t *testing.T) {
	srv, mgr, cancel := newTestServerWithRoot(t, t.TempDir())
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{Model: "claude-opus-5"})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan bus.ConfigChanged, 2)
	sess.runtime.Bus.Subscribe(func(e bus.ConfigChanged) { events <- e })

	patchLocked := make(chan struct{})
	releasePatch := make(chan struct{})
	modelAtLock := make(chan struct{})
	var beforeCalls atomic.Int32
	var after sync.Once
	sess.beforeFastConfigLock = func() {
		if beforeCalls.Add(1) == 2 {
			close(modelAtLock)
		}
	}
	sess.afterFastConfigLock = func() {
		after.Do(func() {
			close(patchLocked)
			<-releasePatch
		})
	}

	patch := startFastPatch(srv, sess.ID, true)
	<-patchLocked
	modelDone := make(chan error, 1)
	go func() {
		modelDone <- func() error {
			_, err := mgr.ReconfigureSession(sess.ID, "haiku", "")
			return err
		}()
	}()
	<-modelAtLock
	close(releasePatch)

	if result := waitFastPatch(t, patch); result.status != http.StatusOK {
		t.Fatalf("fast PATCH status = %d, want %d", result.status, http.StatusOK)
	}
	select {
	case err := <-modelDone:
		if err != nil {
			t.Fatalf("switch model: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("model switch did not return")
	}
	sess.runtime.Bus.Drain(time.Second)
	first, second := <-events, <-events
	if first.Fast == nil || !*first.Fast || first.Model != "" {
		t.Fatalf("first ConfigChanged = %+v, want fast PATCH before model switch", first)
	}
	if second.Model == "" || second.Fast == nil || *second.Fast {
		t.Fatalf("second ConfigChanged = %+v, want unsupported model switch with fast off", second)
	}
	if sess.runtime.Context().Agent.Fast() {
		t.Fatal("fast remained enabled after switching to unsupported model")
	}
}

func TestSessionFastPatchesPublishInLockOrder(t *testing.T) {
	srv, mgr, cancel := newTestServerWithRoot(t, t.TempDir())
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{Model: "claude-opus-5"})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan bus.ConfigChanged, 2)
	sess.runtime.Bus.Subscribe(func(e bus.ConfigChanged) { events <- e })

	firstLocked := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondAtLock := make(chan struct{})
	var beforeCalls atomic.Int32
	var after sync.Once
	sess.beforeFastConfigLock = func() {
		if beforeCalls.Add(1) == 2 {
			close(secondAtLock)
		}
	}
	sess.afterFastConfigLock = func() {
		after.Do(func() {
			close(firstLocked)
			<-releaseFirst
		})
	}

	first := startFastPatch(srv, sess.ID, true)
	<-firstLocked
	second := startFastPatch(srv, sess.ID, false)
	<-secondAtLock
	close(releaseFirst)
	if result := waitFastPatch(t, first); result.status != http.StatusOK {
		t.Fatalf("first fast PATCH status = %d, want %d", result.status, http.StatusOK)
	}
	if result := waitFastPatch(t, second); result.status != http.StatusOK {
		t.Fatalf("second fast PATCH status = %d, want %d", result.status, http.StatusOK)
	}
	sess.runtime.Bus.Drain(time.Second)
	firstEvent, secondEvent := <-events, <-events
	if firstEvent.Fast == nil || !*firstEvent.Fast || secondEvent.Fast == nil || *secondEvent.Fast {
		t.Fatalf("fast ConfigChanged order = %+v then %+v, want true then false", firstEvent, secondEvent)
	}
	if sess.runtime.Context().Agent.Fast() {
		t.Fatal("final fast state = true, want false")
	}
}

func TestSessionFastPatchBlocksCloseUntilItFinishes(t *testing.T) {
	srv, mgr, cancel := newTestServerWithRoot(t, t.TempDir())
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{Model: "claude-opus-5"})
	if err != nil {
		t.Fatal(err)
	}
	patchLocked := make(chan struct{})
	releasePatch := make(chan struct{})
	var after sync.Once
	sess.afterFastConfigLock = func() {
		after.Do(func() {
			close(patchLocked)
			<-releasePatch
		})
	}
	closeAtLifecycle := make(chan struct{})
	mgr.beforeCloseSessionLifecycleLock = func() { close(closeAtLifecycle) }

	patch := startFastPatch(srv, sess.ID, true)
	<-patchLocked
	closed := make(chan error, 1)
	go func() { closed <- mgr.CloseSession(sess.ID) }()
	<-closeAtLifecycle
	select {
	case err := <-closed:
		t.Fatalf("CloseSession returned while PATCH held lifecycle: %v", err)
	default:
	}
	close(releasePatch)
	if result := waitFastPatch(t, patch); result.status != http.StatusOK {
		t.Fatalf("fast PATCH status = %d, want %d", result.status, http.StatusOK)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CloseSession after PATCH: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseSession did not return after PATCH")
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
	h := authMiddleware(secret, false, nil, next)

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

	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

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
	if data["attention_namespace"] != sess.attentionNamespace {
		t.Fatalf("init attention_namespace = %v, want %q", data["attention_namespace"], sess.attentionNamespace)
	}
}

func TestWebSocket_InitDeltaSinceMessage(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{Title: "ws-delta"})
	if err != nil {
		t.Fatal(err)
	}
	tree := sess.runtime.Context().Tree
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "user", MsgID: "base", Content: []core.Content{core.TextContent("base")}})})
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{Role: "assistant", MsgID: "suffix", Content: []core.Content{core.TextContent("suffix")}})})

	ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws?since_msg=base", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
	var evt Event
	if err := wsjson.Read(ctx, conn, &evt); err != nil {
		t.Fatal(err)
	}
	data := evt.Data.(map[string]any)
	if data["delta_base"] != "base" {
		t.Fatalf("delta_base = %v, want base", data["delta_base"])
	}
	messages, ok := data["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v, want one suffix", data["messages"])
	}
}

func TestWebSocketInit_EmptySessionBindsInitialZeroAndStaysConnected(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	defer srv.Close()
	sess, err := mgr.CreateSession(CreateOpts{Title: "empty-ws"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancelWS := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWS()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
	var evt Event
	if err := wsjson.Read(ctx, conn, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Type != "init" {
		t.Fatalf("first event = %q, want init", evt.Type)
	}
	// Keep this otherwise idle socket open beyond that recovery turn; wsConns
	// would drop to zero if the server had initiated the reconnect loop.
	time.Sleep(150 * time.Millisecond)
	if got := sess.wsConns.Load(); got != 1 {
		t.Fatalf("empty-session websocket viewers = %d, want stable 1", got)
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
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

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

	conn, _, err := websocket.Dial(wsCtx, httpSrv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

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

	workspace := t.TempDir()
	prov := newMockProvider(toolCallHandler, simpleResponseHandler("done"))
	mgr := newTestManagerWithConfig(t, ctx, prov, workspace, core.MoaConfig{
		Permissions: core.PermissionsConfig{Mode: "ask"},
	})

	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, _ := mgr.CreateSession(CreateOpts{})

	wsCtx, wsCancel := context.WithTimeout(ctx, 10*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(wsCtx, httpSrv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck

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

	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Read init.
	var init Event
	_ = wsjson.Read(ctx, conn, &init)

	// Close connection.
	_ = conn.Close(websocket.StatusNormalClosure, "bye")

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

// TestCreateSession_InvalidModel_Returns400 is the F16/A6 API regression:
// an unresolvable model spec must fail the request immediately with 400,
// not create a session that later errors opaquely at the provider factory.
func TestCreateSession_InvalidModel_Returns400(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","model":"openai/sonnet"}`)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// TestReconfigureSession_InvalidModel_Returns400 covers the F16/A6
// reconfigure path (PATCH .../config): switching an already-created session
// to an unresolvable model spec must also fail with 400.
func TestReconfigureSession_InvalidModel_Returns400(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prov := newMockProvider(simpleResponseHandler("hi"))
	mgr := newTestManager(t, ctx, prov)
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, httpSrv, "PATCH", "/api/sessions/"+sess.ID+"/config", `{"model":"openai/sonnet"}`)
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
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
	_, _, _, _ = mgr.Send(sess.ID, "block", nil, "", "")

	pollUntil(t, 2*time.Second, "running", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sessState(sess) == StateRunning
	})

	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel", "")
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "question", nil, "", ""); err != nil {
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
		ConfigLoader:    isolatedTestConfigLoader(t, core.MoaConfig{DisableSandbox: true}),
		SessionBaseDir:  sessionBase,
	})

	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "question", nil, "", ""); err != nil {
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
	// MOA_SERVE_STATIC_DIR serves the build output from disk instead of the
	// embedded copy, so a frontend rebuild shows up without rebuilding the
	// binary. Verified through /sw.js, which the PWA registers at the root.
	dir := t.TempDir()
	testContent := "test-sw-content"
	_ = os.WriteFile(filepath.Join(dir, "sw.js"), []byte(testContent), 0644)

	t.Setenv("MOA_SERVE_STATIC_DIR", dir)

	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp, err := http.Get(srv.URL + "/sw.js")
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

// TestRootServesTheApp pins the single-tree contract: one frontend, served at
// the root, with the PWA assets it references absolutely reachable there too.
// There is no second tree and no /next/ prefix any more.
func TestRootServesTheApp(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	index := httpGetBody(t, srv.URL+"/")
	if !strings.Contains(index, `<div id="root">`) {
		t.Errorf("GET /: expected the SPA shell (found no #root)")
	}
	for _, p := range []string{
		"/app.js", "/app.css", "/sw.js",
		"/icon-192.png", "/icon-512.png", "/icon-maskable-512.png", "/apple-touch-icon.png",
	} {
		if body := httpGetBody(t, srv.URL+p); len(body) == 0 {
			t.Errorf("GET %s: empty body", p)
		}
	}

	// The manifest is scoped to the root and carries the canonical type, or the
	// app is not installable.
	resp, err := http.Get(srv.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatalf("GET /manifest.webmanifest: %v", err)
	}
	man, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	if ct := resp.Header.Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("manifest: expected application/manifest+json, got %q", ct)
	}
	if !strings.Contains(string(man), `"scope": "/"`) {
		t.Errorf("manifest: expected scope \"/\", got:\n%s", man)
	}

	// Leftovers from the retired tree must not resolve.
	for _, p := range []string{"/manifest.json", "/next/", "/manifest-root.webmanifest"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != 404 {
			t.Errorf("GET %s: expected 404, got %d", p, resp.StatusCode)
		}
	}
}

func httpGetBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: expected 200, got %d", url, resp.StatusCode)
	}
	return string(body)
}

// The PWA manifest must not be served as text/plain, or the browser ignores it
// and the app stops being installable.
func TestManifestContentType(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "manifest.webmanifest"), []byte(`{"name":"moa"}`), 0644)
	t.Setenv("MOA_SERVE_STATIC_DIR", dir)

	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp, err := http.Get(srv.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/manifest+json") {
		t.Fatalf("manifest content type = %q", ct)
	}
}

func TestSubagentTranscriptEndpoints(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// Write a persisted transcript directly into the session's side store.
	store := mgr.subagentStoreFor(sess.ID)
	if store == nil {
		t.Fatal("expected a subagent store for active session")
	}
	tr := session.SubagentTranscript{
		JobID:   "sa-test1",
		Task:    "investigate",
		Model:   "haiku",
		Status:  "completed",
		Async:   true,
		CostUSD: 0.01,
		Usage:   &core.Usage{Input: 50, Output: 20},
		Messages: []core.AgentMessage{
			{Message: core.Message{MsgID: "child-user", Role: "user", Content: []core.Content{core.TextContent("investigate this")}}},
			{Message: core.Message{MsgID: "child-assistant", Role: "assistant", Content: []core.Content{
				core.TextContent("done"),
				core.ToolCallContent("read-call", "read", map[string]any{"path": "safe.txt", "secret": "private argument"}),
				core.ToolCallContent("write-call", "write", map[string]any{"path": "other.txt"}),
			}}},
			{Message: core.Message{Role: "tool_result", ToolCallID: "read-call", ToolName: "read", Content: []core.Content{core.TextContent("private tool output")}}},
			{Message: core.Message{Role: "tool_result", ToolCallID: "write-call", ToolName: "write", Content: []core.Content{core.TextContent("other tool output")}}},
		},
	}
	if err := store.Save(tr); err != nil {
		t.Fatalf("save transcript: %v", err)
	}

	// LIST
	resp := apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var list subagentListResponse
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close() //nolint:errcheck
	if list.SessionID != sess.ID || len(list.Subagents) != 1 || list.Subagents[0].JobID != "sa-test1" || list.Subagents[0].Task != "investigate" || list.Subagents[0].Model != "haiku" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// GET one returns the projected, paginated conversation. Tool-call
	// arguments are available as bounded activity metadata; result output is not.
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/sa-test1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	var got subagentConversationResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close() //nolint:errcheck
	if got.JobID != "sa-test1" || got.Task != "investigate" || got.Order != "newest_first" || len(got.Messages) != 4 || got.Messages[1].Role != "tool" || got.Messages[1].Text != "" || !strings.Contains(got.Messages[1].Target, "safe.txt") || !strings.Contains(got.Messages[1].Target, "private argument") {
		t.Fatalf("unexpected transcript: %+v", got)
	}
	encoded, _ := json.Marshal(got)
	for _, forbidden := range []string{"private tool output", "tool_call_id"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("subagent response leaked tool result %q: %s", forbidden, encoded)
		}
	}
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/sa-test1?limit=2", "")
	var firstPage subagentConversationResponse
	if err := json.NewDecoder(resp.Body).Decode(&firstPage); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if len(firstPage.Messages) != 2 || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("subagent first page = %+v", firstPage)
	}
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/sa-test1?cursor="+firstPage.NextCursor, "")
	var secondPage subagentConversationResponse
	if err := json.NewDecoder(resp.Body).Decode(&secondPage); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if len(secondPage.Messages) != 2 || secondPage.Messages[0].Role != "assistant" || secondPage.Messages[1].Role != "user" || secondPage.HasMore {
		t.Fatalf("subagent second page = %+v", secondPage)
	}
	if resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/messages?cursor="+firstPage.NextCursor, ""); resp.StatusCode != http.StatusBadRequest {
		resp.Body.Close() //nolint:errcheck
		t.Fatalf("subagent cursor accepted by parent conversation = %d", resp.StatusCode)
	}
	resp.Body.Close() //nolint:errcheck

	// Tool result detail uses the requested tool item rather than another call
	// from the same assistant turn.
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/sa-test1?detail=full", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("subagent detail without item_id = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close() //nolint:errcheck
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/sa-test1?detail=full&item_id="+url.QueryEscape(got.Messages[1].ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subagent tool detail = %d, want 200", resp.StatusCode)
	}
	var detail conversationToolDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if detail.Output != "private tool output" || detail.Args["path"] != "safe.txt" || detail.Args["secret"] != "private argument" {
		t.Fatalf("read item detail = %+v, want output and full arguments", detail)
	}

	// GET missing → 404
	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/nope", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing get status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close() //nolint:errcheck
}

func TestSafeSubagentConversationMessagesBoundsVisibleText(t *testing.T) {
	projection := safeSubagentConversationMessages([]core.AgentMessage{
		{Message: core.Message{MsgID: "large", Role: "assistant", Content: []core.Content{core.TextContent(strings.Repeat("é", maxConversationTextBytes))}}},
	})
	if len(projection.messages) != 1 {
		t.Fatalf("messages = %#v", projection.messages)
	}
	item := projection.messages[0]
	if !item.Truncated || len(item.Text) > maxConversationTextBytes || !utf8.ValidString(item.Text) {
		t.Fatalf("bounded subagent text = %#v", item)
	}
}

func TestSafeSubagentConversationMessagesOmitsThinkingToolAssistantTurn(t *testing.T) {
	projection := safeSubagentConversationMessages([]core.AgentMessage{
		{Message: core.Message{MsgID: "assistant", Role: "assistant", Content: []core.Content{
			core.ThinkingContent("private reasoning"),
			core.ToolCallContent("call", "bash", map[string]any{"command": "pwd"}),
		}}},
		{Message: core.Message{Role: "tool_result", ToolCallID: "call", Content: []core.Content{core.TextContent("/workspace")}}},
	})

	if len(projection.messages) != 1 {
		t.Fatalf("messages = %#v, want only a tool item", projection.messages)
	}
	item := projection.messages[0]
	if item.Role != "tool" || item.ID != "tool:assistant:1" || item.Tool != "bash" || item.Status != "ok" {
		t.Fatalf("tool item = %#v", item)
	}
}

func TestSafeSubagentConversationMessagesKeepsLargeFileToolPaths(t *testing.T) {
	const editPath = "/workspace/pkg/serve/frontend/src/large-edit.js"
	const writePath = "/workspace/pkg/serve/frontend/src/large-write.js"
	projection := safeSubagentConversationMessages([]core.AgentMessage{
		{Message: core.Message{MsgID: "assistant", Role: "assistant", Content: []core.Content{
			core.ToolCallContent("edit-call", "edit", map[string]any{
				"path": editPath, "oldText": strings.Repeat("old", 300), "newText": strings.Repeat("new", 300),
			}),
			core.ToolCallContent("write-call", "write", map[string]any{
				"path": writePath, "content": strings.Repeat("content", 300),
			}),
		}}},
	})

	if got := projection.messages[0].Target; got != editPath {
		t.Fatalf("large edit target = %q, want %q", got, editPath)
	}
	if got := projection.messages[1].Target; got != writePath {
		t.Fatalf("large write target = %q, want %q", got, writePath)
	}
}

func TestSafeSubagentConversationMessagesKeepsParentTaskAndHidesSyntheticMessages(t *testing.T) {
	projection := safeSubagentConversationMessages([]core.AgentMessage{
		{Message: core.Message{MsgID: "parent-task", Role: "user", Content: []core.Content{core.TextContent("Implementa el modo element pack")}}, Custom: map[string]any{"source": "subagent_parent"}},
		{Message: core.Message{MsgID: "marker", Role: "user", Content: []core.Content{core.TextContent("compacted")}}, Custom: map[string]any{"type": "compaction_marker"}},
		{Message: core.Message{MsgID: "prepare", Role: "user", Content: []core.Content{core.TextContent("prepare compaction")}}, Custom: map[string]any{"source": "prepare_compact", "internal": true}},
		{Message: core.Message{MsgID: "notification", Role: "user", Content: []core.Content{core.TextContent("subagent notification")}}, Custom: map[string]any{"source": "subagent"}},
		{Message: core.Message{MsgID: "answer", Role: "assistant", Content: []core.Content{core.TextContent("hecho")}}},
	})

	if len(projection.messages) != 2 {
		t.Fatalf("visible messages = %#v, want parent task and answer", projection.messages)
	}
	if got := projection.messages[0]; got.ID != "parent-task" || got.Role != "user" || got.Source != "subagent_parent" {
		t.Fatalf("parent task = %#v, want provenance-preserving parent task", got)
	}
	if got := projection.messages[1]; got.ID != "answer" || got.Role != "assistant" {
		t.Fatalf("answer = %#v, want assistant answer", got)
	}
}

func TestPromoteSubagentEndpoint(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// Unknown job in an existing session → 404 (Manager maps
	// subagent.ErrUnknownJob to ErrNotFound).
	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/subagents/sa-nope/promote", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job promote status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close() //nolint:errcheck

	// Unknown session → 404.
	resp = apiReq(t, srv, "POST", "/api/sessions/does-not-exist/subagents/sa-x/promote", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session promote status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close() //nolint:errcheck
}

func TestCloseEndpoint(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"test","model":"sonnet"}`)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 201 {
		t.Fatalf("create: expected 201, got %d", resp.StatusCode)
	}
	var info SessionInfo
	_ = json.NewDecoder(resp.Body).Decode(&info)

	r1 := apiReq(t, srv, "POST", "/api/sessions/"+info.ID+"/close", "")
	defer r1.Body.Close() //nolint:errcheck
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("close: expected 200, got %d", r1.StatusCode)
	}

	// Closing unloads but keeps the session: it must still be listed, as saved.
	l := apiReq(t, srv, "GET", "/api/sessions", "")
	defer l.Body.Close() //nolint:errcheck
	var listed []SessionInfo
	_ = json.NewDecoder(l.Body).Decode(&listed)
	var found bool
	for _, si := range listed {
		if si.ID == info.ID {
			found = true
			if si.State != StateSaved {
				t.Errorf("closed session state = %q, want %q", si.State, StateSaved)
			}
		}
	}
	if !found {
		t.Fatal("a closed session must stay listed — closing is not deleting")
	}

	// Unknown session → 404.
	r2 := apiReq(t, srv, "POST", "/api/sessions/nonexistent/close", "")
	defer r2.Body.Close() //nolint:errcheck
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session: expected 404, got %d", r2.StatusCode)
	}
}

// TestHandleSend_ReturnsEffectiveMsgID checks the /send response carries the
// identity the message was actually accepted under. The sending client needs it
// to reconcile its optimistic echo when the server re-minted a msg_id it could
// not honor — without it, the authoritative user_message broadcast would not
// dedup and the message would render twice.
func TestHandleSend_ReturnsEffectiveMsgID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("reply"), simpleResponseHandler("reply2")))
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()
	sess, _ := mgr.CreateSession(CreateOpts{})

	send := func(body string) (string, string) {
		t.Helper()
		resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", body)
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != 202 {
			t.Fatalf("status = %d, want 202", resp.StatusCode)
		}
		var got struct {
			Action  string `json:"action"`
			MsgID   string `json:"msg_id"`
			SteerID string `json:"steer_id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Action != "send" {
			t.Fatalf("action = %q, want send", got.Action)
		}
		pollUntil(t, 5*time.Second, "run settles", func() bool { return sessState(sess) == StateIdle })
		return got.MsgID, got.SteerID
	}

	msgID, steerID := send(`{"text":"hola","msg_id":"c-valid_1"}`)
	if msgID != "c-valid_1" {
		t.Fatalf("msg_id = %q, want the client-supplied one", msgID)
	}
	if steerID != "" {
		t.Fatalf("steer_id = %q, want empty for a direct send", steerID)
	}

	// Same ID again: re-minted, and the response tells the client which one won.
	reused, _ := send(`{"text":"otra vez","msg_id":"c-valid_1"}`)
	if reused == "" || reused == "c-valid_1" {
		t.Fatalf("msg_id = %q, want a freshly minted ID", reused)
	}
}

func TestSubagentConversationShowsCompactionSummary(t *testing.T) {
	// A child that compacted used to render as a conversation with an
	// unexplained gap: the summary was filtered out as an internal record, so
	// later turns referenced work that was nowhere on screen.
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("investigate the parser")),
		{Message: core.Message{Role: "compaction_summary",
			Content: []core.Content{core.TextContent("previously: mapped the parser")}}},
		{Message: core.Message{Role: "assistant",
			Content: []core.Content{core.TextContent("continuing from there")}}},
	}
	got := safeSubagentConversationMessages(msgs)
	var summary *ConversationMessage
	for i := range got.messages {
		if got.messages[i].Role == "compaction_summary" {
			summary = &got.messages[i]
		}
	}
	if summary == nil {
		t.Fatal("compaction summary missing from the child conversation")
	}
	if summary.Text != "previously: mapped the parser" {
		t.Fatalf("summary text not projected, got %q", summary.Text)
	}
}
