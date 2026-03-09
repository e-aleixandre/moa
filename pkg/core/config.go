package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MoaConfig holds sandbox, path, and permission settings. Loaded from config
// files at three levels: global (~/.config/moa/config.json), project (<cwd>/.moa/config.json),
// and session (flags). Merged with OR for booleans, concatenation for slices.
type MoaConfig struct {
	DisableSandbox bool              `json:"disable_sandbox"` // YOLO mode: allow any file path
	AllowedPaths   []string          `json:"allowed_paths"`   // Additional directories accessible outside workspace
	Permissions    PermissionsConfig `json:"permissions"`     // Tool execution permission policy
	PinnedModels   []string          `json:"pinned_models"`   // Model IDs pinned for Ctrl+P cycling
	BraveAPIKey    string            `json:"brave_api_key"`   // Brave Search API key for web_search tool
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
	json.Unmarshal(data, &cfg)
	return cfg
}

func mergeConfigs(base, override MoaConfig) MoaConfig {
	merged := MoaConfig{
		DisableSandbox: base.DisableSandbox || override.DisableSandbox,
		AllowedPaths:   append(base.AllowedPaths, override.AllowedPaths...),
		PinnedModels:   base.PinnedModels, // global-only preference; project level ignored
		Permissions: PermissionsConfig{
			Mode:  base.Permissions.Mode,
			Model: base.Permissions.Model,
			Allow: append(base.Permissions.Allow, override.Permissions.Allow...),
			Deny:  append(base.Permissions.Deny, override.Permissions.Deny...),
			Rules: append(base.Permissions.Rules, override.Permissions.Rules...),
		},
	}
	// Override wins for scalar fields
	if override.BraveAPIKey != "" {
		merged.BraveAPIKey = override.BraveAPIKey
	} else {
		merged.BraveAPIKey = base.BraveAPIKey
	}
	if override.Permissions.Mode != "" {
		merged.Permissions.Mode = override.Permissions.Mode
	}
	if override.Permissions.Model != "" {
		merged.Permissions.Model = override.Permissions.Model
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
		os.Remove(tmp)
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}
