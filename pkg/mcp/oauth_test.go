package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// oauthFixture is a protected streamable MCP server (with its PRM document)
// plus an authorization server with metadata, DCR and a token endpoint. All
// real HTTP; tokens live in a real temp file.
type oauthFixture struct {
	mcp *httptest.Server
	as  *httptest.Server

	mu         sync.Mutex
	accept     map[string]bool // Authorization values the MCP server accepts; nil = no auth
	forbid     bool            // answer every MCP request with 403
	headers    []http.Header   // every request the MCP server saw
	tokenReply func(w http.ResponseWriter, form url.Values)
	refreshes  atomic.Int32
	storePath  string
}

func newOAuthFixture(t *testing.T, requireAuth bool) *oauthFixture {
	t.Helper()
	f := &oauthFixture{storePath: filepath.Join(t.TempDir(), "mcp-oauth.json")}
	if requireAuth {
		f.accept = map[string]bool{}
	}

	f.as = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			fixtureJSON(w, http.StatusOK, map[string]any{
				"issuer":                           f.as.URL,
				"authorization_endpoint":           f.as.URL + "/authorize",
				"token_endpoint":                   f.as.URL + "/token",
				"registration_endpoint":            f.as.URL + "/register",
				"response_types_supported":         []string{"code"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/register":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			fixtureJSON(w, http.StatusCreated, map[string]any{
				"client_id":                  "client-1",
				"redirect_uris":              body["redirect_uris"],
				"token_endpoint_auth_method": "none",
			})
		case "/token":
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") == "refresh_token" {
				f.refreshes.Add(1)
			}
			f.mu.Lock()
			reply := f.tokenReply
			f.mu.Unlock()
			if reply != nil {
				reply(w, r.PostForm)
				return
			}
			fixtureJSON(w, http.StatusOK, map[string]any{
				"access_token": "at-1", "refresh_token": "rt-1", "token_type": "Bearer", "expires_in": 3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.as.Close)

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "oauth-helper", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "echo", Description: "Echoes back"},
		func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct {
			Text string `json:"text"`
		}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo: " + input.Text}}}, nil, nil
		})
	mcpHandler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	f.mcp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			fixtureJSON(w, http.StatusOK, map[string]any{
				"resource":              f.mcp.URL + "/mcp",
				"authorization_servers": []string{f.as.URL},
			})
			return
		}
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		accept := f.accept
		ok := accept == nil || accept[r.Header.Get("Authorization")]
		forbid := f.forbid
		f.mu.Unlock()
		if forbid {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.mcp.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.mcp.Close)
	return f
}

func (f *oauthFixture) url() string { return f.mcp.URL + "/mcp" }

func (f *oauthFixture) setAccept(values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accept = map[string]bool{}
	for _, v := range values {
		f.accept[v] = true
	}
}

func (f *oauthFixture) setTokenReply(fn func(w http.ResponseWriter, form url.Values)) {
	f.mu.Lock()
	f.tokenReply = fn
	f.mu.Unlock()
}

func (f *oauthFixture) seenHeaders() []http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]http.Header(nil), f.headers...)
}

// seed writes a token record to the fixture's file before the store is opened,
// exactly as a previous authorization would have left it.
func (f *oauthFixture) seed(t *testing.T, access string, obtainedAgo time.Duration) {
	t.Helper()
	rec := auth.MCPOAuthRecord{
		ServerURL:     f.url(),
		Resource:      f.url(),
		Issuer:        f.as.URL,
		TokenEndpoint: f.as.URL + "/token",
		ClientID:      "client-1",
		AuthStyle:     int(oauth2.AuthStyleInParams),
		AccessToken:   access,
		RefreshToken:  "rt-1",
		ExpiresAt:     time.Now().Add(time.Hour).UnixMilli(),
		ObtainedAt:    time.Now().Add(-obtainedAgo).UnixMilli(),
	}
	data, err := json.Marshal(map[string]auth.MCPOAuthRecord{auth.MCPOAuthKey(f.url()): rec})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.storePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *oauthFixture) store() *auth.MCPOAuthStore { return auth.MCPOAuthStoreAt(f.storePath) }

func fixtureJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newOAuthManager(t *testing.T, f *oauthFixture, cfg core.MCPServer, onChange func(ServerStatus)) *Manager {
	t.Helper()
	mgr := NewManager(nil, "")
	mgr.SetOAuthStore(f.store())
	if onChange != nil {
		mgr.OnChange(onChange)
	}
	startWait(t, mgr, map[string]core.MCPServer{"remote": cfg}, nil)
	t.Cleanup(mgr.Close)
	return mgr
}

func waitServerState(t *testing.T, mgr *Manager, want ServerState) ServerStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := mgr.Status()
		if len(st) == 1 && st[0].State == want {
			return st[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v, want %s", st, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Test 4: a 401 on a tool call refreshes the token and the SDK retries the
// request on the same MCP session.
func TestOAuthRefreshOn401KeepsSession(t *testing.T) {
	f := newOAuthFixture(t, true)
	f.setAccept("Bearer at-1")
	f.seed(t, "at-1", 10*time.Minute)
	f.setTokenReply(func(w http.ResponseWriter, form url.Values) {
		fixtureJSON(w, http.StatusOK, map[string]any{
			"access_token": "at-2", "refresh_token": "rt-2", "token_type": "Bearer", "expires_in": 3600,
		})
	})
	mgr := newOAuthManager(t, f, core.MCPServer{URL: f.url()}, nil)
	waitServerState(t, mgr, StateReady)

	sess := mgr.byName["remote"]
	sess.mu.Lock()
	before := sess.session
	sess.mu.Unlock()

	f.setAccept("Bearer at-2") // the server revokes at-1
	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	res, err := tools[0].Execute(context.Background(), map[string]any{"text": "hi"}, nil)
	if err != nil || res.IsError || res.Content[0].Text != "echo: hi" {
		t.Fatalf("Execute = %+v, %v; want echo after refresh", res, err)
	}
	if n := f.refreshes.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
	sess.mu.Lock()
	after, state := sess.session, sess.state
	sess.mu.Unlock()
	if after != before || state != StateReady {
		t.Fatalf("session replaced (%v) or state %s; want the same ready session", after != before, state)
	}
	if tok, err := f.store().Token(context.Background(), auth.MCPOAuthKey(f.url())); err != nil || tok.AccessToken != "at-2" {
		t.Fatalf("stored token = %v, %v; want at-2", tok, err)
	}
}

// Test 5: the refresh after a 401 fails with invalid_grant → the live server
// moves to auth_required (reconnect), drops its tools and notifies.
func TestOAuthRefreshInvalidGrantMarksAuthRequired(t *testing.T) {
	f := newOAuthFixture(t, true)
	f.setAccept("Bearer at-1")
	f.seed(t, "at-1", 10*time.Minute)
	f.setTokenReply(func(w http.ResponseWriter, form url.Values) {
		fixtureJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
	})
	changes := make(chan ServerStatus, 32)
	mgr := newOAuthManager(t, f, core.MCPServer{URL: f.url()}, func(st ServerStatus) { changes <- st })
	waitServerState(t, mgr, StateReady)

	f.setAccept() // everything is rejected from now on
	tools := mgr.Tools()
	res, _ := tools[0].Execute(context.Background(), map[string]any{"text": "hi"}, nil)
	if !res.IsError {
		t.Fatalf("Execute = %+v, want an error result", res)
	}

	timeout := time.After(10 * time.Second)
	for {
		select {
		case st := <-changes:
			if st.State != StateAuthRequired {
				continue
			}
			if st.AuthAction != "reconnect" || st.ToolCount != 0 || st.Error != "sign-in required" {
				t.Fatalf("status = %+v, want auth_required/reconnect with no tools", st)
			}
			if got := mgr.Tools(); len(got) != 0 {
				t.Fatalf("Tools() = %d, want 0", len(got))
			}
			if exists, needs := f.store().Has(auth.MCPOAuthKey(f.url())); !exists || !needs {
				t.Fatalf("Has = %v,%v; want needs reauth", exists, needs)
			}
			return
		case <-timeout:
			t.Fatalf("no auth_required notification; status = %+v", mgr.Status())
		}
	}
}

// Test 7a: without stored tokens a static-header server sends exactly its
// configured headers, as before OAuth existed.
func TestOAuthStaticHeadersUnchangedWithoutTokens(t *testing.T) {
	f := newOAuthFixture(t, false)
	mgr := newOAuthManager(t, f, core.MCPServer{
		URL:     f.url(),
		Headers: map[string]string{"Authorization": "Bearer static", "X-Custom": "v"},
	}, nil)
	waitServerState(t, mgr, StateReady)

	seen := f.seenHeaders()
	if len(seen) == 0 {
		t.Fatal("the server saw no requests")
	}
	for _, h := range seen {
		if got := h.Values("Authorization"); len(got) != 1 || got[0] != "Bearer static" {
			t.Fatalf("Authorization = %v, want [Bearer static]", got)
		}
		if h.Get("X-Custom") != "v" {
			t.Fatalf("X-Custom = %q, want v", h.Get("X-Custom"))
		}
	}
}

// Test 7b: with tokens stored for the URL, OAuth's Authorization wins and the
// other static headers are still sent.
func TestOAuthTokenOverridesStaticAuthorization(t *testing.T) {
	f := newOAuthFixture(t, false)
	f.seed(t, "at-1", 10*time.Minute)
	mgr := newOAuthManager(t, f, core.MCPServer{
		URL:     f.url(),
		Headers: map[string]string{"Authorization": "Bearer static", "X-Custom": "v"},
	}, nil)
	waitServerState(t, mgr, StateReady)

	for _, h := range f.seenHeaders() {
		if got := h.Values("Authorization"); len(got) != 1 || got[0] != "Bearer at-1" {
			t.Fatalf("Authorization = %v, want [Bearer at-1]", got)
		}
		if h.Get("X-Custom") != "v" {
			t.Fatalf("X-Custom = %q, want v", h.Get("X-Custom"))
		}
	}
}

// Test 8: a 401 at connect with no tokens → auth_required/connect; after the
// user completes the authorization, every manager waiting on that server
// (i.e. every open session) reconnects by itself.
func TestOAuthConnectAuthRequiredThenAutoReconnect(t *testing.T) {
	f := newOAuthFixture(t, true)
	f.setAccept("Bearer at-1")

	mgrA := newOAuthManager(t, f, core.MCPServer{URL: f.url()}, nil)
	mgrB := newOAuthManager(t, f, core.MCPServer{URL: f.url()}, nil)
	for _, mgr := range []*Manager{mgrA, mgrB} {
		st := waitServerState(t, mgr, StateAuthRequired)
		if st.AuthAction != "connect" || st.Error != "sign-in required" {
			t.Fatalf("status = %+v, want auth_required/connect", st)
		}
	}

	store := mgrA.OAuthStore()
	authorizeURL, err := store.Begin(context.Background(), f.url())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	au, _ := url.Parse(authorizeURL)
	cb, _ := url.Parse(au.Query().Get("redirect_uri"))
	cb.RawQuery = url.Values{"code": {"c"}, "state": {au.Query().Get("state")}}.Encode()
	if err := store.Complete(context.Background(), f.url(), cb.String()); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	for _, mgr := range []*Manager{mgrA, mgrB} {
		st := waitServerState(t, mgr, StateReady)
		if st.ToolCount != 1 || st.AuthAction != "" {
			t.Fatalf("status = %+v, want ready with 1 tool", st)
		}
	}
}

// A 403 is not an authentication problem: the handler lets the SDK retry once
// (as its own handler does), the server fails normally and nothing is
// refreshed.
func TestOAuthForbiddenIsNotAuthRequired(t *testing.T) {
	f := newOAuthFixture(t, false)
	var hits atomic.Int32
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			hits.Add(1)
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(forbidden.Close)
	mgr := newOAuthManager(t, f, core.MCPServer{URL: forbidden.URL}, nil)
	st := waitServerState(t, mgr, StateFailed)
	if st.AuthAction != "" {
		t.Fatalf("status = %+v, want a plain failure", st)
	}
	if n := f.refreshes.Load(); n != 0 {
		t.Fatalf("refreshes = %d, want 0", n)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("server saw %d POSTs, want 2 (initialize + one SDK retry)", n)
	}
}

// A 403 on a live connection fails it as before OAuth existed: the server
// ends up exited, not waiting for sign-in.
func TestOAuthRuntimeForbiddenExitsLikeBefore(t *testing.T) {
	f := newOAuthFixture(t, false)
	mgr := newOAuthManager(t, f, core.MCPServer{
		URL:     f.url(),
		Headers: map[string]string{"Authorization": "Bearer static"},
	}, nil)
	waitServerState(t, mgr, StateReady)

	f.mu.Lock()
	f.forbid = true
	f.mu.Unlock()
	tools := mgr.Tools()
	if res, _ := tools[0].Execute(context.Background(), map[string]any{"text": "hi"}, nil); !res.IsError {
		t.Fatalf("Execute = %+v, want an error result", res)
	}
	st := waitServerState(t, mgr, StateExited)
	if st.AuthAction != "" {
		t.Fatalf("status = %+v, want a plain exit", st)
	}
	if exists, _ := f.store().Has(auth.MCPOAuthKey(f.url())); exists || f.refreshes.Load() != 0 {
		t.Fatal("a 403 must not touch OAuth state")
	}
}

// Start after Close registers nothing and leaves no store subscription;
// racing them ends the same way whichever wins.
func TestStartRacingCloseLeavesNothingBehind(t *testing.T) {
	store := auth.MCPOAuthStoreAt(filepath.Join(t.TempDir(), "mcp-oauth.json"))
	servers := map[string]core.MCPServer{"remote": {URL: "http://127.0.0.1:1/mcp"}}
	check := func(t *testing.T, mgr *Manager) {
		t.Helper()
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		if mgr.unsubscribeOAuth != nil || len(mgr.servers) != 0 || len(mgr.byName) != 0 {
			t.Fatalf("closed manager kept a subscription (%v) or sessions (%d)", mgr.unsubscribeOAuth != nil, len(mgr.servers))
		}
	}

	mgr := NewManager(nil, "")
	mgr.SetOAuthStore(store)
	mgr.Close()
	mgr.Start(context.Background(), servers, nil)
	check(t, mgr)

	for range 50 {
		mgr := NewManager(nil, "")
		mgr.SetOAuthStore(store)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); mgr.Start(context.Background(), servers, nil) }()
		go func() { defer wg.Done(); mgr.Close() }()
		wg.Wait()
		mgr.Close() // Start may have won: Close again is what the owner does last
		check(t, mgr)
	}
}

// A manager without remote servers never opens the OAuth store; one with a
// remote server opens the store of the configured directory.
func TestOAuthStoreIsResolvedLazily(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", dir)

	stdio := NewManager(nil, "")
	stdio.Start(context.Background(), map[string]core.MCPServer{"local": {Command: "true"}}, map[string]bool{"local": true})
	t.Cleanup(stdio.Close)
	if stdio.OAuthStore() != nil {
		t.Fatal("a stdio-only manager opened the OAuth store")
	}

	remote := NewManager(nil, "")
	remote.Start(context.Background(), map[string]core.MCPServer{"remote": {URL: "http://127.0.0.1:1/mcp"}}, nil)
	t.Cleanup(remote.Close)
	if got, want := remote.OAuthStore(), auth.MCPOAuthStoreAt(filepath.Join(dir, "mcp-oauth.json")); got != want {
		t.Fatal("a remote manager should use the store in MOA_CONFIG_DIR")
	}
}
