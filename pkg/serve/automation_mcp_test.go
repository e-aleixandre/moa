package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/mcp"
	"github.com/e-aleixandre/moa/pkg/session"
)

// newRelayMCPServer starts a streamable-HTTP MCP server exposing one
// "mark_task_done" tool — the shape a Linear-style relay would have.
func newRelayMCPServer(t *testing.T) string {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "relay", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "mark_task_done",
		Description: "Marks the originating task done",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct {
		ID string `json:"id"`
	}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "done " + input.ID}},
		}, nil, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

// readBody reads an error response body for assertions.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAutomationRunWithPerRunMCPServer(t *testing.T) {
	relayURL := newRelayMCPServer(t)
	srv, mgr := newAutomationTestServer(t, testAutomationToken)

	body := fmt.Sprintf(`{"prompt":"close the task","mcp_servers":[{"name":"relay","url":%q,"headers":{"Authorization":"Bearer x"}}]}`, relayURL)
	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	run := decodeRun(t, resp)

	sess, ok := mgr.Get(run.SessionID)
	if !ok {
		t.Fatal("session not found")
	}

	// The server arrives through the normal manager, so it is visible to the
	// MCP panel exactly like a configured one.
	status := sess.infra.mcpMgr.Status()
	if len(status) != 1 || status[0].Name != "relay" || status[0].State != mcp.StateReady {
		t.Fatalf("MCP status = %+v, want one ready relay", status)
	}
	if len(status[0].ToolNames) != 1 || status[0].ToolNames[0] != "mark_task_done" {
		t.Fatalf("relay tools = %v", status[0].ToolNames)
	}

	// And the tool is agent-visible in the session's registry.
	var found bool
	for _, spec := range sess.infra.toolReg.Specs() {
		if strings.Contains(spec.Name, "mark_task_done") {
			found = true
		}
	}
	if !found {
		t.Fatal("per-run MCP tool is not registered on the session")
	}

	// The untrusted-MCP gate is about a project .mcp.json, not about servers the
	// operator's own token attached: it must stay off.
	if sess.infra.UntrustedMCP {
		t.Error("per-run MCP servers must not trigger the untrusted-MCP gate")
	}

	// Persisted for resume, headers included.
	saved, _, err := session.FindSession(mgr.sessionBaseDir, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := saved.Metadata[session.MetaMCPServers].([]any)
	if len(entries) != 1 {
		t.Fatalf("persisted mcp_servers = %v, want one entry", saved.Metadata[session.MetaMCPServers])
	}
	entry, _ := entries[0].(map[string]any)
	if entry["name"] != "relay" || entry["url"] != relayURL {
		t.Fatalf("persisted entry = %v", entry)
	}
	headers, _ := entry["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer x" {
		t.Fatalf("persisted headers = %v", headers)
	}
}

func TestAutomationPerRunMCPServersSurviveResume(t *testing.T) {
	relayURL := newRelayMCPServer(t)
	srv, mgr := newAutomationTestServer(t, testAutomationToken)

	body := fmt.Sprintf(`{"prompt":"work","mcp_servers":[{"name":"relay","url":%q}]}`, relayURL)
	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
	defer resp.Body.Close() //nolint:errcheck
	run := decodeRun(t, resp)

	// Unload without deleting: the session file (and its metadata) stays.
	sess, ok := mgr.Get(run.SessionID)
	if !ok {
		t.Fatal("session not found")
	}
	pollUntil(t, 5*time.Second, "automation run finished", func() bool {
		return sessState(sess) == StateIdle
	})
	unloadSession(t, mgr, run.SessionID)
	// unloadSession models a restart, which also drops the process holding the
	// MCP connection. Close it explicitly so the orphaned session's live stream
	// doesn't keep the relay's httptest server open past the test.
	sess.infra.mcpMgr.Close()
	sess.infra.sessionCancel()

	resumed, err := mgr.ResumeSession(run.SessionID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.infra.mcpMgr == nil {
		t.Fatal("resumed session has no MCP manager")
	}
	status := resumed.infra.mcpMgr.Status()
	if len(status) != 1 || status[0].Name != "relay" || status[0].State != mcp.StateReady {
		t.Fatalf("resumed MCP status = %+v, want a ready relay", status)
	}
}

func TestAutomationPerRunMCPAbsentFromNormalSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newMockProvider(simpleResponseHandler("hi"))
	mgr := newTestManagerWithRoot(t, ctx, prov, "/tmp")

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.infra.mcpMgr != nil {
		t.Fatalf("a plain session must have no MCP manager, got %+v", sess.infra.mcpMgr.Status())
	}
}

func TestAutomationMCPServersRejected(t *testing.T) {
	srv, _ := newAutomationTestServer(t, testAutomationToken)

	longName := strings.Repeat("a", maxAutomationMCPNameBytes+1)
	longURL := "https://example.com/" + strings.Repeat("p", maxAutomationMCPURLBytes)
	manyHeaders := make([]string, 0, maxAutomationMCPHeaders+1)
	for i := 0; i <= maxAutomationMCPHeaders; i++ {
		manyHeaders = append(manyHeaders, fmt.Sprintf(`"H%d":"v"`, i))
	}
	bigHeader := strings.Repeat("v", maxAutomationMCPHeaderBytes+1)

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			"command key is a hard error, not ignored",
			`{"prompt":"p","mcp_servers":[{"name":"evil","url":"https://a/b","command":"/bin/sh"}]}`,
			"url-based",
		},
		{
			"args key rejected too",
			`{"prompt":"p","mcp_servers":[{"name":"evil","url":"https://a/b","args":["-c","id"]}]}`,
			"url-based",
		},
		{
			"env key rejected too",
			`{"prompt":"p","mcp_servers":[{"name":"evil","url":"https://a/b","env":{"X":"1"}}]}`,
			"url-based",
		},
		{
			"command without url is still rejected",
			`{"prompt":"p","mcp_servers":[{"name":"evil","command":"/bin/sh"}]}`,
			"url-based",
		},
		{
			"missing url",
			`{"prompt":"p","mcp_servers":[{"name":"relay"}]}`,
			"mcp_servers",
		},
		{
			"non-http scheme",
			`{"prompt":"p","mcp_servers":[{"name":"relay","url":"file:///etc/passwd"}]}`,
			"http",
		},
		{
			"missing name",
			`{"prompt":"p","mcp_servers":[{"url":"https://a/b"}]}`,
			"requires a name",
		},
		{
			"bad name charset",
			`{"prompt":"p","mcp_servers":[{"name":"re lay","url":"https://a/b"}]}`,
			"may contain only",
		},
		{
			"name too long",
			`{"prompt":"p","mcp_servers":[{"name":"` + longName + `","url":"https://a/b"}]}`,
			"name too long",
		},
		{
			"url too long",
			`{"prompt":"p","mcp_servers":[{"name":"relay","url":"` + longURL + `"}]}`,
			"url too long",
		},
		{
			"duplicate names",
			`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://a/b"},{"name":"relay","url":"https://c/d"}]}`,
			"duplicate",
		},
		{
			"too many servers",
			`{"prompt":"p","mcp_servers":[
				{"name":"a","url":"https://a/1"},{"name":"b","url":"https://a/2"},
				{"name":"c","url":"https://a/3"},{"name":"d","url":"https://a/4"},
				{"name":"e","url":"https://a/5"}]}`,
			"too many mcp_servers",
		},
		{
			"too many headers",
			`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://a/b","headers":{` + strings.Join(manyHeaders, ",") + `}}]}`,
			"too many headers",
		},
		{
			"headers too large",
			`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://a/b","headers":{"Authorization":"` + bigHeader + `"}}]}`,
			"headers too large",
		},
		{
			"invalid header name",
			`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://a/b","headers":{"Bad Header":"v"}}]}`,
			"invalid header name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, tt.body, false)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			body := readBody(t, resp)
			if !strings.Contains(body, tt.want) {
				t.Fatalf("body = %q, want it to mention %q", body, tt.want)
			}
		})
	}
}

func TestAutomationMCPNameCollisionRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newMockProvider(simpleResponseHandler("hi"))
	// A configured (never started here — the request is refused first) server
	// named "relay" makes the per-run name unavailable.
	cfg := core.MoaConfig{
		DisableSandbox: true,
		MCPServers:     map[string]core.MCPServer{"relay": {Command: "true"}},
	}
	mgr := newTestManagerWithConfig(t, ctx, prov, "/tmp", cfg)
	srv := httptest.NewServer(NewServer(mgr, WithAutomationToken(testAutomationToken)))
	defer srv.Close()

	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken,
		`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://a/b"}]}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "already a configured server") {
		t.Fatalf("body = %q", body)
	}
}

func TestMCPServersFromMetaRejectsCommandEntries(t *testing.T) {
	// A hand-edited session file must not be able to smuggle a local command
	// back in through the resume path.
	meta := map[string]any{session.MetaMCPServers: []any{
		map[string]any{"name": "ok", "url": "https://a/b"},
		map[string]any{"name": "evil", "url": "https://c/d", "command": "/bin/sh"},
		map[string]any{"name": "bad scheme", "url": "file:///etc/passwd"},
		map[string]any{"name": "no url"},
	}}
	got := mcpServersFromMeta(meta, nil)
	if len(got) != 1 {
		t.Fatalf("got %d servers, want only the valid one: %v", len(got), got)
	}
	srv, ok := got["ok"]
	if !ok || srv.Command != "" || srv.URL != "https://a/b" {
		t.Fatalf("rebuilt server = %+v", got)
	}
}

func TestAutomationMCPMetaRoundTrip(t *testing.T) {
	servers := []AutomationMCPServer{{
		Name:    "relay",
		URL:     "https://relay.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer t"},
	}}
	// Metadata survives a JSON round-trip through the session file.
	raw, err := json.Marshal(map[string]any{session.MetaMCPServers: automationMCPMeta(servers)})
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	got := mcpServersFromMeta(meta, nil)
	want := core.MCPServer{URL: "https://relay.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}
	if len(got) != 1 || got["relay"].URL != want.URL || got["relay"].Headers["Authorization"] != "Bearer t" {
		t.Fatalf("round-tripped = %+v, want %+v", got, want)
	}
}

func TestAutomationMCPCommandAliasVariants(t *testing.T) {
	relayURL := newRelayMCPServer(t)
	srv, mgr := newAutomationTestServer(t, testAutomationToken)

	// Go's JSON decoder matches field names case-insensitively, and a JSON null
	// still fills the RawMessage — both must hit the url-only rule.
	rejected := []struct {
		name string
		body string
	}{
		{"null command", `{"prompt":"p","mcp_servers":[{"name":"evil","url":"https://a/b","command":null}]}`},
		{"capitalized Command", `{"prompt":"p","mcp_servers":[{"name":"evil","url":"https://a/b","Command":"/bin/sh"}]}`},
		{"upper COMMAND", `{"prompt":"p","mcp_servers":[{"name":"evil","url":"https://a/b","COMMAND":"/bin/sh"}]}`},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, tt.body, false)
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if body := readBody(t, resp); !strings.Contains(body, "url-based") {
				t.Fatalf("body = %q", body)
			}
		})
	}

	// An unknown alias is not a command field: it is ignored, and the server
	// that gets built is url-only.
	t.Run("unknown alias is ignored and harmless", func(t *testing.T) {
		body := fmt.Sprintf(`{"prompt":"p","mcp_servers":[{"name":"relay","url":%q,"cmd":"/bin/sh"}]}`, relayURL)
		resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d (%s), want 201", resp.StatusCode, readBody(t, resp))
		}
		run := decodeRun(t, resp)
		sess, ok := mgr.Get(run.SessionID)
		if !ok {
			t.Fatal("session not found")
		}
		status := sess.infra.mcpMgr.Status()
		if len(status) != 1 || status[0].Name != "relay" || status[0].State != mcp.StateReady {
			t.Fatalf("MCP status = %+v, want one ready url-based relay", status)
		}
	})
}

func TestAutomationMCPHeaderValueControlCharsRejected(t *testing.T) {
	srv, _ := newAutomationTestServer(t, testAutomationToken)

	for _, body := range []string{
		`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://a/b","headers":{"Authorization":"Bearer x\r\nX-Evil: 1"}}]}`,
		`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://a/b","headers":{"Authorization":"Bearer x\nfoo"}}]}`,
		`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://a/b","headers":{"Authorization":"Bearer \u0000x"}}]}`,
	} {
		resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d for %s, want 400", resp.StatusCode, body)
		}
		if got := readBody(t, resp); !strings.Contains(got, "invalid header value") {
			t.Fatalf("body = %q", got)
		}
		resp.Body.Close() //nolint:errcheck
	}
}

func TestAutomationMCPURLUserinfoRejected(t *testing.T) {
	srv, _ := newAutomationTestServer(t, testAutomationToken)

	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken,
		`{"prompt":"p","mcp_servers":[{"name":"relay","url":"https://user:pass@a/b"}]}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "use headers") {
		t.Fatalf("body = %q", body)
	}
}

func TestMCPServersFromMetaAppliesFullValidator(t *testing.T) {
	// A hand-edited session file must not restore what the API would refuse.
	manyEntries := []any{}
	for i := 0; i <= maxAutomationMCPServers; i++ {
		manyEntries = append(manyEntries, map[string]any{
			"name": fmt.Sprintf("s%d", i), "url": fmt.Sprintf("https://a/%d", i),
		})
	}
	cases := []struct {
		name string
		meta map[string]any
	}{
		{"over the server cap", map[string]any{session.MetaMCPServers: manyEntries}},
		{"overlong url", map[string]any{session.MetaMCPServers: []any{
			map[string]any{"name": "relay", "url": "https://example.com/" + strings.Repeat("p", maxAutomationMCPURLBytes)},
		}}},
		{"too many headers", map[string]any{session.MetaMCPServers: []any{
			map[string]any{"name": "relay", "url": "https://a/b", "headers": func() map[string]any {
				h := map[string]any{}
				for i := 0; i <= maxAutomationMCPHeaders; i++ {
					h[fmt.Sprintf("H%d", i)] = "v"
				}
				return h
			}()},
		}}},
		{"oversized headers", map[string]any{session.MetaMCPServers: []any{
			map[string]any{"name": "relay", "url": "https://a/b", "headers": map[string]any{
				"Authorization": strings.Repeat("v", maxAutomationMCPHeaderBytes+1),
			}},
		}}},
		{"invalid header name", map[string]any{session.MetaMCPServers: []any{
			map[string]any{"name": "relay", "url": "https://a/b", "headers": map[string]any{"Bad Header": "v"}},
		}}},
		{"control chars in header value", map[string]any{session.MetaMCPServers: []any{
			map[string]any{"name": "relay", "url": "https://a/b", "headers": map[string]any{"Authorization": "x\r\nX-Evil: 1"}},
		}}},
		{"userinfo in url", map[string]any{session.MetaMCPServers: []any{
			map[string]any{"name": "relay", "url": "https://user:pass@a/b"},
		}}},
		{"overlong name", map[string]any{session.MetaMCPServers: []any{
			map[string]any{"name": strings.Repeat("a", maxAutomationMCPNameBytes+1), "url": "https://a/b"},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpServersFromMeta(tc.meta, nil)
			if tc.name == "over the server cap" {
				if len(got) != maxAutomationMCPServers {
					t.Fatalf("got %d servers, want the cap %d", len(got), maxAutomationMCPServers)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("got %v, want nothing started", got)
			}
		})
	}
}

func TestMCPServersFromMetaDropsConfiguredCollisions(t *testing.T) {
	// The operator configured "relay" after the session was created: their
	// server must win, and the stale per-run entry must not be restored.
	meta := map[string]any{session.MetaMCPServers: []any{
		map[string]any{"name": "relay", "url": "https://stale.example.com/mcp"},
		map[string]any{"name": "other", "url": "https://other.example.com/mcp"},
	}}
	configured := map[string]core.MCPServer{"relay": {Command: "true"}}
	got := mcpServersFromMeta(meta, configured)
	if _, ok := got["relay"]; ok {
		t.Fatalf("per-run relay overrode the configured one: %+v", got)
	}
	if got["other"].URL != "https://other.example.com/mcp" {
		t.Fatalf("non-colliding server lost: %+v", got)
	}
}

func TestAutomationResumeConfiguredServerWinsOverPerRun(t *testing.T) {
	relayURL := newRelayMCPServer(t)
	srv, mgr := newAutomationTestServer(t, testAutomationToken)

	body := fmt.Sprintf(`{"prompt":"work","mcp_servers":[{"name":"relay","url":%q}]}`, relayURL)
	resp := automationReq(t, srv, "/api/automation/runs", testAutomationToken, body, false)
	defer resp.Body.Close() //nolint:errcheck
	run := decodeRun(t, resp)

	sess, ok := mgr.Get(run.SessionID)
	if !ok {
		t.Fatal("session not found")
	}
	pollUntil(t, 5*time.Second, "automation run finished", func() bool {
		return sessState(sess) == StateIdle
	})
	unloadSession(t, mgr, run.SessionID)
	sess.infra.mcpMgr.Close()
	sess.infra.sessionCancel()

	// The operator now configures a server of the same name.
	mgr.configLoader = func(string) core.MoaConfig {
		return core.MoaConfig{
			DisableSandbox: true,
			MCPServers:     map[string]core.MCPServer{"relay": {Command: "definitely-not-real-zzz"}},
		}
	}

	resumed, err := mgr.ResumeSession(run.SessionID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	status := resumed.infra.mcpMgr.Status()
	if len(status) != 1 || status[0].Name != "relay" {
		t.Fatalf("resumed MCP status = %+v, want a single relay", status)
	}
	// The configured (command-based, bogus) server was used, so it failed —
	// which is exactly the proof the per-run URL entry did not override it.
	if status[0].State != mcp.StateFailed {
		t.Fatalf("relay state = %s, want the configured command-based server to have been used (failed)", status[0].State)
	}
}

func TestCreateAutomationRunCanonicalizesCWDBeforeCollisionCheck(t *testing.T) {
	// A symlinked cwd must be resolved BEFORE the collision check, so the
	// validated config is the config the session actually runs with.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := newMockProvider(simpleResponseHandler("hi"))
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := core.CanonicalizePath(real)
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	mgr := newTestManagerWithRoot(t, ctx, prov, real)
	base := mgr.configLoader
	mgr.configLoader = func(cwd string) core.MoaConfig {
		seen = append(seen, cwd)
		return base(cwd)
	}

	if _, _, err := mgr.CreateAutomationRun(AutomationRunRequest{
		Prompt:     "p",
		CWD:        link,
		MCPServers: []AutomationMCPServer{{Name: "relay", URL: "https://a/b"}},
	}); err != nil {
		t.Fatalf("CreateAutomationRun: %v", err)
	}
	if len(seen) == 0 || seen[0] != resolved {
		t.Fatalf("collision check consulted %v, want the resolved path %q first", seen, resolved)
	}
}
