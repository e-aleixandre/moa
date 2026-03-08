package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSaveGlobalConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// Patch globalConfigPath for this test by writing directly and using loadConfigFile.
	// We call SaveGlobalConfig after overriding the home via os.UserHomeDir indirection —
	// but since globalConfigPath is private and uses os.UserHomeDir, we test the full
	// read-modify-write cycle by writing an initial file and calling loadConfigFile.

	initial := MoaConfig{PinnedModels: []string{"claude-sonnet-4-5"}}
	data, _ := json.Marshal(initial)
	os.WriteFile(cfgPath, data, 0o600)

	// Simulate what SaveGlobalConfig does (we can't easily redirect home in tests,
	// so we exercise the underlying primitives directly).
	cfg := loadConfigFile(cfgPath)
	cfg.PinnedModels = append(cfg.PinnedModels, "claude-opus-4-5")

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		t.Fatalf("rename: %v", err)
	}

	got := loadConfigFile(cfgPath)
	want := []string{"claude-sonnet-4-5", "claude-opus-4-5"}
	if !slices.Equal(got.PinnedModels, want) {
		t.Fatalf("PinnedModels = %v, want %v", got.PinnedModels, want)
	}
}

func TestSaveGlobalConfig_CreatesDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := SaveGlobalConfig(func(cfg *MoaConfig) {
		cfg.PinnedModels = []string{"claude-haiku-4-5"}
	})
	if err != nil {
		t.Fatalf("SaveGlobalConfig: %v", err)
	}

	cfgPath := filepath.Join(home, ".moa", "config.json")
	got := loadConfigFile(cfgPath)
	if len(got.PinnedModels) != 1 || got.PinnedModels[0] != "claude-haiku-4-5" {
		t.Fatalf("PinnedModels = %v, want [claude-haiku-4-5]", got.PinnedModels)
	}
}

func TestSaveGlobalConfig_PreservesOtherFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write an initial config with permissions set.
	initial := MoaConfig{
		Permissions: PermissionsConfig{Mode: "ask", Allow: []string{"Bash(npm:*)"}},
	}
	cfgDir := filepath.Join(home, ".moa")
	os.MkdirAll(cfgDir, 0o700)
	data, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600)

	// Save only updates pinned models.
	if err := SaveGlobalConfig(func(cfg *MoaConfig) {
		cfg.PinnedModels = []string{"gpt-4o"}
	}); err != nil {
		t.Fatalf("SaveGlobalConfig: %v", err)
	}

	cfgPath := filepath.Join(home, ".moa", "config.json")
	got := loadConfigFile(cfgPath)
	if got.Permissions.Mode != "ask" {
		t.Fatalf("Permissions.Mode = %q, want ask", got.Permissions.Mode)
	}
	if len(got.Permissions.Allow) != 1 || got.Permissions.Allow[0] != "Bash(npm:*)" {
		t.Fatalf("Permissions.Allow = %v, want [Bash(npm:*)]", got.Permissions.Allow)
	}
	if len(got.PinnedModels) != 1 || got.PinnedModels[0] != "gpt-4o" {
		t.Fatalf("PinnedModels = %v, want [gpt-4o]", got.PinnedModels)
	}
}

func TestMergeConfigs_PinnedModelsFromGlobalOnly(t *testing.T) {
	global := MoaConfig{PinnedModels: []string{"claude-sonnet-4-5"}}
	project := MoaConfig{PinnedModels: []string{"gpt-4o"}} // should be ignored
	merged := mergeConfigs(global, project)
	if len(merged.PinnedModels) != 1 || merged.PinnedModels[0] != "claude-sonnet-4-5" {
		t.Fatalf("PinnedModels = %v, want [claude-sonnet-4-5] (project level should be ignored)", merged.PinnedModels)
	}
}
