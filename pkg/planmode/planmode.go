// Package planmode implements a plan-then-execute workflow.
//
// The TUI (or serve) creates a PlanMode, passes the agent's tool registry,
// and calls Enter/Exit/StartExecution at the right moments. PlanMode manages
// tool visibility (unregister/re-register) and provides prompt fragments.
package planmode

import (
	"sync"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tasks"
)

// planningAllowlist are the tool names available during planning mode.
// Tools not in this set are unregistered from the agent's registry.
var planningAllowlist = map[string]bool{
	"read":        true,
	"grep":        true,
	"find":        true,
	"ls":          true,
	"bash":        true, // filtered by IsSafeCommand via FilterToolCall
	"write":       true, // restricted to plan file via FilterToolCall
	"edit":        true, // restricted to plan file via FilterToolCall
	"web_search":  true,
	"fetch_content": true,
	"submit_plan":     true,
	"tasks":           true,
	"subagent":        true, // read-only context gathering; children inherit the restricted registry
	"subagent_status": true,
	"subagent_cancel": true,
}

// Config configures a PlanMode instance.
type Config struct {
	Registry      *core.Registry // the agent's tool registry (mutated on enter/exit)
	SessionDir    string         // directory for plan file storage
	ReviewCfg     ReviewConfig   // model/provider for plan reviewer
	CodeReviewCfg ReviewConfig   // model/provider for code reviewer
	TaskStore     *tasks.Store   // shared task store (owned externally)
}

// PlanMode manages the plan-then-execute workflow state and tool visibility.
// All exported methods are safe for concurrent use.
type PlanMode struct {
	mu            sync.Mutex
	state         State
	registry      *core.Registry
	savedTools    map[string]core.Tool // snapshot before planning mode
	sessionDir    string
	reviewCfg     ReviewConfig
	codeReviewCfg ReviewConfig
	taskStore     *tasks.Store

	// onChange is called after state transitions (for TUI/serve status updates).
	// Called with the mutex released.
	onChange func(mode Mode)
}

// New creates a PlanMode instance. The registry is the agent's live tool registry.
func New(cfg Config) *PlanMode {
	return &PlanMode{
		state:         State{Mode: ModeOff},
		registry:      cfg.Registry,
		sessionDir:    cfg.SessionDir,
		reviewCfg:     cfg.ReviewCfg,
		codeReviewCfg: cfg.CodeReviewCfg,
		taskStore:     cfg.TaskStore,
	}
}

// SetOnChange registers a callback fired after every state transition.
func (pm *PlanMode) SetOnChange(fn func(Mode)) {
	pm.mu.Lock()
	pm.onChange = fn
	pm.mu.Unlock()
}

// Mode returns the current plan mode.
func (pm *PlanMode) Mode() Mode {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.state.Mode
}

// PlanFilePath returns the current plan file path, or "" if not in plan mode.
func (pm *PlanMode) PlanFilePath() string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.state.PlanFilePath
}

// Enter switches to planning mode. Generates a plan file, snapshots current
// tools, and switches to the planning-only tool set.
// Returns the plan file path.
func (pm *PlanMode) Enter() (string, error) {
	pm.mu.Lock()

	if pm.state.Mode != ModeOff {
		pm.mu.Unlock()
		return pm.state.PlanFilePath, nil
	}

	// Reuse slug from previous entry in this session, or generate a new one.
	var planPath, slug string
	if pm.state.SessionSlug != "" && pm.state.PlanFilePath != "" {
		slug = pm.state.SessionSlug
		planPath = pm.state.PlanFilePath
	} else {
		var err error
		planPath, slug, err = newPlanPath(pm.sessionDir)
		if err != nil {
			pm.mu.Unlock()
			return "", err
		}
	}

	// Snapshot current tools.
	pm.savedTools = make(map[string]core.Tool)
	for _, t := range pm.registry.All() {
		pm.savedTools[t.Name] = t
	}

	// Switch to planning tool set.
	pm.switchToPlanningTools()

	pm.state.Mode = ModePlanning
	pm.state.PlanFilePath = planPath
	pm.state.SessionSlug = slug
	pm.state.PlanSubmitted = false

	onChange := pm.onChange
	pm.mu.Unlock()

	if onChange != nil {
		onChange(ModePlanning)
	}
	return planPath, nil
}

// Exit restores normal mode. Restores original tools but preserves the slug
// so re-entering plan mode in the same session reuses it.
func (pm *PlanMode) Exit() {
	pm.mu.Lock()
	if pm.state.Mode == ModeOff {
		pm.mu.Unlock()
		return
	}
	pm.restoreTools()
	// Explicitly remove execution-only tools.
	pm.registry.Unregister("request_review")
	slug := pm.state.SessionSlug
	planPath := pm.state.PlanFilePath
	pm.state = State{
		Mode:         ModeOff,
		SessionSlug:  slug,
		PlanFilePath: planPath,
	}
	onChange := pm.onChange
	pm.mu.Unlock()

	if onChange != nil {
		onChange(ModeOff)
	}
}

// OnPlanSubmitted returns true if submit_plan was called since the last check.
// Resets the flag. Used by TUI to detect when to show the action menu.
func (pm *PlanMode) OnPlanSubmitted() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.state.PlanSubmitted {
		pm.state.PlanSubmitted = false
		pm.state.Mode = ModeReady
		// Remove submit_plan — plan is already submitted.
		pm.registry.Unregister("submit_plan")
		return true
	}
	return false
}

// StartExecution restores full tools and sets mode to EXECUTING.
func (pm *PlanMode) StartExecution() {
	pm.mu.Lock()
	pm.restoreTools()
	core.RegisterOrLog(pm.registry, requestReviewTool(pm))
	pm.state.Mode = ModeExecuting
	pm.state.PlanSubmitted = false
	onChange := pm.onChange
	pm.mu.Unlock()

	if onChange != nil {
		onChange(ModeExecuting)
	}
}

// StartReview sets mode to REVIEWING.
func (pm *PlanMode) StartReview() {
	pm.mu.Lock()
	pm.state.Mode = ModeReviewing
	pm.state.ReviewRounds++
	onChange := pm.onChange
	pm.mu.Unlock()

	if onChange != nil {
		onChange(ModeReviewing)
	}
}

// ReviewDone transitions from REVIEWING back to READY.
func (pm *PlanMode) ReviewDone() {
	pm.mu.Lock()
	pm.state.Mode = ModeReady
	onChange := pm.onChange
	pm.mu.Unlock()

	if onChange != nil {
		onChange(ModeReady)
	}
}

// ContinueRefining transitions from READY back to PLANNING.
func (pm *PlanMode) ContinueRefining() {
	pm.mu.Lock()
	pm.state.Mode = ModePlanning
	pm.state.PlanSubmitted = false
	// Re-register submit_plan (removed when plan was submitted).
	core.RegisterOrLog(pm.registry, submitPlanTool(pm))
	onChange := pm.onChange
	pm.mu.Unlock()

	if onChange != nil {
		onChange(ModePlanning)
	}
}

// FilterToolCall checks if a tool call is allowed in planning/ready/reviewing modes.
// Returns (allowed, reason). Allows everything in OFF and EXECUTING modes.
func (pm *PlanMode) FilterToolCall(toolName string, args map[string]any) (bool, string) {
	pm.mu.Lock()
	mode := pm.state.Mode
	planFile := pm.state.PlanFilePath
	pm.mu.Unlock()

	// Only restrict during planning phases (planning, ready, reviewing).
	// OFF and EXECUTING get full access.
	if mode == ModeOff || mode == ModeExecuting {
		return true, ""
	}

	// Bash: must be a safe (read-only) command.
	if toolName == "bash" {
		cmd, _ := args["command"].(string)
		if !IsSafeCommand(cmd) {
			return false, "In plan mode, bash is read-only. Destructive commands and shell operators are blocked."
		}
		return true, ""
	}

	// Write/edit: restricted to plan file only.
	if toolName == "write" || toolName == "edit" {
		path, _ := args["path"].(string)
		if path != planFile {
			return false, "In plan mode, you can only write/edit the plan file: " + planFile
		}
		return true, ""
	}

	// Multiedit, apply_patch, memory: blocked in planning mode (defense-in-depth;
	// they're also excluded from planningAllowlist so shouldn't be registered).
	if toolName == "multiedit" || toolName == "apply_patch" || toolName == "memory" {
		return false, "In plan mode, file-modifying tools are restricted. Use edit to modify only the plan file."
	}

	return true, ""
}

// TaskStore returns the shared task store (may be nil).
func (pm *PlanMode) TaskStore() *tasks.Store {
	return pm.taskStore
}

// SubmitPlanTool returns the submit_plan tool definition.
func (pm *PlanMode) SubmitPlanTool() core.Tool {
	return submitPlanTool(pm)
}

// SaveState returns the state for session.Metadata persistence.
func (pm *PlanMode) SaveState() map[string]any {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return map[string]any{
		metadataKey: pm.state.SaveToMetadata(),
	}
}

// RestoreState loads state from session.Metadata.
// Does NOT apply runtime effects (tool switching, etc.) — call ApplyRestoredState()
// after restoring to sync the runtime.
func (pm *PlanMode) RestoreState(meta map[string]any) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.state = RestoreFromMetadata(meta)
}

// ApplyRestoredState syncs the runtime (tool registry) with the restored state.
// Must be called after RestoreState() and after tools are registered.
// For PLANNING/READY/REVIEWING: snapshots tools and switches to planning set.
// For EXECUTING: registers the request_review tool.
// For OFF: no-op.
func (pm *PlanMode) ApplyRestoredState() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	switch pm.state.Mode {
	case ModePlanning:
		// Snapshot current tools and switch to planning set (includes submit_plan).
		pm.savedTools = make(map[string]core.Tool)
		for _, t := range pm.registry.All() {
			pm.savedTools[t.Name] = t
		}
		pm.switchToPlanningTools()

	case ModeReady, ModeReviewing:
		// Snapshot current tools and switch to ready set (no submit_plan).
		pm.savedTools = make(map[string]core.Tool)
		for _, t := range pm.registry.All() {
			pm.savedTools[t.Name] = t
		}
		pm.switchToReadyTools()

	case ModeExecuting:
		// Full tools available — register execution-only tools.
		core.RegisterOrLog(pm.registry, requestReviewTool(pm))

	case ModeOff:
		// No-op.
	}
}

// GetReviewConfig returns the plan review configuration.
func (pm *PlanMode) GetReviewConfig() ReviewConfig {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.reviewCfg
}

// --- internal ---

// switchToPlanningTools unregisters non-planning tools and registers plan-mode tools.
// Called with pm.mu held.
func (pm *PlanMode) switchToPlanningTools() {
	// Unregister everything.
	for _, t := range pm.registry.All() {
		pm.registry.Unregister(t.Name)
	}
	// Re-register only planning-allowed tools from the snapshot.
	for name, t := range pm.savedTools {
		if planningAllowlist[name] {
			core.RegisterOrLog(pm.registry, t)
		}
	}
	// Register plan-mode-specific tools.
	core.RegisterOrLog(pm.registry, submitPlanTool(pm))
}

// switchToReadyTools sets up read-only tools (no submit_plan).
// Used in ModeReady/ModeReviewing where the plan is already submitted.
// Called with pm.mu held.
func (pm *PlanMode) switchToReadyTools() {
	for _, t := range pm.registry.All() {
		pm.registry.Unregister(t.Name)
	}
	for name, t := range pm.savedTools {
		if planningAllowlist[name] {
			core.RegisterOrLog(pm.registry, t)
		}
	}
}

// restoreTools restores the original tool set from the snapshot.
// Does NOT add any plan-mode-specific tools — callers register those separately.
// Called with pm.mu held.
func (pm *PlanMode) restoreTools() {
	if pm.savedTools == nil {
		return
	}
	// Unregister everything (including plan-mode-specific tools).
	for _, t := range pm.registry.All() {
		pm.registry.Unregister(t.Name)
	}
	// Re-register original tools.
	for _, t := range pm.savedTools {
		core.RegisterOrLog(pm.registry, t)
	}
	pm.savedTools = nil
}
