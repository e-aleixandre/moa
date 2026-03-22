package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// IsMCPPathTrusted reports whether path is in the config's trusted MCP paths.
func IsMCPPathTrusted(cfg MoaConfig, path string) bool {
	for _, p := range cfg.TrustedMCPPaths {
		if p == path {
			return true
		}
	}
	return false
}

// CanonicalizePath returns a clean, absolute, symlink-resolved path.
// Falls back to Abs+Clean if EvalSymlinks fails (e.g., broken symlinks).
func CanonicalizePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved, nil
	}
	return clean, nil
}

// MoaConfig holds sandbox, path, and permission settings. Loaded from config
// files at three levels: global (~/.config/moa/config.json), project (<cwd>/.moa/config.json),
// and session (flags). Merged with OR for booleans, concatenation for slices.
type MoaConfig struct {
	DisableSandbox  bool              `json:"disable_sandbox"`  // Deprecated: use PathScope. YOLO mode: allow any file path
	AllowedPaths    []string          `json:"allowed_paths"`    // Additional directories accessible outside workspace
	PathScope       string            `json:"path_scope"`       // "workspace", "unrestricted", or "" (derive from permission mode)
	Permissions     PermissionsConfig `json:"permissions"`      // Tool execution permission policy
	PinnedModels    []string          `json:"pinned_models"`    // Model IDs pinned for Ctrl+P cycling
	BraveAPIKey     string            `json:"brave_api_key"`    // Brave Search API key for web_search tool
	MCPServers      map[string]MCPServer `json:"mcp_servers"`   // MCP tool server connections
	TrustedMCPPaths    []string          `json:"trusted_mcp_paths"`    // Project paths trusted for .mcp.json auto-load
	PlanReviewModel    string            `json:"plan_review_model"`    // Model for plan reviewer (default: current model)
	PlanReviewThinking string            `json:"plan_review_thinking"` // Thinking level for plan reviewer (default: "low")
	CodeReviewModel    string            `json:"code_review_model,omitempty"`    // Model for code reviewer (default: plan review model)
	CodeReviewThinking string            `json:"code_review_thinking,omitempty"` // Thinking level for code reviewer (default: plan review thinking)
	MaxBudget            float64           `json:"max_budget"`                       // Max USD per agent run. 0 = unlimited.
	MaxTurns             int               `json:"max_turns,omitempty"`              // Max agent turns per run. 0 = unlimited.
	MaxToolCallsPerTurn  int               `json:"max_tool_calls_per_turn,omitempty"` // Max tool calls per turn. 0 = unlimited.
	MaxRunDurationStr    string            `json:"max_run_duration,omitempty"`        // Max run duration as Go duration string (e.g. "30m"). Empty = unlimited.
	MemoryEnabled        *bool             `json:"memory_enabled,omitempty"`         // nil = true (enabled by default)
	AutoVerify           *bool             `json:"auto_verify,omitempty"`            // nil = false (disabled by default)
}

// IsMemoryEnabled returns whether cross-session memory is enabled.
// Default is true when MemoryEnabled is nil (not configured).
func IsMemoryEnabled(cfg MoaConfig) bool {
	if cfg.MemoryEnabled != nil {
		return *cfg.MemoryEnabled
	}
	return true
}

// IsAutoVerifyEnabled returns whether auto-verify is enabled.
// Default is false when AutoVerify is nil (not configured).
func IsAutoVerifyEnabled(cfg MoaConfig) bool {
	return cfg.AutoVerify != nil && *cfg.AutoVerify
}

// GetMaxRunDuration parses MaxRunDurationStr into a time.Duration.
// Returns 0 (unlimited) if empty or invalid.
func GetMaxRunDuration(cfg MoaConfig) time.Duration {
	if cfg.MaxRunDurationStr == "" {
		return 0
	}
	d, err := time.ParseDuration(cfg.MaxRunDurationStr)
	if err != nil {
		return 0
	}
	return d
}

// MCPServer defines an MCP tool server connection (stdio transport).
type MCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// PermissionsConfig controls tool execution approval.
type PermissionsConfig struct {
	Mode  string   `json:"mode"`  // "yolo", "ask", or "auto" (default: "yolo")
	Allow []string `json:"allow"` // Glob patterns auto-approved in ask mode: "Bash(npm:*)", "edit"
	Deny  []string `json:"deny"`  // Glob patterns always denied (checked before allow)
	Model string   `json:"model"` // Model for auto mode evaluator (e.g. "haiku")
	Rules []string `json:"rules"` // Natural language rules for auto mode
}

// LoadMoaConfig reads and merges config from global and project levels.
// Global: ~/.config/moa/config.json. Project: <cwd>/.moa/config.json.
// Project values override/extend global values.
// Also loads global .mcp.json (always). Project .mcp.json is handled
// separately in main.go behind a trust gate.
func LoadMoaConfig(cwd string) MoaConfig {
	global := loadConfigFile(globalConfigPath())
	project := loadConfigFile(filepath.Join(cwd, ".moa", "config.json"))
	merged := mergeConfigs(global, project)

	// Load global .mcp.json (always trusted).
	globalDir := filepath.Dir(globalConfigPath())
	if globalDir != "" && globalDir != "." {
		globalMCP, err := LoadMCPFile(filepath.Join(globalDir, ".mcp.json"))
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: invalid %s: %v\n",
				filepath.Join(globalDir, ".mcp.json"), err)
		}
		merged.MCPServers = MergeMCPServers(merged.MCPServers, globalMCP)
	}

	return merged
}

func globalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "moa", "config.json")
}

func loadConfigFile(path string) MoaConfig {
	if path == "" {
		return MoaConfig{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return MoaConfig{}
	}
	var cfg MoaConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: corrupt config %s: %v\n", path, err)
		return MoaConfig{}
	}
	return cfg
}

// mergeScalar returns override if non-zero, otherwise base.
func mergeScalar[T comparable](base, override T) T {
	var zero T
	if override != zero {
		return override
	}
	return base
}

func mergeConfigs(base, override MoaConfig) MoaConfig {
	merged := MoaConfig{
		DisableSandbox:  base.DisableSandbox || override.DisableSandbox,
		AllowedPaths:    append(base.AllowedPaths, override.AllowedPaths...),
		PathScope:       mergeScalar(base.PathScope, override.PathScope),
		PinnedModels:    base.PinnedModels, // global-only preference; project level ignored
		MCPServers:      MergeMCPServers(base.MCPServers, override.MCPServers),
		TrustedMCPPaths: base.TrustedMCPPaths, // global-only; persisted via SaveGlobalConfig
		Permissions: PermissionsConfig{
			Mode:  mergeScalar(base.Permissions.Mode, override.Permissions.Mode),
			Model: mergeScalar(base.Permissions.Model, override.Permissions.Model),
			Allow: append(base.Permissions.Allow, override.Permissions.Allow...),
			Deny:  append(base.Permissions.Deny, override.Permissions.Deny...),
			Rules: append(base.Permissions.Rules, override.Permissions.Rules...),
		},
		BraveAPIKey:        mergeScalar(base.BraveAPIKey, override.BraveAPIKey),
		PlanReviewModel:    mergeScalar(base.PlanReviewModel, override.PlanReviewModel),
		PlanReviewThinking: mergeScalar(base.PlanReviewThinking, override.PlanReviewThinking),
		CodeReviewModel:    mergeScalar(base.CodeReviewModel, override.CodeReviewModel),
		CodeReviewThinking: mergeScalar(base.CodeReviewThinking, override.CodeReviewThinking),
	}
	// MaxBudget: project can tighten but not disable a global budget.
	if override.MaxBudget > 0 {
		merged.MaxBudget = override.MaxBudget
	} else {
		merged.MaxBudget = base.MaxBudget
	}
	// MemoryEnabled: project overrides global (explicit wins).
	if override.MemoryEnabled != nil {
		merged.MemoryEnabled = override.MemoryEnabled
	} else {
		merged.MemoryEnabled = base.MemoryEnabled
	}
	// AutoVerify: project overrides global (explicit wins).
	if override.AutoVerify != nil {
		merged.AutoVerify = override.AutoVerify
	} else {
		merged.AutoVerify = base.AutoVerify
	}
	return merged
}

// SaveGlobalConfig reads the current global config, applies update, and writes
// it back atomically. Creates ~/.config/moa/ if it doesn't exist.
func SaveGlobalConfig(update func(*MoaConfig)) error {
	path := globalConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	cfg := loadConfigFile(path)
	update(&cfg)

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	// Atomic write: temp file in same dir → rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}

// mcpFileFormat matches Claude Code's .mcp.json structure.
type mcpFileFormat struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

// LoadMCPFile reads a .mcp.json file. Returns nil map if file doesn't exist.
// Returns error for parse failures so callers can warn the user.
func LoadMCPFile(path string) (map[string]MCPServer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f mcpFileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return f.MCPServers, nil
}

// MergeMCPServers merges server maps. Later maps override earlier ones by name
// (full replacement, not field-level merge).
func MergeMCPServers(maps ...map[string]MCPServer) map[string]MCPServer {
	result := make(map[string]MCPServer)
	for _, m := range maps {
		for k, v := range m {
			result[k] = v
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// ResolvePathScope determines the effective path scope from config values.
// Priority:
//  1. Explicit pathScope ("workspace" or "unrestricted") — use as-is
//  2. Legacy disableSandbox: true → "unrestricted"
//  3. Derive from permission mode:
//     - "yolo" or "ask" → "unrestricted"
//     - "auto" or "" → "workspace"
func ResolvePathScope(pathScope string, disableSandbox bool, permMode string) string {
	if pathScope == "workspace" || pathScope == "unrestricted" {
		return pathScope
	}
	if disableSandbox {
		return "unrestricted"
	}
	switch permMode {
	case "yolo", "ask":
		return "unrestricted"
	default:
		return "workspace"
	}
}
