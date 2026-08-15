// Package bootstrap wires up a complete agent session: tool registry, MCP,
// permissions, subagents, plan mode, skills, verify, and system prompt.
//
// Both the CLI (cmd/moa) and the HTTP server (pkg/serve) call BuildSession
// to avoid duplicating the 14-step setup sequence.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/askuser"
	agentcontext "github.com/e-aleixandre/moa/pkg/context"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/mcp"
	"github.com/e-aleixandre/moa/pkg/memory"
	"github.com/e-aleixandre/moa/pkg/moadocs"
	"github.com/e-aleixandre/moa/pkg/permission"
	"github.com/e-aleixandre/moa/pkg/planmode"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
	"github.com/e-aleixandre/moa/pkg/skill"
	"github.com/e-aleixandre/moa/pkg/subagent"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"github.com/e-aleixandre/moa/pkg/tool"
	"github.com/e-aleixandre/moa/pkg/verify"
)

// Default review thinking level for plan mode (shared between CLI and serve).
const DefaultReviewThinking = "medium"

// SessionConfig configures a session build. Most fields have sensible defaults.
type SessionConfig struct {
	// Required.
	CWD             string                                  // Working directory. Must exist and be a directory.
	Model           core.Model                              // Resolved LLM model.
	Provider        core.Provider                           // LLM provider for the primary model.
	ProviderFactory func(core.Model) (core.Provider, error) // Creates providers for subagents, plan review, etc.

	// Config overrides. When nil, loaded from disk via core.LoadMoaConfig(CWD).
	MoaCfg *core.MoaConfig

	// MCPDisableSources gives the provenance (global/project) of MCP disable
	// vetoes when MoaCfg is injected. The merged MoaCfg.DisabledMCPServers loses
	// which scope each name came from, so callers that inject MoaCfg should also
	// pass the resolved sources; otherwise a project-only veto would be
	// misattributed to global and could never be cleared by editing Project.
	// Ignored when MoaCfg is nil (bootstrap resolves provenance from disk).
	MCPDisableSources *core.MCPDisableSources

	// ExtraMCPServers are session-scoped MCP servers merged on top of the
	// configured ones. They come from the caller (the Automation API attaches
	// per-run servers this way), live and die with the session, and are never
	// written to any config file. A name already taken by a configured server is
	// the caller's responsibility to reject: the merge would silently override
	// operator config.
	ExtraMCPServers map[string]core.MCPServer

	// Context for MCP servers and subagent async jobs. Required.
	Ctx context.Context

	// Agent tuning. Zero values use package defaults.
	ThinkingLevel       string        // Default: "medium"
	MaxTurns            int           // 0 = unlimited (default). Overrides config.json.
	MaxToolCallsPerTurn int           // 0 = unlimited (default). Overrides config.json.
	MaxRunDuration      time.Duration // 0 = unlimited (default). Overrides config.json.
	MaxBudget           float64       // Default: from config. 0 = unlimited.
	DisableSandbox      bool          // Overrides config (OR'd). Deprecated: use PathScope.

	// PathScope override. Empty = derive from config/permissions.
	// Valid values: "workspace", "unrestricted".
	PathScope string
	// ExtraAllowedPaths are merged with config allowed_paths (from --allow-path flags).
	ExtraAllowedPaths []string

	// Permission mode override. Empty = from config or "yolo".
	PermissionMode string
	// Model spec for auto-mode AI evaluator. Empty = "haiku".
	PermissionEvalModel string
	// Headless denies unresolved permissions instead of blocking (no user to approve).
	Headless bool
	// ExtraAllowPatterns are merged with config allow patterns (from --allow flags).
	ExtraAllowPatterns []string

	// PlanMode session dir. If empty, uses CWD.
	PlanSessionDir string

	// Feature toggles. All default to true.
	EnableAskUser bool // Register ask_user tool. Default: true.

	// BeforeWrite is called before write/edit tools modify a file.
	// Used by the checkpoint system to capture pre-edit state.
	BeforeWrite func(path string) error

	// MaterializeContent rehydrates byte-free attachment references before a
	// provider request. Nil preserves legacy inline content unchanged.
	MaterializeContent func(context.Context, []core.Message) ([]core.Message, error)

	// Subagent callbacks. All optional (nil = no-op).
	OnAsyncJobChange func(count int)
	OnAsyncComplete  func(jobID, task, status, resultTail string, truncated bool)

	// OnSubagentStart/OnSubagentEvent/OnSubagentUsage/OnSubagentEnd are the
	// rich, per-child streaming sinks (subagent.Config.OnChildStart/
	// OnChildEvent/OnChildUsage/OnChildEnd). The caller wires these into its
	// own bus (see cmd/moa's preBus, pkg/serve's session_lifecycle closure)
	// since bootstrap has no bus reference of its own. All optional (nil =
	// no-op).
	OnSubagentStart      func(jobID, task, model, thinking, originToolCallID string, async bool, startedAt time.Time, accentIndex int)
	OnSubagentEvent      func(jobID string, inner any)
	OnSubagentUsage      func(jobID string, usage *core.Usage, costUSD float64, contextPct int)
	OnSubagentEnd        func(jobID, task string, async bool, status, result, resultErr string, finishedAt time.Time, usage *core.Usage, costUSD float64)
	SubagentTitleModel   core.Model
	SubagentTitleEnabled bool
	OnSubagentTitle      func(jobID, title string)

	// Background bash callbacks feed the shared session bus/UI. Output is a
	// lossy live delta; end carries the authoritative bounded log.
	OnBashJobStart  func(job tool.BashJobInfo)
	OnBashJobOutput func(job tool.BashJobInfo, delta string)
	OnBashJobEnd    func(job tool.BashJobInfo)

	// SubagentTranscriptLoader loads a finished subagent's persisted transcript
	// (messages plus the model/thinking it ran under) by job ID, enabling the
	// subagent tool's "resume" parameter. Optional (nil = resume unsupported).
	// The caller wires this to its transcript store (see pkg/serve's
	// SubagentStore).
	SubagentTranscriptLoader func(jobID string) (subagent.ResumedTranscript, error)
}

// Session is a fully wired session ready for agent.Run/Send.
type Session struct {
	Agent         *agent.Agent
	ToolReg       *core.Registry
	TaskStore     *tasks.Store
	PlanMode      *planmode.PlanMode
	Goal          *goal.Goal
	AskBridge     *askuser.Bridge
	Gate          *permission.Gate
	MCPManager    *mcp.Manager
	MCPController *mcp.Controller
	MCPPolicy     core.MCPDisablePolicy
	PathPolicy    *tool.PathPolicy
	AgentsMD      string
	Skills        []skill.Skill
	SkillsIndex   string
	SystemPrompt  string
	// BuildBasePrompt regenerates the base system prompt from a tool-spec set,
	// capturing the same inputs (AgentsMD, CWD, verify, indexes) used at
	// construction. The MCP controller uses it to rebuild the prompt after a
	// server is enabled/disabled, so the model is never told about a tool that
	// is no longer registered.
	BuildBasePrompt   func([]core.ToolSpec) string
	MemoryStore       *memory.Store
	SessionCheckpoint *sessioncheckpoint.Slot
	HasVerify         bool
	Model             core.Model
	MoaCfg            core.MoaConfig
	CWD               string // workspace directory

	// UntrustedMCP is true when .mcp.json exists but CWD is not in TrustedMCPPaths.
	UntrustedMCP bool

	// Headless is true when the session was created in headless mode (no user
	// to approve permissions). Preserved so RuntimeConfig() can set GateConfig
	// correctly even when Gate is nil (yolo mode).
	Headless bool

	// agentHolder stores the atomic agent pointer for subagent closures.
	// Set by BuildSession; updated internally on reconfiguration.
	agentHolder atomic.Pointer[agent.Agent]

	// Subagents is the handle onto the subagent job store, returned by
	// subagent.RegisterAll. Used for the init snapshot (reconnect), the agent
	// tray, and cancellation.
	Subagents *subagent.Jobs
	BashJobs  *tool.BashJobs
}

// BuildSession wires up a complete agent session. The returned Session
// contains everything needed to run the agent. Caller owns cleanup:
// - MCPManager.Close() if non-nil
// - Context cancellation for subagent jobs
//
// BuildSession does NOT create the agent — it returns all the pieces needed
// to create one. This allows callers to customize the AgentConfig (e.g.,
// compose permission checks with plan mode filtering) before calling agent.New.
func BuildSession(cfg SessionConfig) (*Session, error) {
	if cfg.CWD == "" {
		return nil, fmt.Errorf("bootstrap: CWD is required")
	}
	if cfg.Provider == nil {
		return nil, fmt.Errorf("bootstrap: Provider is required")
	}
	if cfg.ProviderFactory == nil {
		return nil, fmt.Errorf("bootstrap: ProviderFactory is required")
	}
	if cfg.Ctx == nil {
		return nil, fmt.Errorf("bootstrap: Ctx is required")
	}
	// Spills intentionally outlive an individual tool call so the agent can
	// inspect them. Prune expired ones whenever a session is brought up.
	if err := tool.CleanupSpillFiles(); err != nil {
		slog.Warn("cleanup tool output spills", "error", err)
	}

	// Apply defaults.
	if cfg.ThinkingLevel == "" {
		cfg.ThinkingLevel = "medium"
	}
	var err error
	cfg.ThinkingLevel, err = core.EffectiveThinkingLevel(cfg.Model, cfg.ThinkingLevel)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: thinking level: %w", err)
	}
	// MaxTurns, MaxToolCallsPerTurn, MaxRunDuration: 0 = unlimited.
	// Explicit values from CLI flags take precedence; otherwise fall through
	// to config.json values loaded below.

	// 1. Load config.
	var moaCfg core.MoaConfig
	var mcpDisableSources core.MCPDisableSources
	if cfg.MoaCfg != nil {
		moaCfg = *cfg.MoaCfg
		if cfg.MCPDisableSources != nil {
			// Caller supplied resolved provenance (global vs project); use it so
			// scope editing and the panel tell the truth.
			mcpDisableSources = *cfg.MCPDisableSources
		} else {
			// No provenance available: the merged list can only be treated as
			// global for resolution purposes.
			mcpDisableSources = core.MCPDisableSources{Global: moaCfg.DisabledMCPServers}
		}
	} else {
		resolved := core.LoadMoaConfigResolved(cfg.CWD)
		moaCfg = resolved.Config
		mcpDisableSources = resolved.MCPDisabled
	}

	// Budget: config default, overridden by explicit SessionConfig value.
	maxBudget := moaCfg.MaxBudget
	if cfg.MaxBudget > 0 {
		maxBudget = cfg.MaxBudget
	}

	// Guardrails: config defaults, overridden by explicit SessionConfig values.
	// 0 = unlimited for all three.
	maxTurns := moaCfg.MaxTurns
	if cfg.MaxTurns > 0 {
		maxTurns = cfg.MaxTurns
	}
	maxToolCallsPerTurn := moaCfg.MaxToolCallsPerTurn
	if cfg.MaxToolCallsPerTurn > 0 {
		maxToolCallsPerTurn = cfg.MaxToolCallsPerTurn
	}
	maxRunDuration := core.GetMaxRunDuration(moaCfg)
	if cfg.MaxRunDuration > 0 {
		maxRunDuration = cfg.MaxRunDuration
	}

	// 2. Tool registry.
	// Resolve permission mode early — needed for path scope derivation.
	permMode := permission.Mode(moaCfg.Permissions.Mode)
	if cfg.PermissionMode != "" {
		permMode = permission.Mode(cfg.PermissionMode)
	}
	// moa's default posture is yolo (unrestricted): a single-user local tool.
	// An unset mode resolves to yolo here BEFORE ResolvePathScope, so the
	// effective default path scope is unrestricted (see ResolvePathScope's note).
	if permMode == "" {
		permMode = permission.ModeYolo
	}

	// Resolve path scope: explicit > legacy > derived from permissions.
	effectivePermMode := string(permMode)
	pathScope := cfg.PathScope
	if pathScope == "" {
		pathScope = moaCfg.PathScope
	}
	resolvedScope := core.ResolvePathScope(pathScope, cfg.DisableSandbox || moaCfg.DisableSandbox, effectivePermMode)
	isUnrestricted := resolvedScope == "unrestricted"

	allAllowed := append([]string(nil), moaCfg.AllowedPaths...)
	allAllowed = append(allAllowed, cfg.ExtraAllowedPaths...)
	allAllowed = append(allAllowed, tool.SpillOutputDir())
	pathPolicy := tool.NewPathPolicy(cfg.CWD, allAllowed, isUnrestricted)

	fileTracker := tool.NewFileTracker()
	bashJobs := tool.NewBashJobs(cfg.Ctx, cfg.OnBashJobStart, cfg.OnBashJobOutput, cfg.OnBashJobEnd)
	var bashState *tool.BashState
	if core.IsPersistentShellEnabled(moaCfg) {
		bashState = tool.NewBashState()
	}
	toolReg := core.NewRegistry()
	sessionCheckpoint := sessioncheckpoint.New()
	if err := tool.RegisterBuiltins(toolReg, tool.ToolConfig{
		WorkspaceRoot: cfg.CWD,
		PathPolicy:    pathPolicy,
		BashTimeout:   5 * time.Minute,
		BraveAPIKey:   moaCfg.BraveAPIKey,
		BeforeWrite:   cfg.BeforeWrite,
		FileTracker:   fileTracker,
		BashState:     bashState,
		BashJobs:      bashJobs,
	}); err != nil {
		return nil, fmt.Errorf("register builtins: %w", err)
	}

	// 2b. Script tools from .moa/tools/*.json. These run `bash -c <command>` from
	// the repo, so — like .mcp.json and repo-local config — they are only loaded
	// for directories the user has explicitly trusted. Untrusted repos cannot
	// register shell-executing tools that auto-run at the first prompt.
	if core.IsProjectPathTrusted(moaCfg, cfg.CWD) {
		if err := tool.RegisterScriptTools(toolReg, cfg.CWD); err != nil {
			fmt.Fprintf(os.Stderr, "warning: script tools: %v\n", err)
		}
	}

	// 3. Task store — always available.
	taskStore := tasks.NewStore()
	core.RegisterOrLog(toolReg, tasks.NewTool(taskStore))

	// 3b. moa's own documentation, embedded in the binary. Always available:
	// someone who installed the binary has no copy of the repository, and
	// questions about how to configure or integrate moa can come up in any
	// session, not just one opened inside this repo.
	core.RegisterOrLog(toolReg, moadocs.NewTool())

	// 4. Verify tool. Registered even when the session's own directory has no
	// config: in multi-repo work the code being changed often lives in another
	// worktree, and the tool can target it with cwd. Without this the tool
	// would be missing from exactly the sessions that need it most.
	verifyCfg, verifyErr := verify.LoadConfig(cfg.CWD)
	if verifyErr != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid .moa/verify.json in %s: %v\n", cfg.CWD, verifyErr)
	}
	hasVerify := verifyCfg != nil
	core.RegisterOrLog(toolReg, verify.NewTool(cfg.CWD, pathPolicy))

	// 5. AGENTS.md.
	agentsMD, _ := agentcontext.LoadAgentsMD(cfg.CWD, "")

	// 5b. Memory (global + project). Only the index is injected into the prompt;
	// full facts are read on demand via the memory tool.
	var memStore *memory.Store
	var memoryIndex string
	if core.IsMemoryEnabled(moaCfg) {
		if dir := core.ConfigDir(); dir == "" {
			slog.Warn("memory: cannot determine config directory")
		} else {
			memStore = memory.New(dir, cfg.CWD)
			// Migrations run here, not in New: New is also called by tools and
			// tests that only want the paths, while this is the one place with
			// exactly one call per session start — the "once, before anything
			// reads a fact" they need.
			//
			// A failure is not fatal, and it does not have to be: the migration
			// records what it copied in an explicit marker written last, so a
			// failed run leaves the codebase store unsealed and the legacy
			// stores untouched, and the next start picks up exactly where this
			// one stopped. What the session must not do is refuse to work —
			// memory is an aid, and a user who cannot open a session cannot
			// even ask what went wrong. It continues with whatever the store
			// already holds, which after a partial copy is a subset of the
			// facts, never a wrong one.
			if err := memStore.Migrate(); err != nil {
				slog.Warn("memory: migration failed, will retry on the next start", "error", err)
			}
			memoryIndex = memStore.FormatIndex(memStore.List())
		}
	}
	if memStore != nil {
		if err := tool.RegisterMemory(toolReg, tool.ToolConfig{
			WorkspaceRoot: cfg.CWD,
			MemoryStore:   memStore,
		}); err != nil {
			slog.Warn("memory: failed to register tool", "error", err)
		}
	}

	// 6. Permission gate. Keep it in yolo mode too: yolo auto-approves normal
	// calls, but the gate still enforces hard-coded prompt-injection exceptions.
	allow := append([]string(nil), moaCfg.Permissions.Allow...)
	allow = append(allow, cfg.ExtraAllowPatterns...)
	// "Always allow" approvals are this user's, not the project's, so they are
	// stored outside the repository and merged in here. Deny still wins over
	// them, exactly as it does for the patterns declared in config.
	if projectState, err := core.LoadProjectState(cfg.CWD); err != nil {
		slog.Warn("project state: cannot read saved approvals", "error", err)
	} else {
		allow = append(allow, projectState.PermissionAllow...)
	}
	permCfg := permission.Config{
		Allow:    allow,
		Deny:     moaCfg.Permissions.Deny,
		Rules:    moaCfg.Permissions.Rules,
		Headless: cfg.Headless,
	}
	if permMode == permission.ModeAuto {
		evalModelSpec := moaCfg.Permissions.Model
		if cfg.PermissionEvalModel != "" {
			evalModelSpec = cfg.PermissionEvalModel
		}
		if evalModelSpec == "" {
			evalModelSpec = "haiku"
		}
		evalModel, _ := core.ResolveModel(evalModelSpec)
		evalProv, evalErr := cfg.ProviderFactory(evalModel)
		if evalErr == nil {
			permCfg.Evaluator = permission.NewEvaluator(evalProv, evalModel)
		} else {
			fmt.Fprintf(os.Stderr, "warning: could not create permission evaluator for %q: %v (falling back to ask mode)\n", evalModelSpec, evalErr)
		}
	}
	gate := permission.New(permMode, permCfg)

	// 7. MCP servers.
	untrustedMCP := false
	mcpPath := filepath.Join(cfg.CWD, ".mcp.json")
	if _, statErr := os.Stat(mcpPath); statErr == nil {
		if core.IsMCPPathTrusted(moaCfg, cfg.CWD) {
			projectServers, loadErr := core.LoadMCPFile(mcpPath)
			if loadErr == nil {
				moaCfg.MCPServers = core.MergeMCPServers(moaCfg.MCPServers, projectServers)
			}
		} else {
			untrustedMCP = true
		}
	}
	// Caller-supplied, session-scoped servers. They are explicitly trusted by
	// whoever passed them (they never come from a file on disk), so they do not
	// participate in the .mcp.json trust gate.
	if len(cfg.ExtraMCPServers) > 0 {
		moaCfg.MCPServers = core.MergeMCPServers(moaCfg.MCPServers, cfg.ExtraMCPServers)
	}
	var mcpMgr *mcp.Manager
	var mcpController *mcp.Controller
	mcpPolicy := core.NewMCPDisablePolicy(mcpDisableSources)
	if len(moaCfg.MCPServers) > 0 {
		mcpMgr = mcp.NewManager(nil, cfg.CWD)
		// Resolve the disabled set BEFORE spawning so a vetoed server never
		// starts a process (e.g. never launches a Chrome for a disabled
		// Playwright). Disabled servers still get a placeholder for the panel.
		mcpMgr.Start(cfg.Ctx, moaCfg.MCPServers, mcpPolicy.DisabledSet())
		for _, t := range mcpMgr.Tools() {
			core.RegisterOrLog(toolReg, t)
		}
		// The Controller coordinates policy + registry + prompt on top of the
		// manager. refreshPrompt is wired by the frontend once its runtime
		// exists (SetRefreshPrompt); until then reconciles just skip the refresh.
		mcpController = mcp.NewController(mcp.ControllerConfig{
			Manager:  mcpMgr,
			Registry: toolReg,
			Policy:   mcpPolicy,
		})
	}

	// 8. Skills.
	skills := skill.Discover(cfg.CWD)
	skillsIndex := skill.FormatIndex(skills)
	if len(skills) > 0 {
		core.RegisterOrLog(toolReg, skill.NewTool(skills))
	}

	// 9. Ask user bridge.
	var askBridge *askuser.Bridge
	if cfg.EnableAskUser {
		askBridge = askuser.NewBridge()
		core.RegisterOrLog(toolReg, askuser.NewTool(askBridge))
	}

	// Build the session struct early so subagent closures can reference it.
	sess := &Session{
		ToolReg:           toolReg,
		SessionCheckpoint: sessionCheckpoint,
		TaskStore:         taskStore,
		AskBridge:         askBridge,
		Gate:              gate,
		MCPManager:        mcpMgr,
		MCPController:     mcpController,
		MCPPolicy:         mcpPolicy,
		PathPolicy:        pathPolicy,
		AgentsMD:          agentsMD,
		Skills:            skills,
		SkillsIndex:       skillsIndex,
		HasVerify:         hasVerify,
		MemoryStore:       memStore,
		Model:             cfg.Model,
		MoaCfg:            moaCfg,
		CWD:               cfg.CWD,
		UntrustedMCP:      untrustedMCP,
		Headless:          cfg.Headless,
	}

	// 10. Subagents.
	subagentJobs, err := subagent.RegisterAll(toolReg, subagent.Config{
		DefaultModel: cfg.Model,
		CurrentModel: func() core.Model {
			if a := sess.agentHolder.Load(); a != nil {
				return a.Model()
			}
			return cfg.Model
		},
		CurrentThinkingLevel: func() string {
			if a := sess.agentHolder.Load(); a != nil {
				return a.ThinkingLevel()
			}
			return cfg.ThinkingLevel
		},
		CurrentPermissionCheck: func() func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if a := sess.agentHolder.Load(); a != nil {
				return a.PermissionCheck()
			}
			if gate != nil {
				return gate.Check
			}
			return nil
		},
		ProviderFactory:     cfg.ProviderFactory,
		AgentsMD:            agentsMD,
		ParentTools:         toolReg,
		AppCtx:              cfg.Ctx,
		WorkspaceRoot:       cfg.CWD,
		SkillsIndex:         skillsIndex,
		MemoryIndex:         memoryIndex,
		BashState:           bashState,
		OnAsyncJobChange:    cfg.OnAsyncJobChange,
		OnAsyncComplete:     cfg.OnAsyncComplete,
		ChildMaxTurns:       moaCfg.SubagentMaxTurns,
		ChildMaxRunDuration: core.GetSubagentMaxRunDuration(moaCfg),
		MaxConcurrentAsync:  moaCfg.SubagentMaxConcurrent,
		OnChildStart:        cfg.OnSubagentStart,
		OnChildEvent:        cfg.OnSubagentEvent,
		OnChildUsage:        cfg.OnSubagentUsage,
		OnChildEnd:          cfg.OnSubagentEnd,
		TitleModel:          cfg.SubagentTitleModel,
		TitleEnabled:        cfg.SubagentTitleEnabled,
		OnChildTitle:        cfg.OnSubagentTitle,
		TranscriptLoader:    cfg.SubagentTranscriptLoader,
	})
	if err != nil {
		if mcpMgr != nil {
			mcpMgr.Close()
		}
		return nil, fmt.Errorf("bootstrap: subagent registration: %w", err)
	}
	sess.Subagents = subagentJobs
	sess.BashJobs = bashJobs

	// 11. Plan mode.
	planSessionDir := cfg.PlanSessionDir
	if planSessionDir == "" {
		planSessionDir = core.ConfigDir()
		if planSessionDir == "" {
			planSessionDir = cfg.CWD // last resort
		}
	}
	reviewModel, reviewThinking, err := resolveReviewConfig(cfg.Model, moaCfg.PlanReviewModel, moaCfg.PlanReviewThinking)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: plan review: %w", err)
	}
	codeReviewModel, codeReviewThinking, err := resolveReviewConfig(reviewModel, moaCfg.CodeReviewModel, moaCfg.CodeReviewThinking)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: code review: %w", err)
	}
	// Code review defaults to plan review settings (not the primary model).
	if moaCfg.CodeReviewThinking == "" {
		codeReviewThinking, err = core.EffectiveThinkingLevel(codeReviewModel, reviewThinking)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: code review: %w", err)
		}
	}

	pm := planmode.New(planmode.Config{
		Registry:   toolReg,
		SessionDir: planSessionDir,
		TaskStore:  taskStore,
		ReviewCfg: planmode.ReviewConfig{
			ProviderFactory: cfg.ProviderFactory,
			Model:           reviewModel,
			ThinkingLevel:   reviewThinking,
			ParentTools:     toolReg,
		},
		CodeReviewCfg: planmode.ReviewConfig{
			ProviderFactory: cfg.ProviderFactory,
			Model:           codeReviewModel,
			ThinkingLevel:   codeReviewThinking,
			ParentTools:     toolReg,
		},
	})
	sess.PlanMode = pm
	sess.Goal = goal.New()

	// 12. System prompt (after ALL tools registered).
	sess.BuildBasePrompt = func(specs []core.ToolSpec) string {
		return agentcontext.BuildSystemPrompt(agentcontext.SystemPromptOptions{
			AgentsMD:    agentsMD,
			Tools:       specs,
			CWD:         cfg.CWD,
			HasVerify:   hasVerify,
			MemoryIndex: memoryIndex,
			SkillsIndex: skillsIndex,
		})
	}
	systemPrompt := sess.BuildBasePrompt(toolReg.Specs())
	sess.SystemPrompt = systemPrompt

	// 13. Agent.
	agentCfg := agent.AgentConfig{
		Provider:            cfg.Provider,
		Model:               cfg.Model,
		SystemPrompt:        systemPrompt,
		ThinkingLevel:       cfg.ThinkingLevel,
		CacheTTL:            core.GetCacheTTL(moaCfg),
		Tools:               toolReg,
		WorkspaceRoot:       cfg.CWD,
		MaxTurns:            maxTurns,
		MaxToolCallsPerTurn: maxToolCallsPerTurn,
		MaxRunDuration:      maxRunDuration,
		MaxBudget:           maxBudget,
		MaterializeContent:  cfg.MaterializeContent,
		SessionCheckpoint:   sessionCheckpoint,
	}
	if gate != nil {
		agentCfg.PermissionCheck = gate.Check
	}
	ag, err := agent.New(agentCfg)
	if err != nil {
		if mcpMgr != nil {
			mcpMgr.Close()
		}
		return nil, fmt.Errorf("bootstrap: agent: %w", err)
	}

	sess.Agent = ag
	sess.agentHolder.Store(ag)
	return sess, nil
}

// resolveReviewConfig resolves the model and thinking level for plan/code review.
// Falls back to the parent model and DefaultReviewThinking when not configured.
func resolveReviewConfig(fallbackModel core.Model, modelSpec, thinkingSpec string) (core.Model, string, error) {
	model := fallbackModel
	if modelSpec != "" {
		if err := core.ValidateModelSpec(modelSpec); err != nil {
			return core.Model{}, "", err
		}
		// An explicit provider/model is a valid custom model even when it has
		// no catalog metadata. Keep that provider: silently retaining the
		// parent here can send a review to a different vendor.
		m, _ := core.ResolveModel(modelSpec)
		model = m
	}
	thinking := DefaultReviewThinking
	if thinkingSpec != "" {
		thinking = thinkingSpec
	}
	effective, err := core.EffectiveThinkingLevel(model, thinking)
	if err != nil {
		return core.Model{}, "", err
	}
	return model, effective, nil
}

// FormatSubagentNotification produces the text injected into the agent's
// conversation when an async subagent completes. Shared between CLI and
// serve. The truncated flag indicates that resultTail is only a portion of
// the full output.
func FormatSubagentNotification(jobID, task, status, resultTail string, truncated bool) string {
	switch status {
	case "completed":
		label := "Result:\n"
		if truncated {
			label = "Result (truncated — use subagent_status for full output):\n"
		}
		return fmt.Sprintf("[subagent completed] Job %s finished.\nTask: %s\n\n%s%s", jobID, task, label, resultTail)
	case "failed":
		return fmt.Sprintf("[subagent failed] Job %s failed.\nTask: %s\nError: %s", jobID, task, resultTail)
	case "cancelled":
		return fmt.Sprintf("[subagent cancelled] Job %s was cancelled.\nTask: %s", jobID, task)
	default:
		return ""
	}
}

// bashNotificationTailLines is how many trailing lines of a completed async
// bash job's output are reinjected into the agent's conversation. Mirrors the
// subagent async result tail.
const bashNotificationTailLines = 50

// FormatBashNotification produces the text injected into the agent's
// conversation when an async background bash job completes. Mirrors
// FormatSubagentNotification so the CLI and serve reinjection paths stay
// symmetric. output is the job's full captured output (already capped at 50KB
// by BashJobs); it is truncated here to the trailing lines with a pointer to
// bash_status for the rest.
func FormatBashNotification(jobID, command, status, output string) string {
	command = firstLine(command)
	switch status {
	case "completed":
		tail, truncated := tailLines(output, bashNotificationTailLines)
		label := "Output:\n"
		if truncated {
			label = "Output (truncated — use bash_status for full output):\n"
		}
		if tail == "" {
			tail = "(no output)"
		}
		return fmt.Sprintf("[bash job completed] Job %s finished.\nCommand: %s\n\n%s%s", jobID, command, label, tail)
	case "failed":
		tail, truncated := tailLines(output, bashNotificationTailLines)
		label := "Output:\n"
		if truncated {
			label = "Output (truncated — use bash_status for full output):\n"
		}
		return fmt.Sprintf("[bash job failed] Job %s failed.\nCommand: %s\n%s%s", jobID, command, label, tail)
	case "cancelled":
		return fmt.Sprintf("[bash job cancelled] Job %s was cancelled.\nCommand: %s", jobID, command)
	default:
		return ""
	}
}

// firstLine returns the first line of s, capped to ~120 runes, so a multi-line
// or very long command stays a stable single-line header.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func tailLines(s string, n int) (string, bool) {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s, false
	}
	return strings.Join(lines[len(lines)-n:], "\n"), true
}
