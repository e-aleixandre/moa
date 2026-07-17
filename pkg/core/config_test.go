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

func TestSaveProjectConfig_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	if err := SaveProjectConfig(cwd, func(cfg *MoaConfig) {
		cfg.Permissions.Allow = append(cfg.Permissions.Allow, "Bash(git:*)")
	}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}

	// Untrusted dir: the repo-local config must NOT be merged (C1 trust gate).
	if got := LoadMoaConfig(cwd); slices.Contains(got.Permissions.Allow, "Bash(git:*)") {
		t.Fatalf("untrusted project Allow leaked into config: %v", got.Permissions.Allow)
	}

	// Once the dir is trusted, the project config applies.
	if err := SaveGlobalConfig(func(cfg *MoaConfig) {
		cfg.TrustedProjectPaths = append(cfg.TrustedProjectPaths, cwd)
	}); err != nil {
		t.Fatalf("SaveGlobalConfig: %v", err)
	}
	got := LoadMoaConfig(cwd)
	if !slices.Contains(got.Permissions.Allow, "Bash(git:*)") {
		t.Fatalf("trusted project Allow = %v, want to contain Bash(git:*)", got.Permissions.Allow)
	}
}

func TestSaveProjectConfig_CreatesDir(t *testing.T) {
	cwd := t.TempDir()
	// .moa does not exist yet.
	if err := SaveProjectConfig(cwd, func(cfg *MoaConfig) {
		cfg.Permissions.Allow = []string{"edit"}
	}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}

	cfgPath := filepath.Join(cwd, ".moa", "config.json")
	got := loadConfigFile(cfgPath)
	if !slices.Equal(got.Permissions.Allow, []string{"edit"}) {
		t.Fatalf("Permissions.Allow = %v, want [edit]", got.Permissions.Allow)
	}
}

func TestSaveProjectConfig_PreservesOtherFields(t *testing.T) {
	cwd := t.TempDir()

	// Seed the project config with a mode and an existing allow entry.
	if err := SaveProjectConfig(cwd, func(cfg *MoaConfig) {
		cfg.Permissions.Mode = "ask"
		cfg.Permissions.Allow = []string{"Bash(npm:*)"}
	}); err != nil {
		t.Fatalf("seed SaveProjectConfig: %v", err)
	}

	// A later save appends only a new allow pattern.
	if err := SaveProjectConfig(cwd, func(cfg *MoaConfig) {
		cfg.Permissions.Allow = append(cfg.Permissions.Allow, "Bash(git:*)")
	}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}

	got := loadConfigFile(filepath.Join(cwd, ".moa", "config.json"))
	if got.Permissions.Mode != "ask" {
		t.Fatalf("Permissions.Mode = %q, want ask", got.Permissions.Mode)
	}
	want := []string{"Bash(npm:*)", "Bash(git:*)"}
	if !slices.Equal(got.Permissions.Allow, want) {
		t.Fatalf("Permissions.Allow = %v, want %v", got.Permissions.Allow, want)
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

func TestMergeConfigs_GuardrailsCanOnlyTighten(t *testing.T) {
	base := MoaConfig{MaxTurns: 10, MaxToolCallsPerTurn: 20, MaxRunDurationStr: "30m"}
	tighter := MoaConfig{MaxTurns: 5, MaxToolCallsPerTurn: 10, MaxRunDurationStr: "10m"}
	got := mergeConfigs(base, tighter)
	if got.MaxTurns != 5 || got.MaxToolCallsPerTurn != 10 || got.MaxRunDurationStr != "10m" {
		t.Fatalf("tightened guardrails = %+v", got)
	}
	looser := MoaConfig{MaxTurns: 50, MaxToolCallsPerTurn: 100, MaxRunDurationStr: "1h"}
	got = mergeConfigs(base, looser)
	if got.MaxTurns != 10 || got.MaxToolCallsPerTurn != 20 || got.MaxRunDurationStr != "30m" {
		t.Fatalf("loosened guardrails must retain base: %+v", got)
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

func TestIsProjectPathTrusted(t *testing.T) {
	// Exact match.
	cfg := MoaConfig{TrustedProjectPaths: []string{"/a/b"}}
	if !IsProjectPathTrusted(cfg, "/a/b") {
		t.Fatal("expected /a/b to be trusted")
	}
	if IsProjectPathTrusted(cfg, "/x/y") {
		t.Fatal("expected /x/y to not be trusted")
	}
	if IsProjectPathTrusted(MoaConfig{}, "/a/b") {
		t.Fatal("expected empty config to trust nothing")
	}

	// Symlink/spelling-insensitive: a dir trusted via one path matches a
	// canonicalized query for the same dir (guards the serve /var→/private/var case).
	dir := t.TempDir()
	canon, err := CanonicalizePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	trusted := MoaConfig{TrustedProjectPaths: []string{dir}}
	if !IsProjectPathTrusted(trusted, canon) {
		t.Fatalf("trusted %q should match canonical %q", dir, canon)
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
		MCPServers:      map[string]MCPServer{"db": {Command: "global-db"}},
		TrustedMCPPaths: []string{"/trusted/project"},
	}
	project := MoaConfig{
		MCPServers:      map[string]MCPServer{"db": {Command: "project-db"}},
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

func TestIsMemoryEnabled_Default(t *testing.T) {
	if !IsMemoryEnabled(MoaConfig{}) {
		t.Error("expected memory enabled by default (nil)")
	}
}

func TestIsMemoryEnabled_ExplicitTrue(t *testing.T) {
	v := true
	if !IsMemoryEnabled(MoaConfig{MemoryEnabled: &v}) {
		t.Error("expected memory enabled when explicitly true")
	}
}

func TestIsMemoryEnabled_ExplicitFalse(t *testing.T) {
	v := false
	if IsMemoryEnabled(MoaConfig{MemoryEnabled: &v}) {
		t.Error("expected memory disabled when explicitly false")
	}
}

func TestMergeConfigs_MemoryEnabled_ProjectOverride(t *testing.T) {
	v := false
	base := MoaConfig{}                     // nil = default true
	project := MoaConfig{MemoryEnabled: &v} // explicitly false
	merged := mergeConfigs(base, project)
	if IsMemoryEnabled(merged) {
		t.Error("project false should override global nil")
	}
}

func TestMergeConfigs_MemoryEnabled_GlobalOnly(t *testing.T) {
	v := false
	base := MoaConfig{MemoryEnabled: &v}
	project := MoaConfig{} // nil = no override
	merged := mergeConfigs(base, project)
	if IsMemoryEnabled(merged) {
		t.Error("global false should persist when project has no override")
	}
}

func TestIsAutoVerifyEnabled_NilIsFalse(t *testing.T) {
	cfg := MoaConfig{}
	if IsAutoVerifyEnabled(cfg) {
		t.Error("nil AutoVerify should be false")
	}
}

func TestIsAutoVerifyEnabled_TrueWhenSet(t *testing.T) {
	v := true
	cfg := MoaConfig{AutoVerify: &v}
	if !IsAutoVerifyEnabled(cfg) {
		t.Error("expected true")
	}
}

func TestIsAutoVerifyEnabled_FalseWhenExplicit(t *testing.T) {
	v := false
	cfg := MoaConfig{AutoVerify: &v}
	if IsAutoVerifyEnabled(cfg) {
		t.Error("expected false")
	}
}

func TestMergeConfigs_AutoVerify_ProjectOverridesGlobal(t *testing.T) {
	globalVal := true
	projectVal := false
	base := MoaConfig{AutoVerify: &globalVal}
	project := MoaConfig{AutoVerify: &projectVal}
	merged := mergeConfigs(base, project)
	if IsAutoVerifyEnabled(merged) {
		t.Error("project false should override global true")
	}
}

func TestMergeConfigs_AutoVerify_NilFallsThrough(t *testing.T) {
	globalVal := true
	base := MoaConfig{AutoVerify: &globalVal}
	project := MoaConfig{} // nil
	merged := mergeConfigs(base, project)
	if !IsAutoVerifyEnabled(merged) {
		t.Error("global true should persist when project has no override")
	}
}

func TestIsPersistentShellEnabled_Default(t *testing.T) {
	if !IsPersistentShellEnabled(MoaConfig{}) {
		t.Error("expected persistent shell enabled by default (nil)")
	}
}

func TestIsPersistentShellEnabled_ExplicitTrue(t *testing.T) {
	v := true
	if !IsPersistentShellEnabled(MoaConfig{PersistentShell: &v}) {
		t.Error("expected persistent shell enabled when explicitly true")
	}
}

func TestIsPersistentShellEnabled_ExplicitFalse(t *testing.T) {
	v := false
	if IsPersistentShellEnabled(MoaConfig{PersistentShell: &v}) {
		t.Error("expected persistent shell disabled when explicitly false")
	}
}

func TestMergeConfigs_PersistentShell_ProjectOverride(t *testing.T) {
	v := false
	base := MoaConfig{}                       // nil = default true
	project := MoaConfig{PersistentShell: &v} // explicitly false
	merged := mergeConfigs(base, project)
	if IsPersistentShellEnabled(merged) {
		t.Error("project false should override global nil")
	}
}

func TestMergeConfigs_PersistentShell_NilFallsThrough(t *testing.T) {
	v := false
	base := MoaConfig{PersistentShell: &v}
	project := MoaConfig{} // nil
	merged := mergeConfigs(base, project)
	if IsPersistentShellEnabled(merged) {
		t.Error("global false should persist when project has no override")
	}
}

func TestIsUpdateCheckEnabledAndProjectOverride(t *testing.T) {
	if !IsUpdateCheckEnabled(MoaConfig{}) {
		t.Error("update checks should default to enabled")
	}
	disabled, enabled := false, true
	merged := mergeConfigs(MoaConfig{}, MoaConfig{UpdateCheck: &disabled})
	if IsUpdateCheckEnabled(merged) {
		t.Error("project false should disable update checks")
	}
	merged = mergeConfigs(MoaConfig{UpdateCheck: &disabled}, MoaConfig{UpdateCheck: &enabled})
	if IsUpdateCheckEnabled(merged) {
		t.Error("project config must not re-enable a global update-check opt-out")
	}
	merged = mergeConfigs(MoaConfig{UpdateCheck: &enabled}, MoaConfig{UpdateCheck: &disabled})
	if IsUpdateCheckEnabled(merged) {
		t.Error("project config may disable update checks")
	}
}

func TestGetSTTLanguage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty defaults to english", "", "en"},
		{"explicit spanish", "es", "es"},
		{"explicit english", "en", "en"},
		{"auto lowercases to detect", "auto", ""},
		{"AUTO any case", "AUTO", ""},
		{"trims whitespace", "  es  ", "es"},
		{"uppercase normalized", "ES", "es"},
		{"invalid too long falls back", "spanish", "en"},
		{"invalid non-letters falls back", "e5", "en"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GetSTTLanguage(MoaConfig{STTLanguage: c.in})
			if got != c.want {
				t.Errorf("GetSTTLanguage(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMergeConfigs_STTLanguage_ProjectOverride(t *testing.T) {
	base := MoaConfig{STTLanguage: "es"}
	project := MoaConfig{STTLanguage: "en"}
	merged := mergeConfigs(base, project)
	if got := GetSTTLanguage(merged); got != "en" {
		t.Errorf("project override: got %q, want en", got)
	}
}
