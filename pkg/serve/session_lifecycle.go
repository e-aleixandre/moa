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
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tool"
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

	// Validate the model spec up front for explicit, user-driven creation.
	// An unknown bare name, or a "provider/model" spec whose model portion
	// matches a *known* model registered under a different provider (e.g.
	// "openai/sonnet"), is rejected here instead of silently falling back to
	// the default model or surfacing an opaque provider-factory error later.
	// A "provider/model" spec that simply isn't in the registry is still
	// accepted as a genuine custom model (reduced context/pricing metadata,
	// but usable).
	if opts.Model != "" {
		if err := core.ValidateModelSpec(opts.Model); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidModel, err)
		}
	}

	// A title chosen explicitly at creation is treated as manual, so auto-titling
	// won't overwrite it. (The web never sets one — titles come from the first
	// message — so this only affects programmatic callers.)
	titleSource := ""
	if opts.Title != "" {
		titleSource = session.TitleSourceManual
	}

	// Resolve ID + persistence first.
	store, err := session.NewFileStore(m.sessionBaseDir, cwd)
	if err != nil {
		return nil, fmt.Errorf("create session store: %w", err)
	}
	persisted := store.Create()
	persisted.Title = opts.Title
	persisted.TitleSource = titleSource
	id := persisted.ID

	var bopts *buildOpts
	if titleSource != "" {
		bopts = &buildOpts{titleSource: titleSource}
	}
	sess, err := m.buildManagedSession(id, opts.Title, opts.Model, cwd, bopts)
	if err != nil {
		return nil, err
	}

	// Persist before exposing the session. A successful create must not turn
	// into an invisible ephemeral conversation on the next restart.
	model, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
	thinking, _ := bus.QueryTyped[bus.GetThinkingLevel, string](sess.runtime.Bus, bus.GetThinkingLevel{})
	permMode, _ := bus.QueryTyped[bus.GetPermissionMode, string](sess.runtime.Bus, bus.GetPermissionMode{})
	persisted.SetRuntimeMetadata(bootstrap.FullModelSpec(model), sess.CWD, permMode, thinking)
	if err := store.Save(persisted); err != nil {
		if sess.infra.mcpMgr != nil {
			sess.infra.mcpMgr.Close()
		}
		sess.infra.sessionCancel()
		sess.runtime.Close()
		return nil, fmt.Errorf("create session persistence: %w", err)
	}

	sp := newServePersister(persisted, store, func() string {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.Title
	})
	sess.persister = sp
	sess.runtime.AttachPersister(sp)

	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
	m.invalidateSavedCache()
	return sess, nil
}

// buildOpts provides optional initial state for session construction.
type buildOpts struct {
	initialMessages        []core.AgentMessage
	initialCompactionEpoch int
	initialThinking        string // applied via SetThinking after construction
	titleSource            string // how the resumed title was set (session.TitleSource*)

	// V2 session tree
	initialEntries []session.Entry
	initialLeafID  string
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
	moaCfg := m.loadConfig(cwd)

	cpStore := checkpoint.New(20)
	subagentTexts := &sync.Map{}

	// Forward-declare for closures.
	var sess *ManagedSession

	bs, err := bootstrap.BuildSession(bootstrap.SessionConfig{
		CWD:             cwd,
		Model:           model,
		Provider:        prov,
		ProviderFactory: m.providerFactory,
		MoaCfg:          &moaCfg,
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
		OnSubagentStart: func(jobID, task, model string, async bool) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.SubagentStarted{
					SessionID: s.ID, JobID: jobID, Task: task, Model: model, Async: async,
				})
			}
		},
		OnSubagentEvent: func(jobID string, inner any) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.SubagentEvent{
					SessionID: s.ID, JobID: jobID, Inner: inner,
				})
			}
		},
		OnSubagentEnd: func(jobID, status string, usage *core.Usage, costUSD float64) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.SubagentEnded{
					SessionID: s.ID, JobID: jobID, Status: status, Usage: usage, CostUSD: costUSD,
				})
				s.persistSubagentTranscript(jobID, status, usage, costUSD)
			}
		},
		OnBashJobStart: func(job tool.BashJobInfo) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.BashJobStarted{SessionID: s.ID, JobID: job.JobID, Command: job.Command, CWD: job.CWD})
			}
		},
		OnBashJobOutput: func(jobID, delta string) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.BashJobOutput{SessionID: s.ID, JobID: jobID, Delta: delta})
			}
		},
		OnBashJobEnd: func(job tool.BashJobInfo) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.BashJobEnded{SessionID: s.ID, JobID: job.JobID, Status: job.Status, Output: job.Output})
			}
		},
		SubagentTranscriptLoader: func(jobID string) ([]core.AgentMessage, error) {
			s := sess
			if s == nil || s.persister == nil {
				return nil, fmt.Errorf("transcript store unavailable")
			}
			store := s.persister.subagentStore(s.ID)
			if store == nil {
				return nil, fmt.Errorf("transcript store unavailable")
			}
			t, err := store.Load(jobID)
			if err != nil {
				return nil, err
			}
			return t.Messages, nil
		},
	})
	if err != nil {
		sessionCancel()
		return nil, err
	}

	shared := newSharedFiles()
	core.RegisterOrLog(bs.ToolReg, newSendFileTool(tool.ToolConfig{WorkspaceRoot: bs.CWD, PathPolicy: bs.PathPolicy}, id, shared))

	// Build RuntimeConfig from bootstrap session + serve-specific fields.
	rcfg := bs.RuntimeConfig()
	rcfg.SessionID = id
	rcfg.Ctx = sessionCtx
	rcfg.Checkpoints = cpStore
	rcfg.ProviderFactory = m.providerFactory
	rcfg.BaseSystemPrompt = "" // serve: plan mode prompts don't include base (pre-existing behavior)
	rcfg.SteerFilter = func(text string) bool {
		_, was := subagentTexts.LoadAndDelete(text)
		return !was
	}
	if opts != nil {
		if len(opts.initialEntries) > 0 {
			rcfg.InitialEntries = opts.initialEntries
			rcfg.InitialLeafID = opts.initialLeafID
		} else {
			rcfg.InitialMessages = opts.initialMessages
			rcfg.InitialCompactionEpoch = opts.initialCompactionEpoch
		}
	}

	rt, err := bus.NewSessionRuntime(rcfg)
	if err != nil {
		sessionCancel()
		if bs.MCPManager != nil {
			bs.MCPManager.Close()
		}
		return nil, err
	}

	// GetSubagents answers the WS init snapshot query (reconnect): live
	// (running/cancelling) subagent jobs plus their transcript so far. bus
	// itself doesn't know about pkg/subagent, so this handler is registered
	// here, from the frontend that owns the *subagent.Jobs handle.
	rt.Bus.OnQuery(func(q bus.GetSubagents) ([]bus.SubagentSnapshot, error) {
		if bs.Subagents == nil {
			return nil, nil
		}
		var out []bus.SubagentSnapshot
		for _, info := range bs.Subagents.Snapshot() {
			if info.Status != "running" && info.Status != "cancelling" {
				continue
			}
			out = append(out, bus.SubagentSnapshot{
				JobID:    info.JobID,
				Task:     info.Task,
				Model:    info.Model,
				Status:   info.Status,
				Async:    info.Async,
				Messages: bs.Subagents.Messages(info.JobID),
			})
		}
		return out, nil
	})
	rt.Bus.OnQuery(func(q bus.GetBashJobs) ([]bus.BashJobSnapshot, error) {
		if bs.BashJobs == nil {
			return nil, nil
		}
		infos := bs.BashJobs.Snapshot()
		out := make([]bus.BashJobSnapshot, 0, len(infos))
		for _, info := range infos {
			out = append(out, bus.BashJobSnapshot{JobID: info.JobID, Command: info.Command, CWD: info.CWD, Status: info.Status, Output: info.Output})
		}
		return out, nil
	})

	// Apply thinking level if restoring.
	if opts != nil && opts.initialThinking != "" {
		_ = rt.Bus.Execute(bus.SetThinking{Level: opts.initialThinking})
	}

	// PlanMode onChange is owned by the runtime (NewSessionRuntime sets it).
	// No need to override here — it publishes PlanModeChanged and rebuilds
	// the system prompt automatically.

	sess = &ManagedSession{
		ID:         id,
		Title:      title,
		CWD:        cwd,
		Created:    time.Now(),
		Updated:    time.Now(),
		cacheTTL:   core.CacheTTLDuration(moaCfg),
		runtime:    rt,
		subagents:  bs.Subagents,
		bashJobs:   bs.BashJobs,
		pathPolicy: bs.PathPolicy,
		infra: serveInfra{
			sessionCtx:    sessionCtx,
			sessionCancel: sessionCancel,
			toolReg:       bs.ToolReg,
			mcpMgr:        bs.MCPManager,
			UntrustedMCP:  bs.UntrustedMCP,
		},
		sharedFiles: shared,
	}
	if opts != nil {
		sess.TitleSource = opts.titleSource
		// A resumed session with prior history has already lived past its first
		// run, so don't re-generate its title.
		if len(opts.initialMessages) > 0 || len(opts.initialEntries) > 0 {
			sess.autoTitled.Store(true)
		}
	}

	// Wire Web Push and auto-titling before the session can run (no-ops if the
	// respective feature is unavailable).
	m.subscribePush(sess)
	m.subscribeAutoTitle(sess)
	m.subscribeCacheClock(sess)
	m.subscribeAttention(sess)

	return sess, nil
}

var (
	ErrNotFound     = errors.New("session not found")
	ErrBusy         = errors.New("session is busy")
	ErrInvalidCWD   = errors.New("invalid working directory")
	ErrInvalidModel = errors.New("invalid model")
)

// Delete aborts any running agent, closes resources, and removes the session.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	if _, resuming := m.resuming[id]; resuming {
		m.mu.Unlock()
		return ErrBusy
	}
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
		_ = removeSessionAttachDir(id)
		return nil
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	// Mark deleted to prevent persistence from resurrecting.
	if sess.persister != nil {
		sess.persister.markDeleted()
	}

	// Stop Web Push subscribers BEFORE closing the runtime, so events drained
	// during bus shutdown cannot notify for a session that no longer exists.
	sess.deleted.Store(true)
	for _, unsub := range sess.pushUnsubs {
		unsub()
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
			// Remove the side directory of persisted subagent transcripts.
			_ = session.NewSubagentStore(store.Dir(), id).Remove()
		}
	}
	m.invalidateSavedCache()
	_ = removeSessionAttachDir(id)
	return nil
}

// reapStaleAttachments removes session attachment directories older than 24h.
// Best-effort: only directories whose name matches sessionIDPattern are
// touched; the base dir itself and unrelated entries are left alone. If the
// base dir is a symlink it is refused (never followed) to avoid deleting
// through it.
func reapStaleAttachments() {
	base := attachmentsBaseDir()
	if info, err := os.Lstat(base); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return // missing, unreadable, or a symlink we won't follow
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	const maxAge = 24 * time.Hour
	for _, entry := range entries {
		// entry.IsDir()/Info() use the entry's own type (a symlink reports
		// IsDir()==false), so links are skipped here; the name must also match
		// the strict session-id pattern.
		if !entry.IsDir() || !sessionIDPattern.MatchString(entry.Name()) {
			continue
		}
		info, ierr := entry.Info()
		if ierr != nil {
			continue
		}
		if time.Since(info.ModTime()) > maxAge {
			// Route through the symlink-safe remover (validates id + refuses to
			// follow a symlinked base/session dir) instead of a raw RemoveAll.
			_ = removeSessionAttachDir(entry.Name())
		}
	}
}

// ResumeSession loads a saved session from disk and creates a full runtime.
func (m *Manager) ResumeSession(id string) (*ManagedSession, error) {
	// Reserve the ID without exposing a nil placeholder to readers.
	m.mu.Lock()
	if _, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	if _, ok := m.resuming[id]; ok {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	m.resuming[id] = struct{}{}
	m.mu.Unlock()

	cleanup := func() {
		m.mu.Lock()
		delete(m.resuming, id)
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
		initialEntries:         saved.Entries,
		initialLeafID:          saved.LeafID,
		titleSource:            saved.TitleSource,
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
		sess.runtime.SyncPlanMode()
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
	// Resuming an archived session implicitly unarchives it (see design
	// decision: archiving is presentation-only, and reopening a session is
	// explicit user intent to work on it again). The in-memory session never
	// carries Archived=true; persist the unarchive on disk if needed.
	sess.Archived = false
	if saved.Archived {
		if err := store.SetArchived(id, false); err != nil {
			slog.Warn("resume: failed to unarchive session", "id", id, "error", err)
		}
		m.invalidateSavedCache()
	}
	m.mu.Lock()
	delete(m.resuming, id)
	m.sessions[id] = sess
	m.mu.Unlock()
	return sess, nil
}

// shutdownDrainBudget bounds how long Shutdown waits, in total across all
// sessions, for active runs to observe the cancelled root context and settle
// before flushing. Best-effort: if it expires we flush anyway.
const shutdownDrainBudget = 5 * time.Second

// Shutdown synchronously flushes every active session to disk. Call it after the
// HTTP server has stopped accepting requests and before the process exits, so a
// turn that finished just before shutdown is persisted even though the async
// RunEnded→TreeSynced→save chain may not have drained.
//
// A SIGTERM cancels the root context, which cancels each in-flight run. Before
// flushing we wait — bounded by shutdownDrainBudget across the whole process —
// for active sessions to leave the running/permission state, so a snapshot
// captures the complete final turn rather than a partial one. If the budget
// expires we flush regardless (best effort beats losing the turn entirely).
func (m *Manager) Shutdown() {
	if m.scheduler != nil {
		m.scheduler.Close()
	}
	m.mu.RLock()
	sessions := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s != nil { // skip nil placeholders held during ResumeSession
			sessions = append(sessions, s)
		}
	}
	m.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), shutdownDrainBudget)
	defer cancel()
	for _, s := range sessions {
		if !s.runtime.WaitSettled(ctx) {
			slog.Warn("shutdown drain budget expired; flushing active session", "session", s.ID)
		}
	}

	for _, s := range sessions {
		if err := s.runtime.Flush(); err != nil {
			slog.Warn("shutdown flush failed", "session", s.ID, "error", err)
		}
		s.flushLiveSubagentTranscripts()
		// Close the runtime after flushing: this drains the bus's async
		// persistence reactor (Bus.Close waits for subscriber goroutines to
		// finish their queued events) so no delayed save can still be writing
		// to the session dir after Shutdown returns. Without this an async
		// RunEnded→save could race a caller that removes the session dir right
		// after Shutdown (e.g. t.TempDir cleanup in tests). Idempotent.
		s.runtime.Close()
	}
	if m.attention != nil {
		m.attention.Close()
	}
}

// flushLiveSubagentTranscripts persists the transcript of every still-live
// subagent job, so an async agent that was mid-run at shutdown isn't lost.
// Their messages are already accumulated incrementally (see setMessages on
// message_end), so this captures the best-available snapshot.
func (s *ManagedSession) flushLiveSubagentTranscripts() {
	if s.subagents == nil {
		return
	}
	for _, info := range s.subagents.Snapshot() {
		if info.Status != "running" && info.Status != "cancelling" {
			continue // finished ones were already persisted on OnSubagentEnd
		}
		s.persistSubagentTranscript(info.JobID, info.Status, nil, 0)
	}
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
		newMgr = mcp.NewManager(nil, s.CWD)
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
		if strings.HasPrefix(spec.Name, mcp.ToolPrefix) {
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
