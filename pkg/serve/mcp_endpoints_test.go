package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
