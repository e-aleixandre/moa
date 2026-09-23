package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/mcp"
)

// newOAuthProtectedMCP starts an authorization server (metadata, DCR, token)
// and a streamable MCP server that only accepts the token it issues. It
// returns the MCP URL and a func reporting the PKCE verifier the token
// endpoint received.
func newOAuthProtectedMCP(t *testing.T) (string, func() string) {
	t.Helper()
	var mu sync.Mutex
	var verifier string

	var as *httptest.Server
	as = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, http.StatusOK, map[string]any{
				"issuer":                           as.URL,
				"authorization_endpoint":           as.URL + "/authorize",
				"token_endpoint":                   as.URL + "/token",
				"registration_endpoint":            as.URL + "/register",
				"response_types_supported":         []string{"code"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/register":
			writeJSON(w, http.StatusCreated, map[string]any{
				"client_id":                  "client-1",
				"client_secret":              "cs-secret-1",
				"token_endpoint_auth_method": "client_secret_post",
			})
		case "/token":
			_ = r.ParseForm()
			mu.Lock()
			verifier = r.PostForm.Get("code_verifier")
			mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{
				"access_token": "at-secret-1", "refresh_token": "rt-secret-1", "token_type": "Bearer", "expires_in": 3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(as.Close)

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "oauth-helper", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "echo", Description: "Echoes back"},
		func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct{}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil, nil
		})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	var rs *httptest.Server
	rs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			writeJSON(w, http.StatusOK, map[string]any{
				"resource":              rs.URL + "/mcp",
				"authorization_servers": []string{as.URL},
			})
			return
		}
		if r.Header.Get("Authorization") != "Bearer at-secret-1" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+rs.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs.URL + "/mcp", func() string {
		mu.Lock()
		defer mu.Unlock()
		return verifier
	}
}

// Test 9: Connect from the panel — start returns the authorize URL, finish
// takes the pasted callback, stores tokens, reconnects the server, and no
// response ever carries a secret.
func TestMCPOAuthEndpoints(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	mcpURL, verifier := newOAuthProtectedMCP(t)
	srv, mgr := newTestServerWithMCP(t, core.MoaConfig{
		DisableSandbox: true,
		MCPServers: map[string]core.MCPServer{
			"remote": {URL: mcpURL},
			"local":  {Command: "definitely-not-real-zzz"},
		},
	})
	sess, err := mgr.CreateSession(CreateOpts{Title: "oauth"})
	if err != nil {
		t.Fatal(err)
	}
	waitMCPSettled(t, sess.infra.mcpMgr)

	var bodies []string
	call := func(method, path, body string, csrf bool) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set("X-Moa-Request", "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		data, _ := io.ReadAll(resp.Body)
		bodies = append(bodies, string(data))
		return resp.StatusCode, string(data)
	}
	base := "/api/sessions/" + sess.ID + "/mcp/"

	code, body := call("GET", "/api/sessions/"+sess.ID+"/mcp", "", true)
	if code != http.StatusOK || !strings.Contains(body, `"state":"auth_required"`) || !strings.Contains(body, `"auth_action":"connect"`) {
		t.Fatalf("GET mcp = %d %s, want remote auth_required/connect", code, body)
	}
	// Waiting for sign-in needs the user but is not down: it has its own count.
	if sum := sess.mcpSummary(); sum == nil || sum.AuthRequired != 1 || sum.Unhealthy != 1 {
		t.Fatalf("mcpSummary = %+v, want AuthRequired=1 (remote) and Unhealthy=1 (local)", sum)
	}

	if code, _ := call("POST", base+"remote/oauth/start", "", false); code != http.StatusForbidden {
		t.Fatalf("start without X-Moa-Request = %d, want 403", code)
	}
	if code, _ := call("POST", base+"nope/oauth/start", "", true); code != http.StatusNotFound {
		t.Fatalf("unknown server = %d, want 404", code)
	}
	if code, _ := call("POST", base+"local/oauth/start", "", true); code != http.StatusBadRequest {
		t.Fatalf("command server = %d, want 400", code)
	}

	code, body = call("POST", base+"remote/oauth/start", "", true)
	if code != http.StatusOK {
		t.Fatalf("start = %d %s", code, body)
	}
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil || started.AuthorizeURL == "" {
		t.Fatalf("start body = %s", body)
	}
	au, _ := url.Parse(started.AuthorizeURL)
	cb, _ := url.Parse(au.Query().Get("redirect_uri"))
	cb.RawQuery = url.Values{"code": {"code-secret-1"}, "state": {"wrong"}}.Encode()
	finishBody := func(u string) string {
		b, _ := json.Marshal(map[string]string{"url": u})
		return string(b)
	}

	if code, _ := call("POST", base+"remote/oauth/finish", finishBody(cb.String()), false); code != http.StatusForbidden {
		t.Fatalf("finish without X-Moa-Request = %d, want 403", code)
	}
	code, body = call("POST", base+"remote/oauth/finish", finishBody(cb.String()), true)
	if code != http.StatusBadRequest || !strings.Contains(body, "doesn't match") {
		t.Fatalf("finish with wrong state = %d %s, want 400", code, body)
	}

	cb.RawQuery = url.Values{"code": {"code-secret-1"}, "state": {au.Query().Get("state")}}.Encode()
	code, body = call("POST", base+"remote/oauth/finish", finishBody(cb.String()), true)
	if code != http.StatusOK {
		t.Fatalf("finish = %d %s", code, body)
	}
	var st mcp.ControllerStatus
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != mcp.StateReady || st.ToolCount != 1 || st.AuthAction != "" {
		t.Fatalf("finish status = %+v, want ready with 1 tool", st)
	}

	// The tool reaches the session's registry once it is idle.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := sess.infra.toolReg.Get(mcp.ServerToolPrefix("remote") + "echo"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote tool never registered after sign-in")
		}
		time.Sleep(20 * time.Millisecond)
	}

	secrets := []string{"at-secret-1", "rt-secret-1", "cs-secret-1", "code-secret-1", verifier()}
	for _, b := range bodies {
		for _, s := range secrets {
			if s != "" && strings.Contains(b, s) {
				t.Fatalf("response %q leaks %q", b, s)
			}
		}
	}
}

func TestMCPOAuthSignOutClearsCredentialsAndAllSessions(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", configDir)
	mcpURL, _ := newOAuthProtectedMCP(t)
	srv, mgr := newTestServerWithMCP(t, core.MoaConfig{DisableSandbox: true, MCPServers: map[string]core.MCPServer{
		"remote": {URL: mcpURL, Headers: map[string]string{"Authorization": "Bearer stale-static"}},
	}})
	first, err := mgr.CreateSession(CreateOpts{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.CreateSession(CreateOpts{Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	waitMCPSettled(t, first.infra.mcpMgr)
	waitMCPSettled(t, second.infra.mcpMgr)

	call := func(path string, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Moa-Request", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close() //nolint:errcheck
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data)
	}
	base := "/api/sessions/" + first.ID + "/mcp/remote/oauth/"
	code, body := call(base+"start", "")
	if code != http.StatusOK {
		t.Fatalf("start = %d %s", code, body)
	}
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil {
		t.Fatal(err)
	}
	authorizeURL, _ := url.Parse(started.AuthorizeURL)
	callback, _ := url.Parse(authorizeURL.Query().Get("redirect_uri"))
	callback.RawQuery = url.Values{"code": {"code"}, "state": {authorizeURL.Query().Get("state")}}.Encode()
	finishBody, _ := json.Marshal(map[string]string{"url": callback.String()})
	code, body = call(base+"finish", string(finishBody))
	if code != http.StatusOK || !strings.Contains(body, `"state":"ready"`) {
		t.Fatalf("finish = %d %s", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := second.mcpServerStatus("remote")
		if st.State == mcp.StateReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second session never connected: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if code, _ := call(base+"signout", ""); code != http.StatusOK {
		t.Fatalf("signout = %d", code)
	}
	for _, sess := range []*ManagedSession{first, second} {
		st, _ := sess.mcpServerStatus("remote")
		if st.State != mcp.StateAuthRequired || st.AuthAction != "connect" || st.ToolCount != 0 {
			t.Fatalf("session %s after signout = %+v", sess.ID, st)
		}
	}
	store := first.infra.mcpMgr.OAuthStore()
	if token, err := store.Token(context.Background(), auth.MCPOAuthKey(mcpURL)); token != nil || !errors.Is(err, auth.ErrMCPAuthRequired) {
		t.Fatalf("signed-out token = %v, %v", token, err)
	}
	persisted, err := os.ReadFile(filepath.Join(configDir, "mcp-oauth.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"at-secret-1", "rt-secret-1", "cs-secret-1"} {
		if strings.Contains(string(persisted), secret) {
			t.Fatalf("signout left %q on disk", secret)
		}
	}
	// A fresh Connect goes through discovery and authorization rather than the
	// configured static Authorization header.
	code, body = call(base+"start", "")
	if code != http.StatusOK || !strings.Contains(body, "authorize_url") {
		t.Fatalf("start after signout = %d %s", code, body)
	}
}
