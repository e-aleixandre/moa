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
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/bootstrap"
	"github.com/ealeixandre/moa/pkg/bus"
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

// CreateSession creates a new agent session.
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
		id = newID()
	}

	sess, err := m.buildManagedSession(id, opts.Title, opts.Model, cwd, nil)
	if err != nil {
		return nil, err
	}

	// Attach persistence immediately.
	if persisted != nil && store != nil {
		// Get metadata from bus queries.
		model, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
		thinking, _ := bus.QueryTyped[bus.GetThinkingLevel, string](sess.runtime.Bus, bus.GetThinkingLevel{})
		permMode, _ := bus.QueryTyped[bus.GetPermissionMode, string](sess.runtime.Bus, bus.GetPermissionMode{})

		persisted.SetRuntimeMetadata(
			fullModelSpec(model),
			sess.CWD,
			permMode,
			thinking,
		)
		_ = store.Save(persisted)

		sp := newServePersister(persisted, store, func() string {
			sess.mu.Lock()
			defer sess.mu.Unlock()
			return sess.Title
		})
		sess.persister = sp
		sess.runtime.AttachPersister(sp)
	}

	m.invalidateSavedCache()
	return sess, nil
}

// buildOpts provides optional initial state for session construction.
type buildOpts struct {
	initialMessages        []core.AgentMessage
	initialCompactionEpoch int
	initialThinking        string // applied via SetThinking after construction
}

// buildManagedSession creates an in-memory managed session with full runtime.
// Does NOT touch persistence.
func (m *Manager) buildManagedSession(id, title, modelSpec, cwd string, opts *buildOpts) (*ManagedSession, error) {
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

	cpStore := checkpoint.New(20)
	subagentTexts := &sync.Map{}

	// Forward-declare for closures.
	var sess *ManagedSession

	bs, err := bootstrap.BuildSession(bootstrap.SessionConfig{
		CWD:             cwd,
		Model:           model,
		Provider:        prov,
		ProviderFactory: m.providerFactory,
		Ctx:             sessionCtx,
		EnableAskUser:   true,
		BeforeWrite:     cpStore.Capture,
		OnAsyncJobChange: func(count int) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.SubagentCountChanged{SessionID: s.ID, Count: count})
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
			b := s.runtime.Bus
			b.Publish(bus.SubagentCompleted{SessionID: s.ID, JobID: jobID, Task: task, Status: status, Text: agentText})

			if s.runtime.State.Current() == bus.StateRunning {
				subagentTexts.Store(agentText, struct{}{})
				_ = b.Execute(bus.SteerAgent{Text: agentText})
			} else {
				err := b.Execute(bus.SendPrompt{
					Text: agentText,
					Custom: map[string]any{
						"source":          "subagent",
						"subagent_job_id": jobID,
						"subagent_task":   task,
						"subagent_status": status,
						"subagent_result": resultTail,
					},
				})
				if err != nil {
					subagentTexts.Store(agentText, struct{}{})
					_ = b.Execute(bus.SteerAgent{Text: agentText})
				}
			}
		},
	})
	if err != nil {
		sessionCancel()
		return nil, err
	}

	// Build RuntimeConfig.
	rcfg := bus.RuntimeConfig{
		SessionID:        id,
		Ctx:              sessionCtx,
		Agent:            bs.Agent,
		TaskStore:        bs.TaskStore,
		Checkpoints:      cpStore,
		PlanMode:         bs.PlanMode,
		Gate:             bs.Gate,
		PathPolicy:       bs.PathPolicy,
		AskBridge:        bs.AskBridge,
		ProviderFactory:  m.providerFactory,
		BaseSystemPrompt: "",
		SteerFilter: func(text string) bool {
			_, was := subagentTexts.LoadAndDelete(text)
			return !was
		},
	}
	if opts != nil {
		rcfg.InitialMessages = opts.initialMessages
		rcfg.InitialCompactionEpoch = opts.initialCompactionEpoch
	}

	rt, err := bus.NewSessionRuntime(rcfg)
	if err != nil {
		sessionCancel()
		if bs.MCPManager != nil {
			bs.MCPManager.Close()
		}
		return nil, err
	}

	// Apply thinking level if restoring.
	if opts != nil && opts.initialThinking != "" {
		_ = rt.Bus.Execute(bus.SetThinking{Level: opts.initialThinking})
	}

	// PlanMode onChange → publish on bus.
	bs.PlanMode.SetOnChange(func(mode planmode.Mode) {
		d := bus.PlanModeChanged{SessionID: id, Mode: string(mode)}
		if mode != planmode.ModeOff {
			d.PlanFile = bs.PlanMode.PlanFilePath()
		}
		rt.Bus.Publish(d)
	})

	sess = &ManagedSession{
		ID:      id,
		Title:   title,
		CWD:     cwd,
		Created: time.Now(),
		Updated: time.Now(),
		runtime: rt,
		infra: serveInfra{
			sessionCtx:    sessionCtx,
			sessionCancel: sessionCancel,
			toolReg:       bs.ToolReg,
			mcpMgr:        bs.MCPManager,
			UntrustedMCP:  bs.UntrustedMCP,
		},
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

// Delete aborts any running agent, closes resources, and removes the session.
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

	// Mark deleted to prevent persistence from resurrecting.
	if sess.persister != nil {
		sess.persister.markDeleted()
	}

	// Close MCP connections before context cancellation.
	if sess.infra.mcpMgr != nil {
		sess.infra.mcpMgr.Close()
	}

	// Cancel session context — stops bridges, subagent jobs, and in-flight runs.
	sess.infra.sessionCancel()

	// Close runtime — stops bridges, aborts agent, closes bus.
	sess.runtime.Close()

	// Delete from disk.
	if sess.persister != nil {
		sess.persister.mu.Lock()
		store := sess.persister.store
		sess.persister.mu.Unlock()
		if store != nil {
			_ = store.Delete(id)
		}
	}
	m.invalidateSavedCache()
	return nil
}

// ResumeSession loads a saved session from disk and creates a full runtime.
func (m *Manager) ResumeSession(id string) (*ManagedSession, error) {
	// Reserve the slot to prevent concurrent resumes.
	m.mu.Lock()
	if _, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	m.sessions[id] = nil // nil placeholder
	m.mu.Unlock()

	cleanup := func() {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}

	// 1. Load from disk.
	saved, store, err := session.FindSession(m.sessionBaseDir, id)
	if err != nil {
		cleanup()
		return nil, err
	}

	modelID, cwd, savedPermMode, savedThinking := saved.RuntimeMeta()
	if cwd == "" {
		cwd = m.workspaceRoot
	}

	// 2. Build with initial state.
	sess, err := m.buildManagedSession(saved.ID, saved.Title, modelID, cwd, &buildOpts{
		initialMessages:        saved.Messages,
		initialCompactionEpoch: saved.CompactionEpoch,
		initialThinking:        savedThinking,
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("resume: %w", err)
	}

	// 3. Restore permission mode.
	if savedPermMode != "" {
		if err := sess.runtime.Bus.Execute(bus.SetPermissionMode{Mode: savedPermMode}); err != nil {
			slog.Warn("resume: permission mode", "id", id, "error", err)
		}
	}

	// 4. Restore task/plan/path metadata.
	// Tasks and plan mode use direct restore methods (initialization, not runtime commands).
	// Path policy uses bus commands for consistency.
	sctx := sess.runtime.Context()
	if sctx.TaskStore != nil && saved.Metadata != nil {
		sctx.TaskStore.RestoreFromMetadata(saved.Metadata)
	}
	if sctx.PlanMode != nil && saved.Metadata != nil {
		sctx.PlanMode.RestoreState(saved.Metadata)
		sctx.PlanMode.ApplyRestoredState()
	}
	if saved.Metadata != nil {
		savedScope, savedPaths := saved.PathMeta()
		if savedScope != "" {
			_ = sess.runtime.Bus.Execute(bus.SetPathScope{Scope: savedScope})
		}
		for _, p := range savedPaths {
			_ = sess.runtime.Bus.Execute(bus.AddAllowedPath{Path: p})
		}
	}

	// 5. Attach persistence.
	sp := newServePersister(saved, store, func() string {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.Title
	})
	sess.persister = sp
	sess.runtime.AttachPersister(sp)

	// 6. Finalize.
	sess.Created = saved.Created
	return sess, nil
}

// reloadMCP reloads MCP servers for a session.
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
		newMgr.Start(s.infra.sessionCtx, merged)
		newTools = newMgr.Tools()
	}

	if newMgr != nil && len(newTools) == 0 {
		newMgr.Close()
		return fmt.Errorf("MCP servers started but no tools available; keeping existing tools")
	}

	// Phase 2: check state via bus.
	state := s.runtime.State.Current()
	if state == bus.StateRunning || state == bus.StatePermission {
		if newMgr != nil {
			newMgr.Close()
		}
		return ErrBusy
	}

	// Phase 3: swap tools.
	s.mu.Lock()
	oldMgr := s.infra.mcpMgr

	// Deregister old MCP tools.
	for _, spec := range s.infra.toolReg.Specs() {
		if strings.HasPrefix(spec.Name, "mcp__") {
			s.infra.toolReg.Unregister(spec.Name)
		}
	}
	// Register new tools.
	for _, t := range newTools {
		core.RegisterOrLog(s.infra.toolReg, t)
	}
	s.infra.mcpMgr = newMgr
	s.infra.UntrustedMCP = false
	s.mu.Unlock()

	// Phase 4: cleanup old.
	if oldMgr != nil {
		oldMgr.Close()
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func modelDisplayName(m core.Model) string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

func fullModelSpec(m core.Model) string {
	if m.Provider != "" {
		return m.Provider + "/" + m.ID
	}
	return m.ID
}
