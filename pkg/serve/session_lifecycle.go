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

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/bootstrap"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/checkpoint"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/mcp"
	"github.com/e-aleixandre/moa/pkg/permission"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/subagent"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// CreateOpts configures a new session.
type CreateOpts struct {
	Model          string `json:"model"`
	Thinking       string `json:"thinking"`
	PermissionMode string `json:"permission_mode"`
	Title          string `json:"title"`
	CWD            string `json:"cwd"`
	// Origin records who created the session ("user" when empty). Free-form so
	// automation callers can label their integration, e.g. "linear-webhook".
	Origin string `json:"origin"`
	// extraMeta carries additional creation-time metadata (automation
	// bookkeeping such as the idempotency key and callback target). Not part of
	// the public JSON body — automation handlers set it.
	extraMeta map[string]any
	// extraMCPServers are session-scoped MCP servers to start alongside the
	// configured ones (the Automation API's per-run servers). Like extraMeta,
	// not part of the public JSON body: they are implicitly trusted, so only
	// internal callers that already carry operator authority may set them.
	extraMCPServers map[string]core.MCPServer
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
	if opts.Thinking != "" && !core.IsValidThinkingLevel(opts.Thinking) {
		return nil, fmt.Errorf("%w: %q (choose: %s)", ErrInvalidThinking, opts.Thinking, core.ThinkingLevelOptions())
	}
	if opts.PermissionMode != "" {
		switch permission.Mode(opts.PermissionMode) {
		case permission.ModeYolo, permission.ModeAsk, permission.ModeAuto:
		default:
			return nil, fmt.Errorf("%w: %q", ErrInvalidPermissionMode, opts.PermissionMode)
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
	persisted.SetOrigin(opts.Origin)
	for k, v := range opts.extraMeta {
		if persisted.Metadata == nil {
			persisted.Metadata = make(map[string]any)
		}
		persisted.Metadata[k] = v
	}
	id := persisted.ID

	var bopts *buildOpts
	if titleSource != "" || opts.Thinking != "" || opts.PermissionMode != "" || len(opts.extraMCPServers) > 0 {
		bopts = &buildOpts{titleSource: titleSource, initialThinking: opts.Thinking, initialPermissionMode: opts.PermissionMode, extraMCPServers: opts.extraMCPServers}
	}
	sess, err := m.buildManagedSession(id, opts.Title, opts.Model, cwd, bopts)
	if err != nil {
		return nil, err
	}
	sess.Origin = persisted.Origin()
	sess.automationCreated = automationCreatedMeta(opts.extraMeta)
	// Wire the outbound completion callback before the session is reachable, so
	// the very first run cannot end before the subscription exists. A no-op
	// unless the caller supplied a callback_url.
	m.subscribeAutomationCallback(sess, opts.extraMeta)

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
	m.initializeAttentionRuntimeLocked(sess)
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
	return sess, nil
}

// buildOpts provides optional initial state for session construction.
type buildOpts struct {
	initialMessages        []core.AgentMessage
	initialCompactionEpoch int
	initialThinking        string // applied via SetThinking after construction
	initialPermissionMode  string // applied via SetPermissionMode after construction
	titleSource            string // how the resumed title was set (session.TitleSource*)

	// V2 session tree
	initialEntries  []session.Entry
	initialLeafID   string
	initialMetadata map[string]any

	// extraMCPServers are session-scoped MCP servers merged on top of the
	// configured ones (see CreateOpts.extraMCPServers). On resume they are
	// rebuilt from the persisted metadata.
	extraMCPServers map[string]core.MCPServer
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
	autoTitleModel, autoTitleEnabled := m.resolveAuxiliaryModel(moaCfg.AutoTitleModel, id, "auto title")
	briefModel, briefEnabled := m.resolveAuxiliaryModel(moaCfg.SessionBriefModel, id, "session brief")
	var mcpSources *core.MCPDisableSources
	if m.mcpSourcesLoader != nil {
		mcpSources = m.mcpSourcesLoader(cwd)
	}

	cpStore := checkpoint.New(20)
	subagentTexts := &sync.Map{}
	// The scope bundles BOTH halves of the attachment capability (producing
	// references and resolving them) into one value, so bootstrap cannot wire
	// the materializer and forget the producer, or vice versa.
	//
	// An ID the store refuses to own is not fatal: attachments could not have
	// been stored under it before either, so the session loads without the
	// capability (inline, as before) instead of becoming unopenable.
	var attachScope *attachment.Scope
	if m.attachStore != nil {
		var err error
		attachScope, err = attachment.NewScope(m.attachStore, id)
		if err != nil {
			slog.Warn("attachment scope unavailable for session", "session", id, "error", err)
		}
	}

	// Forward-declare for closures.
	var sess *ManagedSession

	var extraMCPServers map[string]core.MCPServer
	if opts != nil {
		extraMCPServers = opts.extraMCPServers
	}

	bs, err := bootstrap.BuildSession(bootstrap.SessionConfig{
		CWD:               cwd,
		Model:             model,
		Provider:          prov,
		ProviderFactory:   m.providerFactory,
		MoaCfg:            &moaCfg,
		MCPDisableSources: mcpSources,
		ExtraMCPServers:   extraMCPServers,
		Ctx:               sessionCtx,
		EnableAskUser:     true,
		BeforeWrite:       cpStore.Capture,
		AttachmentScope:   attachScope,
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

			if s.runtime.State.Current() == bus.StateRunning {
				subagentTexts.Store(agentText, struct{}{})
				_ = b.Execute(bus.SteerAgent{ID: core.NewSteerID(), Text: agentText, Internal: true})
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
					_ = b.Execute(bus.SteerAgent{ID: core.NewSteerID(), Text: agentText, Internal: true})
				}
			}
		},
		OnSubagentStart: func(jobID, task, model, thinking, originToolCallID string, async bool, startedAt time.Time, accentIndex int) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.SubagentStarted{
					SessionID: s.ID, JobID: jobID, OriginToolCallID: originToolCallID, Task: task, Model: model, Thinking: thinking, Async: async, StartedAt: startedAt, AccentIndex: accentIndex,
				})
			}
		},
		SubagentTitleModel:   autoTitleModel,
		SubagentTitleEnabled: autoTitleEnabled,
		OnSubagentTitle: func(jobID, title string) {
			if s := sess; s != nil {
				s.persistSubagentTranscript(jobID, "running", "", "", time.Time{}, nil, 0)
				s.runtime.Bus.Publish(bus.SubagentTitleChanged{SessionID: s.ID, JobID: jobID, Title: title})
			}
		},
		OnSubagentEvent: func(jobID string, inner any) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.SubagentEvent{
					SessionID: s.ID, JobID: jobID, Inner: inner,
				})
			}
		},
		OnSubagentUsage: func(jobID string, usage *core.Usage, costUSD float64, contextPct int) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.SubagentUsage{
					SessionID: s.ID, JobID: jobID, Usage: usage, CostUSD: costUSD, ContextPercent: contextPct,
				})
			}
		},
		OnSubagentEnd: func(jobID, task string, async bool, status, result, resultErr string, finishedAt time.Time, usage *core.Usage, costUSD float64) {
			if s := sess; s != nil {
				// Persist first: a client that reconnects immediately after the
				// terminal WS event must be able to restore this same outcome.
				s.persistSubagentTranscript(jobID, status, result, resultErr, finishedAt, usage, costUSD)
				s.runtime.Bus.Publish(bus.SubagentEnded{
					SessionID: s.ID, JobID: jobID, Task: task, Async: async, Status: status,
					Result: result, Error: resultErr, FinishedAt: finishedAt, Usage: usage, CostUSD: costUSD,
				})
			}
		},
		OnBashJobStart: func(job tool.BashJobInfo) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.BashJobStarted{SessionID: s.ID, JobID: job.JobID, OwnerAgentID: job.OwnerAgentID, Command: job.Command, CWD: job.CWD})
			}
		},
		OnBashJobOutput: func(job tool.BashJobInfo, delta string) {
			if s := sess; s != nil {
				s.runtime.Bus.Publish(bus.BashJobOutput{SessionID: s.ID, JobID: job.JobID, OwnerAgentID: job.OwnerAgentID, Delta: delta})
			}
		},
		OnBashJobEnd: func(job tool.BashJobInfo) {
			s := sess
			if s == nil {
				return
			}
			b := s.runtime.Bus
			// The tray must see completion before its follow-up. Keep this bash
			// active for quiescence until the deferred settled event, after the
			// reinjection has been scheduled.
			b.Publish(bus.BashJobEnded{SessionID: s.ID, JobID: job.JobID, OwnerAgentID: job.OwnerAgentID, Status: job.Status, Output: job.Output})
			defer b.Publish(bus.BashJobSettled{SessionID: s.ID, JobID: job.JobID})
			// A bash_wait already consumed this job's result — don't deliver
			// it twice (single delivery lane).
			if job.Awaited {
				return
			}
			agentText := bootstrap.FormatBashNotification(job.JobID, job.Command, job.Status, job.Output)
			if agentText == "" {
				return
			}
			b.Publish(bus.BashCompleted{SessionID: s.ID, JobID: job.JobID, OwnerAgentID: job.OwnerAgentID, Command: job.Command, Status: job.Status, Text: agentText})
			// An owned job belongs to the child transcript. Its completion is
			// still published for UI routing, but never reinjected into the
			// parent agent/session as a root notification.
			if job.OwnerAgentID != "" {
				return
			}

			if s.runtime.State.Current() == bus.StateRunning {
				subagentTexts.Store(agentText, struct{}{})
				_ = b.Execute(bus.SteerAgent{ID: core.NewSteerID(), Text: agentText, Internal: true})
			} else {
				err := b.Execute(bus.SendPrompt{
					Text: agentText,
					Custom: map[string]any{
						"source":       "bash_job",
						"bash_job_id":  job.JobID,
						"bash_command": job.Command,
						"bash_status":  job.Status,
					},
				})
				if err != nil {
					subagentTexts.Store(agentText, struct{}{})
					_ = b.Execute(bus.SteerAgent{ID: core.NewSteerID(), Text: agentText, Internal: true})
				}
			}
		},
		SubagentTranscriptLoader: func(jobID string) (subagent.ResumedTranscript, error) {
			s := sess
			if s == nil || s.persister == nil {
				return subagent.ResumedTranscript{}, fmt.Errorf("transcript store unavailable")
			}
			store := s.persister.subagentStore(s.ID)
			if store == nil {
				return subagent.ResumedTranscript{}, fmt.Errorf("transcript store unavailable")
			}
			t, err := store.Load(jobID)
			if err != nil {
				return subagent.ResumedTranscript{}, err
			}
			return subagent.ResumedTranscript{Messages: t.Messages, Model: t.Model, Thinking: t.Thinking}, nil
		},
	})
	if err != nil {
		sessionCancel()
		return nil, err
	}
	if m.attachStore != nil {
		viewDir, err := m.attachStore.EnsureSessionViewDir(id)
		if err != nil {
			sessionCancel()
			return nil, fmt.Errorf("prepare attachment views: %w", err)
		}
		if err := bs.PathPolicy.AddPath(viewDir); err != nil {
			sessionCancel()
			return nil, fmt.Errorf("grant attachment view access: %w", err)
		}
	}

	shared := newSharedFiles()
	core.RegisterOrLog(bs.ToolReg, newSendFileTool(tool.ToolConfig{WorkspaceRoot: bs.CWD, PathPolicy: bs.PathPolicy}, id, shared, m.attachStore))

	// Build RuntimeConfig from bootstrap session + serve-specific fields.
	rcfg := bs.RuntimeConfig()
	rcfg.SessionID = id
	rcfg.Ctx = sessionCtx
	rcfg.Checkpoints = cpStore
	rcfg.ProviderFactory = m.providerFactory
	// Keep the full base system prompt (identity + tool guidance + Persistence).
	// RuntimeConfig() already set it from the agent's prompt; do NOT wipe it.
	// rebuildSystemPrompt composes BaseSystemPrompt + mode fragments and *replaces*
	// the whole prompt, so an empty base makes every mode transition — and every
	// ResumeSession (SyncPlanMode) — call SetSystemPrompt("") and strip the agent
	// down to no system prompt at all. That left resumed serve sessions (i.e. any
	// session after a reconnect/redeploy) running with an empty prompt, which is
	// exactly why models behaved erratically and stalled. TUI never wiped it.
	rcfg.SteerFilter = func(text string) bool {
		_, was := subagentTexts.LoadAndDelete(text)
		return !was
	}
	if opts != nil {
		rcfg.InitialMetadata = opts.initialMetadata
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
	// (running/cancelling) subagent jobs plus terminal owners of retained bash
	// jobs, with their transcript. bus itself doesn't know about pkg/subagent,
	// so this handler is registered here, from the frontend that owns the
	// *subagent.Jobs handle.
	rt.Bus.OnQuery(func(q bus.GetSubagents) ([]bus.SubagentSnapshot, error) {
		if bs.Subagents == nil {
			return nil, nil
		}
		var bashInfos []tool.BashJobInfo
		if bs.BashJobs != nil {
			bashInfos = bs.BashJobs.Snapshot()
		}
		return initSubagentSnapshots(bs.Subagents.Snapshot(), bashInfos, bs.Subagents.Messages), nil
	})
	rt.Bus.OnQuery(func(q bus.GetBashJobs) ([]bus.BashJobSnapshot, error) {
		if bs.BashJobs == nil {
			return nil, nil
		}
		infos := bs.BashJobs.Snapshot()
		out := make([]bus.BashJobSnapshot, 0, len(infos))
		for _, info := range infos {
			out = append(out, bus.BashJobSnapshot{JobID: info.JobID, OwnerAgentID: info.OwnerAgentID, Command: info.Command, CWD: info.CWD, Status: info.Status, Output: info.Output})
		}
		return out, nil
	})

	// Apply thinking level if restoring.
	if opts != nil && opts.initialThinking != "" {
		if err := rt.Bus.Execute(bus.SetThinking{Level: opts.initialThinking}); err != nil {
			if bs.MCPManager != nil {
				bs.MCPManager.Close()
			}
			sessionCancel()
			rt.Close()
			return nil, err
		}
	}
	if opts != nil && opts.initialPermissionMode != "" {
		if err := rt.Bus.Execute(bus.SetPermissionMode{Mode: opts.initialPermissionMode}); err != nil {
			if bs.MCPManager != nil {
				bs.MCPManager.Close()
			}
			sessionCancel()
			rt.Close()
			return nil, err
		}
	}

	// PlanMode onChange is owned by the runtime (NewSessionRuntime sets it).
	// No need to override here — it publishes PlanModeChanged and rebuilds
	// the system prompt automatically.

	sess = &ManagedSession{
		ID:                  id,
		serverInstance:      m.serverInstance,
		Title:               title,
		CWD:                 cwd,
		Created:             time.Now(),
		Updated:             time.Now(),
		modelProvider:       model.Provider,
		cacheTTL:            core.CacheTTLDuration(moaCfg),
		autoTitleModel:      autoTitleModel,
		autoTitleEnabled:    autoTitleEnabled,
		sessionBriefModel:   briefModel,
		sessionBriefEnabled: briefEnabled,
		runtime:             rt,
		subagents:           bs.Subagents,
		bashJobs:            bs.BashJobs,
		pathPolicy:          bs.PathPolicy,
		infra: serveInfra{
			sessionCtx:      sessionCtx,
			sessionCancel:   sessionCancel,
			toolReg:         bs.ToolReg,
			mcpMgr:          bs.MCPManager,
			mcpController:   bs.MCPController,
			mcpPolicy:       bs.MCPPolicy,
			buildBasePrompt: bs.BuildBasePrompt,
			UntrustedMCP:    bs.UntrustedMCP,
		},
		sharedFiles: shared,
	}
	// Wire the MCP controller's prompt refresh now that the runtime exists: when
	// a server is enabled/disabled the tool set changes, so the base system
	// prompt must be rebuilt from the registry (never string-patched) and
	// re-applied to the agent. bs.BuildBasePrompt captures the same inputs used
	// at construction.
	sess.wireMCPRefresh()
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
	m.subscribeAttachmentReleases(sess)
	m.subscribePush(sess)
	m.subscribeAutoTitle(sess)
	m.subscribeSessionBrief(sess)
	m.subscribeUsageCache(sess)
	m.subscribeUnreadResults(sess)
	// A resumed transcript has no in-memory brief after a server restart. Queue
	// one refresh rather than generating synchronously: briefPending coalesces
	// this with any immediate bus trigger, while briefRunning and the per-session
	// cooldown keep each resumed session to one cheap-model call at a time.
	if opts != nil && len(sess.History()) > 0 && sess.briefUpdated.IsZero() {
		m.scheduleSessionBrief(sess)
	}
	m.subscribeCacheClock(sess)
	m.subscribeAttention(sess)
	// A resumed automation session carries its callback target in the persisted
	// metadata, so the loop keeps closing across a restart. Freshly created ones
	// are wired by CreateSession, which holds the creation-time metadata.
	if opts != nil {
		m.subscribeAutomationCallback(sess, opts.initialMetadata)
	}

	return sess, nil
}

func (m *Manager) subscribeAttachmentReleases(sess *ManagedSession) {
	if m.attachStore == nil {
		return
	}
	sess.runtime.Bus.Subscribe(func(e bus.SteersCanceled) {
		m.releaseAttachmentIDs(e.SessionID, e.AttachmentIDs)
	})
}

// initSubagentSnapshots retains a terminal child while a reconnect snapshot
// still contains a bash job it owns. Without its real owner, clients can only
// manufacture a running placeholder and incorrectly surface the owned job as
// root activity.
func initSubagentSnapshots(infos []subagent.JobInfo, bashInfos []tool.BashJobInfo, messages func(string) []core.AgentMessage) []bus.SubagentSnapshot {
	owners := make(map[string]struct{})
	for _, bash := range bashInfos {
		if bash.OwnerAgentID != "" {
			owners[bash.OwnerAgentID] = struct{}{}
		}
	}

	var out []bus.SubagentSnapshot
	for _, info := range infos {
		live := info.Status == "running" || info.Status == "cancelling"
		if !live {
			if _, ownsBash := owners[info.JobID]; !ownsBash {
				continue
			}
		}
		out = append(out, bus.SubagentSnapshot{
			JobID:            info.JobID,
			OriginToolCallID: info.OriginToolCallID,
			Task:             info.Task,
			Title:            info.Title,
			Model:            info.Model,
			Thinking:         info.Thinking,
			Status:           info.Status,
			Async:            info.Async,
			Messages:         messages(info.JobID),
			StartedAt:        info.StartedAt,
			Usage:            info.Usage,
			CostUSD:          info.CostUSD,
			ContextPercent:   info.ContextPercent,
			AccentIndex:      info.AccentIndex,
		})
	}
	return out
}

var (
	ErrNotFound                = errors.New("session not found")
	ErrBusy                    = errors.New("session is busy")
	ErrInvalidCWD              = errors.New("invalid working directory")
	ErrInvalidModel            = errors.New("invalid model")
	ErrInvalidThinking         = errors.New("invalid thinking level")
	ErrInvalidPermissionMode   = errors.New("invalid permission mode")
	ErrNoMCP                   = errors.New("session has no MCP servers")
	ErrInvalidAttentionCursor  = errors.New("invalid attention cursor")
	ErrStaleAttentionNamespace = errors.New("stale attention namespace")
)

// Delete aborts any running agent, closes resources, and removes the session.
//
// It runs under automationMu (the same guard as CreateAutomationRun's
// check-create-send-register sequence) so a delete cannot interleave with a run
// creation and leave an idempotency key pointing at a session that is already
// gone. Lock order is automationMu → m.mu, as in CreateAutomationRun.
func (m *Manager) Delete(id string) error {
	m.automationMu.Lock()
	defer m.automationMu.Unlock()
	return m.deleteSession(id)
}

// deleteSession is Delete's body. Callers must hold automationMu.
func (m *Manager) deleteSession(id string) error {
	if m.automation != nil {
		// A deleted session must not keep answering an idempotency key: the next
		// retry should create a fresh run rather than resolve to a gone session.
		m.automation.forget(id)
	}
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
		m.forgetUnseen(id)
		m.forgetSecretBatches(id)
		_ = removeSessionAttachDir(id)
		if m.attachStore != nil {
			if err := m.attachStore.ReleaseSession(id); err != nil {
				slog.Warn("release session attachments", "session", id, "error", err)
			}
		}
		return nil
	}
	delete(m.sessions, id)
	m.deactivateAttentionRuntime(sess)
	m.mu.Unlock()
	sess.deleted.Store(true)
	m.forgetUnseen(id)
	m.forgetSecretBatches(id)
	// Mark closing and drain the runtime's users before tearing it down, so a
	// /send or /command already holding this pointer can't start a run into a
	// runtime that is going away. Delete does NOT refuse a busy session (unlike
	// close: the conversation is being destroyed either way), it only waits for
	// the in-flight request to return. Taken outside m.mu, as in CloseSession.
	sess.closing.Store(true)
	sess.drainLifecycleUsers()
	// Mark deleted to prevent persistence from resurrecting.
	if sess.persister != nil {
		sess.persister.markDeleted()
	}

	// Stop Web Push subscribers BEFORE closing the runtime, so events drained
	// during bus shutdown cannot notify for a session that no longer exists.
	// Coordinate with the final brief write: a generator holds sess.mu while it
	// checks deleted and stores the three brief fields.
	sess.mu.Lock()
	sess.deleted.Store(true)
	sess.mu.Unlock()
	for _, unsub := range sess.pushUnsubs {
		unsub()
	}
	if sess.usageUnsub != nil {
		sess.usageUnsub()
	}
	if sess.unreadUnsub != nil {
		sess.unreadUnsub()
	}
	// Delete removed the first snapshot before it waited for in-flight users.
	// Sweep again so a secret request that had already passed that boundary
	// cannot leave a newly registered batch behind.
	m.forgetSecretBatches(id)

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
	if m.attachStore != nil {
		if err := m.attachStore.ReleaseSession(id); err != nil {
			slog.Warn("release session attachments", "session", id, "error", err)
		}
	}
	return nil
}

// drainLifecycleUsers blocks until every in-flight request that may be using
// this session's runtime (a send, a command, a scheduled delivery) has
// returned, and — since `closing` is already set — guarantees no new one
// starts. Callers use it as a barrier before tearing the runtime down.
func (s *ManagedSession) drainLifecycleUsers() {
	s.lifecycle.Lock()
	s.lifecycle.Unlock() //nolint:staticcheck // SA2001: the empty section IS the barrier
}

// CloseSession unloads an active session from memory, leaving it on disk where
// it lists as "saved" and can be reopened with ResumeSession.
//
// This is what "close" means to a user: the conversation stops occupying a live
// runtime (agent, MCP connections, bridges) but stays in the list and loses
// nothing. Closing a session that is already only on disk is a no-op, so the
// action is idempotent from any client.
//
// Refused with ErrBusy unless the session is fully quiescent: not running, not
// awaiting a permission decision, and with no background work (async subagents,
// bash jobs, verifiers) still in flight. StateIdle alone is not enough — closing
// cancels the session context, which would kill that work and lose its output.
//
// Concurrency. The close is admitted under the state lock (DoIfQuiescent), which
// is the same lock a run-start takes, so a /send cannot slip between the check
// and the teardown: either it starts a run first (and the close is refused), or
// it finds the session already marked closing. The ID stays reserved in
// m.resuming until the teardown finishes, so a concurrent ResumeSession cannot
// build a second runtime from disk while the old one is still flushing.
//
// Runs under automationMu like Delete, so a close cannot interleave with the
// automation check-create-send-register sequence. Lock order: automationMu → m.mu.
func (m *Manager) CloseSession(id string) error {
	m.automationMu.Lock()
	defer m.automationMu.Unlock()

	m.mu.Lock()
	if _, resuming := m.resuming[id]; resuming {
		m.mu.Unlock()
		return ErrBusy
	}
	sess, ok := m.sessions[id]
	if !ok || sess == nil {
		m.mu.Unlock()
		// Not loaded. Report whether it exists at all, so a stale client that
		// closes a deleted session gets a 404 instead of a silent success.
		if _, _, err := session.FindSessionReadOnly(m.sessionBaseDir, id); err != nil {
			if errors.Is(err, session.ErrNotFound) {
				return ErrNotFound
			}
			return err
		}
		return nil
	}
	// Establish close's send boundary while m.mu still identifies this runtime.
	// A sender that was already using it, or which acquires lifecycle after this
	// point but before the writer lock, advances this value before it releases
	// lifecycle and therefore makes this close lose the race.
	sendGeneration := sess.sendGeneration.Load()
	m.mu.Unlock()

	// Drain the users of this runtime before deciding anything. Taking the
	// lifecycle write lock waits for in-flight sends and locks out new ones, so
	// the quiescence check below cannot be invalidated by a run starting right
	// after it. Acquired OUTSIDE m.mu: senders hold it while doing bus work, so
	// holding m.mu across it would stall every unrelated session.
	//
	// A Send that was admitted while this close waited may have a very fast
	// provider and return to idle before we inspect the runtime; its acceptance
	// still wins this race.
	sess.lifecycle.Lock()
	defer sess.lifecycle.Unlock()
	if sess.sendGeneration.Load() != sendGeneration {
		return ErrBusy
	}

	// A RunEnded fan-out schedules the automatic reactors asynchronously, so a
	// close arriving right after a turn could observe "no background work" just
	// before auto-verify or a goal verifier marks itself active. Drain the
	// accepted publication batch first, the same way WaitQuiescent does, so the
	// quiescence check below sees that work.
	sess.runtime.Bus.Drain(2 * time.Second)

	m.mu.Lock()
	// Re-check under the lock: a concurrent close or delete may have won while
	// this one waited for the lifecycle lock.
	if _, resuming := m.resuming[id]; resuming {
		m.mu.Unlock()
		return ErrBusy
	}
	if cur, still := m.sessions[id]; !still || cur != sess {
		m.mu.Unlock()
		return nil // already closed or deleted by someone else
	}
	// Admit the close atomically against run-start, and hold the ID reserved
	// (m.resuming doubles as the lifecycle barrier) so ResumeSession waits for
	// the teardown instead of racing it.
	admitted := sess.runtime.DoIfQuiescent(func() {
		sess.closing.Store(true)
		delete(m.sessions, id)
		m.resuming[id] = struct{}{}
	})
	m.mu.Unlock()
	if !admitted {
		return ErrBusy
	}
	defer func() {
		m.mu.Lock()
		delete(m.resuming, id)
		m.mu.Unlock()
	}()

	// Flush before tearing anything down: the final turn must be on disk before
	// the runtime that holds it goes away. Subagent transcripts are captured the
	// same way Shutdown does it, before the context cancellation below can kill
	// a still-live child.
	if err := sess.runtime.Flush(); err != nil {
		// Keep the runtime alive: unloading it here would discard the only copy
		// of a conversation whose final snapshot could not be written.
		m.mu.Lock()
		m.sessions[id] = sess
		sess.closing.Store(false)
		m.mu.Unlock()
		return fmt.Errorf("close session: flush: %w", err)
	}
	sess.flushLiveSubagentTranscripts()

	// An automation idempotency key must not resolve to a session that is no
	// longer loaded: the interaction endpoints refuse to resume saved sessions,
	// so a retry should start a fresh run instead of hitting a dead reference.
	if m.automation != nil {
		m.automation.forget(id)
	}
	m.deactivateAttentionRuntime(sess)

	// Same teardown order as delete — push subscribers first so events drained
	// during shutdown can't notify for a session that is no longer live, then
	// MCP, then the session context, then the runtime.
	for _, unsub := range sess.pushUnsubs {
		unsub()
	}
	if sess.usageUnsub != nil {
		sess.usageUnsub()
	}
	if sess.unreadUnsub != nil {
		sess.unreadUnsub()
	}
	m.forgetSecretBatches(id)
	if sess.infra.mcpMgr != nil {
		sess.infra.mcpMgr.Close()
	}
	sess.infra.sessionCancel()
	// Close drains the bus's async persistence reactor, so no delayed save can
	// still be writing when the deferred unreserve lets a resume rebuild this
	// session from the same files.
	sess.runtime.Close()

	// Attachments are NOT released here: unlike delete, the conversation still
	// exists and must render its images when reopened.
	m.invalidateSavedCache()
	return nil
}

// reapStaleAttachments removes session attachment directories older than 24h.
// Best-effort: only directories whose name matches sessionIDPattern are
// touched; the base dir itself and unrelated entries are left alone. If the
// base dir is a symlink it is refused (never followed) to avoid deleting
// through it.
func reapStaleAttachments() {
	reapStaleAttachmentsIn(attachmentsBaseDir())
	// Sweep the pre-per-user default too: after the switch nothing else would
	// ever look at it again, leaving whatever was uploaded there on disk.
	if legacy := legacyAttachmentsBaseDir(); legacy != "" {
		reapStaleAttachmentsIn(legacy)
	}
}

func reapStaleAttachmentsIn(base string) {
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
			_ = removeSessionAttachDirIn(base, entry.Name())
		}
	}
}

// ResumeSession loads a saved session from disk and creates a full runtime.
func (m *Manager) ResumeSession(id string) (*ManagedSession, error) {
	return m.resumeSession(id, 0)
}

// resumeSession is ResumeSession with an optional cap: when maxLoaded > 0 the
// reservation is refused if the resident set (loaded + resuming) has already
// reached it. The check happens inside the same critical section as the
// reservation so concurrent callers cannot race past the cap.
func (m *Manager) resumeSession(id string, maxLoaded int) (*ManagedSession, error) {
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
	if maxLoaded > 0 && len(m.sessions)+len(m.resuming) >= maxLoaded {
		m.mu.Unlock()
		return nil, ErrAutomationTooManySessions
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
		initialMetadata:        saved.Metadata,
		titleSource:            saved.TitleSource,
		// Per-run MCP servers are session-scoped: they only exist in this
		// session's metadata, so a resume has to bring them back or the agent
		// silently loses the tools the automation caller attached. A name the
		// operator has since configured is dropped, not merged on top: the
		// merge order would otherwise let a stale per-run server override
		// operator config, which the API refuses at creation time.
		extraMCPServers: mcpServersFromMeta(saved.Metadata, m.configuredMCPServers(cwd)),
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("resume: %w", err)
	}
	sess.Origin = saved.Origin()
	sess.automationCreated = automationCreatedMeta(saved.Metadata)
	// 3. Restore permission mode and the context limit.
	if savedPermMode != "" {
		if err := sess.runtime.Bus.Execute(bus.SetPermissionMode{Mode: savedPermMode}); err != nil {
			slog.Warn("resume: permission mode", "id", id, "error", err)
		}
	}
	if at := saved.CompactAtMeta(); at > 0 {
		if err := sess.runtime.Bus.Execute(bus.SetCompactAt{Tokens: at}); err != nil {
			slog.Warn("resume: context limit", "id", id, "error", err)
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
	m.mu.Lock()
	delete(m.resuming, id)
	m.initializeAttentionRuntimeLocked(sess)
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
	if m.secretReaperCancel != nil {
		m.secretReaperCancel()
		if m.secretReaperDone != nil {
			<-m.secretReaperDone
		}
	}
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
		// Cancel the session context once the flush has captured everything:
		// events drained by the Close below (an async RunEnded, say) can still
		// reach subscribers that spawn work of their own — the automation
		// callback waits for quiescence and then POSTs. The cancelled context is
		// what makes that work give up instead of outliving the shutdown.
		s.infra.sessionCancel()
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
		s.persistSubagentTranscript(info.JobID, info.Status, "", "", time.Time{}, nil, 0)
	}
}

// MCPStatus returns the policy-decorated health snapshot of this session's MCP
// servers (empty if none are configured). Each entry carries the applied enabled
// state, desired-enabled, the scopes that veto it, and any pending action.
func (s *ManagedSession) MCPStatus() []mcp.ControllerStatus {
	s.mu.Lock()
	ctrl := s.infra.mcpController
	s.mu.Unlock()
	if ctrl == nil {
		return nil
	}
	return ctrl.Status()
}

// mcpSummary rolls this session's MCP server states into the glanceable counts
// carried in SessionInfo. Returns nil when there are no servers, so the status
// line indicator stays absent rather than showing an empty "0 servers".
//
// A disabled server is a voluntary choice, so it is counted as Disabled (neutral)
// rather than Unhealthy; only an enabled server that failed or exited is an
// alarm. Pending counts servers whose desired policy hasn't been applied yet.
func (s *ManagedSession) mcpSummary() *MCPSummary {
	status := s.MCPStatus()
	if len(status) == 0 {
		return nil
	}
	sum := &MCPSummary{Total: len(status)}
	for _, st := range status {
		switch st.State {
		case mcp.StateDisabled:
			sum.Disabled++
		case mcp.StateReady:
			sum.Ready++
		case mcp.StateFailed, mcp.StateExited:
			// Enabled but not ready; starting is transient, failed/exited alerts.
			sum.Unhealthy++
		}
		if st.PendingAction != "" {
			sum.Pending++
		}
	}
	return sum
}

// wireMCPRefresh connects the MCP controller's prompt-refresh hook to this
// session's runtime, and registers a manager OnChange callback that publishes
// MCPChanged so clients recolor the status line live. When a server is
// enabled/disabled/restarted the tool set changes, so the base system prompt is
// rebuilt from the registry (never string-patched) and re-applied to the agent.
// No-op if there is no controller or prompt builder.
func (s *ManagedSession) wireMCPRefresh() {
	ctrl := s.infra.mcpController
	build := s.infra.buildBasePrompt
	reg := s.infra.toolReg
	mgr := s.infra.mcpMgr
	rt := s.runtime
	if ctrl == nil || build == nil || reg == nil || rt == nil {
		return
	}
	ctrl.SetRefreshPrompt(func() {
		rt.RefreshBaseSystemPrompt(build(reg.Specs()))
	})
	if mgr != nil {
		// Fired from a manager goroutine on any server transition. We recompute
		// the summary (cheap) and publish; the bus fan-out is asynchronous, so
		// this never blocks the manager's lifecycle work.
		mgr.OnChange(func(mcp.ServerStatus) { s.publishMCPChanged() })
	}
}

// publishMCPChanged emits the current MCP summary on the session bus so open
// clients recolor the status line and an open panel re-fetches detail. No-op
// when the session has no MCP servers.
func (s *ManagedSession) publishMCPChanged() {
	sum := s.mcpSummary()
	if sum == nil {
		return
	}
	s.runtime.Bus.Publish(bus.MCPChanged{
		SessionID: s.ID,
		Total:     sum.Total,
		Ready:     sum.Ready,
		Disabled:  sum.Disabled,
		Unhealthy: sum.Unhealthy,
		Pending:   sum.Pending,
	})
}

// RestartMCPServer restarts a single MCP server for this session and re-syncs
// the tool registry with its (possibly changed) tool set. Other servers are
// untouched. Returns ErrNoMCP if the session has no MCP manager,
// mcp.ErrUnknownServer for a name it doesn't manage, mcp.ErrServerDisabled for a
// disabled server (enable it first), or ErrBusy if the session is running or
// awaiting a permission decision.
func (s *ManagedSession) RestartMCPServer(name string) (mcp.ServerStatus, error) {
	// Serialize with reconcile/reload so a restart never runs concurrently with a
	// policy reconcile or a manager swap; the desired-policy check inside
	// Controller.Restart then reflects any disable committed before we acquired
	// the guard (finding: restart must not respawn a now-disabled server).
	s.mcpLifecycleMu.Lock()
	defer s.mcpLifecycleMu.Unlock()

	// A session being closed is having its MCP manager torn down; respawning a
	// server into it would leave a process nobody owns.
	if s.closing.Load() {
		return mcp.ServerStatus{}, ErrNotFound
	}

	s.mu.Lock()
	ctrl := s.infra.mcpController
	ctx := s.infra.sessionCtx
	s.mu.Unlock()
	if ctrl == nil {
		return mcp.ServerStatus{}, ErrNoMCP
	}

	// A restart swaps this server's tool schemas. If a model turn is in flight
	// (or a permission decision is pending), it was handed the pre-restart tools
	// and could then hit an "unknown tool" between response and dispatch. Run the
	// restart inside DoIfQuiescent so it holds the state lock across the tool-set
	// mutation: a SendPrompt can't transition Idle/Error → Running in the gap and
	// receive the old registry. If not quiescent, refuse with ErrBusy.
	var st mcp.ServerStatus
	var restartErr error
	if !s.runtime.DoIfQuiescent(func() { st, restartErr = ctrl.Restart(ctx, name) }) {
		return mcp.ServerStatus{}, ErrBusy
	}
	return st, restartErr
}

// reloadMCP reloads MCP servers for a session.
func (s *ManagedSession) reloadMCP(sessionCfg core.MoaConfig) error {
	// Phase 1: prepare (no mutation).
	projectServers, err := core.LoadMCPFile(filepath.Join(s.CWD, ".mcp.json"))
	if err != nil {
		return err
	}

	merged := core.MergeMCPServers(sessionCfg.MCPServers, projectServers)

	// Use the LIVE disable policy (session toggles mutate the controller, not the
	// bootstrap snapshot in infra.mcpPolicy), so a reload doesn't revive a server
	// disabled since startup.
	reloadPolicy := s.infra.mcpPolicy
	if s.infra.mcpController != nil {
		reloadPolicy = s.infra.mcpController.Policy()
	}

	var newMgr *mcp.Manager
	var newTools []core.Tool
	if len(merged) > 0 {
		newMgr = mcp.NewManager(nil, s.CWD)
		// Honor the session's disable policy on reload too, so a vetoed server
		// isn't spawned just because the project's .mcp.json became trusted.
		newMgr.Start(s.infra.sessionCtx, merged, reloadPolicy.DisabledSet())
		newTools = newMgr.Tools()
	}

	if newMgr != nil && len(newTools) == 0 {
		newMgr.Close()
		return fmt.Errorf("MCP servers started but no tools available; keeping existing tools")
	}

	// Phase 2+3: swap the tool set atomically against run-start. DoIfQuiescent
	// holds the state lock across the swap, so a SendPrompt can't transition
	// Idle/Error → Running between a busy check and the registry mutation and be
	// handed a half-swapped tool set. If not quiescent, refuse with ErrBusy.
	swapped := s.runtime.DoIfQuiescent(func() {
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
		// Rebuild the controller over the new manager (the old one pointed at the
		// now-closed manager) and rewire its prompt refresh.
		if newMgr != nil {
			s.infra.mcpController = mcp.NewController(mcp.ControllerConfig{
				Manager:  newMgr,
				Registry: s.infra.toolReg,
				Policy:   reloadPolicy,
			})
		} else {
			s.infra.mcpController = nil
		}
		s.infra.UntrustedMCP = false
		s.mu.Unlock()

		s.wireMCPRefresh()

		// Cleanup old manager after the swap (still inside the barrier: closing it
		// can't race a run that was blocked from starting).
		if oldMgr != nil {
			oldMgr.Close()
		}
	})
	if !swapped {
		if newMgr != nil {
			newMgr.Close()
		}
		return ErrBusy
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
