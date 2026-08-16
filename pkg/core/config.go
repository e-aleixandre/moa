package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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

// CanonicalOrRaw is the exported form of canonicalOrRaw: a clean, absolute,
// symlink-resolved path, falling back to the input on failure. Used to compare
// session working directories when fanning out project-scoped preferences.
func CanonicalOrRaw(path string) string { return canonicalOrRaw(path) }

// LoadGlobalConfig loads only the user's global moa config
// (~/.config/moa/config.json), without merging any project config. Callers that
// need the trusted-project allowlist or global-only settings use this.
func LoadGlobalConfig() MoaConfig {
	return loadConfigFile(globalConfigPath())
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
	DisabledMCPServers     []string             `json:"disabled_mcp_servers,omitempty"`          // MCP server names vetoed at this config level (server stays configured but is not started)
	TrustedMCPPaths        []string             `json:"trusted_mcp_paths"`                       // Project paths trusted for .mcp.json auto-load
	TrustedProjectPaths    []string             `json:"trusted_project_paths"`                   // Project paths trusted for .moa/config.json + .moa/tools/* auto-load
	PlanReviewModel        string               `json:"plan_review_model"`                       // Model for plan reviewer (default: current model)
	PlanReviewThinking     string               `json:"plan_review_thinking"`                    // Thinking level for plan reviewer (default: "low")
	CodeReviewModel        string               `json:"code_review_model,omitempty"`             // Model for code reviewer (default: plan review model)
	CodeReviewThinking     string               `json:"code_review_thinking,omitempty"`          // Thinking level for code reviewer (default: plan review thinking)
	AutoTitleModel         string               `json:"auto_title_model,omitempty"`              // "auto", "off", or model spec for automatic session titles
	SessionBriefModel      string               `json:"session_brief_model,omitempty"`           // "auto", "off", or model spec for web/Pulse session briefs
	MaxBudget              float64              `json:"max_budget"`                              // Max USD per agent run. 0 = unlimited.
	MaxTurns               int                  `json:"max_turns,omitempty"`                     // Max agent turns per run. 0 = unlimited.
	MaxToolCallsPerTurn    int                  `json:"max_tool_calls_per_turn,omitempty"`       // Max tool calls per turn. 0 = unlimited.
	MaxRunDurationStr      string               `json:"max_run_duration,omitempty"`              // Max run duration as Go duration string (e.g. "30m"). Empty = unlimited.
	MemoryEnabled          *bool                `json:"memory_enabled,omitempty"`                // nil = true (enabled by default)
	AutoVerify             *bool                `json:"auto_verify,omitempty"`                   // nil = false (disabled by default)
	PersistentShell        *bool                `json:"persistent_shell,omitempty"`              // nil = true (enabled by default)
	UpdateCheck            *bool                `json:"update_check,omitempty"`                  // nil = true (check stable releases at most every 6h)
	CacheTTL               string               `json:"cache_ttl,omitempty"`                     // Interactive prompt-cache TTL: "5m" (default) or "1h". Only "1h" changes behavior.
	STTLanguage            string               `json:"stt_language,omitempty"`                  // Speech-to-text language as ISO-639-1 (e.g. "es", "en"). Empty = "en"; "auto" lets the model detect.
	STTModel               string               `json:"stt_model,omitempty"`                     // Speech-to-text model id. Empty = "gpt-transcribe".
	STTVocabulary          []string             `json:"stt_vocabulary,omitempty"`                // Words the transcriber tends to get wrong (names, jargon). Keep it short: long lists hurt accuracy.
	SubagentMaxTurns       int                  `json:"subagent_max_turns,omitempty"`            // Max turns per subagent run. 0 = use package default.
	SubagentMaxRunDuration string               `json:"subagent_max_run_duration,omitempty"`     // Max subagent run duration as Go duration string. Empty = use package default.
	SubagentMaxConcurrent  int                  `json:"subagent_max_concurrent_async,omitempty"` // Max concurrent async subagents. 0 = use package default.
	SubagentAllowedModels  []string             `json:"subagent_allowed_models,omitempty"`       // Model IDs a subagent may run under. Empty/absent = no restriction (opt-in).
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

// IsUpdateCheckEnabled returns whether release update checks are enabled.
// They are enabled by default; MOA_NO_UPDATE_CHECK=1 is handled by pkg/release.
func IsUpdateCheckEnabled(cfg MoaConfig) bool {
	return cfg.UpdateCheck == nil || *cfg.UpdateCheck
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

// DefaultSTTModel is the speech-to-text model used when none is configured.
//
// gpt-transcribe replaced whisper-1 as the default: on our own Spanish dictation
// it was faster, 25% cheaper per minute, and noticeably better at technical
// names (whisper turned "MCP" into "MSP" and "goreleaser" into "Gore Leaser").
const DefaultSTTModel = "gpt-transcribe"

// GetSTTModel returns the speech-to-text model id to send to the provider.
//
// Set "stt_model" in config to try another one (e.g. "whisper-1" to go back, or
// "gpt-4o-mini-transcribe" for half the price) without needing a new build:
// these models appear and change price faster than moa releases.
func GetSTTModel(cfg MoaConfig) string {
	if model := strings.TrimSpace(cfg.STTModel); model != "" {
		return model
	}
	return DefaultSTTModel
}

// sttVocabularyLimit caps how many terms reach the provider.
//
// This is a quality ceiling, not an API one. Every vendor that offers custom
// vocabulary warns that long lists make transcription WORSE — the model starts
// forcing your terms onto words that merely sound similar, and it can also
// disturb punctuation and language detection. Deepgram recommends 20-50 terms;
// we take the top of that range and keep the first ones, since people write the
// words that actually fail first.
const sttVocabularyLimit = 50

// BuildSTTPrompt turns a configured vocabulary into the provider's prompt hint.
//
// The prompt is a hint, not a substitution: it biases spelling toward these
// words without forcing them. We send it as a plain comma-separated list, the
// shape OpenAI documents for "a list of correct spellings".
//
// It deliberately uses "prompt" rather than the newer "keywords" field: whisper-1
// rejects keywords outright, and the model is user-configurable, so a vocabulary
// that only works on some models would be a trap. Both produce the same result
// in practice.
//
// Terms are trimmed, blank ones dropped, and duplicates removed case-insensitively
// (keeping the first spelling, which is the one the user cared to write).
func BuildSTTPrompt(vocabulary []string) string {
	if len(vocabulary) == 0 {
		return ""
	}
	terms := make([]string, 0, len(vocabulary))
	seen := make(map[string]bool, len(vocabulary))
	for _, term := range vocabulary {
		term = strings.TrimSpace(term)
		// Newlines would break the multipart field, and a comma inside a term
		// would read as two terms; both are user typos, so normalize to spaces.
		term = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ',' {
				return ' '
			}
			return r
		}, term)
		term = strings.Join(strings.Fields(term), " ")
		if term == "" {
			continue
		}
		key := strings.ToLower(term)
		if seen[key] {
			continue
		}
		seen[key] = true
		terms = append(terms, term)
		if len(terms) == sttVocabularyLimit {
			break
		}
	}
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, ", ")
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

// MCPServer defines an MCP tool server connection. A server is EITHER
// command-based (stdio: a local subprocess) OR url-based (streamable HTTP: a
// remote endpoint). Setting both, or neither, is a configuration error.
type MCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	// URL is the streamable-HTTP endpoint of a remote MCP server. Only http and
	// https are accepted. It is an outbound connection to an endpoint the
	// operator configured, so it carries the same trust as the rest of the file.
	URL string `json:"url"`
	// Headers are extra HTTP headers sent on every request to URL (typically
	// Authorization). Ignored for command-based servers.
	Headers map[string]string `json:"headers"`
}

// IsRemote reports whether the server is reached over HTTP rather than spawned
// as a subprocess.
func (s MCPServer) IsRemote() bool { return s.URL != "" }

// Validate checks that the entry describes exactly one transport, and that a
// remote one points at an absolute http(s) URL.
func (s MCPServer) Validate() error {
	if s.Command != "" && s.URL != "" {
		return errors.New(`set either "command" or "url", not both`)
	}
	if s.Command == "" && s.URL == "" {
		return errors.New(`missing "command" or "url"`)
	}
	if s.URL == "" {
		return nil
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("invalid url: %v", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an absolute http or https URL")
	}
	if u.User != nil {
		// Userinfo would travel in every log line and metadata dump that shows
		// the endpoint. Credentials belong in headers, which are treated as
		// secrets.
		return errors.New("credentials in URL not allowed; use headers")
	}
	return nil
}

// ValidateMCPServers checks every entry of a server map, reporting the first
// offending name (in name order, so the message is stable) so the user can fix
// the file.
func ValidateMCPServers(servers map[string]MCPServer) error {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := servers[name].Validate(); err != nil {
			return fmt.Errorf("mcp server %q: %w", name, err)
		}
	}
	return nil
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

	// Your own settings for this project, kept with your config rather than in
	// the repository. They come last so they win over the project's, and go
	// through the same merge, so a limit can be tightened but never relaxed.
	// No trust gate: this file is yours, and moa never writes to it.
	if state, err := LoadProjectState(cwd); err != nil {
		slog.Warn("project state: cannot read your settings for this project", "error", err)
	} else if state.Config != nil {
		merged = mergeConfigs(merged, *state.Config)
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
	return ConfigSubdir("config.json")
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
	if err := ValidateAuxiliaryModelConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid auxiliary model config %s: %v; disabling affected feature\n", path, err)
		if ValidateAuxiliaryModelSpec(cfg.AutoTitleModel) != nil {
			cfg.AutoTitleModel = "off"
		}
		if ValidateAuxiliaryModelSpec(cfg.SessionBriefModel) != nil {
			cfg.SessionBriefModel = "off"
		}
	}
	return cfg
}

// ValidateAuxiliaryModelConfig validates the two background-model settings
// without requiring credentials. Credential availability is intentionally a
// startup concern: a valid config remains valid when a user logs out.
func ValidateAuxiliaryModelConfig(cfg MoaConfig) error {
	if err := ValidateAuxiliaryModelSpec(cfg.AutoTitleModel); err != nil {
		return fmt.Errorf("auto_title_model: %w", err)
	}
	if err := ValidateAuxiliaryModelSpec(cfg.SessionBriefModel); err != nil {
		return fmt.Errorf("session_brief_model: %w", err)
	}
	return nil
}

// mergeScalar returns override if non-zero, otherwise base.
func mergeScalar[T comparable](base, override T) T {
	var zero T
	if override != zero {
		return override
	}
	return base
}

// concat joins two slices into a new one. Unlike append(base, override...) it
// can never write into base's spare capacity, so a merged config cannot corrupt
// the config it was merged from.
func concat[T any](base, override []T) []T {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make([]T, 0, len(base)+len(override))
	out = append(out, base...)
	out = append(out, override...)
	return out
}

func mergeConfigs(base, override MoaConfig) MoaConfig {
	merged := MoaConfig{
		DisableSandbox: base.DisableSandbox || override.DisableSandbox,
		AllowedPaths:   append(base.AllowedPaths, override.AllowedPaths...),
		PathScope:      mergeScalar(base.PathScope, override.PathScope),
		PinnedModels:   base.PinnedModels, // global-only preference; project level ignored
		// Global-only like PinnedModels: a delegation allowlist is the owner's
		// policy, so a project config must not be able to widen (or narrow) it.
		SubagentAllowedModels: base.SubagentAllowedModels,
		MCPServers:            MergeMCPServers(base.MCPServers, override.MCPServers),
		// disabled is a veto that accumulates across levels: a project can add
		// to but never relax a global disable (union, deduplicated). Provenance
		// is lost here, so a scope-aware caller uses LoadMoaConfigResolved.
		DisabledMCPServers:  unionStrings(base.DisabledMCPServers, override.DisabledMCPServers),
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
		AutoTitleModel:     mergeScalar(base.AutoTitleModel, override.AutoTitleModel),
		SessionBriefModel:  mergeScalar(base.SessionBriefModel, override.SessionBriefModel),
		CacheTTL:           mergeScalar(base.CacheTTL, override.CacheTTL),
		STTLanguage:        mergeScalar(base.STTLanguage, override.STTLanguage),
		STTModel:           mergeScalar(base.STTModel, override.STTModel),
		// Vocabulary accumulates instead of replacing: a project adds its own
		// jargon on top of the names you set globally, rather than erasing them.
		// Concatenated into a fresh slice so neither input is ever aliased.
		STTVocabulary: concat(base.STTVocabulary, override.STTVocabulary),
	}
	// MaxBudget: project can tighten but not disable a global budget.
	if override.MaxBudget > 0 {
		merged.MaxBudget = override.MaxBudget
	} else {
		merged.MaxBudget = base.MaxBudget
	}
	// Project config may tighten resource guardrails but never relax a global
	// limit. A zero project value means "not specified", not "unlimited".
	merged.MaxTurns = tighterPositiveLimit(base.MaxTurns, override.MaxTurns)
	merged.MaxToolCallsPerTurn = tighterPositiveLimit(base.MaxToolCallsPerTurn, override.MaxToolCallsPerTurn)
	merged.MaxRunDurationStr = tighterDurationLimit(base.MaxRunDurationStr, override.MaxRunDurationStr)
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
	// A global opt-out is a privacy preference, not a project setting: a
	// trusted repository must never silently turn GitHub requests back on.
	if base.UpdateCheck != nil && !*base.UpdateCheck {
		merged.UpdateCheck = base.UpdateCheck
	} else if override.UpdateCheck != nil {
		merged.UpdateCheck = override.UpdateCheck
	} else {
		merged.UpdateCheck = base.UpdateCheck
	}
	merged.SubagentMaxTurns = mergeScalar(base.SubagentMaxTurns, override.SubagentMaxTurns)
	merged.SubagentMaxRunDuration = mergeScalar(base.SubagentMaxRunDuration, override.SubagentMaxRunDuration)
	merged.SubagentMaxConcurrent = mergeScalar(base.SubagentMaxConcurrent, override.SubagentMaxConcurrent)
	return merged
}

func tighterPositiveLimit(base, override int) int {
	if override <= 0 {
		return base
	}
	if base <= 0 || override < base {
		return override
	}
	return base
}

func tighterDurationLimit(base, override string) string {
	if override == "" {
		return base
	}
	overrideDuration, err := time.ParseDuration(override)
	if err != nil || overrideDuration <= 0 {
		return base
	}
	if base == "" {
		return override
	}
	baseDuration, err := time.ParseDuration(base)
	if err != nil || baseDuration <= 0 || overrideDuration < baseDuration {
		return override
	}
	return base
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

// UpdatePinnedModels returns models with id added or removed. Adding preserves
// the existing order and appends new IDs; removing preserves the order of IDs
// that remain.
func UpdatePinnedModels(models []string, id string, pinned bool) []string {
	if pinned {
		updated := append([]string(nil), models...)
		for _, model := range updated {
			if model == id {
				return updated
			}
		}
		return append(updated, id)
	}

	updated := make([]string, 0, len(models))
	for _, model := range models {
		if model != id {
			updated = append(updated, model)
		}
	}
	return updated
}

// saveConfigFile is the read-modify-write primitive shared by SaveGlobalConfig
// and SaveProjectConfig. It re-reads path from disk, applies update, and writes
// the result back atomically (temp file → rename), creating parent dirs.
func saveConfigFile(path string, update func(*MoaConfig)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	unlock, err := lockConfigFile(path + ".lock")
	if err != nil {
		return fmt.Errorf("locking config: %w", err)
	}
	defer unlock()

	cfg := loadConfigFile(path)
	update(&cfg)

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	// Atomic write: temp file in same dir → rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("creating config temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}

// mcpFileFormat matches Claude Code's .mcp.json structure.
type mcpFileFormat struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

// LoadMCPFile reads a .mcp.json file. Returns nil map if file doesn't exist.
// Returns error for parse failures — and for entries that don't describe
// exactly one valid transport — so callers can warn the user instead of
// silently starting a half-defined server.
func LoadMCPFile(path string) (map[string]MCPServer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f mcpFileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	if err := ValidateMCPServers(f.MCPServers); err != nil {
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
