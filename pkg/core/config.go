package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// IsProjectPathTrusted reports whether path is trusted to auto-load its
// repo-local .moa/config.json and .moa/tools/*. Repo-local config can escalate
// permissions and register shell-executing tools, so — like .mcp.json — it is
// only honored for directories the user has explicitly trusted.
//
// Paths are compared after canonicalization (abs + symlink-resolved) so a dir
// trusted via one spelling still matches when a caller later canonicalizes cwd
// (e.g. the serve path resolves /var → /private/var on macOS).
func IsProjectPathTrusted(cfg MoaConfig, path string) bool {
	target := canonicalOrRaw(path)
	for _, p := range cfg.TrustedProjectPaths {
		if p == path || canonicalOrRaw(p) == target {
			return true
		}
	}
	return false
}

// canonicalOrRaw canonicalizes path, falling back to the raw value if that
// fails (e.g. the directory no longer exists).
func canonicalOrRaw(path string) string {
	if c, err := CanonicalizePath(path); err == nil {
		return c
	}
	return path
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
	DisableSandbox         bool                 `json:"disable_sandbox"`                         // Deprecated: use PathScope. YOLO mode: allow any file path
	AllowedPaths           []string             `json:"allowed_paths"`                           // Additional directories accessible outside workspace
	PathScope              string               `json:"path_scope"`                              // "workspace", "unrestricted", or "" (derive from permission mode)
	Permissions            PermissionsConfig    `json:"permissions"`                             // Tool execution permission policy
	PinnedModels           []string             `json:"pinned_models"`                           // Model IDs pinned for Ctrl+P cycling
	BraveAPIKey            string               `json:"brave_api_key"`                           // Brave Search API key for web_search tool
	MCPServers             map[string]MCPServer `json:"mcp_servers"`                             // MCP tool server connections
	TrustedMCPPaths        []string             `json:"trusted_mcp_paths"`                       // Project paths trusted for .mcp.json auto-load
	TrustedProjectPaths    []string             `json:"trusted_project_paths"`                   // Project paths trusted for .moa/config.json + .moa/tools/* auto-load
	PlanReviewModel        string               `json:"plan_review_model"`                       // Model for plan reviewer (default: current model)
	PlanReviewThinking     string               `json:"plan_review_thinking"`                    // Thinking level for plan reviewer (default: "low")
	CodeReviewModel        string               `json:"code_review_model,omitempty"`             // Model for code reviewer (default: plan review model)
	CodeReviewThinking     string               `json:"code_review_thinking,omitempty"`          // Thinking level for code reviewer (default: plan review thinking)
	MaxBudget              float64              `json:"max_budget"`                              // Max USD per agent run. 0 = unlimited.
	MaxTurns               int                  `json:"max_turns,omitempty"`                     // Max agent turns per run. 0 = unlimited.
	MaxToolCallsPerTurn    int                  `json:"max_tool_calls_per_turn,omitempty"`       // Max tool calls per turn. 0 = unlimited.
	MaxRunDurationStr      string               `json:"max_run_duration,omitempty"`              // Max run duration as Go duration string (e.g. "30m"). Empty = unlimited.
	MemoryEnabled          *bool                `json:"memory_enabled,omitempty"`                // nil = true (enabled by default)
	AutoVerify             *bool                `json:"auto_verify,omitempty"`                   // nil = false (disabled by default)
	PersistentShell        *bool                `json:"persistent_shell,omitempty"`              // nil = true (enabled by default)
	CacheTTL               string               `json:"cache_ttl,omitempty"`                     // Interactive prompt-cache TTL: "5m" (default) or "1h". Only "1h" changes behavior.
	STTLanguage            string               `json:"stt_language,omitempty"`                  // Speech-to-text language as ISO-639-1 (e.g. "es", "en"). Empty = "en"; "auto" lets the model detect.
	SubagentMaxTurns       int                  `json:"subagent_max_turns,omitempty"`            // Max turns per subagent run. 0 = use package default.
	SubagentMaxRunDuration string               `json:"subagent_max_run_duration,omitempty"`     // Max subagent run duration as Go duration string. Empty = use package default.
	SubagentMaxConcurrent  int                  `json:"subagent_max_concurrent_async,omitempty"` // Max concurrent async subagents. 0 = use package default.
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

// IsPersistentShellEnabled returns whether the bash tool persists cwd and
// exported env across calls. Default is true when PersistentShell is nil.
func IsPersistentShellEnabled(cfg MoaConfig) bool {
	if cfg.PersistentShell != nil {
		return *cfg.PersistentShell
	}
	return true
}

// GetCacheTTL returns the prompt-cache TTL for the interactive agent. Only "1h"
// is honored; anything else (including empty or a typo) yields "" — the
// Anthropic default of 5 minutes. Subagents and one-shot calls never use this.
func GetCacheTTL(cfg MoaConfig) string {
	if cfg.CacheTTL == "1h" {
		return "1h"
	}
	return ""
}

// CacheTTLDuration maps the configured cache retention to a concrete window.
// Anthropic's default ephemeral cache lives 5 minutes; the extended window
// ("1h") lives an hour. Each request refreshes the timer, so the cache stays
// warm until the last run + this duration.
func CacheTTLDuration(cfg MoaConfig) time.Duration {
	if GetCacheTTL(cfg) == "1h" {
		return time.Hour
	}
	return 5 * time.Minute
}

// GetSTTLanguage returns the ISO-639-1 language hint for speech-to-text.
// Default is "en" (English) when unset — a safe international default that also
// avoids Whisper mis-detecting short/ambiguous clips. Set "stt_language" in
// config (e.g. "es") to override; "auto" (any case) yields "" so the model
// auto-detects.
//
// The value is normalized to a lowercase two-letter code. Anything that isn't a
// plausible ISO-639-1 code (wrong length, non-letters) falls back to "en" so a
// typo can't turn every transcription into an HTTP 400 from the provider.
func GetSTTLanguage(cfg MoaConfig) string {
	lang := strings.ToLower(strings.TrimSpace(cfg.STTLanguage))
	if lang == "" {
		return "en"
	}
	if lang == "auto" {
		return ""
	}
	if len(lang) != 2 || lang[0] < 'a' || lang[0] > 'z' || lang[1] < 'a' || lang[1] > 'z' {
		return "en"
	}
	return lang
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

// GetSubagentMaxRunDuration parses SubagentMaxRunDuration into a
// time.Duration. Returns 0 (use package default) if empty or invalid.
func GetSubagentMaxRunDuration(cfg MoaConfig) time.Duration {
	if cfg.SubagentMaxRunDuration == "" {
		return 0
	}
	d, err := time.ParseDuration(cfg.SubagentMaxRunDuration)
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

	// The repo-local .moa/config.json can escalate permissions (mode, allow/deny,
	// disable_sandbox) and comes from whatever repo the user happens to be in, so
	// it is only merged for explicitly-trusted directories — mirroring the
	// .mcp.json trust gate. Untrusted dirs get global config only. The interactive
	// trust prompt (CLI) is the sole path that adds a dir to TrustedProjectPaths.
	merged := global
	if IsProjectPathTrusted(global, cwd) {
		project := loadConfigFile(filepath.Join(cwd, ".moa", "config.json"))
		merged = mergeConfigs(global, project)
	}

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
		DisableSandbox:      base.DisableSandbox || override.DisableSandbox,
		AllowedPaths:        append(base.AllowedPaths, override.AllowedPaths...),
		PathScope:           mergeScalar(base.PathScope, override.PathScope),
		PinnedModels:        base.PinnedModels, // global-only preference; project level ignored
		MCPServers:          MergeMCPServers(base.MCPServers, override.MCPServers),
		TrustedMCPPaths:     base.TrustedMCPPaths,     // global-only; persisted via SaveGlobalConfig
		TrustedProjectPaths: base.TrustedProjectPaths, // global-only; persisted via SaveGlobalConfig
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
		CacheTTL:           mergeScalar(base.CacheTTL, override.CacheTTL),
		STTLanguage:        mergeScalar(base.STTLanguage, override.STTLanguage),
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
	// PersistentShell: project overrides global (explicit wins).
	if override.PersistentShell != nil {
		merged.PersistentShell = override.PersistentShell
	} else {
		merged.PersistentShell = base.PersistentShell
	}
	merged.SubagentMaxTurns = mergeScalar(base.SubagentMaxTurns, override.SubagentMaxTurns)
	merged.SubagentMaxRunDuration = mergeScalar(base.SubagentMaxRunDuration, override.SubagentMaxRunDuration)
	merged.SubagentMaxConcurrent = mergeScalar(base.SubagentMaxConcurrent, override.SubagentMaxConcurrent)
	return merged
}

// SaveGlobalConfig reads the current global config, applies update, and writes
// it back atomically. Creates ~/.config/moa/ if it doesn't exist.
func SaveGlobalConfig(update func(*MoaConfig)) error {
	path := globalConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}
	return saveConfigFile(path, update)
}

// SaveProjectConfig reads the current project config, applies update, and writes
// it back atomically. Creates <cwd>/.moa/ if it doesn't exist.
func SaveProjectConfig(cwd string, update func(*MoaConfig)) error {
	return saveConfigFile(filepath.Join(cwd, ".moa", "config.json"), update)
}

// saveConfigFile is the read-modify-write primitive shared by SaveGlobalConfig
// and SaveProjectConfig. It re-reads path from disk, applies update, and writes
// the result back atomically (temp file → rename), creating parent dirs.
func saveConfigFile(path string, update func(*MoaConfig)) error {
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
//
// permMode is expected to be already resolved by the caller: bootstrap defaults
// an unset permission mode to "yolo" (moa's out-of-the-box posture for a
// single-user local tool) BEFORE calling this, so in normal operation the
// empty-mode branch below is never hit and the effective default scope is
// "unrestricted". The empty-mode → "workspace" branch is only a conservative
// fallback for direct callers that pass an unresolved mode; it does NOT reflect
// the CLI default.
//
// Priority:
//  1. Explicit pathScope ("workspace" or "unrestricted") — use as-is
//  2. Legacy disableSandbox: true → "unrestricted"
//  3. Derive from permission mode:
//     - "yolo" or "ask" → "unrestricted"
//     - "auto" → "workspace"
//     - "" (unresolved) → "workspace" (conservative fallback; see note above)
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
