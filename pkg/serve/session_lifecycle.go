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
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/bootstrap"
	"github.com/ealeixandre/moa/pkg/checkpoint"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
)

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
		persisted.SetRuntimeMetadata(
			fullModelSpec(sess.runtime.resolvedModel),
			sess.CWD,
			sess.permissionMode(),
			sess.runtime.agent.ThinkingLevel(),
		)
		_ = store.Save(persisted)
		sess.persistence.persisted = persisted
		sess.persistence.store = store
	}

	m.invalidateSavedCache()
	return sess, nil
}

// buildManagedSession creates an in-memory managed session with full runtime
// (tools, MCP, permissions, subagents, agent). Does NOT touch persistence.
// Used by both CreateSession (new sessions) and ResumeSession (restoring saved).
func (m *Manager) buildManagedSession(id, title, modelSpec, cwd string) (*ManagedSession, error) {
	// Resolve + canonicalize CWD.
	canonical, err := core.CanonicalizePath(cwd)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCWD, err)
	}
	if info, statErr := os.Stat(canonical); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrInvalidCWD, canonical)
	}
	cwd = canonical

	// Resolve model.
	model := m.defaultModel
	if modelSpec != "" {
		model, _ = core.ResolveModel(modelSpec)
	}

	// Create provider.
	prov, err := m.providerFactory(model)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}

	sessionCtx, sessionCancel := context.WithCancel(m.baseCtx)

	// File checkpoints for /undo.
	cpStore := checkpoint.New(20)

	// Forward-declare for closures (populated before the session is exposed).
	var sess *ManagedSession
	var bs *bootstrap.Session

	// Bootstrap: single function wires up tools, MCP, permissions, subagents,
	// plan mode, skills, verify, and agent.
	bs, err = bootstrap.BuildSession(bootstrap.SessionConfig{
		CWD:             cwd,
		Model:           model,
		Provider:        prov,
		ProviderFactory: m.providerFactory,
		Ctx:             sessionCtx,
		EnableAskUser:   true,
		BeforeWrite:     cpStore.Capture,
		OnAsyncJobChange: func(count int) {
			if s := sess; s != nil {
				s.broadcast(Event{Type: "subagent_count", Data: SubagentCountData{Count: count}})
			}
		},
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			s := sess
			if s == nil {
				return
			}
			agentText := bootstrap.FormatSubagentNotification(jobID, task, status, resultTail, truncated)
			if agentText == "" {
				return
			}
			if a := bs.Agent; a != nil {
				// Track this text so broadcastAgentEvent suppresses the
				// duplicate steer WS event when the agent processes it.
				s.runtime.subagentTexts.Store(agentText, struct{}{})
				if a.IsRunning() {
					a.Steer(agentText)
				} else {
					// Agent is idle — start a new run so the LLM can
					// react to the subagent completion (same as TUI's
					// startNotificationRun).
					s.startNotificationRun(agentText, map[string]any{
						"source":           "subagent",
						"subagent_job_id":  jobID,
						"subagent_task":    task,
						"subagent_status":  status,
						"subagent_result":  resultTail,
					})
				}
			}
			s.broadcast(Event{Type: "subagent_complete", Data: SubagentCompleteData{
				JobID:  jobID,
				Task:   task,
				Status: status,
				Text:   agentText,
			}})
		},
	})
	if err != nil {
		sessionCancel()
		return nil, err
	}

	ag := bs.Agent
	pm := bs.PlanMode

	// Compose permission check: plan mode filter + permission gate.
	// Reads sess.runtime.gate under lock so SetPermissionMode changes take effect immediately.
	if err := ag.SetPermissionCheck(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
		if allowed, reason := pm.FilterToolCall(name, args); !allowed {
			return &core.ToolCallDecision{Block: true, Reason: reason, Kind: core.ToolCallDecisionKindPolicy}
		}
		sess.mu.Lock()
		g := sess.runtime.gate
		sess.mu.Unlock()
		if g != nil {
			return g.Check(ctx, name, args)
		}
		return nil
	}); err != nil {
		sessionCancel()
		if bs.MCPManager != nil {
			bs.MCPManager.Close()
		}
		return nil, err
	}

	// Build managed session.
	sess = &ManagedSession{
		ID:      id,
		Title:   title,
		State:   StateIdle,
		Model:   modelDisplayName(model),
		CWD:     cwd,
		Created: time.Now(),
		Updated: time.Now(),
		runtime: sessionRuntime{
			agent:         ag,
			gate:          bs.Gate,
			unsub:         func() {}, // set below
			sessionCtx:    sessionCtx,
			sessionCancel: sessionCancel,
			toolReg:       bs.ToolReg,
			agentsMD:      bs.AgentsMD,
			resolvedModel: model,
			mcpMgr:        bs.MCPManager,
			UntrustedMCP:  bs.UntrustedMCP,
			taskStore:     bs.TaskStore,
			planMode:      pm,
			checkpoints:   cpStore,
		},
		approvals: sessionApprovals{
			askBridge: bs.AskBridge,
		},
	}

	pm.SetOnChange(func(mode planmode.Mode) {
		d := PlanModeData{Mode: string(mode)}
		if mode != planmode.ModeOff {
			d.PlanFile = pm.PlanFilePath()
		}
		sess.broadcast(Event{Type: "plan_mode", Data: d})
	})

	sess.runtime.unsub = ag.Subscribe(func(e core.AgentEvent) {
		sess.broadcastAgentEvent(e)
	})

	if bs.Gate != nil {
		sess.approvals.bridgeStop = make(chan struct{})
		go sess.permissionBridge(sessionCtx)
	}
	if bs.AskBridge != nil {
		go sess.askUserBridge(sessionCtx)
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
	sess.persistence.deleted = true
	store := sess.persistence.store
	sess.persistence.persisted = nil
	sess.persistence.store = nil
	sess.mu.Unlock()

	// Close MCP connections before context cancellation.
	if sess.runtime.mcpMgr != nil {
		sess.runtime.mcpMgr.Close()
	}

	// Cancel session context — stops bridge, subagent jobs, and in-flight runs.
	sess.runtime.sessionCancel()

	sess.runtime.unsub()
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

	modelID, cwd, savedPermMode, savedThinking := saved.RuntimeMeta()
	if cwd == "" {
		cwd = m.workspaceRoot
	}

	sess, err := m.buildManagedSession(saved.ID, saved.Title, modelID, cwd)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("resume: %w", err)
	}

	// Restore thinking level from persisted metadata.
	if savedThinking != "" {
		_ = sess.runtime.agent.Reconfigure(nil, sess.runtime.agent.Model(), savedThinking)
	}

	// Restore permission mode from persisted metadata.
	if savedPermMode != "" {
		// Use SetPermissionMode which handles gate creation and bridge wiring.
		if _, pmErr := m.SetPermissionMode(sess.ID, savedPermMode); pmErr != nil {
			slog.Warn("resume: could not restore permission mode", "id", saved.ID, "mode", savedPermMode, "error", pmErr)
		}
	}

	if err := sess.runtime.agent.LoadState(saved.Messages, saved.CompactionEpoch); err != nil {
		sess.runtime.sessionCancel()
		sess.runtime.unsub()
		if sess.runtime.mcpMgr != nil {
			sess.runtime.mcpMgr.Close()
		}
		m.mu.Lock()
		delete(m.sessions, sess.ID)
		m.mu.Unlock()
		return nil, fmt.Errorf("resume: load state: %w", err)
	}

	sess.mu.Lock()
	sess.messages = saved.Messages
	sess.persistence.persisted = saved
	sess.persistence.store = store
	sess.Created = saved.Created
	// Restore task store state.
	if sess.runtime.taskStore != nil && saved.Metadata != nil {
		sess.runtime.taskStore.RestoreFromMetadata(saved.Metadata)
	}
	// Restore plan mode state.
	if sess.runtime.planMode != nil && saved.Metadata != nil {
		sess.runtime.planMode.RestoreState(saved.Metadata)
		sess.runtime.planMode.ApplyRestoredState()
	}
	sess.mu.Unlock()

	return sess, nil
}

// ReconfigureSession changes the model and/or thinking level of a session.
// Only allowed when the session is idle (not running).

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
		newMgr.Start(s.runtime.sessionCtx, merged)
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

	oldMgr := s.runtime.mcpMgr

	// Deregister old MCP tools (prefixed "mcp__").
	for _, spec := range s.runtime.toolReg.Specs() {
		if strings.HasPrefix(spec.Name, "mcp__") {
			s.runtime.toolReg.Unregister(spec.Name)
		}
	}

	// Register new tools.
	for _, t := range newTools {
		core.RegisterOrLog(s.runtime.toolReg, t)
	}
	s.runtime.mcpMgr = newMgr
	s.runtime.UntrustedMCP = false
	s.mu.Unlock()

	// Phase 3: cleanup old manager (outside lock — Close may block).
	if oldMgr != nil {
		oldMgr.Close()
	}

	return nil
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) //nolint:errcheck // crypto/rand.Read never fails on supported platforms
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
