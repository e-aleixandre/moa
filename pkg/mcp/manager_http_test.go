package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/e-aleixandre/moa/pkg/core"
)

// newRemoteTestServer starts an httptest server speaking streamable HTTP MCP
// with a single "echo" tool, and returns its URL plus a counter of the
// Authorization header values it saw.
func newRemoteTestServer(t *testing.T) (url string, lastAuth *atomic.Value) {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "remote-helper", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "echo",
		Description: "Echoes back",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct {
		Text string `json:"text"`
	}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo: " + input.Text}},
		}, nil, nil
	})

	auth := &atomic.Value{}
	auth.Store("")
	handler := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	return httpServer.URL, auth
}

func TestManagerRemoteServerLifecycle(t *testing.T) {
	url, _ := newRemoteTestServer(t)

	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"remote": {URL: url},
	}, nil)
	defer mgr.Close()

	st := mgr.Status()
	if len(st) != 1 || st[0].State != StateReady {
		t.Fatalf("status = %+v, want one ready server", st)
	}
	if st[0].ToolCount != 1 || st[0].ToolNames[0] != "echo" {
		t.Fatalf("tools = %+v, want [echo]", st[0].ToolNames)
	}

	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() = %d, want 1", len(tools))
	}
	res, err := tools[0].Execute(context.Background(), map[string]any{"text": "hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || len(res.Content) == 0 || res.Content[0].Text != "echo: hi" {
		t.Fatalf("result = %+v, want \"echo: hi\"", res)
	}

	// A remote server has no process, so a restart must still work (and must
	// not be refused as a processless teardown).
	got, err := mgr.RestartServer(context.Background(), "remote")
	if err != nil {
		t.Fatalf("RestartServer: %v", err)
	}
	if got.State != StateReady || got.ToolCount != 1 {
		t.Fatalf("post-restart status = %+v, want ready with 1 tool", got)
	}

	// The tool captured before the restart routes through the new generation.
	res, err = tools[0].Execute(context.Background(), map[string]any{"text": "again"}, nil)
	if err != nil {
		t.Fatalf("Execute after restart: %v", err)
	}
	if res.IsError || res.Content[0].Text != "echo: again" {
		t.Fatalf("post-restart result = %+v", res)
	}
}

func TestManagerRemoteServerHeaders(t *testing.T) {
	url, lastAuth := newRemoteTestServer(t)

	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"remote": {URL: url, Headers: map[string]string{"Authorization": "Bearer s3cret"}},
	}, nil)
	defer mgr.Close()

	if st := mgr.Status(); len(st) != 1 || st[0].State != StateReady {
		t.Fatalf("status = %+v, want ready", st)
	}
	if got := lastAuth.Load().(string); got != "Bearer s3cret" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer s3cret")
	}
}

func TestManagerRemoteServerDisableEnable(t *testing.T) {
	url, _ := newRemoteTestServer(t)

	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"remote": {URL: url},
	}, nil)
	defer mgr.Close()

	// Disabling a processless server must complete cleanly and drop its tools.
	st, err := mgr.SetServerEnabled(context.Background(), "remote", false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if st.State != StateDisabled || st.ToolCount != 0 {
		t.Fatalf("disabled status = %+v", st)
	}
	st, err = mgr.SetServerEnabled(context.Background(), "remote", true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if st.State != StateReady || st.ToolCount != 1 {
		t.Fatalf("enabled status = %+v", st)
	}
}

func TestManagerRemoteServerUnreachable(t *testing.T) {
	// A closed listener: connect must fail into StateFailed rather than hang.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"remote": {URL: url},
	}, nil)
	defer mgr.Close()

	st := mgr.Status()
	if len(st) != 1 || st[0].State != StateFailed || st[0].Error == "" {
		t.Fatalf("status = %+v, want a failed server with an error", st)
	}
}

func TestManagerRejectsInvalidServerConfig(t *testing.T) {
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"both": {Command: "echo", URL: "https://example.com/mcp"},
		"none": {},
	}, nil)
	defer mgr.Close()

	for _, st := range mgr.Status() {
		if st.State != StateFailed {
			t.Fatalf("server %s = %s, want failed", st.Name, st.State)
		}
	}
}
