package core

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// MoaConfig holds sandbox and path settings. Loaded from config files
// at three levels: global (~/.moa/config.json), project (<cwd>/.moa/config.json),
// and session (--yolo flag). Merged with OR for booleans, concatenation for slices.
type MoaConfig struct {
	DisableSandbox bool     `json:"disable_sandbox"` // YOLO mode: allow any file path
	AllowedPaths   []string `json:"allowed_paths"`   // Additional directories accessible outside workspace
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
	return MoaConfig{
		DisableSandbox: base.DisableSandbox || override.DisableSandbox,
		AllowedPaths:   append(base.AllowedPaths, override.AllowedPaths...),
	}
}
