// Package bootstrap wires up a complete agent session: tool registry, MCP,
// permissions, subagents, plan mode, skills, verify, and system prompt.
//
// Both the CLI (cmd/agent) and the HTTP server (pkg/serve) call BuildSession
// to avoid duplicating the 14-step setup sequence.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/askuser"
	agentcontext "github.com/ealeixandre/moa/pkg/context"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/mcp"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/skill"
	"github.com/ealeixandre/moa/pkg/subagent"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/tool"
	"github.com/ealeixandre/moa/pkg/verify"
)

// Default review thinking level for plan mode (shared between CLI and serve).
const DefaultReviewThinking = "medium"

// SessionConfig configures a session build. Most fields have sensible defaults.
type SessionConfig struct {
	// Required.
	CWD             string                                   // Working directory. Must exist and be a directory.
	Model           core.Model                               // Resolved LLM model.
	Provider        core.Provider                             // LLM provider for the primary model.
	ProviderFactory func(core.Model) (core.Provider, error)  // Creates providers for subagents, plan review, etc.

	// Config overrides. When nil, loaded from disk via core.LoadMoaConfig(CWD).
	MoaCfg *core.MoaConfig

	// Context for MCP servers and subagent async jobs. Required.
	Ctx context.Context

	// Agent tuning. Zero values use package defaults.
	ThinkingLevel       string        // Default: "medium"
	MaxTurns            int           // Default: 50
	MaxToolCallsPerTurn int           // Default: 20
	MaxRunDuration      time.Duration // Default: 30m
	MaxBudget           float64       // Default: from config. 0 = unlimited.
	DisableSandbox      bool          // Overrides config (OR'd).

	// Permission mode override. Empty = from config or "yolo".
	PermissionMode string
	// Model spec for auto-mode AI evaluator. Empty = "haiku".
	PermissionEvalModel string

	// PlanMode session dir. If empty, uses CWD.
	PlanSessionDir string

	// Feature toggles. All default to true.
	EnableAskUser bool // Register ask_user tool. Default: true.

	// BeforeWrite is called before write/edit tools modify a file.
	// Used by the checkpoint system to capture pre-edit state.
	BeforeWrite func(path string) error

	// Subagent callbacks. All optional (nil = no-op).
	OnAsyncJobChange func(count int)
	OnAsyncComplete  func(jobID, task, status, resultTail string)
}

// Session is a fully wired session ready for agent.Run/Send.
type Session struct {
	Agent       *agent.Agent
	ToolReg     *core.Registry
	TaskStore   *tasks.Store
	PlanMode    *planmode.PlanMode
	AskBridge   *askuser.Bridge
	Gate        *permission.Gate
	MCPManager  *mcp.Manager
	AgentsMD    string
	Skills      []skill.Skill
	SkillsIndex string
	SystemPrompt string
	HasVerify   bool
	Model       core.Model
	MoaCfg      core.MoaConfig

	// UntrustedMCP is true when .mcp.json exists but CWD is not in TrustedMCPPaths.
	UntrustedMCP bool

	// AgentHolder exposes the atomic agent pointer for subagent closures.
	// Set after BuildSession returns. Callers must call StoreAgent(ag) before
	// using subagents that depend on parent model/thinking/permissions.
	agentHolder atomic.Pointer[agent.Agent]
}

// StoreAgent updates the agent pointer visible to subagent closures.
// Must be called after agent creation and any reconfiguration.
func (s *Session) StoreAgent(ag *agent.Agent) {
	s.agentHolder.Store(ag)
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

	// Apply defaults.
	if cfg.ThinkingLevel == "" {
		cfg.ThinkingLevel = "medium"
	}
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = 50
	}
	if cfg.MaxToolCallsPerTurn == 0 {
		cfg.MaxToolCallsPerTurn = 20
	}
	if cfg.MaxRunDuration == 0 {
		cfg.MaxRunDuration = 30 * time.Minute
	}

	// 1. Load config.
	var moaCfg core.MoaConfig
	if cfg.MoaCfg != nil {
		moaCfg = *cfg.MoaCfg
	} else {
		moaCfg = core.LoadMoaConfig(cfg.CWD)
	}

	// Budget: config default, overridden by explicit SessionConfig value.
	maxBudget := moaCfg.MaxBudget
	if cfg.MaxBudget > 0 {
		maxBudget = cfg.MaxBudget
	}

	// 2. Tool registry.
	toolReg := core.NewRegistry()
	if err := tool.RegisterBuiltins(toolReg, tool.ToolConfig{
		WorkspaceRoot:  cfg.CWD,
		DisableSandbox: cfg.DisableSandbox || moaCfg.DisableSandbox,
		AllowedPaths:   append(moaCfg.AllowedPaths, tool.SpillOutputDir()),
		BashTimeout:    5 * time.Minute,
		BraveAPIKey:    moaCfg.BraveAPIKey,
		BeforeWrite:    cfg.BeforeWrite,
	}); err != nil {
		return nil, fmt.Errorf("register builtins: %w", err)
	}

	// 2b. Script tools from .moa/tools/*.json.
	if err := tool.RegisterScriptTools(toolReg, cfg.CWD); err != nil {
		fmt.Fprintf(os.Stderr, "warning: script tools: %v\n", err)
	}

	// 3. Task store — always available.
	taskStore := tasks.NewStore()
	core.RegisterOrLog(toolReg, tasks.NewTool(taskStore))

	// 4. Verify tool.
	verifyCfg, verifyErr := verify.LoadConfig(cfg.CWD)
	if verifyErr != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid .moa/verify.json in %s: %v\n", cfg.CWD, verifyErr)
	}
	hasVerify := verifyCfg != nil
	if hasVerify {
		core.RegisterOrLog(toolReg, verify.NewTool(cfg.CWD))
	}

	// 5. AGENTS.md.
	agentsMD, _ := agentcontext.LoadAgentsMD(cfg.CWD, os.Getenv("AGENT_HOME"))

	// 6. Permission gate.
	permMode := permission.Mode(moaCfg.Permissions.Mode)
	if cfg.PermissionMode != "" {
		permMode = permission.Mode(cfg.PermissionMode)
	}
	if permMode == "" {
		permMode = permission.ModeYolo
	}
	var gate *permission.Gate
	if permMode != permission.ModeYolo {
		permCfg := permission.Config{
			Allow: moaCfg.Permissions.Allow,
			Deny:  moaCfg.Permissions.Deny,
			Rules: moaCfg.Permissions.Rules,
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
			}
		}
		gate = permission.New(permMode, permCfg)
	}

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
	var mcpMgr *mcp.Manager
	if len(moaCfg.MCPServers) > 0 {
		mcpMgr = mcp.NewManager(nil)
		mcpMgr.Start(cfg.Ctx, moaCfg.MCPServers)
		for _, t := range mcpMgr.Tools() {
			core.RegisterOrLog(toolReg, t)
		}
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
		ToolReg:      toolReg,
		TaskStore:    taskStore,
		AskBridge:    askBridge,
		Gate:         gate,
		MCPManager:   mcpMgr,
		AgentsMD:     agentsMD,
		Skills:       skills,
		SkillsIndex:  skillsIndex,
		HasVerify:    hasVerify,
		Model:        cfg.Model,
		MoaCfg:       moaCfg,
		UntrustedMCP: untrustedMCP,
	}

	// 10. Subagents.
	if err := subagent.RegisterAll(toolReg, subagent.Config{
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
		ProviderFactory: cfg.ProviderFactory,
		AgentsMD:        agentsMD,
		ParentTools:     toolReg,
		AppCtx:          cfg.Ctx,
		WorkspaceRoot:   cfg.CWD,
		SkillsIndex:     skillsIndex,
		OnAsyncJobChange: cfg.OnAsyncJobChange,
		OnAsyncComplete:  cfg.OnAsyncComplete,
	}); err != nil {
		if mcpMgr != nil {
			mcpMgr.Close()
		}
		return nil, fmt.Errorf("bootstrap: subagent registration: %w", err)
	}

	// 11. Plan mode.
	planSessionDir := cfg.PlanSessionDir
	if planSessionDir == "" {
		planSessionDir = cfg.CWD
	}
	reviewModel, reviewThinking := resolveReviewConfig(cfg.Model, moaCfg.PlanReviewModel, moaCfg.PlanReviewThinking)
	codeReviewModel, codeReviewThinking := resolveReviewConfig(reviewModel, moaCfg.CodeReviewModel, moaCfg.CodeReviewThinking)
	// Code review defaults to plan review settings (not the primary model).
	if moaCfg.CodeReviewThinking == "" {
		codeReviewThinking = reviewThinking
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

	// 12. System prompt (after ALL tools registered).
	systemPrompt := agentcontext.BuildSystemPrompt(agentsMD, toolReg.Specs(), cfg.CWD, hasVerify, skillsIndex)
	sess.SystemPrompt = systemPrompt

	// 13. Agent.
	agentCfg := agent.AgentConfig{
		Provider:            cfg.Provider,
		Model:               cfg.Model,
		SystemPrompt:        systemPrompt,
		ThinkingLevel:       cfg.ThinkingLevel,
		Tools:               toolReg,
		WorkspaceRoot:       cfg.CWD,
		MaxTurns:            cfg.MaxTurns,
		MaxToolCallsPerTurn: cfg.MaxToolCallsPerTurn,
		MaxRunDuration:      cfg.MaxRunDuration,
		MaxBudget:           maxBudget,
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
func resolveReviewConfig(fallbackModel core.Model, modelSpec, thinkingSpec string) (core.Model, string) {
	model := fallbackModel
	if modelSpec != "" {
		if m, ok := core.ResolveModel(modelSpec); ok {
			model = m
		}
	}
	thinking := DefaultReviewThinking
	if thinkingSpec != "" {
		thinking = thinkingSpec
	}
	return model, thinking
}

// FormatSubagentNotification produces the text injected into the agent's
// conversation when an async subagent completes. Shared between CLI and serve.
func FormatSubagentNotification(jobID, task, status, resultTail string) string {
	switch status {
	case "completed":
		return fmt.Sprintf("[subagent completed] Job %s finished.\nTask: %s\n\nResult (last 50 lines):\n%s", jobID, task, resultTail)
	case "failed":
		return fmt.Sprintf("[subagent failed] Job %s failed.\nTask: %s\nError: %s", jobID, task, resultTail)
	case "cancelled":
		return fmt.Sprintf("[subagent cancelled] Job %s was cancelled.\nTask: %s", jobID, task)
	default:
		return ""
	}
}
