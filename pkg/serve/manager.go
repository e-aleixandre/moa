// Package serve provides an HTTP/WebSocket server for managing multiple
// agent sessions through a web dashboard.
package serve

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/askuser"
	"github.com/ealeixandre/moa/pkg/checkpoint"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tasks"
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

// ManagedSession wraps an agent with metadata for the web dashboard.
type ManagedSession struct {
	// Public identity (read after creation, mutated under mu for Title/State/Error).
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	State   SessionState `json:"state"`
	Model   string       `json:"model"`
	CWD     string       `json:"cwd"`
	Created time.Time    `json:"created"`
	Updated time.Time    `json:"updated"`
	Error   string       `json:"error,omitempty"`

	mu sync.Mutex // protects mutable fields below and Title/State/Error above

	// --- Runtime: agent lifecycle and context ---
	runtime sessionRuntime

	// --- Subscribers: WebSocket event fan-out ---
	subscribers []chan Event

	// --- Conversation state (mutated under mu) ---
	messages  []core.AgentMessage
	runCancel context.CancelFunc

	// --- Approval bridges (permission + ask_user) ---
	approvals sessionApprovals

	// --- Persistence ---
	persistence sessionPersistence
}

// sessionRuntime holds the agent, tools, MCP, and session lifetime context.
// Immutable after construction (except mcpMgr on reload).
type sessionRuntime struct {
	agent         *agent.Agent
	gate          *permission.Gate
	unsub         func()             // unsubscribe from agent events
	sessionCtx    context.Context    // per-session lifetime; cancelled on Delete
	sessionCancel context.CancelFunc // cancels sessionCtx
	toolReg       *core.Registry
	agentsMD      string
	resolvedModel core.Model
	mcpMgr        *mcp.Manager // nil when no MCP; closed on Delete
	UntrustedMCP  bool         // true when .mcp.json exists but not trusted
	taskStore     *tasks.Store
	planMode      *planmode.PlanMode
	checkpoints   *checkpoint.Store // nil when checkpoints disabled

	// subagentTexts tracks notification texts injected via Steer/Enqueue
	// so broadcastAgentEvent can suppress duplicate steer WS events.
	subagentTexts sync.Map
}

// sessionApprovals tracks pending permission and ask_user prompts.
type sessionApprovals struct {
	pending            *pendingPermission
	lastResolvedPermID string
	askBridge          *askuser.Bridge
	pendingAsk         *pendingAskUser
	bridgeStop         chan struct{} // closed to stop the current permissionBridge goroutine
}

// sessionPersistence handles disk storage.
type sessionPersistence struct {
	persisted *session.Session   // backing session on disk; nil if no store
	store     *session.FileStore // scoped store for this session's CWD; nil if no store
	deleted   bool               // set on Delete to prevent save() from resurrecting
}

// SessionInfo is the public representation returned by List/Get endpoints.
type SessionInfo struct {
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	State          SessionState `json:"state"`
	Model          string       `json:"model"`
	Thinking       string       `json:"thinking"`
	CWD            string       `json:"cwd"`
	Created        time.Time    `json:"created"`
	Updated        time.Time    `json:"updated"`
	Error          string       `json:"error,omitempty"`
	UntrustedMCP   bool         `json:"untrusted_mcp,omitempty"`
	PlanMode       string       `json:"plan_mode,omitempty"`
	PlanFile       string       `json:"plan_file,omitempty"`
	ContextPercent int          `json:"context_percent"` // 0-100, -1 if unknown
	PermissionMode string       `json:"permission_mode"` // "yolo", "ask", "auto"
}

// wsSubscriberBuffer is the capacity of per-WebSocket event channels.
// Larger than the agent emitter buffer (256) because WebSocket writes can
// stall briefly on network backpressure. Slow consumers are disconnected.
const (
	wsSubscriberBuffer = 512 // per-WS event channel capacity (larger than agent's 256 for network backpressure)
	maxTitleLength     = 80  // auto-generated session title cap
)

// Subscribe registers a channel to receive session events. Returns the channel
// and an unsubscribe function. The caller must read from the channel; slow
// consumers are disconnected (channel closed) to prevent stream corruption.
func (s *ManagedSession) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, wsSubscriberBuffer)
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
		s.broadcast(Event{Type: e.AssistantEvent.Type, Data: DeltaData{
			Delta: e.AssistantEvent.Delta,
		}})

	case core.AgentEventMessageEnd:
		var fullText string
		for _, c := range e.Message.Content {
			if c.Type == "text" {
				fullText += c.Text
			}
		}
		s.broadcast(Event{Type: "message_end", Data: MessageEndData{Text: fullText}})

	case core.AgentEventToolExecStart:
		s.broadcast(Event{Type: "tool_start", Data: ToolStartData{
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Args:       e.Args,
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
			s.broadcast(Event{Type: "tool_update", Data: ToolUpdateData{
				ToolCallID: e.ToolCallID,
				Delta:      delta,
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
		s.broadcast(Event{Type: "tool_end", Data: ToolEndData{
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			IsError:    e.IsError,
			Rejected:   e.Rejected,
			Result:     text,
		}})
		if e.ToolName == "tasks" && s.runtime.taskStore != nil {
			s.broadcast(Event{Type: "tasks_update", Data: TasksUpdateData{
				Tasks: s.runtime.taskStore.Tasks(),
			}})
		}

	case core.AgentEventSteer:
		// Suppress steer events that originated from subagent completions —
		// the frontend already got the subagent_complete event.
		if _, wasSubagent := s.runtime.subagentTexts.LoadAndDelete(e.Text); wasSubagent {
			break
		}
		s.broadcast(Event{Type: "steer", Data: SteerData{Text: e.Text}})

	default:
		s.broadcast(Event{Type: e.Type})
	}
}

func (s *ManagedSession) info() SessionInfo {
	thinking := ""
	if s.runtime.agent != nil {
		thinking = s.runtime.agent.ThinkingLevel()
	}
	info := SessionInfo{
		ID:             s.ID,
		Title:          s.Title,
		State:          s.State,
		Model:          s.Model,
		Thinking:       thinking,
		CWD:            s.CWD,
		Created:        s.Created,
		Updated:        s.Updated,
		Error:          s.Error,
		UntrustedMCP:   s.runtime.UntrustedMCP,
		ContextPercent: s.contextPercent(),
		PermissionMode: s.permissionMode(),
	}
	if s.runtime.planMode != nil {
		mode := s.runtime.planMode.Mode()
		if mode != planmode.ModeOff {
			info.PlanMode = string(mode)
			info.PlanFile = s.runtime.planMode.PlanFilePath()
		}
	}
	return info
}

// contextPercent returns the context usage as 0-100, or -1 if unavailable.
func (s *ManagedSession) contextPercent() int {
	if s.runtime.agent == nil {
		return -1
	}
	model := s.runtime.agent.Model()
	if model.MaxInput <= 0 {
		return -1
	}
	msgs := s.runtime.agent.Messages()
	est := core.EstimateContextTokens(msgs, "", nil, s.runtime.agent.CompactionEpoch())
	pct := (est.Tokens * 100) / model.MaxInput
	if pct > 100 {
		pct = 100
	}
	return pct
}

// permissionMode returns the active permission mode string.
func (s *ManagedSession) permissionMode() string {
	if s.runtime.gate == nil {
		return string(permission.ModeYolo)
	}
	return string(s.runtime.gate.Mode())
}

// save persists the session to disk. No-op if persistence is unavailable
// or the session has been deleted.
func (s *ManagedSession) save() {
	s.mu.Lock()
	if s.persistence.deleted || s.persistence.persisted == nil || s.persistence.store == nil {
		s.mu.Unlock()
		return
	}
	s.persistence.persisted.Title = s.Title
	s.persistence.persisted.Messages = make([]core.AgentMessage, len(s.messages))
	copy(s.persistence.persisted.Messages, s.messages)
	s.persistence.persisted.CompactionEpoch = s.runtime.agent.CompactionEpoch()
	s.persistence.persisted.SetRuntimeMetadata(
		fullModelSpec(s.runtime.resolvedModel),
		s.CWD,
		s.permissionMode(),
		s.runtime.agent.ThinkingLevel(),
	)
	// Persist task store state.
	if s.runtime.taskStore != nil {
		if s.persistence.persisted.Metadata == nil {
			s.persistence.persisted.Metadata = make(map[string]any)
		}
		for k, v := range s.runtime.taskStore.SaveToMetadata() {
			s.persistence.persisted.Metadata[k] = v
		}
	}
	// Persist plan mode state.
	if s.runtime.planMode != nil {
		if s.persistence.persisted.Metadata == nil {
			s.persistence.persisted.Metadata = make(map[string]any)
		}
		for k, v := range s.runtime.planMode.SaveState() {
			s.persistence.persisted.Metadata[k] = v
		}
	}
	snapshot := *s.persistence.persisted
	store := s.persistence.store
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
		ag := sess.runtime.agent
		sess.mu.Unlock()
		// Steer the running agent — injected between tool calls.
		// Don't broadcast here: the WS "steer" event is emitted later when the
		// agent actually processes it (AgentEventSteer in broadcastAgentEvent).
		ag.Steer(text)
		return "steer", nil
	}
	sess.State = StateRunning
	sess.Updated = time.Now()

	runCtx, cancel := context.WithCancel(sess.runtime.sessionCtx)
	sess.runCancel = cancel

	if sess.Title == "" {
		title := text
		if len(title) > maxTitleLength {
			title = title[:maxTitleLength] + "…"
		}
		sess.Title = title
	}
	sess.mu.Unlock()

	sess.broadcast(Event{Type: "state_change", Data: StateChangeData{
		State: string(StateRunning),
	}})

	go func() {
		defer cancel()

		// Open checkpoint for undo tracking.
		if sess.runtime.checkpoints != nil {
			label := text
			if len(label) > 60 {
				label = label[:60] + "…"
			}
			sess.runtime.checkpoints.Begin(label)
		}

		msgs, err := sess.runtime.agent.Send(runCtx, text)

		// Close checkpoint: Commit on success/error, Discard on cancel.
		if sess.runtime.checkpoints != nil {
			if err != nil && runCtx.Err() != nil {
				sess.runtime.checkpoints.Discard()
			} else {
				sess.runtime.checkpoints.Commit()
			}
		}

		sess.mu.Lock()
		sess.messages = msgs
		sess.runCancel = nil
		// Clear stale approval state (run ended while waiting).
		sess.approvals.pending = nil
		sess.approvals.pendingAsk = nil
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

		sess.broadcast(Event{Type: "state_change", Data: StateChangeData{
			State: string(newState),
			Error: errText,
		}})
		sess.broadcastContextUpdate()

		if finalText := extractFinalText(msgs); finalText != "" {
			sess.broadcast(Event{Type: "run_end", Data: RunEndData{Text: finalText}})
		}
	}()
	return "send", nil
}

// startNotificationRun triggers an agent run from a subagent completion notification.
// Must be called with sess.mu NOT held. The session must be in StateIdle.
func (sess *ManagedSession) startNotificationRun(text string) {
	sess.mu.Lock()
	if sess.State != StateIdle {
		sess.mu.Unlock()
		// Agent is now running (race with user Send) — enqueue instead.
		if a := sess.runtime.agent; a != nil {
			a.Enqueue(text)
		}
		return
	}
	sess.State = StateRunning
	sess.Updated = time.Now()

	runCtx, cancel := context.WithCancel(sess.runtime.sessionCtx)
	sess.runCancel = cancel
	sess.mu.Unlock()

	sess.broadcast(Event{Type: "state_change", Data: StateChangeData{
		State: string(StateRunning),
	}})

	go func() {
		defer cancel()

		if sess.runtime.checkpoints != nil {
			sess.runtime.checkpoints.Begin("subagent notification")
		}

		msgs, err := sess.runtime.agent.Send(runCtx, text)

		if sess.runtime.checkpoints != nil {
			if err != nil && runCtx.Err() != nil {
				sess.runtime.checkpoints.Discard()
			} else {
				sess.runtime.checkpoints.Commit()
			}
		}

		sess.mu.Lock()
		sess.messages = msgs
		sess.runCancel = nil
		sess.approvals.pending = nil
		sess.approvals.pendingAsk = nil
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

		sess.broadcast(Event{Type: "state_change", Data: StateChangeData{
			State: string(newState),
			Error: errText,
		}})
		sess.broadcastContextUpdate()

		if finalText := extractFinalText(msgs); finalText != "" {
			sess.broadcast(Event{Type: "run_end", Data: RunEndData{Text: finalText}})
		}
	}()
}

// broadcastContextUpdate sends the current context usage percentage to WS clients.
func (s *ManagedSession) broadcastContextUpdate() {
	pct := s.contextPercent()
	if pct < 0 {
		return
	}
	s.broadcast(Event{Type: "context_update", Data: ContextUpdateData{ContextPercent: pct}})
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
// CommandResult is the response from executing a slash command.
type CommandResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}
