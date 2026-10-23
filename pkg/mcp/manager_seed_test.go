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
	down    atomic.Bool
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
		if r.down.Load() {
			http.Error(w, "gone", http.StatusBadGateway)
			return
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

// discover starts a manager against servers once, as the session that sent
// the tools did, and returns that session's snapshot.
func discover(t *testing.T, servers map[string]core.MCPServer) map[string][]ToolDef {
	t.Helper()
	mgr := NewManager(nil, "")
	startWait(t, mgr, servers, nil)
	defer mgr.Close()
	return NewController(ControllerConfig{Manager: mgr, Registry: core.NewRegistry()}).Snapshot()
}

func resultText(res core.Result) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestRemoteServerStartsWithSeededToolsAndCallWaitsForHandshake(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	remote := newSwappableRemote(t, "Echoes back")
	servers := map[string]core.MCPServer{"remote": {URL: remote.url}}

	first := NewManager(nil, "")
	startWait(t, first, servers, nil)
	sent := first.Tools()
	snap := NewController(ControllerConfig{Manager: first, Registry: core.NewRegistry()}).Snapshot()
	first.Close()
	if len(sent) != 1 {
		t.Fatalf("first start tools = %+v, want echo", sent)
	}

	remote.hold()
	second := NewManager(nil, "")
	second.SeedTools(snap)
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

func TestSeededToolWhoseDefinitionChangedIsRefused(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	remote := newSwappableRemote(t, "Echoes back")
	servers := map[string]core.MCPServer{"remote": {URL: remote.url}}

	snap := discover(t, servers)

	remote.setDescription("Echoes back, louder")
	second := NewManager(nil, "")
	second.SeedTools(snap)
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
}

// The seed is authoritative: a configured server absent from it starts with
// no tools, and a seeded server that is no longer configured is not offered.
func TestSeedIsAuthoritativeForConfiguredServers(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	remote := newSwappableRemote(t, "Echoes back")
	servers := map[string]core.MCPServer{"remote": {URL: remote.url}}
	snap := discover(t, servers)

	remote.hold()
	mgr := NewManager(nil, "")
	mgr.SeedTools(map[string][]ToolDef{"gone": snap["remote"]})
	mgr.Start(context.Background(), servers, nil)
	defer mgr.Close()
	if got := mgr.Tools(); len(got) != 0 {
		t.Fatalf("offered %d tools; want none for a server the snapshot did not hold, and none for one no longer configured", len(got))
	}
}

// Tools registered at session construction are owned by the Controller even
// when the handshake fails before it is built, so the failure withdraws them.
func TestControllerOwnsToolsOfAServerThatFailedDuringConstruction(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	remote := newSwappableRemote(t, "Echoes back")
	servers := map[string]core.MCPServer{"remote": {URL: remote.url}}
	snap := discover(t, servers)

	remote.hold()
	mgr := NewManager(nil, "")
	mgr.SeedTools(snap)
	mgr.Start(context.Background(), servers, nil)
	defer mgr.Close()
	reg := core.NewRegistry()
	ctrl := NewController(ControllerConfig{Manager: mgr, Registry: reg})
	if _, ok := reg.Get("mcp__remote__echo"); !ok {
		t.Fatal("cached tool not registered by the Controller")
	}
	remote.down.Store(true)
	remote.release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.WaitSettled(ctx); err != nil {
		t.Fatal(err)
	}
	ctrl.SyncServer("remote")
	if _, ok := reg.Get("mcp__remote__echo"); ok {
		t.Fatal("failed server's tool still registered after sync")
	}
	if got := ctrl.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot after withdrawal = %+v, want empty", got)
	}
}

// A call waiting for its server's handshake ends with its own context, and
// within a bound of its own, even when the handshake itself overruns.
func TestSeededToolCallWaitIsCancellableAndBounded(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	remote := newSwappableRemote(t, "Echoes back")
	servers := map[string]core.MCPServer{"remote": {URL: remote.url}}
	snap := discover(t, servers)

	remote.hold()
	mgr := NewManager(nil, "")
	mgr.SeedTools(snap)
	mgr.Start(context.Background(), servers, nil)
	defer mgr.Close()
	tool := mgr.Tools()[0]

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	res, err := tool.Execute(ctx, map[string]any{"text": "hi"}, nil)
	if err != nil || !res.IsError || !strings.Contains(resultText(res), "still connecting") {
		t.Fatalf("cancelled wait = %+v, %v; want a still-connecting error", res, err)
	}
	if waited := time.Since(began); waited > 2*time.Second {
		t.Fatalf("cancelled call waited %s", waited)
	}

	old := toolLifecycleWait
	toolLifecycleWait = 200 * time.Millisecond
	defer func() { toolLifecycleWait = old }()
	began = time.Now()
	res, _ = tool.Execute(context.Background(), map[string]any{"text": "hi"}, nil)
	if !res.IsError || !strings.Contains(resultText(res), "still connecting") || time.Since(began) > 2*time.Second {
		t.Fatalf("bounded wait = %+v after %s", res, time.Since(began))
	}
}
