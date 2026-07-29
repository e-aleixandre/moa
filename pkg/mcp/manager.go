package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ealeixandre/moa/pkg/core"
)

const serverStartTimeout = 15 * time.Second

// ToolPrefix is the namespace prefix for MCP tool names ("mcp__<server>__<tool>").
// Exported so other packages can detect MCP tools without hardcoding the prefix.
const ToolPrefix = "mcp__"

// ServerState is the lifecycle state of a single MCP server, surfaced to the UI.
type ServerState string

const (
	// StateReady means the server is connected and its tools are callable.
	StateReady ServerState = "ready"
	// StateFailed means the server never connected (or a restart failed).
	StateFailed ServerState = "failed"
	// StateExited means the server was connected but its process has since died.
	StateExited ServerState = "exited"
	// StateRestarting means a restart is in progress.
	StateRestarting ServerState = "restarting"
)

// ServerStatus is an immutable snapshot of one server's health, for the UI.
type ServerStatus struct {
	Name      string      `json:"name"`
	State     ServerState `json:"state"`
	ToolCount int         `json:"tool_count"`
	ToolNames []string    `json:"tool_names,omitempty"`
	Error     string      `json:"error,omitempty"`
	// StartedAt is when the server last connected; zero if it never has.
	StartedAt time.Time `json:"started_at,omitempty"`
	// ChangedAt is when the state last changed.
	ChangedAt time.Time `json:"changed_at,omitempty"`
}

// Manager owns MCP client sessions and their lifecycle.
//
// It is safe for concurrent use: tool calls run the moment the agent schedules
// them, while restarts and exit-detection mutate server state from other
// goroutines. Callers observe changes through the OnChange callback (set once
// before Start) plus Status snapshots.
type Manager struct {
	logger *slog.Logger
	cwd    string

	mu       sync.Mutex
	servers  []*serverSession // stable order = config discovery order
	byName   map[string]*serverSession
	configs  map[string]core.MCPServer // last config per server, for restart
	onChange func(ServerStatus)        // notified on any state transition (may be nil)
	closed   bool
}

// serverSession holds one MCP server subprocess and its live connection. The
// connection lives behind the mutex so a restart can swap it without the tool
// closures (which route through the session by name) capturing a dead
// generation.
type serverSession struct {
	name string

	mu        sync.Mutex
	cmd       *exec.Cmd
	client    *sdkmcp.Client
	session   *sdkmcp.ClientSession
	state     ServerState
	err       string
	tools     []toolInfo
	startedAt time.Time
	changedAt time.Time
	// gen increments on every (re)connect. The exit watcher captures the gen it
	// was started for and ignores its notification if a newer generation has
	// already taken over — so a slow Wait() from an old process can't clobber
	// the state of a fresh restart.
	gen uint64
}

// toolInfo is the discovered metadata for one MCP tool, kept so tools can be
// rebuilt (identically) after a restart without re-listing.
type toolInfo struct {
	name        string
	description string
	params      json.RawMessage
}

// NewManager creates a Manager whose servers run in cwd. Pass nil for the
// default logger; an empty cwd inherits the process working directory.
func NewManager(logger *slog.Logger, cwd string) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		logger: logger,
		cwd:    cwd,
		byName: map[string]*serverSession{},
	}
}

// OnChange registers a callback fired whenever a server's state changes
// (connect, exit, restart). It must be set before Start and is not called
// concurrently for the same server. A nil callback disables notifications.
func (m *Manager) OnChange(fn func(ServerStatus)) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

// Start spawns all configured MCP servers in parallel, performs the handshake,
// and discovers tools. Servers that fail to start are recorded as failed and
// skipped (non-fatal) — they still appear in Status so the UI can show the
// error and offer a restart.
func (m *Manager) Start(ctx context.Context, servers map[string]core.MCPServer) {
	m.mu.Lock()
	m.configs = make(map[string]core.MCPServer, len(servers))
	for name, cfg := range servers {
		m.configs[name] = cfg
	}
	m.mu.Unlock()

	// Parallel start: a slow server no longer serializes the 15s timeout behind
	// every other one. Names are sorted for a stable server order in the UI.
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sortStrings(names)

	results := make([]*serverSession, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			results[i] = m.dialServer(ctx, name, servers[name])
		}(i, name)
	}
	wg.Wait()

	m.mu.Lock()
	for _, sess := range results {
		m.servers = append(m.servers, sess)
		m.byName[sess.name] = sess
	}
	m.mu.Unlock()

	for _, sess := range results {
		st := sess.status()
		if st.State == StateReady {
			m.logger.Info("MCP server connected", "server", st.Name, "tools", st.ToolCount)
		} else {
			m.logger.Warn("MCP server failed to start", "server", st.Name, "error", st.Error)
		}
		m.notify(st)
	}
}

// dialServer connects one server and returns a serverSession recording the
// outcome (ready or failed). It never returns nil: a failed dial still yields a
// placeholder session so the server is visible and restartable.
func (m *Manager) dialServer(ctx context.Context, name string, cfg core.MCPServer) *serverSession {
	sess := &serverSession{name: name}
	if err := m.connect(ctx, sess, cfg); err != nil {
		sess.setFailed(err.Error())
	}
	return sess
}

// connect starts the subprocess, performs the handshake, discovers tools, and
// arms the exit watcher. On success the session is left in StateReady. The
// caller owns the serverSession; connect takes its lock internally.
func (m *Manager) connect(ctx context.Context, sess *serverSession, cfg core.MCPServer) error {
	startCtx, cancel := context.WithTimeout(ctx, serverStartTimeout)
	defer cancel()

	cmd := exec.Command(cfg.Command, cfg.Args...)
	if m.cwd != "" {
		cmd.Dir = m.cwd
	}
	if len(cfg.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	setProcGroup(cmd)

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "moa",
		Version: "0.1.0",
	}, nil)

	transport := &sdkmcp.CommandTransport{Command: cmd}
	session, err := client.Connect(startCtx, transport, nil)
	if err != nil {
		killProcGroup(cmd)
		return fmt.Errorf("connect: %w", err)
	}

	listResult, err := session.ListTools(startCtx, nil)
	if err != nil {
		_ = session.Close()
		killProcGroup(cmd)
		return fmt.Errorf("list tools: %w", err)
	}

	var tools []toolInfo
	for _, mcpTool := range listResult.Tools {
		var params json.RawMessage
		if mcpTool.InputSchema != nil {
			params, _ = json.Marshal(mcpTool.InputSchema)
		}
		tools = append(tools, toolInfo{
			name:        mcpTool.Name,
			description: mcpTool.Description,
			params:      params,
		})
	}

	sess.mu.Lock()
	sess.cmd = cmd
	sess.client = client
	sess.session = session
	sess.tools = tools
	sess.state = StateReady
	sess.err = ""
	now := time.Now()
	sess.startedAt = now
	sess.changedAt = now
	sess.gen++
	gen := sess.gen
	sess.mu.Unlock()

	m.watchExit(sess, session, cmd, gen)
	return nil
}

// watchExit blocks (in a goroutine) until the connection dies, then marks the
// server exited — unless a newer generation already replaced it.
func (m *Manager) watchExit(sess *serverSession, session *sdkmcp.ClientSession, cmd *exec.Cmd, gen uint64) {
	go func() {
		_ = session.Wait()
		// Reap any grandchildren the SDK's Close left behind (or that outlived a
		// crash of the direct child).
		killProcGroup(cmd)

		sess.mu.Lock()
		if sess.gen != gen || sess.state == StateRestarting {
			// Superseded by a restart; its own connect already set fresh state.
			sess.mu.Unlock()
			return
		}
		sess.state = StateExited
		sess.err = "server process exited"
		sess.tools = nil
		sess.changedAt = time.Now()
		st := sess.statusLocked()
		sess.mu.Unlock()

		m.logger.Warn("MCP server exited", "server", sess.name)
		m.notify(st)
	}()
}

// Tools returns all discovered MCP tools wrapped as core.Tool. The wrapped
// closures route calls through the Manager by server name, so they keep working
// across a restart (which swaps the underlying session) and return a clear
// error when the server is down.
func (m *Manager) Tools() []core.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []core.Tool
	for _, s := range m.servers {
		s.mu.Lock()
		tools := append([]toolInfo(nil), s.tools...)
		s.mu.Unlock()
		for _, ti := range tools {
			all = append(all, m.wrapTool(s, ti))
		}
	}
	return all
}

// Status returns an immutable snapshot of every server's health, in config
// discovery order, for the UI.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServerStatus, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, s.status())
	}
	return out
}

// ErrUnknownServer is returned by RestartServer for a name the Manager does not
// manage.
var ErrUnknownServer = errors.New("unknown MCP server")

// RestartServer tears down one server's process tree and starts it again,
// re-discovering its tools. Other servers are untouched. The returned status is
// the post-restart snapshot; a failed restart leaves the server in StateFailed
// (still restartable). Tool names may differ across generations, so the caller
// should re-sync its tool registry from Tools() afterwards.
func (m *Manager) RestartServer(ctx context.Context, name string) (ServerStatus, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ServerStatus{}, errors.New("manager closed")
	}
	sess, ok := m.byName[name]
	cfg, hasCfg := m.configs[name]
	m.mu.Unlock()
	if !ok || !hasCfg {
		return ServerStatus{}, ErrUnknownServer
	}

	// Mark restarting and grab the old connection to tear down.
	sess.mu.Lock()
	sess.state = StateRestarting
	sess.err = ""
	sess.changedAt = time.Now()
	sess.gen++ // invalidate the old exit watcher
	oldSession := sess.session
	oldCmd := sess.cmd
	sess.session = nil
	restartStatus := sess.statusLocked()
	sess.mu.Unlock()
	m.notify(restartStatus)

	// Tear the old process tree down fully before re-dialing so a fixed-port or
	// single-profile server (e.g. Playwright) doesn't collide with itself.
	if oldSession != nil {
		_ = oldSession.Close()
	}
	if oldCmd != nil {
		killProcGroup(oldCmd)
	}

	err := m.connect(ctx, sess, cfg)
	if err != nil {
		sess.setFailed(err.Error())
		st := sess.status()
		m.logger.Warn("MCP server restart failed", "server", name, "error", err)
		m.notify(st)
		return st, nil
	}
	st := sess.status()
	m.logger.Info("MCP server restarted", "server", name, "tools", st.ToolCount)
	m.notify(st)
	return st, nil
}

// Close gracefully shuts down all server sessions and kills their process trees.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	servers := m.servers
	m.servers = nil
	m.byName = map[string]*serverSession{}
	m.mu.Unlock()

	for _, s := range servers {
		s.mu.Lock()
		session := s.session
		cmd := s.cmd
		s.session = nil
		s.mu.Unlock()
		if session != nil {
			if err := session.Close(); err != nil {
				m.logger.Warn("MCP session close error", "server", s.name, "error", err)
			}
		}
		if cmd != nil {
			killProcGroup(cmd)
		}
	}
}

func (m *Manager) notify(st ServerStatus) {
	m.mu.Lock()
	fn := m.onChange
	m.mu.Unlock()
	if fn != nil {
		fn(st)
	}
}

// --- serverSession helpers ---

func (s *serverSession) setFailed(msg string) {
	s.mu.Lock()
	s.state = StateFailed
	s.err = msg
	s.tools = nil
	s.session = nil
	s.changedAt = time.Now()
	s.mu.Unlock()
}

func (s *serverSession) status() ServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *serverSession) statusLocked() ServerStatus {
	names := make([]string, len(s.tools))
	for i, t := range s.tools {
		names[i] = t.name
	}
	return ServerStatus{
		Name:      s.name,
		State:     s.state,
		ToolCount: len(s.tools),
		ToolNames: names,
		Error:     s.err,
		StartedAt: s.startedAt,
		ChangedAt: s.changedAt,
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// sanitizeToolName converts arbitrary strings to provider-safe tool names.
// OpenAI requires ^[a-zA-Z0-9_-]{1,64}$. Invalid chars become '_', truncated to 64.
func sanitizeToolName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	result := b.String()
	if len(result) > 64 {
		// A blind [:64] cut can collapse two distinct long names into the same
		// tool, silently shadowing one. Keep a short hash of the full name so
		// the truncated form stays unique (55 + "_" + 8 hex = 64 chars).
		h := fnv.New32a()
		_, _ = h.Write([]byte(result))
		result = fmt.Sprintf("%s_%08x", result[:55], h.Sum32())
	}
	if result == "" {
		result = "unnamed"
	}
	return result
}

// wrapTool builds a core.Tool whose Execute routes through the serverSession by
// reading its current connection under lock. This is what lets a tool survive a
// restart: the closure never captures a specific *ClientSession, so after a
// restart swaps sess.session the same tool calls the fresh generation.
func (m *Manager) wrapTool(sess *serverSession, ti toolInfo) core.Tool {
	fullName := sanitizeToolName(ToolPrefix + sess.name + "__" + ti.name)
	label := sess.name + "/" + ti.name
	toolName := ti.name

	// Effect defaults to EffectUnknown (zero value), which the scheduler
	// treats as a barrier — safe for external tools with unknown side effects.
	return core.Tool{
		Name:        fullName,
		Label:       label,
		Description: ti.description,
		Parameters:  ti.params,
		Execute: func(ctx context.Context, args map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			sess.mu.Lock()
			session := sess.session
			state := sess.state
			sess.mu.Unlock()
			if session == nil {
				return core.ErrorResult(fmt.Sprintf("MCP server %s is %s", sess.name, state)), nil
			}
			// ClientSession.CallTool is concurrency-safe (jsonrpc2 uses
			// internal locking for writes, request IDs for response routing).
			result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
				Name:      toolName,
				Arguments: args,
			})
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("MCP tool %s failed: %v", label, err)), nil
			}
			return convertMCPResult(result), nil
		},
	}
}

func convertMCPResult(r *sdkmcp.CallToolResult) core.Result {
	if r == nil {
		return core.TextResult("(no result)")
	}

	var content []core.Content
	for _, c := range r.Content {
		switch v := c.(type) {
		case *sdkmcp.TextContent:
			content = append(content, core.TextContent(v.Text))
		case *sdkmcp.ImageContent:
			content = append(content, core.ImageContent(base64.StdEncoding.EncodeToString(v.Data), v.MIMEType))
		default:
			// Unknown content type (audio, resource, etc.) — JSON fallback.
			if data, err := json.Marshal(c); err == nil {
				content = append(content, core.TextContent(string(data)))
			}
		}
	}

	if len(content) == 0 {
		content = []core.Content{core.TextContent("(empty result)")}
	}

	return core.Result{
		Content: content,
		IsError: r.IsError,
	}
}
