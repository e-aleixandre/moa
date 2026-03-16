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
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

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

	cfgPath := filepath.Join(home, ".config", "moa", "config.json")
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
	cfgDir := filepath.Join(home, ".config", "moa")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Save only updates pinned models.
	if err := SaveGlobalConfig(func(cfg *MoaConfig) {
		cfg.PinnedModels = []string{"gpt-4o"}
	}); err != nil {
		t.Fatalf("SaveGlobalConfig: %v", err)
	}

	cfgPath := filepath.Join(home, ".config", "moa", "config.json")
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

func TestLoadMCPFile_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte(`{
		"mcpServers": {
			"db": {
				"command": "mcp-sqlite",
				"args": ["/path/to.db"],
				"env": {"DEBUG": "1"}
			},
			"fs": {
				"command": "mcp-filesystem",
				"args": ["/home"]
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	servers, err := LoadMCPFile(path)
	if err != nil {
		t.Fatalf("LoadMCPFile: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("got %d servers, want 2", len(servers))
	}
	db := servers["db"]
	if db.Command != "mcp-sqlite" {
		t.Fatalf("db.Command = %q, want mcp-sqlite", db.Command)
	}
	if !slices.Equal(db.Args, []string{"/path/to.db"}) {
		t.Fatalf("db.Args = %v", db.Args)
	}
	if db.Env["DEBUG"] != "1" {
		t.Fatalf("db.Env = %v", db.Env)
	}
	fs := servers["fs"]
	if fs.Command != "mcp-filesystem" {
		t.Fatalf("fs.Command = %q", fs.Command)
	}
}

func TestLoadMCPFile_Invalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte(`not json`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadMCPFile(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestLoadMCPFile_NotExist(t *testing.T) {
	_, err := LoadMCPFile("/nonexistent/.mcp.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("expected not-exist error, got: %v", err)
	}
}

func TestMergeMCPServers_Override(t *testing.T) {
	global := map[string]MCPServer{
		"db": {Command: "global-db", Args: []string{"--global"}, Env: map[string]string{"X": "1"}},
		"fs": {Command: "global-fs"},
	}
	project := map[string]MCPServer{
		"db": {Command: "project-db", Args: []string{"--project"}},
	}

	merged := MergeMCPServers(global, project)
	if len(merged) != 2 {
		t.Fatalf("got %d servers, want 2", len(merged))
	}
	// "db" fully replaced by project — no field-level merge
	db := merged["db"]
	if db.Command != "project-db" {
		t.Fatalf("db.Command = %q, want project-db", db.Command)
	}
	if !slices.Equal(db.Args, []string{"--project"}) {
		t.Fatalf("db.Args = %v, want [--project]", db.Args)
	}
	if db.Env != nil {
		t.Fatalf("db.Env = %v, want nil (full replacement, not field merge)", db.Env)
	}
	// "fs" untouched
	if merged["fs"].Command != "global-fs" {
		t.Fatalf("fs.Command = %q, want global-fs", merged["fs"].Command)
	}
}

func TestMergeMCPServers_Empty(t *testing.T) {
	if got := MergeMCPServers(nil, nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	if got := MergeMCPServers(nil, map[string]MCPServer{}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestIsMCPPathTrusted(t *testing.T) {
	cfg := MoaConfig{TrustedMCPPaths: []string{"/a/b", "/c/d"}}
	if !IsMCPPathTrusted(cfg, "/a/b") {
		t.Fatal("expected /a/b to be trusted")
	}
	if !IsMCPPathTrusted(cfg, "/c/d") {
		t.Fatal("expected /c/d to be trusted")
	}
	if IsMCPPathTrusted(cfg, "/x/y") {
		t.Fatal("expected /x/y to not be trusted")
	}
	if IsMCPPathTrusted(MoaConfig{}, "/a/b") {
		t.Fatal("expected empty config to trust nothing")
	}
}

func TestCanonicalizePath(t *testing.T) {
	dir := t.TempDir()
	// Resolve the temp dir itself (macOS: /var -> /private/var).
	canonDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}

	// Absolute path stable after canonicalization.
	got, err := CanonicalizePath(dir)
	if err != nil {
		t.Fatalf("CanonicalizePath(%q): %v", dir, err)
	}
	if got != canonDir {
		t.Fatalf("got %q, want %q", got, canonDir)
	}

	// Double slashes cleaned (sub doesn't exist, so symlinks can't be
	// resolved — but path is still cleaned).
	got, err = CanonicalizePath(dir + "//sub")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Clean(dir), "sub")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Trailing slash stripped.
	got, err = CanonicalizePath(dir + "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != canonDir {
		t.Fatalf("got %q, want %q", got, canonDir)
	}
}

func TestMergeConfigs_MCPServers(t *testing.T) {
	global := MoaConfig{
		MCPServers: map[string]MCPServer{"db": {Command: "global-db"}},
		TrustedMCPPaths: []string{"/trusted/project"},
	}
	project := MoaConfig{
		MCPServers: map[string]MCPServer{"db": {Command: "project-db"}},
		TrustedMCPPaths: []string{"/other"}, // should be ignored (global only)
	}
	merged := mergeConfigs(global, project)

	if merged.MCPServers["db"].Command != "project-db" {
		t.Fatalf("MCPServers[db].Command = %q, want project-db", merged.MCPServers["db"].Command)
	}
	// TrustedMCPPaths from global only
	if !slices.Equal(merged.TrustedMCPPaths, []string{"/trusted/project"}) {
		t.Fatalf("TrustedMCPPaths = %v, want [/trusted/project]", merged.TrustedMCPPaths)
	}
}

func TestMergeConfigs_MaxBudget(t *testing.T) {
	tests := []struct {
		name     string
		base     float64
		override float64
		want     float64
	}{
		{"global only", 5, 0, 5},
		{"project sets budget", 0, 3, 3},
		{"project tightens", 5, 2, 2},
		{"both zero", 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			merged := mergeConfigs(
				MoaConfig{MaxBudget: tc.base},
				MoaConfig{MaxBudget: tc.override},
			)
			if merged.MaxBudget != tc.want {
				t.Errorf("MaxBudget = %f, want %f", merged.MaxBudget, tc.want)
			}
		})
	}
}

func TestResolvePathScope(t *testing.T) {
	tests := []struct {
		name           string
		pathScope      string
		disableSandbox bool
		permMode       string
		want           string
	}{
		// Explicit path_scope always wins
		{"explicit workspace", "workspace", true, "yolo", "workspace"},
		{"explicit unrestricted", "unrestricted", false, "auto", "unrestricted"},

		// Legacy disable_sandbox
		{"legacy disable_sandbox", "", true, "auto", "unrestricted"},
		{"legacy disable_sandbox with perm", "", true, "", "unrestricted"},

		// Derive from permission mode
		{"yolo implies unrestricted", "", false, "yolo", "unrestricted"},
		{"ask implies unrestricted", "", false, "ask", "unrestricted"},
		{"auto implies workspace", "", false, "auto", "workspace"},
		{"empty perm implies workspace", "", false, "", "workspace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolvePathScope(tc.pathScope, tc.disableSandbox, tc.permMode)
			if got != tc.want {
				t.Errorf("ResolvePathScope(%q, %v, %q) = %q, want %q",
					tc.pathScope, tc.disableSandbox, tc.permMode, got, tc.want)
			}
		})
	}
}

func TestMergeConfigs_PathScope(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		override string
		want     string
	}{
		{"both empty", "", "", ""},
		{"global only", "workspace", "", "workspace"},
		{"project overrides", "workspace", "unrestricted", "unrestricted"},
		{"project only", "", "workspace", "workspace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			merged := mergeConfigs(
				MoaConfig{PathScope: tc.base},
				MoaConfig{PathScope: tc.override},
			)
			if merged.PathScope != tc.want {
				t.Errorf("PathScope = %q, want %q", merged.PathScope, tc.want)
			}
		})
	}
}

func TestPathScope_JSONRoundTrip(t *testing.T) {
	cfg := MoaConfig{PathScope: "workspace", AllowedPaths: []string{"/extra"}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var got MoaConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.PathScope != "workspace" {
		t.Fatalf("PathScope = %q after roundtrip", got.PathScope)
	}
}

func TestResolvePathScope_DisableSandboxWithoutPathScope(t *testing.T) {
	// Config has disable_sandbox: true but no explicit path_scope.
	// ResolvePathScope should return "unrestricted".
	got := ResolvePathScope("", true, "")
	if got != "unrestricted" {
		t.Fatalf("ResolvePathScope('', true, '') = %q, want 'unrestricted'", got)
	}
	// Also works with any permission mode.
	got = ResolvePathScope("", true, "auto")
	if got != "unrestricted" {
		t.Fatalf("ResolvePathScope('', true, 'auto') = %q, want 'unrestricted'", got)
	}
}

func TestResolvePathScope_YoloWithoutPathScope(t *testing.T) {
	// Permissions mode "yolo" without explicit path_scope → "unrestricted".
	got := ResolvePathScope("", false, "yolo")
	if got != "unrestricted" {
		t.Fatalf("ResolvePathScope('', false, 'yolo') = %q, want 'unrestricted'", got)
	}
}
