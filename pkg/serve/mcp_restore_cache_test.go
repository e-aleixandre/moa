package serve

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/mcp"
	"github.com/e-aleixandre/moa/pkg/session"
)

const restoreRelayTool = "mcp__relay__mark_task_done"

// restartableRelay is a real streamable-HTTP MCP server at a fixed URL whose
// handshake can be held, as a slow remote server is while moa restarts.
type restartableRelay struct {
	URL  string
	http *httptest.Server

	mu   sync.Mutex
	gate chan struct{} // nil = open
	down bool          // every request fails, as with a server that is gone
}

func newRestartableRelay(t *testing.T, description string) *restartableRelay {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "relay", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "mark_task_done",
		Description: description,
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input struct {
		ID string `json:"id"`
	}) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "done " + input.ID}},
		}, nil, nil
	})
	r := &restartableRelay{}
	mcpHandler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	r.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		gate, down := r.gate, r.down
		r.mu.Unlock()
		if down {
			http.Error(w, "gone", http.StatusBadGateway)
			return
		}
		if gate != nil {
			select {
			case <-gate:
			case <-req.Context().Done():
				return
			}
			r.mu.Lock()
			down = r.down
			r.mu.Unlock()
			if down {
				http.Error(w, "gone", http.StatusBadGateway)
				return
			}
		}
		mcpHandler.ServeHTTP(w, req)
	}))
	r.URL = r.http.URL
	t.Cleanup(func() {
		r.open()
		r.http.Close()
	})
	return r
}

func (r *restartableRelay) hold() {
	r.mu.Lock()
	if r.gate == nil {
		r.gate = make(chan struct{})
	}
	r.mu.Unlock()
}

func (r *restartableRelay) goDown() {
	r.mu.Lock()
	r.down = true
	r.mu.Unlock()
}

func (r *restartableRelay) open() {
	r.mu.Lock()
	if r.gate != nil {
		close(r.gate)
		r.gate = nil
	}
	r.mu.Unlock()
}

// restartSession models a moa restart for one session: the runtime is
// unloaded, the process holding the MCP connection goes away, and the session
// is restored from its file on disk. whileDown runs in between, to change
// how the MCP server behaves when moa comes back.
func restartSession(t *testing.T, mgr *Manager, sess *ManagedSession, whileDown func()) *ManagedSession {
	t.Helper()
	unloadSession(t, mgr, sess.ID)
	sess.infra.mcpMgr.Close()
	sess.infra.sessionCancel()
	whileDown()
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	return resumed
}

func recvRequest(t *testing.T, requests <-chan core.Request, within time.Duration, what string) core.Request {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(within):
		t.Fatalf("%s did not reach the provider within %s", what, within)
		return core.Request{}
	}
}

func sameToolSpecs(a, b []core.ToolSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Description != b[i].Description || !bytes.Equal(a[i].Parameters, b[i].Parameters) {
			return false
		}
	}
	return true
}

func toolNames(specs []core.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		if strings.HasPrefix(s.Name, mcp.ToolPrefix) {
			out = append(out, s.Name)
		}
	}
	return out
}

// newRestoreFixture runs one turn in a session with a remote MCP server, so the
// server's tools have been sent to the model, and returns that request.
func newRestoreFixture(t *testing.T, relay *restartableRelay, handlers ...mockHandler) (*Manager, *ManagedSession, chan core.Request, core.Request) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	requests := make(chan core.Request, 8)
	record := func(next mockHandler) mockHandler {
		return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			requests <- req
			return next(ctx, req)
		}
	}
	all := []mockHandler{record(simpleResponseHandler("first done"))}
	for _, h := range handlers {
		all = append(all, record(h))
	}
	provider := newMockProvider(all...)
	mgr := newTestManagerWithConfig(t, context.Background(), provider, t.TempDir(), core.MoaConfig{
		DisableSandbox: true,
		MCPServers:     map[string]core.MCPServer{"relay": {URL: relay.URL}},
	})
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "first", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	before := recvRequest(t, requests, 10*time.Second, "pre-restart request")
	if !requestHasTool(before, restoreRelayTool) {
		t.Fatalf("pre-restart tools = %v, want the relay tool", toolNames(before.Tools))
	}
	return mgr, sess, requests, before
}

// A restart must not change the tool prefix of a warm session: the first
// request after restoring carries the MCP tools exactly as they were sent
// before, without waiting for the server to reconnect.
func TestRestoredSessionReusesMCPToolDefinitionsWithoutWaiting(t *testing.T) {
	relay := newRestartableRelay(t, "Marks the originating task done")
	mgr, sess, requests, before := newRestoreFixture(t, relay, simpleResponseHandler("second done"))

	// The server is slow to come back after the restart.
	resumed := restartSession(t, mgr, sess, relay.hold)
	defer relay.open()
	if _, _, _, err := mgr.Send(resumed.ID, "second", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	after := recvRequest(t, requests, 3*time.Second, "first request after restoring")
	if !sameToolSpecs(before.Tools, after.Tools) {
		t.Fatalf("first request after restoring changed the tools:\nbefore %v\nafter  %v", toolNames(before.Tools), toolNames(after.Tools))
	}
	if before.System != after.System {
		t.Fatal("first request after restoring changed the system prompt")
	}
}

// The model may call a restored MCP tool before its server reconnects: that
// call alone waits for the connection and then runs against the live server.
func TestRestoredMCPToolCallWaitsForReconnect(t *testing.T) {
	relay := newRestartableRelay(t, "Marks the originating task done")
	mgr, sess, requests, _ := newRestoreFixture(t, relay,
		func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return toolCallResponse("call-1", restoreRelayTool, map[string]any{"id": "42"}), nil
		},
		simpleResponseHandler("second done"),
	)

	resumed := restartSession(t, mgr, sess, relay.hold)
	if _, _, _, err := mgr.Send(resumed.ID, "close task 42", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	first := recvRequest(t, requests, 3*time.Second, "first request after restoring")
	if !requestHasTool(first, restoreRelayTool) {
		t.Fatalf("restored tools = %v, want the relay tool", toolNames(first.Tools))
	}
	time.Sleep(200 * time.Millisecond) // the call is now waiting on the handshake
	relay.open()
	next := recvRequest(t, requests, 10*time.Second, "request after the tool call")
	result := lastMessage(next)
	if result.Role != "tool_result" || !strings.Contains(textOf(result), "done 42") {
		t.Fatalf("tool result = %+v, want the live server's answer", result)
	}
}

// A server that does not come back must not leave its restored tools hanging:
// a call says why it failed, and the tools are withdrawn once the turn ends.
func TestRestoredMCPToolsOfAnUnreachableServer(t *testing.T) {
	relay := newRestartableRelay(t, "Marks the originating task done")
	mgr, sess, requests, _ := newRestoreFixture(t, relay,
		func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return toolCallResponse("call-1", restoreRelayTool, map[string]any{"id": "42"}), nil
		},
		simpleResponseHandler("second done"),
	)

	resumed := restartSession(t, mgr, sess, relay.hold)
	if _, _, _, err := mgr.Send(resumed.ID, "close task 42", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	first := recvRequest(t, requests, 3*time.Second, "first request after restoring")
	if !requestHasTool(first, restoreRelayTool) {
		t.Fatalf("restored tools = %v, want the relay tool", toolNames(first.Tools))
	}
	time.Sleep(200 * time.Millisecond) // the call is now waiting on the handshake
	relay.goDown()
	relay.open()
	next := recvRequest(t, requests, 20*time.Second, "request after the tool call")
	result := lastMessage(next)
	text := textOf(result)
	if result.Role != "tool_result" || !strings.Contains(text, "relay") || !strings.Contains(text, "failed") {
		t.Fatalf("tool result = %q, want an error naming the server and the failure", text)
	}
	pollUntil(t, 10*time.Second, "unreachable server's tools withdrawn", func() bool {
		_, ok := resumed.infra.toolReg.Get(restoreRelayTool)
		return !ok
	})
}

func textOf(m core.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// A server whose tools the session stopped offering before the restart is not
// brought back: the snapshot is what the session was sending, not what the
// server once listed.
func TestRestoredSessionDoesNotResurrectWithdrawnMCPTools(t *testing.T) {
	relay := newRestartableRelay(t, "Marks the originating task done")
	mgr, sess, requests, _ := newRestoreFixture(t, relay,
		simpleResponseHandler("without relay"),
		simpleResponseHandler("after restart"),
	)
	pollUntil(t, 5*time.Second, "first turn settled", func() bool { return sessState(sess) == StateIdle })
	relay.goDown()
	if _, err := sess.RestartMCPServer("relay"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	pollUntil(t, 5*time.Second, "failed server's tools withdrawn", func() bool {
		_, ok := sess.infra.toolReg.Get(restoreRelayTool)
		return !ok
	})
	if _, _, _, err := mgr.Send(sess.ID, "second", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	before := recvRequest(t, requests, 5*time.Second, "request without the relay")
	if requestHasTool(before, restoreRelayTool) {
		t.Fatal("withdrawn tool still offered before the restart")
	}

	resumed := restartSession(t, mgr, sess, relay.hold)
	defer relay.open()
	if _, _, _, err := mgr.Send(resumed.ID, "third", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	after := recvRequest(t, requests, 3*time.Second, "first request after restoring")
	if !sameToolSpecs(before.Tools, after.Tools) {
		t.Fatalf("restoring changed the tools:\nbefore %v\nafter  %v", toolNames(before.Tools), toolNames(after.Tools))
	}
}

// A session saved before snapshots existed restores as before (its MCP tools
// arrive when the server connects) and gains a snapshot on its next save,
// with each schema stored byte for byte.
func TestRestoredSessionWithoutMCPSnapshot(t *testing.T) {
	relay := newRestartableRelay(t, "Marks the originating task done")
	mgr, sess, requests, before := newRestoreFixture(t, relay, simpleResponseHandler("second done"))

	resumed := restartSession(t, mgr, sess, func() {
		saved, store, err := session.FindSession(mgr.sessionBaseDir, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := saved.Metadata[session.MetaMCPTools]; !ok {
			t.Fatal("saved session has no MCP tool snapshot")
		}
		delete(saved.Metadata, session.MetaMCPTools)
		if err := store.Save(saved); err != nil {
			t.Fatal(err)
		}
	})
	waitMCPSettled(t, resumed.infra.mcpMgr)
	pollUntil(t, 5*time.Second, "relay tool registered after connecting", func() bool {
		_, ok := resumed.infra.toolReg.Get(restoreRelayTool)
		return ok
	})
	if _, _, _, err := mgr.Send(resumed.ID, "second", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	recvRequest(t, requests, 5*time.Second, "request after restoring")
	unloadSession(t, mgr, resumed.ID)
	saved, _, err := session.FindSession(mgr.sessionBaseDir, resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	seed := mcpToolSeedFromMeta(saved.Metadata)
	var sent core.ToolSpec
	for _, spec := range before.Tools {
		if spec.Name == restoreRelayTool {
			sent = spec
		}
	}
	if len(seed["relay"]) != 1 || seed["relay"][0].Name != "mark_task_done" || !bytes.Equal(seed["relay"][0].Params, sent.Parameters) {
		t.Fatalf("snapshot after the next save = %+v, want the relay tool with its exact schema", seed)
	}
}
