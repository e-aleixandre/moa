// Package serve provides an HTTP/WebSocket server for managing multiple
// agent sessions through a web dashboard.
package serve

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/files"
	"github.com/ealeixandre/moa/pkg/mcp"
	"github.com/ealeixandre/moa/pkg/push"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/subagent"
	"github.com/ealeixandre/moa/pkg/tool"
	"github.com/ealeixandre/moa/pkg/usage"
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

// ManagedSession wraps a bus.SessionRuntime with metadata for the web dashboard.
type ManagedSession struct {
	// Immutable after construction.
	ID      string    `json:"id"`
	CWD     string    `json:"cwd"`
	Created time.Time `json:"created"`

	// pathPolicy is the runtime-mutable path access policy shared with the
	// session's tools; attachments-to-disk add the session's attachment dir
	// to it so the agent can read/write files it was given.
	pathPolicy *tool.PathPolicy

	// attachMu serializes per-session attachment processing so the on-disk
	// quota check is atomic against concurrent /send requests.
	attachMu sync.Mutex

	// Mutable under mu.
	mu          sync.Mutex
	Title       string
	TitleSource string // "manual" | "auto" | "" (legacy=auto); see session.TitleSource
	Updated     time.Time

	// Bus runtime — owns all session state.
	runtime *bus.SessionRuntime

	// subagents is the handle onto the subagent job store (bootstrap.Session.Subagents),
	// used to answer the GetSubagents query and future cancellation from the UI.
	subagents *subagent.Jobs

	// Serve-specific persistence.
	persister *servePersister

	// Serve-specific infrastructure (MCP, toolReg — not agent).
	infra serveInfra

	// sharedFiles holds files the agent explicitly shared via send_file.
	// Allowlist for GET /api/sessions/{id}/files/{fileID}. In-memory only.
	sharedFiles *sharedFiles

	// Web Push: live count of WebSocket clients watching this session (gates
	// non-blocking notifications), a "deleted" guard against late pushes, and
	// the bus unsubscribe funcs registered by subscribePush.
	wsConns    atomic.Int32
	deleted    atomic.Bool
	autoTitled atomic.Bool // guards one-shot auto-title generation (see autotitle.go)
	pushUnsubs []func()

	// verifyRunning serializes the web /verify command: two concurrent POSTs
	// must not run verify.Execute at once and interleave AutoVerify events.
	verifyRunning atomic.Bool
}

// title returns the current session title under lock.
func (s *ManagedSession) title() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Title
}

// serveInfra holds serve-layer infrastructure that doesn't belong in the bus.
type serveInfra struct {
	sessionCtx    context.Context
	sessionCancel context.CancelFunc
	toolReg       *core.Registry
	mcpMgr        *mcp.Manager
	UntrustedMCP  bool
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

const (
	maxTitleLength = 80 // auto-generated session title cap
)

// History returns a copy of the session's conversation messages.
func (s *ManagedSession) History() []core.AgentMessage {
	msgs, _ := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](s.runtime.Bus, bus.GetMessages{})
	return msgs
}

// persistSubagentTranscript writes a finished subagent's transcript to the
// side store so it survives restarts and can be reopened. Best-effort: logged
// and dropped on error. Metadata (task/model/async/messages) comes from the
// live Jobs handle, which still holds the job at OnChildEnd time.
func (s *ManagedSession) persistSubagentTranscript(jobID, status string, usage *core.Usage, costUSD float64) {
	if s.persister == nil || s.subagents == nil {
		return
	}
	store := s.persister.subagentStore(s.ID)
	if store == nil {
		return
	}
	t := session.SubagentTranscript{
		JobID:      jobID,
		Status:     status,
		Usage:      usage,
		CostUSD:    costUSD,
		FinishedAt: time.Now(),
		Messages:   s.subagents.Messages(jobID),
	}
	for _, info := range s.subagents.Snapshot() {
		if info.JobID == jobID {
			t.Task = info.Task
			t.Model = info.Model
			t.Async = info.Async
			t.StartedAt = info.StartedAt
			break
		}
	}
	if err := store.Save(t); err != nil {
		slog.Warn("subagent transcript save failed", "session", s.ID, "job", jobID, "error", err)
	}
}

// info returns the session info via bus queries.
func (s *ManagedSession) info() SessionInfo {
	b := s.runtime.Bus
	model, _ := bus.QueryTyped[bus.GetModel, core.Model](b, bus.GetModel{})
	thinking, _ := bus.QueryTyped[bus.GetThinkingLevel, string](b, bus.GetThinkingLevel{})
	ctxPct, _ := bus.QueryTyped[bus.GetContextUsage, int](b, bus.GetContextUsage{})
	permMode, _ := bus.QueryTyped[bus.GetPermissionMode, string](b, bus.GetPermissionMode{})
	state, _ := bus.QueryTyped[bus.GetSessionState, string](b, bus.GetSessionState{})
	stateErr, _ := bus.QueryTyped[bus.GetSessionError, string](b, bus.GetSessionError{})
	planInfo, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})

	s.mu.Lock()
	info := SessionInfo{
		ID:             s.ID,
		Title:          s.Title,
		State:          SessionState(state),
		Model:          modelDisplayName(model),
		Thinking:       thinking,
		CWD:            s.CWD,
		Created:        s.Created,
		Updated:        s.Updated,
		Error:          stateErr,
		UntrustedMCP:   s.infra.UntrustedMCP,
		ContextPercent: ctxPct,
		PermissionMode: permMode,
	}
	s.mu.Unlock()
	if planInfo.Mode != "off" {
		info.PlanMode = planInfo.Mode
		info.PlanFile = planInfo.PlanFile
	}
	return info
}

// Manager owns all active sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*ManagedSession
	baseCtx  context.Context

	providerFactory func(model core.Model) (core.Provider, error)
	transcriber     core.Transcriber // nil when no speech-to-text is available
	usagePoller     *usage.Poller    // nil when plan usage tracking is unavailable
	pushStore       *push.Store      // nil when Web Push is unavailable
	pushDispatcher  *push.Dispatcher // nil when Web Push is unavailable
	defaultModel    core.Model
	workspaceRoot   string
	moaCfg          core.MoaConfig
	sessionBaseDir  string // root for session stores; empty = default (~/.config/moa/sessions/)

	// savedCache caches the result of session.ListAll to avoid
	// re-scanning disk on every poll (frontend polls every 3s).
	savedCacheMu  sync.Mutex
	savedCache    []session.Summary
	savedCacheAt  time.Time
	savedCacheTTL time.Duration // default 30s, configurable for tests

	// fileScanner is shared across /api/sessions/{id}/files requests.
	// Invalidated on successful edit tool completions.
	fileScanner *files.Scanner
}

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	ProviderFactory func(model core.Model) (core.Provider, error)
	Transcriber     core.Transcriber // optional; enables POST /api/transcribe
	UsagePoller     *usage.Poller    // optional; enables GET /api/usage
	PushStore       *push.Store      // optional; enables Web Push
	PushDispatcher  *push.Dispatcher // optional; enables Web Push
	DefaultModel    core.Model
	WorkspaceRoot   string
	MoaCfg          core.MoaConfig
	SessionBaseDir  string // root for session stores; empty = default
}

// NewManager creates a Manager. The context controls the lifetime of all agent
// runs — cancelling it aborts every active session.
func NewManager(ctx context.Context, cfg ManagerConfig) *Manager {
	m := &Manager{
		sessions:        make(map[string]*ManagedSession),
		baseCtx:         ctx,
		providerFactory: cfg.ProviderFactory,
		transcriber:     cfg.Transcriber,
		usagePoller:     cfg.UsagePoller,
		pushStore:       cfg.PushStore,
		pushDispatcher:  cfg.PushDispatcher,
		defaultModel:    cfg.DefaultModel,
		workspaceRoot:   cfg.WorkspaceRoot,
		moaCfg:          cfg.MoaCfg,
		sessionBaseDir:  cfg.SessionBaseDir,
		savedCacheTTL:   30 * time.Second,
		fileScanner:     files.NewScanner(),
	}
	reapStaleAttachments()
	return m
}

// Send delivers a user message (with optional attachments) to a session.
// If idle: starts a new agent run via bus.
// If running/permission: steers the running agent (attachments not allowed there).
// Returns the action taken: "send" or "steer".
func (m *Manager) Send(sessionID, text string, atts []Attachment) (string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return "", ErrNotFound
	}

	state := sess.runtime.State.Current()
	if state == bus.StateRunning || state == bus.StatePermission {
		if len(atts) > 0 {
			return "", ErrAttachmentsWhileRunning
		}
		// Steer the running agent.
		_ = sess.runtime.Bus.Execute(bus.SteerAgent{Text: text})
		return "steer", nil
	}

	// Set title on first message: from the text, or from the first
	// attachment's name if the text is empty.
	sess.mu.Lock()
	if sess.Title == "" {
		title := text
		if title == "" && len(atts) > 0 {
			title = atts[0].Name
		}
		if len(title) > maxTitleLength {
			title = title[:maxTitleLength] + "…"
		}
		sess.Title = title
	}
	sess.Updated = time.Now()
	sess.mu.Unlock()

	if len(atts) == 0 {
		if err := sess.runtime.Bus.Execute(bus.SendPrompt{Text: text}); err != nil {
			return "", err
		}
		return "send", nil
	}

	supportsDocs := false
	if model, qerr := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{}); qerr == nil {
		if prov, perr := m.providerFactory(model); perr == nil {
			supportsDocs = core.ProviderSupportsDocuments(prov)
		}
	}
	// Serialize attachment processing per session so the per-session on-disk
	// quota check (dirSize + running total) is atomic against concurrent
	// /send requests to the same idle session.
	sess.attachMu.Lock()
	content, err := buildAttachmentContent(atts, sessionID, sess.pathPolicy, supportsDocs)
	sess.attachMu.Unlock()
	if err != nil {
		return "", err
	}
	if text != "" {
		content = append(content, core.TextContent(text))
	}
	if err := sess.runtime.Bus.Execute(bus.SendPromptWithContent{Content: content}); err != nil {
		return "", err
	}
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
		if s == nil {
			continue // nil placeholder during ResumeSession
		}
		list = append(list, s.info())
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

// cachedSavedSessions returns saved sessions from disk, using a TTL cache.
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

// InvalidateFileCache invalidates the file scanner cache for a given CWD.
// Called after successful file edits to keep file suggestions fresh.
func (m *Manager) InvalidateFileCache(cwd string) {
	if m.fileScanner != nil && cwd != "" {
		m.fileScanner.Invalidate(cwd)
	}
}

// FileScanner returns the shared file scanner instance.
func (m *Manager) FileScanner() *files.Scanner {
	return m.fileScanner
}

// CommandResult is the response from executing a slash command.
type CommandResult struct {
	OK           bool   `json:"ok"`
	Message      string `json:"message"`
	NewSessionID string `json:"newSessionId,omitempty"`
}
