package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/e-aleixandre/moa/pkg/core"
)

// swappableRemote is a remote MCP server at a fixed URL whose echo tool
// description can change and whose requests can be held, as a server that is
// updated, or slow to answer, while moa restarts.
type swappableRemote struct {
	url     string
	current atomic.Pointer[sdkmcp.Server]
	mu      sync.Mutex
	gate    chan struct{}
}

func newSwappableRemote(t *testing.T, description string) *swappableRemote {
	t.Helper()
	r := &swappableRemote{}
	r.setDescription(description)
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return r.current.Load() }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		gate := r.gate
		r.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-req.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, req)
	}))
	r.url = srv.URL
	t.Cleanup(func() {
		r.release()
		srv.Close()
	})
	return r
}

func (r *swappableRemote) setDescription(description string) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "remote-helper", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "echo", Description: description}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct {
		Text string `json:"text"`
	}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo: " + input.Text}}}, nil, nil
	})
	r.current.Store(server)
}

func (r *swappableRemote) hold() {
	r.mu.Lock()
	r.gate = make(chan struct{})
	r.mu.Unlock()
}

func (r *swappableRemote) release() {
	r.mu.Lock()
	if r.gate != nil {
		close(r.gate)
		r.gate = nil
	}
	r.mu.Unlock()
}

func resultText(res core.Result) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestRemoteServerStartsWithCachedToolsAndCallWaitsForHandshake(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	remote := newSwappableRemote(t, "Echoes back")
	servers := map[string]core.MCPServer{"remote": {URL: remote.url}}

	first := NewManager(nil, "")
	startWait(t, first, servers, nil)
	sent := first.Tools()
	first.Close()
	if len(sent) != 1 {
		t.Fatalf("first start tools = %+v, want echo", sent)
	}

	remote.hold()
	second := NewManager(nil, "")
	second.Start(context.Background(), servers, nil)
	defer second.Close()
	restored := second.Tools()
	if len(restored) != 1 || restored[0].Spec().Name != sent[0].Spec().Name ||
		restored[0].Description != sent[0].Description || string(restored[0].Parameters) != string(sent[0].Parameters) {
		t.Fatalf("restored tools = %+v, want exactly %+v", restored, sent)
	}
	if st := second.Status()[0]; st.State != StateStarting {
		t.Fatalf("state = %s, want the handshake still in flight", st.State)
	}

	done := make(chan core.Result, 1)
	go func() {
		res, _ := restored[0].Execute(context.Background(), map[string]any{"text": "hi"}, nil)
		done <- res
	}()
	select {
	case res := <-done:
		t.Fatalf("call returned before the handshake: %+v", res)
	case <-time.After(200 * time.Millisecond):
	}
	remote.release()
	select {
	case res := <-done:
		if res.IsError || resultText(res) != "echo: hi" {
			t.Fatalf("call after handshake = %+v, want the live answer", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call did not finish after the handshake")
	}
}

func TestCachedToolWhoseDefinitionChangedIsRefused(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	remote := newSwappableRemote(t, "Echoes back")
	servers := map[string]core.MCPServer{"remote": {URL: remote.url}}

	first := NewManager(nil, "")
	startWait(t, first, servers, nil)
	first.Close()

	remote.setDescription("Echoes back, louder")
	second := NewManager(nil, "")
	second.Start(context.Background(), servers, nil)
	defer second.Close()
	stale := second.Tools()[0]
	if stale.Description != "Echoes back" {
		t.Fatalf("restored description = %q, want the cached one", stale.Description)
	}
	res, err := stale.Execute(context.Background(), map[string]any{"text": "hi"}, nil)
	if err != nil || !res.IsError || !strings.Contains(resultText(res), "changed its definition") {
		t.Fatalf("stale call = %+v, %v; want a refusal naming the change", res, err)
	}
	live, _ := second.ToolsForServer("remote")
	if len(live) != 1 || live[0].Description != "Echoes back, louder" {
		t.Fatalf("live tools = %+v, want the new definition", live)
	}
	res, _ = live[0].Execute(context.Background(), map[string]any{"text": "hi"}, nil)
	if res.IsError || resultText(res) != "echo: hi" {
		t.Fatalf("call with the new definition = %+v", res)
	}
	second.Close()

	third := NewManager(nil, "")
	third.Start(context.Background(), servers, nil)
	defer third.Close()
	if got := third.Tools(); len(got) != 1 || got[0].Description != "Echoes back, louder" {
		t.Fatalf("next start offers %+v, want the refreshed cache", got)
	}
}
