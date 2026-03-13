// Package serve provides an HTTP/WebSocket server for managing multiple
// agent sessions through a web dashboard.
package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"sync/atomic"

	"github.com/ealeixandre/moa/pkg/agent"
	agentcontext "github.com/ealeixandre/moa/pkg/context"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/subagent"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/tool"
	"github.com/ealeixandre/moa/pkg/verify"
)

// SessionState describes the current state of a managed session.
type SessionState string

const (
	StateIdle       SessionState = "idle"       // waiting for user input
	StateRunning    SessionState = "running"    // agent is executing
	StatePermission SessionState = "permission" // blocked on permission approval
	StateError      SessionState = "error"      // last run errored (still usable)
	StateSaved      SessionState = "saved"      // on disk but not loaded into memory
)

// Event is a JSON-serializable event sent to WebSocket clients.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// ManagedSession wraps an agent with metadata for the web dashboard.
type ManagedSession struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	State   SessionState `json:"state"`
	Model   string       `json:"model"`
	CWD     string       `json:"cwd"`
	Created time.Time    `json:"created"`
	Updated time.Time    `json:"updated"`
	Error   string       `json:"error,omitempty"`

	agent              *agent.Agent
	gate               *permission.Gate
	unsub              func()
	sessionCtx         context.Context    // per-session lifetime; cancelled on Delete
	sessionCancel      context.CancelFunc // cancels sessionCtx (bridge, subagents, MCP, runs)
	subscribers        []chan Event
	mu                 sync.Mutex
	messages           []core.AgentMessage
	runCancel          context.CancelFunc
	pending            *pendingPermission
	lastResolvedPermID string

	// Task tracking and plan mode.
	taskStore *tasks.Store
	planMode  *planmode.PlanMode

	// Per-session internals for MCP trust reload and subagent config.
	toolReg       *core.Registry
	agentsMD      string
	resolvedModel core.Model
	mcpMgr        *mcp.Manager // nil when no MCP; closed on Delete
	UntrustedMCP  bool         // true when .mcp.json exists but not trusted

	// Persistence.
	persisted *session.Session   // backing session on disk; nil if no store
	store     *session.FileStore // scoped store for this session's CWD; nil if no store
	deleted   bool               // set on Delete to prevent save() from resurrecting
}

// SessionInfo is the public representation returned by List/Get endpoints.
type SessionInfo struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	State        SessionState `json:"state"`
	Model        string       `json:"model"`
	Thinking     string       `json:"thinking"`
	CWD          string       `json:"cwd"`
	Created      time.Time    `json:"created"`
	Updated      time.Time    `json:"updated"`
	Error        string       `json:"error,omitempty"`
	UntrustedMCP bool         `json:"untrusted_mcp,omitempty"`
	PlanMode     string       `json:"plan_mode,omitempty"`
	PlanFile     string       `json:"plan_file,omitempty"`
}

// Subscribe registers a channel to receive session events. Returns the channel
// and an unsubscribe function. The caller must read from the channel; slow
// consumers are disconnected (channel closed) to prevent stream corruption.
func (s *ManagedSession) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 512)
	s.mu.Lock()
	s.subscribers = append(s.subscribers, ch)
	s.mu.Unlock()
	unsubscribe := func() {
		s.mu.Lock()
		for i, c := range s.subscribers {
			if c == ch {
				s.subscribers = slices.Delete(s.subscribers, i, i+1)
				break
			}
		}
		s.mu.Unlock()
	}
	return ch, unsubscribe
}

// History returns a copy of the session's conversation messages.
func (s *ManagedSession) History() []core.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := make([]core.AgentMessage, len(s.messages))
	copy(msgs, s.messages)
	return msgs
}

// broadcast sends an event to all subscribers. Slow consumers (full channel)
// are disconnected by closing their channel rather than silently dropping
// events, which would corrupt the stream.
func (s *ManagedSession) broadcast(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stale []int
	for i, ch := range s.subscribers {
		select {
		case ch <- e:
		default:
			stale = append(stale, i)
		}
	}
	for i := len(stale) - 1; i >= 0; i-- {
		idx := stale[i]
		close(s.subscribers[idx])
		s.subscribers = slices.Delete(s.subscribers, idx, idx+1)
	}
}

func (s *ManagedSession) closeSubscribers() {
	s.mu.Lock()
	for _, ch := range s.subscribers {
		close(ch)
	}
	s.subscribers = nil
	s.mu.Unlock()
}

// broadcastAgentEvent converts a core.AgentEvent to an Event and broadcasts it.
// Sends full text on message_end so the UI can recover from dropped deltas.
func (s *ManagedSession) broadcastAgentEvent(e core.AgentEvent) {
	switch e.Type {
	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return
		}
		s.broadcast(Event{Type: e.AssistantEvent.Type, Data: map[string]any{
			"delta": e.AssistantEvent.Delta,
		}})

	case core.AgentEventMessageEnd:
		var fullText string
		for _, c := range e.Message.Content {
			if c.Type == "text" {
				fullText += c.Text
			}
		}
		s.broadcast(Event{Type: "message_end", Data: map[string]any{
			"text": fullText,
		}})

	case core.AgentEventToolExecStart:
		s.broadcast(Event{Type: "tool_start", Data: map[string]any{
			"tool_call_id": e.ToolCallID,
			"tool_name":    e.ToolName,
			"args":         e.Args,
		}})

	case core.AgentEventToolExecUpdate:
		var delta string
		if e.Result != nil {
			for _, c := range e.Result.Content {
				if c.Type == "text" {
					delta += c.Text
				}
			}
		}
		if delta != "" {
			s.broadcast(Event{Type: "tool_update", Data: map[string]any{
				"tool_call_id": e.ToolCallID,
				"delta":        delta,
			}})
		}

	case core.AgentEventToolExecEnd:
		var text string
		if e.Result != nil {
			for _, c := range e.Result.Content {
				if c.Type == "text" {
					text += c.Text
				}
			}
		}
		s.broadcast(Event{Type: "tool_end", Data: map[string]any{
			"tool_call_id": e.ToolCallID,
			"tool_name":    e.ToolName,
			"is_error":     e.IsError,
			"rejected":     e.Rejected,
			"result":       text,
		}})
		// Broadcast updated task list after tasks tool changes.
		if e.ToolName == "tasks" && s.taskStore != nil {
			s.broadcast(Event{Type: "tasks_update", Data: map[string]any{
				"tasks": s.taskStore.Tasks(),
			}})
		}

	default:
		s.broadcast(Event{Type: e.Type})
	}
}

func (s *ManagedSession) info() SessionInfo {
	thinking := ""
	if s.agent != nil {
		thinking = s.agent.ThinkingLevel()
	}
	info := SessionInfo{
		ID:           s.ID,
		Title:        s.Title,
		State:        s.State,
		Model:        s.Model,
		Thinking:     thinking,
		CWD:          s.CWD,
		Created:      s.Created,
		Updated:      s.Updated,
		Error:        s.Error,
		UntrustedMCP: s.UntrustedMCP,
	}
	if s.planMode != nil {
		mode := s.planMode.Mode()
		if mode != planmode.ModeOff {
			info.PlanMode = string(mode)
			info.PlanFile = s.planMode.PlanFilePath()
		}
	}
	return info
}

// save persists the session to disk. No-op if persistence is unavailable
// or the session has been deleted.
func (s *ManagedSession) save() {
	s.mu.Lock()
	if s.deleted || s.persisted == nil || s.store == nil {
		s.mu.Unlock()
		return
	}
	s.persisted.Title = s.Title
	s.persisted.Messages = make([]core.AgentMessage, len(s.messages))
	copy(s.persisted.Messages, s.messages)
	s.persisted.CompactionEpoch = s.agent.CompactionEpoch()
	// Persist task store state.
	if s.taskStore != nil {
		if s.persisted.Metadata == nil {
			s.persisted.Metadata = make(map[string]any)
		}
		for k, v := range s.taskStore.SaveToMetadata() {
			s.persisted.Metadata[k] = v
		}
	}
	// Persist plan mode state.
	if s.planMode != nil {
		if s.persisted.Metadata == nil {
			s.persisted.Metadata = make(map[string]any)
		}
		for k, v := range s.planMode.SaveState() {
			s.persisted.Metadata[k] = v
		}
	}
	snapshot := *s.persisted
	store := s.store
	s.mu.Unlock()

	if err := store.Save(&snapshot); err != nil {
		slog.Warn("session save failed", "id", s.ID, "error", err)
	}
}

// Manager owns all active sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*ManagedSession
	baseCtx  context.Context

	providerFactory func(model core.Model) (core.Provider, error)
	transcriber     core.Transcriber // nil when no speech-to-text is available
	defaultModel    core.Model
	workspaceRoot   string
	moaCfg          core.MoaConfig
	sessionBaseDir  string // root for session stores; empty = default (~/.config/moa/sessions/)

	// savedCache caches the result of session.ListAll to avoid
	// re-scanning disk on every poll (frontend polls every 3s).
	// TTL-based: re-scans when older than savedCacheTTL.
	// Invalidated immediately on create/delete/resume.
	savedCacheMu  sync.Mutex
	savedCache    []session.Summary
	savedCacheAt  time.Time
	savedCacheTTL time.Duration // default 30s, configurable for tests
}

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	ProviderFactory func(model core.Model) (core.Provider, error)
	Transcriber     core.Transcriber // optional; enables POST /api/transcribe
	DefaultModel    core.Model
	WorkspaceRoot   string
	MoaCfg          core.MoaConfig
	SessionBaseDir  string // root for session stores; empty = default
}

// NewManager creates a Manager. The context controls the lifetime of all agent
// runs — cancelling it aborts every active session.
func NewManager(ctx context.Context, cfg ManagerConfig) *Manager {
	return &Manager{
		sessions:        make(map[string]*ManagedSession),
		baseCtx:         ctx,
		providerFactory: cfg.ProviderFactory,
		transcriber:     cfg.Transcriber,
		defaultModel:    cfg.DefaultModel,
		workspaceRoot:   cfg.WorkspaceRoot,
		moaCfg:          cfg.MoaCfg,
		sessionBaseDir:  cfg.SessionBaseDir,
		savedCacheTTL:   30 * time.Second,
	}
}

// CreateOpts configures a new session.
type CreateOpts struct {
	Model string `json:"model"`
	Title string `json:"title"`
	CWD   string `json:"cwd"`
}

// CreateSession creates a new agent session. The agent is configured with
// tools, permissions, and system prompt scoped to the session's working
// directory — matching CLI behavior for config, AGENTS.md, and MCP trust.
func (m *Manager) CreateSession(opts CreateOpts) (*ManagedSession, error) {
	cwd := opts.CWD
	if cwd == "" {
		cwd = m.workspaceRoot
	}

	// Resolve ID + persistence first.
	var persisted *session.Session
	var store *session.FileStore
	id := ""
	if s, err := session.NewFileStore(m.sessionBaseDir, cwd); err == nil {
		store = s
		persisted = store.Create()
		persisted.Title = opts.Title
		id = persisted.ID
	}
	if id == "" {
		id = newID() // fallback when persistence unavailable
	}

	sess, err := m.buildManagedSession(id, opts.Title, opts.Model, cwd)
	if err != nil {
		return nil, err
	}

	// Finalize persistence.
	if persisted != nil {
		persisted.Metadata = map[string]any{
			"model": fullModelSpec(sess.resolvedModel),
			"cwd":   sess.CWD,
		}
		_ = store.Save(persisted)
		sess.persisted = persisted
		sess.store = store
	}

	m.invalidateSavedCache()
	return sess, nil
}

// buildManagedSession creates an in-memory managed session with full runtime
// (tools, MCP, permissions, subagents, agent). Does NOT touch persistence.
// Used by both CreateSession (new sessions) and ResumeSession (restoring saved).
func (m *Manager) buildManagedSession(id, title, modelSpec, cwd string) (*ManagedSession, error) {
	// 1. Resolve + canonicalize CWD.
	canonical, err := core.CanonicalizePath(cwd)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCWD, err)
	}
	if info, statErr := os.Stat(canonical); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrInvalidCWD, canonical)
	}
	cwd = canonical

	// 2. Load config for session CWD (global + project, like CLI).
	sessionCfg := core.LoadMoaConfig(cwd)

	// 3. Model.
	model := m.defaultModel
	if modelSpec != "" {
		model, _ = core.ResolveModel(modelSpec)
	}

	// 4. Provider.
	prov, err := m.providerFactory(model)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}

	// 5. Tool registry scoped to session CWD.
	toolReg := core.NewRegistry()
	tool.RegisterBuiltins(toolReg, tool.ToolConfig{
		WorkspaceRoot:  cwd,
		DisableSandbox: sessionCfg.DisableSandbox,
		AllowedPaths:   append(sessionCfg.AllowedPaths, tool.SpillOutputDir()),
		BashTimeout:    5 * time.Minute,
		BraveAPIKey:    sessionCfg.BraveAPIKey,
	})

	// 6. Task store — always available.
	taskStore := tasks.NewStore()
	toolReg.Register(tasks.NewTool(taskStore))

	// 6b. Verify tool — register only if .moa/verify.json exists and is valid.
	verifyCfg, verifyErr := verify.LoadConfig(cwd)
	if verifyErr != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid .moa/verify.json in %s: %v\n", cwd, verifyErr)
	}
	if verifyCfg != nil {
		toolReg.Register(verify.NewTool(cwd))
	}

	// 7. AGENTS.md from session CWD.
	agentsMD, _ := agentcontext.LoadAgentsMD(cwd, os.Getenv("AGENT_HOME"))

	// 8. Permission gate from session config (mirrors CLI logic).
	permMode := permission.Mode(sessionCfg.Permissions.Mode)
	if permMode == "" {
		permMode = permission.ModeYolo
	}
	var gate *permission.Gate
	if permMode != permission.ModeYolo {
		permCfg := permission.Config{
			Allow: sessionCfg.Permissions.Allow,
			Deny:  sessionCfg.Permissions.Deny,
			Rules: sessionCfg.Permissions.Rules,
		}
		if permMode == permission.ModeAuto {
			evalModelSpec := sessionCfg.Permissions.Model
			if evalModelSpec == "" {
				evalModelSpec = "haiku"
			}
			evalModel, _ := core.ResolveModel(evalModelSpec)
			evalProv, evalErr := m.providerFactory(evalModel)
			if evalErr == nil {
				permCfg.Evaluator = permission.NewEvaluator(evalProv, evalModel)
			}
		}
		gate = permission.New(permMode, permCfg)
	}

	// 9. MCP servers.
	sessionCtx, sessionCancel := context.WithCancel(m.baseCtx)
	untrustedMCP := false
	mcpPath := filepath.Join(cwd, ".mcp.json")
	if _, statErr := os.Stat(mcpPath); statErr == nil {
		if core.IsMCPPathTrusted(sessionCfg, cwd) {
			projectServers, loadErr := core.LoadMCPFile(mcpPath)
			if loadErr == nil {
				sessionCfg.MCPServers = core.MergeMCPServers(sessionCfg.MCPServers, projectServers)
			}
		} else {
			untrustedMCP = true
		}
	}

	var mcpMgr *mcp.Manager
	if len(sessionCfg.MCPServers) > 0 {
		mcpMgr = mcp.NewManager(nil)
		mcpMgr.Start(sessionCtx, sessionCfg.MCPServers)
		for _, t := range mcpMgr.Tools() {
			toolReg.Register(t)
		}
	}

	// 10. Subagents. Declare sess and agHolder before closures; both are
	// populated before the session is exposed to callers.
	var sess *ManagedSession
	var agHolder atomic.Pointer[agent.Agent]

	if err := subagent.RegisterAll(toolReg, subagent.Config{
		DefaultModel: model,
		CurrentModel: func() core.Model {
			if a := agHolder.Load(); a != nil {
				return a.Model()
			}
			return model
		},
		CurrentThinkingLevel: func() string {
			if a := agHolder.Load(); a != nil {
				return a.ThinkingLevel()
			}
			return "medium"
		},
		CurrentPermissionCheck: func() func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if a := agHolder.Load(); a != nil {
				return a.PermissionCheck()
			}
			if gate != nil {
				return gate.Check
			}
			return nil
		},
		ProviderFactory: m.providerFactory,
		AgentsMD:        agentsMD,
		ParentTools:     toolReg,
		AppCtx:          sessionCtx,
		WorkspaceRoot:   cwd,
		OnAsyncJobChange: func(count int) {
			if s := sess; s != nil {
				s.broadcast(Event{Type: "subagent_count", Data: map[string]any{
					"count": count,
				}})
			}
		},
		OnAsyncComplete: func(jobID, task, status, resultTail string) {
			s := sess
			if s == nil {
				return
			}
			var agentText string
			switch status {
			case "completed":
				agentText = fmt.Sprintf("[subagent completed] Job %s finished.\nTask: %s\n\nResult (last 50 lines):\n%s", jobID, task, resultTail)
			case "failed":
				agentText = fmt.Sprintf("[subagent failed] Job %s failed.\nTask: %s\nError: %s", jobID, task, resultTail)
			case "cancelled":
				agentText = fmt.Sprintf("[subagent cancelled] Job %s was cancelled.\nTask: %s", jobID, task)
			default:
				return
			}

			if a := agHolder.Load(); a != nil {
				if a.IsRunning() {
					a.Steer(agentText)
				} else {
					a.Enqueue(agentText)
				}
			}

			s.broadcast(Event{Type: "subagent_complete", Data: map[string]any{
				"job_id": jobID,
				"task":   task,
				"status": status,
				"text":   agentText,
			}})
		},
	}); err != nil {
		sessionCancel()
		return nil, fmt.Errorf("subagent registration: %w", err)
	}

	// 11. Plan mode.
	reviewModel := model
	if sessionCfg.PlanReviewModel != "" {
		if rm, ok := core.ResolveModel(sessionCfg.PlanReviewModel); ok {
			reviewModel = rm
		}
	}
	reviewThinking := "medium"
	if sessionCfg.PlanReviewThinking != "" {
		reviewThinking = sessionCfg.PlanReviewThinking
	}
	codeReviewModel := reviewModel
	if sessionCfg.CodeReviewModel != "" {
		if crm, ok := core.ResolveModel(sessionCfg.CodeReviewModel); ok {
			codeReviewModel = crm
		}
	}
	codeReviewThinking := reviewThinking
	if sessionCfg.CodeReviewThinking != "" {
		codeReviewThinking = sessionCfg.CodeReviewThinking
	}

	pm := planmode.New(planmode.Config{
		Registry:   toolReg,
		SessionDir: cwd,
		TaskStore:  taskStore,
		ReviewCfg: planmode.ReviewConfig{
			ProviderFactory: m.providerFactory,
			Model:           reviewModel,
			ThinkingLevel:   reviewThinking,
			ParentTools:     toolReg,
		},
		CodeReviewCfg: planmode.ReviewConfig{
			ProviderFactory: m.providerFactory,
			Model:           codeReviewModel,
			ThinkingLevel:   codeReviewThinking,
			ParentTools:     toolReg,
		},
	})

	// 12. System prompt (after ALL tools registered: builtins + MCP + subagents).
	systemPrompt := agentcontext.BuildSystemPrompt(agentsMD, toolReg.Specs(), cwd, verifyCfg != nil)

	// 13. Agent.
	agentCfg := agent.AgentConfig{
		Provider:            prov,
		Model:               model,
		SystemPrompt:        systemPrompt,
		ThinkingLevel:       "medium",
		Tools:               toolReg,
		WorkspaceRoot:       cwd,
		MaxTurns:            50,
		MaxToolCallsPerTurn: 20,
		MaxRunDuration:      30 * time.Minute,
		MaxBudget:           sessionCfg.MaxBudget,
	}
	// Compose permission check: plan mode filter + permission gate.
	agentCfg.PermissionCheck = func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
		if allowed, reason := pm.FilterToolCall(name, args); !allowed {
			return &core.ToolCallDecision{Block: true, Reason: reason, Kind: core.ToolCallDecisionKindPolicy}
		}
		if gate != nil {
			return gate.Check(ctx, name, args)
		}
		return nil
	}

	ag, err := agent.New(agentCfg)
	if err != nil {
		sessionCancel()
		return nil, err
	}
	agHolder.Store(ag)

	// 14. Build session.
	sess = &ManagedSession{
		ID:            id,
		Title:         title,
		State:         StateIdle,
		Model:         modelDisplayName(model),
		CWD:           cwd,
		Created:       time.Now(),
		Updated:       time.Now(),
		agent:         ag,
		gate:          gate,
		sessionCtx:    sessionCtx,
		sessionCancel: sessionCancel,
		toolReg:       toolReg,
		agentsMD:      agentsMD,
		resolvedModel: model,
		mcpMgr:        mcpMgr,
		UntrustedMCP:  untrustedMCP,
		taskStore:     taskStore,
		planMode:      pm,
	}

	pm.SetOnChange(func(mode planmode.Mode) {
		data := map[string]any{
			"mode": string(mode),
		}
		if mode != planmode.ModeOff {
			data["plan_file"] = pm.PlanFilePath()
		}
		sess.broadcast(Event{Type: "plan_mode", Data: data})
	})

	sess.unsub = ag.Subscribe(func(e core.AgentEvent) {
		sess.broadcastAgentEvent(e)
	})

	if gate != nil {
		go sess.permissionBridge(sessionCtx)
	}

	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
	return sess, nil
}

var (
	ErrNotFound   = errors.New("session not found")
	ErrBusy       = errors.New("session is busy")
	ErrInvalidCWD = errors.New("invalid working directory")
)

// Send delivers a user message to a session and starts the agent run.
// Returns ErrBusy if the session is already running/waiting for permission.
// The run executes in a background goroutine; results stream via WebSocket.
// Send sends a message to the session. If the session is idle, it starts a
// new agent turn. If the session is running, it steers the agent (injects the
// message between steps). Returns the action taken: "send", "steer".
func (m *Manager) Send(sessionID, text string) (string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return "", ErrNotFound
	}

	sess.mu.Lock()
	if sess.State == StateRunning || sess.State == StatePermission {
		ag := sess.agent
		sess.mu.Unlock()
		// Steer the running agent — injected between tool calls.
		ag.Steer(text)
		sess.broadcast(Event{Type: "steer", Data: map[string]any{"text": text}})
		return "steer", nil
	}
	sess.State = StateRunning
	sess.Updated = time.Now()

	runCtx, cancel := context.WithCancel(sess.sessionCtx)
	sess.runCancel = cancel

	if sess.Title == "" {
		title := text
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		sess.Title = title
	}
	sess.mu.Unlock()

	sess.broadcast(Event{Type: "state_change", Data: map[string]any{
		"state": string(StateRunning),
	}})

	go func() {
		defer cancel()
		msgs, err := sess.agent.Send(runCtx, text)

		// Drain emitter so all async events (text_delta, message_end, turn_end, etc.)
		// are delivered before terminal run events.
		// Ordering invariant:
		//   agent.Send() -> agent.Drain() -> state_change -> run_end
		// This keeps structural turn events ahead of run_end in the websocket stream.
		sess.agent.Drain(2 * time.Second)

		sess.mu.Lock()
		sess.messages = msgs
		sess.runCancel = nil
		// Clear any stale pending permission (run ended while waiting).
		sess.pending = nil
		if err != nil && runCtx.Err() == nil {
			sess.State = StateError
			sess.Error = err.Error()
		} else {
			sess.State = StateIdle
			sess.Error = ""
		}
		sess.Updated = time.Now()
		newState := sess.State
		errText := sess.Error
		sess.mu.Unlock()

		sess.save()

		data := map[string]any{"state": string(newState)}
		if errText != "" {
			data["error"] = errText
		}
		sess.broadcast(Event{Type: "state_change", Data: data})

		if finalText := extractFinalText(msgs); finalText != "" {
			sess.broadcast(Event{Type: "run_end", Data: map[string]any{
				"text": finalText,
			}})
		}
	}()
	return "send", nil
}

// List returns info for all sessions, sorted by updated time descending.
func (m *Manager) List() []SessionInfo {
	m.mu.RLock()
	active := make(map[string]*ManagedSession, len(m.sessions))
	for id, s := range m.sessions {
		active[id] = s
	}
	m.mu.RUnlock()

	list := make([]SessionInfo, 0)
	for _, s := range active {
		s.mu.Lock()
		list = append(list, s.info())
		s.mu.Unlock()
	}

	// Merge saved sessions from all project directories (cached).
	saved := m.cachedSavedSessions()
	for _, sum := range saved {
		if _, isActive := active[sum.ID]; isActive {
			continue
		}
		model, _ := sum.Metadata["model"].(string)
		cwd, _ := sum.Metadata["cwd"].(string)
		list = append(list, SessionInfo{
			ID:      sum.ID,
			Title:   sum.Title,
			State:   StateSaved,
			Model:   model,
			CWD:     cwd,
			Created: sum.Created,
			Updated: sum.Updated,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Updated.After(list[j].Updated)
	})
	return list
}

// cachedSavedSessions returns saved sessions from disk, using a TTL cache
// to avoid re-scanning on every poll (frontend polls every 3s).
func (m *Manager) cachedSavedSessions() []session.Summary {
	m.savedCacheMu.Lock()
	defer m.savedCacheMu.Unlock()
	if m.savedCache != nil && time.Since(m.savedCacheAt) < m.savedCacheTTL {
		return m.savedCache
	}
	saved, _ := session.ListAll(m.sessionBaseDir)
	m.savedCache = saved
	m.savedCacheAt = time.Now()
	return saved
}

// invalidateSavedCache forces the next List() call to re-scan disk.
func (m *Manager) invalidateSavedCache() {
	m.savedCacheMu.Lock()
	m.savedCache = nil
	m.savedCacheMu.Unlock()
}

// Get returns a managed session by ID.
func (m *Manager) Get(id string) (*ManagedSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if s == nil {
		return nil, false // nil placeholder during resume
	}
	return s, ok
}

// Delete aborts any running agent, unsubscribes events, and removes the session.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		// Not active — try disk.
		if err := session.DeleteByID(m.sessionBaseDir, id); err != nil {
			if errors.Is(err, session.ErrNotFound) {
				return ErrNotFound
			}
			return err
		}
		m.invalidateSavedCache()
		return nil
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	// Mark deleted to prevent save() from resurrecting.
	sess.mu.Lock()
	sess.deleted = true
	store := sess.store
	sess.persisted = nil
	sess.store = nil
	sess.mu.Unlock()

	// Close MCP connections before context cancellation.
	if sess.mcpMgr != nil {
		sess.mcpMgr.Close()
	}

	// Cancel session context — stops bridge, subagent jobs, and in-flight runs.
	sess.sessionCancel()

	sess.unsub()
	sess.closeSubscribers()

	// Delete from disk.
	if store != nil {
		_ = store.Delete(id)
	}
	m.invalidateSavedCache()
	return nil
}

// ResumeSession loads a saved session from disk and creates a full runtime.
// On failure, only runtime resources are cleaned up — disk state is untouched.
func (m *Manager) ResumeSession(id string) (*ManagedSession, error) {
	// Use full Lock (not RLock) for check-and-reserve to prevent TOCTOU:
	// two concurrent ResumeSession calls for the same ID could both pass
	// an RLock check and create duplicate runtimes.
	m.mu.Lock()
	if _, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	// Reserve the slot with a nil placeholder to block concurrent resumes.
	m.sessions[id] = nil
	m.mu.Unlock()

	// On any error below, release the reserved slot.
	cleanup := func() {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}

	saved, store, err := session.FindSession(m.sessionBaseDir, id)
	if err != nil {
		cleanup()
		return nil, err
	}

	modelID, _ := saved.Metadata["model"].(string)
	cwd, _ := saved.Metadata["cwd"].(string)
	if cwd == "" {
		cwd = m.workspaceRoot
	}

	sess, err := m.buildManagedSession(saved.ID, saved.Title, modelID, cwd)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("resume: %w", err)
	}

	if err := sess.agent.LoadState(saved.Messages, saved.CompactionEpoch); err != nil {
		sess.sessionCancel()
		sess.unsub()
		if sess.mcpMgr != nil {
			sess.mcpMgr.Close()
		}
		m.mu.Lock()
		delete(m.sessions, sess.ID)
		m.mu.Unlock()
		return nil, fmt.Errorf("resume: load state: %w", err)
	}

	sess.mu.Lock()
	sess.messages = saved.Messages
	sess.persisted = saved
	sess.store = store
	sess.Created = saved.Created
	// Restore task store state.
	if sess.taskStore != nil && saved.Metadata != nil {
		sess.taskStore.RestoreFromMetadata(saved.Metadata)
	}
	// Restore plan mode state.
	if sess.planMode != nil && saved.Metadata != nil {
		sess.planMode.RestoreState(saved.Metadata)
		sess.planMode.ApplyRestoredState()
	}
	sess.mu.Unlock()

	return sess, nil
}

// ReconfigureSession changes the model and/or thinking level of a session.
// Only allowed when the session is idle (not running).
func (m *Manager) ReconfigureSession(sessionID, modelSpec, thinking string) (map[string]string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return nil, ErrNotFound
	}

	sess.mu.Lock()
	if sess.State == StateRunning || sess.State == StatePermission {
		sess.mu.Unlock()
		return nil, ErrBusy
	}

	// Resolve model (keep current if empty).
	model := sess.resolvedModel
	if modelSpec != "" {
		model, _ = core.ResolveModel(modelSpec)
	}

	// Resolve thinking (keep current if empty).
	thinkingLevel := sess.agent.ThinkingLevel()
	if thinking != "" {
		thinkingLevel = normalizeThinkingLevel(thinking)
	}
	sess.mu.Unlock()

	// Create provider for the (possibly new) model.
	prov, err := m.providerFactory(model)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}

	// Reconfigure the agent (strips thinking blocks on model change).
	if err := sess.agent.Reconfigure(prov, model, thinkingLevel); err != nil {
		return nil, err
	}

	sess.mu.Lock()
	sess.resolvedModel = model
	sess.Model = modelDisplayName(model)
	result := map[string]string{
		"model":    sess.Model,
		"thinking": thinkingLevel,
	}
	sess.mu.Unlock()

	sess.broadcast(Event{Type: "config_change", Data: result})
	return result, nil
}

func normalizeThinkingLevel(level string) string {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "none":
		return "off"
	default:
		return normalized
	}
}

// CommandResult is the response from executing a slash command.
type CommandResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// ExecCommand executes a slash command in a session.
func (m *Manager) ExecCommand(sessionID, rawCommand string) (*CommandResult, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return nil, ErrNotFound
	}

	parts := strings.Fields(rawCommand)
	if len(parts) == 0 {
		return &CommandResult{OK: false, Message: "empty command"}, nil
	}

	// Strip leading "/" if present (frontend may send it either way).
	cmd := strings.TrimPrefix(parts[0], "/")
	args := parts[1:]

	switch cmd {
	case "clear":
		sess.mu.Lock()
		if sess.State == StateRunning || sess.State == StatePermission {
			sess.mu.Unlock()
			return nil, ErrBusy
		}
		sess.mu.Unlock()

		if err := sess.agent.Reset(); err != nil {
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}

		sess.mu.Lock()
		sess.messages = nil
		sess.mu.Unlock()

		sess.save()
		sess.broadcast(Event{Type: "command", Data: map[string]any{
			"command": "clear",
		}})
		return &CommandResult{OK: true, Message: "conversation cleared"}, nil

	case "compact":
		sess.mu.Lock()
		if sess.State == StateRunning || sess.State == StatePermission {
			sess.mu.Unlock()
			return nil, ErrBusy
		}
		sess.mu.Unlock()

		if _, err := sess.agent.Compact(sess.sessionCtx); err != nil {
			return &CommandResult{OK: false, Message: "compaction failed: " + err.Error()}, nil
		}

		sess.mu.Lock()
		sess.messages = sess.agent.Messages()
		sess.mu.Unlock()

		sess.save()
		sess.broadcast(Event{Type: "command", Data: map[string]any{
			"command":  "compact",
			"messages": sess.agent.Messages(),
		}})
		return &CommandResult{OK: true, Message: "conversation compacted"}, nil

	case "model":
		if len(args) == 0 {
			return &CommandResult{OK: false, Message: "usage: /model <name>"}, nil
		}
		result, err := m.ReconfigureSession(sessionID, strings.Join(args, " "), "")
		if err != nil {
			if errors.Is(err, ErrBusy) {
				return nil, ErrBusy
			}
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		return &CommandResult{OK: true, Message: "model: " + result["model"]}, nil

	case "thinking":
		if len(args) == 0 {
			return &CommandResult{OK: false, Message: "usage: /thinking <off|low|medium|high>"}, nil
		}
		result, err := m.ReconfigureSession(sessionID, "", args[0])
		if err != nil {
			if errors.Is(err, ErrBusy) {
				return nil, ErrBusy
			}
			return &CommandResult{OK: false, Message: err.Error()}, nil
		}
		return &CommandResult{OK: true, Message: "thinking: " + result["thinking"]}, nil

	case "plan":
		if sess.planMode == nil {
			return &CommandResult{OK: false, Message: "plan mode not available"}, nil
		}
		sess.mu.Lock()
		if sess.State == StateRunning || sess.State == StatePermission {
			sess.mu.Unlock()
			return nil, ErrBusy
		}
		sess.mu.Unlock()

		mode := sess.planMode.Mode()

		if len(args) > 0 && args[0] == "exit" {
			if mode == planmode.ModeOff {
				return &CommandResult{OK: false, Message: "not in plan mode"}, nil
			}
			sess.planMode.Exit()
			sess.broadcast(Event{Type: "plan_mode", Data: map[string]any{
				"mode": string(planmode.ModeOff),
			}})
			return &CommandResult{OK: true, Message: "exited plan mode"}, nil
		}

		if mode == planmode.ModeOff {
			planPath, err := sess.planMode.Enter()
			if err != nil {
				return &CommandResult{OK: false, Message: err.Error()}, nil
			}
			sess.broadcast(Event{Type: "plan_mode", Data: map[string]any{
				"mode":      string(planmode.ModePlanning),
				"plan_file": planPath,
			}})
			return &CommandResult{OK: true, Message: "entered plan mode → " + planPath}, nil
		}

		return &CommandResult{OK: true, Message: "plan mode: " + string(mode)}, nil

	case "tasks":
		if sess.taskStore == nil {
			return &CommandResult{OK: false, Message: "task tracking not available"}, nil
		}
		if len(args) == 0 {
			// List tasks.
			taskList := sess.taskStore.Tasks()
			if len(taskList) == 0 {
				return &CommandResult{OK: true, Message: "No tasks"}, nil
			}
			done := 0
			var lines []string
			for _, t := range taskList {
				icon := "☐"
				if t.Status == "done" {
					icon = "☑"
					done++
				}
				lines = append(lines, fmt.Sprintf("%s #%d: %s", icon, t.ID, t.Title))
			}
			lines = append(lines, fmt.Sprintf("\n%d/%d complete", done, len(taskList)))
			return &CommandResult{OK: true, Message: strings.Join(lines, "\n")}, nil
		}
		switch args[0] {
		case "done":
			if len(args) < 2 {
				return &CommandResult{OK: false, Message: "usage: /tasks done <id>"}, nil
			}
			var id int
			if _, err := fmt.Sscanf(args[1], "%d", &id); err != nil {
				return &CommandResult{OK: false, Message: "invalid task ID: " + args[1]}, nil
			}
			if !sess.taskStore.MarkDone(id) {
				return &CommandResult{OK: false, Message: fmt.Sprintf("task #%d not found", id)}, nil
			}
			sess.broadcast(Event{Type: "tasks_update", Data: map[string]any{
				"tasks": sess.taskStore.Tasks(),
			}})
			return &CommandResult{OK: true, Message: fmt.Sprintf("✅ Task #%d marked done", id)}, nil
		case "reset":
			sess.taskStore.Reset()
			sess.broadcast(Event{Type: "tasks_update", Data: map[string]any{
				"tasks": sess.taskStore.Tasks(),
			}})
			return &CommandResult{OK: true, Message: "Tasks cleared"}, nil
		default:
			return &CommandResult{OK: false, Message: "usage: /tasks [done <id> | reset]"}, nil
		}

	default:
		return &CommandResult{OK: false, Message: "unknown command: /" + cmd}, nil
	}
}

// Cancel aborts the running agent in a session.
// Cancels runCtx (set by Send), which propagates to the agent's internal
// context and causes the Send goroutine to classify the result as idle
// (not error).
func (m *Manager) Cancel(sessionID string) error {
	sess, ok := m.Get(sessionID)
	if !ok {
		return ErrNotFound
	}
	sess.mu.Lock()
	if sess.State != StateRunning && sess.State != StatePermission {
		sess.mu.Unlock()
		return fmt.Errorf("session is not running")
	}
	cancel := sess.runCancel
	sess.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// reloadMCP loads .mcp.json, starts new MCP servers, and swaps them in.
// On failure, existing MCP tools are preserved. Idempotent.
// Returns ErrBusy if the session is running.
func (s *ManagedSession) reloadMCP(sessionCfg core.MoaConfig) error {
	// Phase 1: prepare (no mutation).
	projectServers, err := core.LoadMCPFile(filepath.Join(s.CWD, ".mcp.json"))
	if err != nil {
		return err
	}

	merged := core.MergeMCPServers(sessionCfg.MCPServers, projectServers)

	var newMgr *mcp.Manager
	var newTools []core.Tool
	if len(merged) > 0 {
		newMgr = mcp.NewManager(nil)
		newMgr.Start(s.sessionCtx, merged)
		newTools = newMgr.Tools()
	}

	// Abort if new manager produced no tools but old one had some — likely
	// transient failure. Keep old tools intact.
	if newMgr != nil && len(newTools) == 0 {
		newMgr.Close()
		return fmt.Errorf("MCP servers started but no tools available; keeping existing tools")
	}

	// Phase 2: swap (under lock, no blocking I/O).
	s.mu.Lock()
	if s.State == StateRunning || s.State == StatePermission {
		s.mu.Unlock()
		if newMgr != nil {
			newMgr.Close()
		}
		return ErrBusy
	}

	oldMgr := s.mcpMgr

	// Deregister old MCP tools (prefixed "mcp__").
	for _, spec := range s.toolReg.Specs() {
		if strings.HasPrefix(spec.Name, "mcp__") {
			s.toolReg.Unregister(spec.Name)
		}
	}

	// Register new tools.
	for _, t := range newTools {
		s.toolReg.Register(t)
	}
	s.mcpMgr = newMgr
	s.UntrustedMCP = false
	s.mu.Unlock()

	// Phase 3: cleanup old manager (outside lock — Close may block).
	if oldMgr != nil {
		oldMgr.Close()
	}

	return nil
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func modelDisplayName(m core.Model) string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

// fullModelSpec returns a provider-qualified model spec for persistence.
// Format: "provider/id" when provider is set, otherwise just "id".
func fullModelSpec(m core.Model) string {
	if m.Provider != "" {
		return m.Provider + "/" + m.ID
	}
	return m.ID
}

func extractFinalText(msgs []core.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			var parts []string
			for _, c := range msgs[i].Content {
				if c.Type == "text" && c.Text != "" {
					parts = append(parts, c.Text)
				}
			}
			return strings.Join(parts, "")
		}
	}
	return ""
}
