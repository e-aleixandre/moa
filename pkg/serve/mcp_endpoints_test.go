package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
)

// A session with no MCP servers configured returns an empty (non-null) list, and
// a restart against it is a clean 404 rather than a panic.
func TestMCPStatusEndpoint_NoServers(t *testing.T) {
	ts, mgr, cancel := newTestServer(t)
	defer cancel()
	defer ts.Close()

	sess, err := mgr.CreateSession(CreateOpts{Title: "no mcp"})
	if err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, ts, "GET", "/api/sessions/"+sess.ID+"/mcp", "")
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Servers []json.RawMessage `json:"servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Servers == nil {
		t.Fatal("servers should be an empty array, not null")
	}
	if len(body.Servers) != 0 {
		t.Fatalf("expected 0 servers, got %d", len(body.Servers))
	}
}

func TestMCPRestartEndpoint_NoServers(t *testing.T) {
	ts, mgr, cancel := newTestServer(t)
	defer cancel()
	defer ts.Close()

	sess, err := mgr.CreateSession(CreateOpts{Title: "no mcp"})
	if err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, ts, "POST", "/api/sessions/"+sess.ID+"/mcp/whatever/restart", "")
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMCPStatusEndpoint_UnknownSession(t *testing.T) {
	ts, _, cancel := newTestServer(t)
	defer cancel()
	defer ts.Close()

	resp := apiReq(t, ts, "GET", "/api/sessions/does-not-exist/mcp", "")
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// A server named in disabled_mcp_servers is not spawned: even with a bogus
// command it reports "disabled" (not "failed"), because a disabled server never
// dials. This is the end-to-end proof that bootstrap resolves the policy before
// starting the manager.
func TestMCPStatusEndpoint_ConfigDisabledNotSpawned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := newMockProvider()
	moaCfg := core.MoaConfig{
		DisableSandbox: true,
		MCPServers: map[string]core.MCPServer{
			"ghost": {Command: "definitely-not-a-real-binary-zzz"},
		},
		DisabledMCPServers: []string{"ghost"},
	}
	mgr := newTestManagerWithConfig(t, ctx, provider, t.TempDir(), moaCfg)

	sess, err := mgr.CreateSession(CreateOpts{Title: "disabled mcp"})
	if err != nil {
		t.Fatal(err)
	}

	status := sess.MCPStatus()
	if len(status) != 1 {
		t.Fatalf("expected 1 server, got %d", len(status))
	}
	if status[0].Name != "ghost" || status[0].State != mcp.StateDisabled {
		t.Fatalf("status = %+v, want ghost disabled", status[0])
	}
	// A disabled server exposes no tools.
	if status[0].ToolCount != 0 {
		t.Fatalf("disabled server has %d tools, want 0", status[0].ToolCount)
	}
	// Restarting a disabled server is refused (enable it first).
	if _, err := sess.RestartMCPServer("ghost"); !errors.Is(err, mcp.ErrServerDisabled) {
		t.Fatalf("restart of disabled server: err = %v, want ErrServerDisabled", err)
	}
}

// newTestServerWithMCP builds a running server whose sessions have one MCP
// server configured with a bogus command (so it lands in "failed", never
// spawning anything real). Enough to exercise the status/toggle contract.
func newTestServerWithMCP(t *testing.T, moaCfg core.MoaConfig) (*httptest.Server, *Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(), t.TempDir(), moaCfg)
	srv := httptest.NewServer(NewServer(mgr))
	t.Cleanup(srv.Close)
	return srv, mgr
}

// GET returns the extended contract: per-server enabled/desired_enabled fields,
// the available_scopes map, and a (possibly empty) unmatched_disabled array.
func TestMCPStatusEndpoint_ExtendedShape(t *testing.T) {
	moaCfg := core.MoaConfig{
		DisableSandbox: true,
		MCPServers:     map[string]core.MCPServer{"svc": {Command: "definitely-not-real-zzz"}},
	}
	srv, mgr := newTestServerWithMCP(t, moaCfg)

	sess, err := mgr.CreateSession(CreateOpts{Title: "mcp shape"})
	if err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/mcp", "")
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Servers []struct {
			Name           string `json:"name"`
			Enabled        bool   `json:"enabled"`
			DesiredEnabled bool   `json:"desired_enabled"`
		} `json:"servers"`
		AvailableScopes map[string]struct {
			Writable bool `json:"writable"`
		} `json:"available_scopes"`
		UnmatchedDisabled []json.RawMessage `json:"unmatched_disabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Servers) != 1 || body.Servers[0].Name != "svc" {
		t.Fatalf("servers = %+v, want one 'svc'", body.Servers)
	}
	if !body.Servers[0].Enabled || !body.Servers[0].DesiredEnabled {
		t.Fatalf("svc should be enabled/desired-enabled: %+v", body.Servers[0])
	}
	if !body.AvailableScopes["session"].Writable || !body.AvailableScopes["global"].Writable {
		t.Fatalf("session/global scopes should be writable: %+v", body.AvailableScopes)
	}
	if body.UnmatchedDisabled == nil {
		t.Fatal("unmatched_disabled should be [] not null")
	}
}

// A session-scope PATCH disables the server in memory and the change is
// observable in GET: desired_enabled flips to false with a session veto.
func TestMCPToggleEndpoint_SessionScope(t *testing.T) {
	moaCfg := core.MoaConfig{
		DisableSandbox: true,
		MCPServers:     map[string]core.MCPServer{"svc": {Command: "definitely-not-real-zzz"}},
	}
	srv, mgr := newTestServerWithMCP(t, moaCfg)

	sess, err := mgr.CreateSession(CreateOpts{Title: "toggle"})
	if err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, srv, "PATCH", "/api/sessions/"+sess.ID+"/mcp/svc", `{"scope":"session","disabled":true}`)
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	// Session is idle, so the change applies immediately -> 200 (not 202).
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var patch struct {
		Server struct {
			DesiredEnabled bool     `json:"desired_enabled"`
			DisabledScopes []string `json:"disabled_scopes"`
		} `json:"server"`
		Affected int `json:"affected_sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&patch); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if patch.Server.DesiredEnabled {
		t.Fatal("svc should be desired-disabled after toggle")
	}
	if len(patch.Server.DisabledScopes) != 1 || patch.Server.DisabledScopes[0] != "session" {
		t.Fatalf("disabled_scopes = %v, want [session]", patch.Server.DisabledScopes)
	}
	if patch.Affected != 1 {
		t.Fatalf("affected_sessions = %d, want 1", patch.Affected)
	}

	// The applied state should now be disabled (reconciled while idle).
	st, ok := sess.mcpServerStatus("svc")
	if !ok || st.State != mcp.StateDisabled {
		t.Fatalf("svc state = %v (ok=%v), want disabled", st.State, ok)
	}
}

// PATCH validation: an unknown scope is a 400, an unknown server is a 404.
func TestMCPToggleEndpoint_Validation(t *testing.T) {
	moaCfg := core.MoaConfig{
		DisableSandbox: true,
		MCPServers:     map[string]core.MCPServer{"svc": {Command: "definitely-not-real-zzz"}},
	}
	srv, mgr := newTestServerWithMCP(t, moaCfg)

	sess, err := mgr.CreateSession(CreateOpts{Title: "toggle validation"})
	if err != nil {
		t.Fatal(err)
	}

	bad := apiReq(t, srv, "PATCH", "/api/sessions/"+sess.ID+"/mcp/svc", `{"scope":"nonsense","disabled":true}`)
	bad.Body.Close() //nolint:errcheck // test cleanup
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad scope status = %d, want 400", bad.StatusCode)
	}

	missing := apiReq(t, srv, "PATCH", "/api/sessions/"+sess.ID+"/mcp/nope", `{"scope":"session","disabled":true}`)
	missing.Body.Close() //nolint:errcheck // test cleanup
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown server status = %d, want 404", missing.StatusCode)
	}
}
