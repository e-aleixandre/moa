package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("MCP_CWD_OUTPUT"), []byte(mustGetwd(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "cwd-helper", Version: "0.1"}, nil)
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}

// TestMCPServerHelper is a long-lived helper MCP server used by the lifecycle
// tests (status, exit detection, restart). It exposes one "ping" tool and runs
// until its stdin closes or it is killed. A PID file lets a test kill the exact
// subprocess to simulate a crash.
func TestMCPServerHelper(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_SERVER_HELPER") != "1" {
		return
	}
	if pidFile := os.Getenv("MCP_PID_OUTPUT"); pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	// MCP_PID_APPEND collects every spawned generation's PID (one per line) so a
	// concurrency test can later assert that none leaked past Close.
	if pidLog := os.Getenv("MCP_PID_APPEND"); pidLog != "" {
		if f, err := os.OpenFile(pidLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			_ = f.Close()
		}
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "ping-helper", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "ping",
		Description: "Replies pong",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}},
		}, nil, nil
	})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
}

func helperServerConfig(pidFile string) core.MCPServer {
	return core.MCPServer{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPServerHelper$", "--"},
		Env: map[string]string{
			"GO_WANT_MCP_SERVER_HELPER": "1",
			"MCP_PID_OUTPUT":            pidFile,
		},
	}
}

func TestManagerStatusAfterStart(t *testing.T) {
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": helperServerConfig(""),
	}, nil)
	defer mgr.Close()

	st := mgr.Status()
	if len(st) != 1 {
		t.Fatalf("expected 1 server status, got %d", len(st))
	}
	if st[0].Name != "ping" || st[0].State != StateReady {
		t.Fatalf("status = %+v, want ready ping", st[0])
	}
	if st[0].ToolCount != 1 || len(st[0].ToolNames) != 1 || st[0].ToolNames[0] != "ping" {
		t.Fatalf("tools = %+v, want [ping]", st[0].ToolNames)
	}
}

func TestManagerStatusFailedServer(t *testing.T) {
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"broken": {Command: "definitely-not-a-real-binary-xyz"},
	}, nil)
	defer mgr.Close()

	st := mgr.Status()
	if len(st) != 1 || st[0].State != StateFailed {
		t.Fatalf("status = %+v, want one failed server", st)
	}
	if st[0].Error == "" {
		t.Fatal("failed server should carry an error message")
	}
	// A failed server is still restartable (unknown binary stays failed).
	got, err := mgr.RestartServer(context.Background(), "broken")
	if err != nil {
		t.Fatalf("RestartServer returned error: %v", err)
	}
	if got.State != StateFailed {
		t.Fatalf("restart of broken server = %s, want failed", got.State)
	}
}

func TestManagerDetectsServerExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	mgr := NewManager(nil, "")

	changes := make(chan ServerStatus, 16)
	mgr.OnChange(func(s ServerStatus) { changes <- s })

	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": helperServerConfig(pidFile),
	}, nil)
	defer mgr.Close()

	// Capture the tool the way the registry does: once, at startup. The closure
	// must keep working (routing through the session) after the server dies.
	var pingTool *core.Tool
	for _, tl := range mgr.Tools() {
		tl := tl
		if tl.Label == "ping/ping" {
			pingTool = &tl
		}
	}
	if pingTool == nil {
		t.Fatal("ping tool missing")
	}

	pid := waitForPID(t, pidFile)

	// Kill the subprocess out from under the manager: it must notice and flip
	// to exited, and a tool call must then fail cleanly (not hang).
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	_ = proc.Kill()

	if !waitForState(t, mgr, "ping", StateExited, 5*time.Second) {
		t.Fatal("manager did not detect the server exit")
	}

	// The OnChange callback must have fired with the exit (ready first, then
	// exited). Drain what we have and require at least one exited notification.
	sawExit := false
	for drained := false; !drained; {
		select {
		case s := <-changes:
			if s.Name == "ping" && s.State == StateExited {
				sawExit = true
			}
		default:
			drained = true
		}
	}
	if !sawExit {
		t.Fatal("OnChange never reported the exit")
	}

	// A tool call against the dead server returns an error result, not a hang.
	res, err := pingTool.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Execute returned a hard error: %v", err)
	}
	if !res.IsError {
		t.Fatal("tool call against exited server should be an error result")
	}
}

func TestManagerRestartServer(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": helperServerConfig(pidFile),
	}, nil)
	defer mgr.Close()

	pid1 := waitForPID(t, pidFile)

	st, err := mgr.RestartServer(context.Background(), "ping")
	if err != nil {
		t.Fatalf("RestartServer: %v", err)
	}
	if st.State != StateReady {
		t.Fatalf("post-restart state = %s, want ready", st.State)
	}

	pid2 := waitForPID(t, pidFile)
	if pid2 == pid1 {
		t.Fatal("restart did not spawn a new process")
	}

	// Tools still work after restart (closures route through the fresh session).
	for _, tl := range mgr.Tools() {
		if tl.Label == "ping/ping" {
			res, err := tl.Execute(context.Background(), map[string]any{}, nil)
			if err != nil || res.IsError {
				t.Fatalf("ping after restart failed: err=%v res=%+v", err, res)
			}
			return
		}
	}
	t.Fatal("ping tool missing after restart")
}

func TestManagerRestartUnknownServer(t *testing.T) {
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), nil, nil)
	defer mgr.Close()
	if _, err := mgr.RestartServer(context.Background(), "nope"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("err = %v, want ErrUnknownServer", err)
	}
}

func TestManagerStartInitiallyDisabled(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": helperServerConfig(pidFile),
	}, map[string]bool{"ping": true})
	defer mgr.Close()

	st := mgr.Status()
	if len(st) != 1 || st[0].State != StateDisabled {
		t.Fatalf("status = %+v, want one disabled server", st)
	}
	if st[0].ToolCount != 0 {
		t.Fatalf("disabled server should have 0 tools, got %d", st[0].ToolCount)
	}
	if st[0].Error != "" {
		t.Fatalf("disabled is not a failure; error should be empty, got %q", st[0].Error)
	}
	// No process should have been spawned.
	if _, err := os.ReadFile(pidFile); err == nil {
		t.Fatal("disabled server must not spawn a process")
	}
	// The server has no tools registered.
	if tools := mgr.Tools(); len(tools) != 0 {
		t.Fatalf("disabled server exposes %d tools, want 0", len(tools))
	}
}

func TestManagerRestartDisabledServerRefused(t *testing.T) {
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": helperServerConfig(""),
	}, map[string]bool{"ping": true})
	defer mgr.Close()

	if _, err := mgr.RestartServer(context.Background(), "ping"); !errors.Is(err, ErrServerDisabled) {
		t.Fatalf("err = %v, want ErrServerDisabled", err)
	}
}

func TestManagerEnableThenDisable(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": helperServerConfig(pidFile),
	}, map[string]bool{"ping": true})
	defer mgr.Close()

	// Enable: it should dial, become ready, and expose its tool.
	st, err := mgr.SetServerEnabled(context.Background(), "ping", true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if st.State != StateReady || st.ToolCount != 1 {
		t.Fatalf("after enable = %+v, want ready with 1 tool", st)
	}
	pid := waitForPID(t, pidFile)
	if !processAlive(pid) {
		t.Fatal("enabled server process should be alive")
	}
	if len(mgr.Tools()) != 1 {
		t.Fatalf("enabled server should expose 1 tool, got %d", len(mgr.Tools()))
	}

	// Disable: it should tear the process down and drop its tools.
	st, err = mgr.SetServerEnabled(context.Background(), "ping", false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if st.State != StateDisabled || st.ToolCount != 0 {
		t.Fatalf("after disable = %+v, want disabled with 0 tools", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("disabled server process should have been killed")
	}
	if len(mgr.Tools()) != 0 {
		t.Fatalf("disabled server should expose 0 tools, got %d", len(mgr.Tools()))
	}
	// A disabled server that never exited must NOT be reported as exited: the
	// state stays disabled (the exit watcher was invalidated).
	if s := mgr.Status(); len(s) != 1 || s[0].State != StateDisabled {
		t.Fatalf("status after disable = %+v, want disabled (not exited)", s)
	}
}

func TestManagerEnableIdempotent(t *testing.T) {
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": helperServerConfig(""),
	}, nil) // starts enabled
	defer mgr.Close()

	if !waitForState(t, mgr, "ping", StateReady, 5*time.Second) {
		t.Fatal("server not ready")
	}
	// Enabling an already-running server is a no-op, not a restart.
	st, err := mgr.SetServerEnabled(context.Background(), "ping", true)
	if err != nil {
		t.Fatalf("enable no-op: %v", err)
	}
	if st.State != StateReady {
		t.Fatalf("state = %s, want ready", st.State)
	}
}

func TestManagerDisableIdempotent(t *testing.T) {
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": helperServerConfig(""),
	}, map[string]bool{"ping": true})
	defer mgr.Close()

	st, err := mgr.SetServerEnabled(context.Background(), "ping", false)
	if err != nil {
		t.Fatalf("disable no-op: %v", err)
	}
	if st.State != StateDisabled {
		t.Fatalf("state = %s, want disabled", st.State)
	}
}

// pidTrackingConfig runs the ping helper but also appends every generation's PID
// to pidLog, so a concurrency test can verify no process leaks past Close.
func pidTrackingConfig(pidLog string) core.MCPServer {
	return core.MCPServer{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPServerHelper$", "--"},
		Env: map[string]string{
			"GO_WANT_MCP_SERVER_HELPER": "1",
			"MCP_PID_APPEND":            pidLog,
		},
	}
}

// readPIDs returns every distinct PID the helper has recorded so far.
func readPIDs(t *testing.T, pidLog string) []int {
	t.Helper()
	data, err := os.ReadFile(pidLog)
	if err != nil {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pid, err := strconv.Atoi(line); err == nil && !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, signal 0 probes for existence without affecting the process.
	return proc.Signal(syscall.Signal(0)) == nil
}

// TestManagerConcurrentRestartsNoOrphan drives many concurrent restarts of the
// same server. Every restart spawns a fresh process, and the per-server
// lifecycle lock must ensure that at any moment only the current generation is
// owned; after Close, none of the spawned processes may survive. A regression
// (two restarts both dialing) would leak a process the manager never tracks,
// which Close cannot reap — this test would then find it alive.
func TestManagerConcurrentRestartsNoOrphan(t *testing.T) {
	pidLog := filepath.Join(t.TempDir(), "pids")
	mgr := NewManager(nil, "")
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"ping": pidTrackingConfig(pidLog),
	}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = mgr.RestartServer(context.Background(), "ping")
		}()
	}
	wg.Wait()

	// Exactly one generation should be owned and ready after the dust settles.
	if !waitForState(t, mgr, "ping", StateReady, 5*time.Second) {
		t.Fatal("server not ready after concurrent restarts")
	}

	mgr.Close()

	// After Close, every process the helper ever spawned must be dead. A leaked
	// generation (the blocker) would still be alive here.
	pids := readPIDs(t, pidLog)
	if len(pids) < 2 {
		t.Fatalf("expected several spawned generations, got %d", len(pids))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive := []int{}
		for _, pid := range pids {
			if processAlive(pid) {
				alive = append(alive, pid)
			}
		}
		if len(alive) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes leaked past Close: %v", alive)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestManagerCloseRacingRestartNoOrphan races Close against a restart of the
// same server. Whichever wins, no spawned process may survive Close: if the
// restart commits a fresh connection it must be torn down, and if Close wins
// the restart must bail before spawning.
func TestManagerCloseRacingRestartNoOrphan(t *testing.T) {
	for attempt := 0; attempt < 5; attempt++ {
		pidLog := filepath.Join(t.TempDir(), "pids")
		mgr := NewManager(nil, "")
		mgr.Start(context.Background(), map[string]core.MCPServer{
			"ping": pidTrackingConfig(pidLog),
		}, nil)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = mgr.RestartServer(context.Background(), "ping")
		}()
		go func() {
			defer wg.Done()
			mgr.Close()
		}()
		wg.Wait()

		pids := readPIDs(t, pidLog)
		deadline := time.Now().Add(5 * time.Second)
		for {
			alive := []int{}
			for _, pid := range pids {
				if processAlive(pid) {
					alive = append(alive, pid)
				}
			}
			if len(alive) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("attempt %d: processes leaked past Close: %v", attempt, alive)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func waitForPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("helper never wrote its PID to %s", pidFile)
	return 0
}

func waitForState(t *testing.T, mgr *Manager, name string, want ServerState, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, s := range mgr.Status() {
			if s.Name == name && s.State == want {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestManagerStartsServerInConfiguredCWD(t *testing.T) {
	cwd := t.TempDir()
	output := filepath.Join(t.TempDir(), "server-cwd")
	mgr := NewManager(nil, cwd)
	mgr.Start(context.Background(), map[string]core.MCPServer{
		"cwd-helper": {
			Command: os.Args[0],
			Args:    []string{"-test.run=^TestMCPHelperProcess$", "--"},
			Env: map[string]string{
				"GO_WANT_MCP_HELPER": "1",
				"MCP_CWD_OUTPUT":     output,
			},
		},
	}, nil)
	defer mgr.Close()

	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("MCP helper did not report its working directory: %v", err)
	}
	if string(got) != cwd {
		t.Errorf("MCP server cwd = %q, want %q", got, cwd)
	}
}

// --- sanitizeToolName ---

func TestSanitizeToolName_Valid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mcp__db__query", "mcp__db__query"},
		{"hello-world_123", "hello-world_123"},
		{"a", "a"},
	}
	for _, tc := range cases {
		if got := sanitizeToolName(tc.in); got != tc.want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeToolName_Invalid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello.world", "hello_world"},
		{"path/to/tool", "path_to_tool"},
		{"has spaces", "has_spaces"},
		{"special@#$chars", "special___chars"},
	}
	for _, tc := range cases {
		if got := sanitizeToolName(tc.in); got != tc.want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeToolName_TooLong(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := sanitizeToolName(long)
	if len(got) != 64 {
		t.Errorf("len = %d, want 64", len(got))
	}
}

func TestSanitizeToolName_TooLongNoCollision(t *testing.T) {
	// Two distinct names sharing a >64-char prefix must not collapse into the
	// same tool (which would silently shadow one of them).
	a := strings.Repeat("a", 70) + "_one"
	b := strings.Repeat("a", 70) + "_two"
	ga, gb := sanitizeToolName(a), sanitizeToolName(b)
	if len(ga) != 64 || len(gb) != 64 {
		t.Fatalf("lengths = %d, %d, want 64", len(ga), len(gb))
	}
	if ga == gb {
		t.Errorf("distinct long names collided: both -> %q", ga)
	}
}

func TestSanitizeToolName_Empty(t *testing.T) {
	if got := sanitizeToolName(""); got != "unnamed" {
		t.Errorf("sanitizeToolName(\"\") = %q, want \"unnamed\"", got)
	}
	// All invalid chars → all replaced → then empty after? No, they become '_'.
	if got := sanitizeToolName("..."); got != "___" {
		t.Errorf("sanitizeToolName(\"...\") = %q, want \"___\"", got)
	}
}

// --- convertMCPResult ---

func TestConvertMCPResult_Nil(t *testing.T) {
	r := convertMCPResult(nil)
	if len(r.Content) != 1 || r.Content[0].Text != "(no result)" {
		t.Fatalf("unexpected result for nil: %+v", r)
	}
}

func TestConvertMCPResult_Empty(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{})
	if len(r.Content) != 1 || r.Content[0].Text != "(empty result)" {
		t.Fatalf("unexpected result for empty: %+v", r)
	}
}

func TestConvertMCPResult_Text(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello"}},
	})
	if len(r.Content) != 1 || r.Content[0].Type != "text" || r.Content[0].Text != "hello" {
		t.Fatalf("unexpected: %+v", r)
	}
	if r.IsError {
		t.Fatal("unexpected IsError=true")
	}
}

func TestConvertMCPResult_Image(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.ImageContent{Data: []byte("png-data"), MIMEType: "image/png"}},
	})
	if len(r.Content) != 1 || r.Content[0].Type != "image" {
		t.Fatalf("unexpected: %+v", r)
	}
	if r.Content[0].MimeType != "image/png" {
		t.Fatalf("MimeType = %q", r.Content[0].MimeType)
	}
}

func TestConvertMCPResult_Error(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "something went wrong"}},
		IsError: true,
	})
	if !r.IsError {
		t.Fatal("expected IsError=true")
	}
	if r.Content[0].Text != "something went wrong" {
		t.Fatalf("text = %q", r.Content[0].Text)
	}
}

func TestConvertMCPResult_UnknownContentType(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.AudioContent{Data: []byte("audio"), MIMEType: "audio/wav"}},
	})
	if len(r.Content) != 1 || r.Content[0].Type != "text" {
		t.Fatalf("expected JSON fallback text, got: %+v", r)
	}
}

// --- In-memory integration test ---

func TestWrapMCPTool_InMemory(t *testing.T) {
	ctx := context.Background()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "0.1"}, nil)
	type greetInput struct {
		Name string
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "greet",
		Description: "Greets a person",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input greetInput) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Hello, " + input.Name + "!"}},
		}, nil, nil
	})

	st, ct := sdkmcp.NewInMemoryTransports()

	srvSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = srvSession.Close() }()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "moa-test", Version: "0.1"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Discover tools
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(list.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(list.Tools))
	}

	// Wrap as core.Tool via the manager's routing wrapper.
	m := NewManager(nil, "")
	sess := &serverSession{name: "test-server", session: session, state: StateReady}
	ti := toolInfo{name: list.Tools[0].Name, description: list.Tools[0].Description}
	tool := m.wrapTool(sess, ti)

	// Verify metadata
	if tool.Name != "mcp__test-server__greet" {
		t.Fatalf("Name = %q", tool.Name)
	}
	if tool.Label != "test-server/greet" {
		t.Fatalf("Label = %q", tool.Label)
	}
	if tool.Description != "Greets a person" {
		t.Fatalf("Description = %q", tool.Description)
	}

	// Call tool
	result, err := tool.Execute(ctx, map[string]any{"Name": "World"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "Hello, World!" {
		t.Fatalf("result = %+v", result)
	}
	if result.IsError {
		t.Fatal("unexpected IsError")
	}
}

func TestWrapMCPTool_ErrorResult(t *testing.T) {
	ctx := context.Background()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "fail",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "bad input"}},
			IsError: true,
		}, nil, nil
	})

	st, ct := sdkmcp.NewInMemoryTransports()
	srvSession, _ := server.Connect(ctx, st, nil)
	defer func() { _ = srvSession.Close() }()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "moa-test", Version: "0.1"}, nil)
	session, _ := client.Connect(ctx, ct, nil)
	defer func() { _ = session.Close() }()

	list, _ := session.ListTools(ctx, nil)
	m := NewManager(nil, "")
	sess := &serverSession{name: "test-server", session: session, state: StateReady}
	tool := m.wrapTool(sess, toolInfo{name: list.Tools[0].Name})

	result, err := tool.Execute(ctx, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}
}
