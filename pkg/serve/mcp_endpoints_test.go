package serve

import (
	"encoding/json"
	"net/http"
	"testing"
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
