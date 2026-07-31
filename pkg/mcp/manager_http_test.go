package mcp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

func TestRemoteClientDoesNotFollowRedirects(t *testing.T) {
	// A redirect must never carry the configured headers to another origin.
	var secondHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/mcp", http.StatusFound)
	}))
	defer first.Close()

	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"remote": {URL: first.URL, Headers: map[string]string{"Authorization": "Bearer s3cret"}},
	}, nil)
	defer mgr.Close()

	st := mgr.Status()
	if len(st) != 1 || st[0].State != StateFailed {
		t.Fatalf("status = %+v, want a failed server (redirects are not followed)", st)
	}
	if n := secondHits.Load(); n != 0 {
		t.Fatalf("redirect target received %d requests, want 0 (credentials must not travel)", n)
	}
}

// blackholeListener accepts connections and never writes a response.
func blackholeListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold it open, read nothing, answer nothing.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return "http://" + ln.Addr().String()
}

func TestRemoteBlackholeDoesNotHang(t *testing.T) {
	// Shorten the transport bounds so the test doesn't wait the production 15s.
	prevHeader := remoteResponseHeaderTimeout
	remoteResponseHeaderTimeout = 300 * time.Millisecond
	t.Cleanup(func() { remoteResponseHeaderTimeout = prevHeader })

	url := blackholeListener(t)

	mgr := NewManager(nil, "")
	start := time.Now()
	mgr.Start(context.Background(), map[string]core.MCPServer{"remote": {URL: url}}, nil)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("connect took %v, want it bounded by the response-header timeout", elapsed)
	}
	st := mgr.Status()
	if len(st) != 1 || st[0].State != StateFailed {
		t.Fatalf("status = %+v, want failed", st)
	}

	// Close must return promptly even though the peer answers nothing.
	closed := make(chan struct{})
	go func() {
		mgr.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a blackholed remote server")
	}
}

func TestRemoteToolCallGetsADeadline(t *testing.T) {
	// A deadline-less context must not let a dead remote wedge the call forever.
	prev := remoteToolCallTimeout
	remoteToolCallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { remoteToolCallTimeout = prev })

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "staller", Version: "0.1"}, nil)
	entered := make(chan struct{})
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "stall",
		Description: "Never returns until cancelled",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct{}) (*sdkmcp.CallToolResult, any, error) {
		close(entered)
		<-ctx.Done() // simulate a dead remote: the result never comes
		return nil, nil, ctx.Err()
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{"remote": {URL: httpServer.URL}}, nil)
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() = %d, want 1", len(tools))
	}
	start := time.Now()
	res, err := tools[0].Execute(context.Background(), map[string]any{}, nil)
	elapsed := time.Since(start)
	select {
	case <-entered:
		// The handler genuinely stalled: the failure below is the deadline.
	default:
		t.Fatalf("tool handler never ran; Execute failed for an unrelated reason: %+v, %v", res, err)
	}
	if err == nil && !res.IsError {
		t.Fatalf("Execute succeeded (%+v), want a deadline failure", res)
	}
	// Bounded by the shortened cap (plus generous slack for CI scheduling),
	// far below any other timeout in play.
	if elapsed < remoteToolCallTimeout || elapsed > 3*time.Second {
		t.Fatalf("stalled call returned after %v, want ~%v (the remote cap)", elapsed, remoteToolCallTimeout)
	}
}
