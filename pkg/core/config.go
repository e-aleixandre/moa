package core

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// MoaConfig holds sandbox, path, and permission settings. Loaded from config
// files at three levels: global (~/.moa/config.json), project (<cwd>/.moa/config.json),
// and session (flags). Merged with OR for booleans, concatenation for slices.
type MoaConfig struct {
	DisableSandbox bool              `json:"disable_sandbox"` // YOLO mode: allow any file path
	AllowedPaths   []string          `json:"allowed_paths"`   // Additional directories accessible outside workspace
	Permissions    PermissionsConfig `json:"permissions"`     // Tool execution permission policy
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
// Global: ~/.moa/config.json. Project: <cwd>/.moa/config.json.
// Project values override/extend global values.
func LoadMoaConfig(cwd string) MoaConfig {
	global := loadConfigFile(globalConfigPath())
	project := loadConfigFile(filepath.Join(cwd, ".moa", "config.json"))
	return mergeConfigs(global, project)
}

func globalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".moa", "config.json")
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
	json.Unmarshal(data, &cfg)
	return cfg
}

func mergeConfigs(base, override MoaConfig) MoaConfig {
	merged := MoaConfig{
		DisableSandbox: base.DisableSandbox || override.DisableSandbox,
		AllowedPaths:   append(base.AllowedPaths, override.AllowedPaths...),
		Permissions: PermissionsConfig{
			Mode:  base.Permissions.Mode,
			Model: base.Permissions.Model,
			Allow: append(base.Permissions.Allow, override.Permissions.Allow...),
			Deny:  append(base.Permissions.Deny, override.Permissions.Deny...),
			Rules: append(base.Permissions.Rules, override.Permissions.Rules...),
		},
	}
	// Override wins for scalar fields
	if override.Permissions.Mode != "" {
		merged.Permissions.Mode = override.Permissions.Mode
	}
	if override.Permissions.Model != "" {
		merged.Permissions.Model = override.Permissions.Model
	}
	return merged
}
