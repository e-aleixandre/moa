package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUnionStrings(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{"both empty", nil, nil, nil},
		{"dedup within a", []string{"x", "x"}, nil, []string{"x"}},
		{"dedup across", []string{"a", "b"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"drops empties", []string{"", "a", ""}, []string{""}, []string{"a"}},
		{"stable order", []string{"z", "a"}, []string{"m"}, []string{"z", "a", "m"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unionStrings(tc.a, tc.b)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unionStrings(%v,%v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestMergeConfigs_DisabledMCPServersUnion(t *testing.T) {
	base := MoaConfig{DisabledMCPServers: []string{"playwright", "shared"}}
	override := MoaConfig{DisabledMCPServers: []string{"shared", "postgres"}}
	got := mergeConfigs(base, override).DisabledMCPServers
	want := []string{"playwright", "shared", "postgres"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged disabled = %v, want %v (dedup union)", got, want)
	}
}

func TestMergeConfigs_DisabledMCPServersProjectCannotRelaxGlobal(t *testing.T) {
	// A project config has no way to remove a global veto: the merge is a union,
	// so "playwright" stays disabled regardless of the project list.
	global := MoaConfig{DisabledMCPServers: []string{"playwright"}}
	project := MoaConfig{DisabledMCPServers: []string{"other"}}
	got := mergeConfigs(global, project).DisabledMCPServers
	found := false
	for _, s := range got {
		if s == "playwright" {
			found = true
		}
	}
	if !found {
		t.Fatalf("global veto lost after merge: %v", got)
	}
}

func TestResolveMCPDisabled_AccumulatingVetoes(t *testing.T) {
	p := MCPDisablePolicy{
		Global:  map[string]struct{}{"g": {}, "gp": {}},
		Project: map[string]struct{}{"p": {}, "gp": {}},
		Session: map[string]struct{}{"s": {}},
	}
	cases := []struct {
		name       string
		wantScopes []MCPDisableScope
	}{
		{"enabled", nil},
		{"g", []MCPDisableScope{MCPScopeGlobal}},
		{"p", []MCPDisableScope{MCPScopeProject}},
		{"s", []MCPDisableScope{MCPScopeSession}},
		{"gp", []MCPDisableScope{MCPScopeGlobal, MCPScopeProject}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveMCPDisabled(tc.name, p)
			if got.Disabled != (len(tc.wantScopes) > 0) {
				t.Fatalf("Disabled = %v, want %v", got.Disabled, len(tc.wantScopes) > 0)
			}
			if !reflect.DeepEqual(got.Scopes, tc.wantScopes) {
				t.Fatalf("Scopes = %v, want %v", got.Scopes, tc.wantScopes)
			}
		})
	}
}

func TestMCPDisablePolicy_DisabledSet(t *testing.T) {
	p := MCPDisablePolicy{
		Global:  map[string]struct{}{"a": {}},
		Project: map[string]struct{}{"b": {}},
		Session: map[string]struct{}{"a": {}, "c": {}},
	}
	got := p.DisabledSet()
	want := map[string]bool{"a": true, "b": true, "c": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DisabledSet = %v, want %v", got, want)
	}
}

func TestNewMCPDisablePolicy_SessionStartsEmpty(t *testing.T) {
	p := NewMCPDisablePolicy(MCPDisableSources{
		Global:  []string{"g"},
		Project: []string{"p"},
	})
	if len(p.Session) != 0 {
		t.Fatalf("session set should start empty, got %v", p.Session)
	}
	if _, ok := p.Global["g"]; !ok {
		t.Fatal("global veto missing")
	}
	if _, ok := p.Project["p"]; !ok {
		t.Fatal("project veto missing")
	}
}

func TestLoadMoaConfigResolved_Provenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	// Global config: disables "playwright" and trusts the project cwd.
	writeConfigJSON(t, filepath.Join(home, ".config", "moa", "config.json"), MoaConfig{
		DisabledMCPServers:  []string{"playwright"},
		TrustedProjectPaths: []string{cwd},
	})
	// Project config: disables "postgres".
	writeConfigJSON(t, filepath.Join(cwd, ".moa", "config.json"), MoaConfig{
		DisabledMCPServers: []string{"postgres"},
	})

	loaded := LoadMoaConfigResolved(cwd)
	if !loaded.MCPDisabled.ProjectTrusted {
		t.Fatal("project should be trusted")
	}
	if !reflect.DeepEqual(loaded.MCPDisabled.Global, []string{"playwright"}) {
		t.Fatalf("global provenance = %v", loaded.MCPDisabled.Global)
	}
	if !reflect.DeepEqual(loaded.MCPDisabled.Project, []string{"postgres"}) {
		t.Fatalf("project provenance = %v", loaded.MCPDisabled.Project)
	}
}

func TestLoadMoaConfigResolved_UntrustedProjectIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	// Global config does NOT trust the project cwd.
	writeConfigJSON(t, filepath.Join(home, ".config", "moa", "config.json"), MoaConfig{
		DisabledMCPServers: []string{"playwright"},
	})
	writeConfigJSON(t, filepath.Join(cwd, ".moa", "config.json"), MoaConfig{
		DisabledMCPServers: []string{"postgres"},
	})

	loaded := LoadMoaConfigResolved(cwd)
	if loaded.MCPDisabled.ProjectTrusted {
		t.Fatal("project should NOT be trusted")
	}
	if len(loaded.MCPDisabled.Project) != 0 {
		t.Fatalf("untrusted project preference must be ignored, got %v", loaded.MCPDisabled.Project)
	}
	if !reflect.DeepEqual(loaded.MCPDisabled.Global, []string{"playwright"}) {
		t.Fatalf("global provenance = %v", loaded.MCPDisabled.Global)
	}
}

func writeConfigJSON(t *testing.T, path string, cfg MoaConfig) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestSetMCPServerDisabled(t *testing.T) {
	// Add to empty.
	cfg := &MoaConfig{}
	SetMCPServerDisabled(cfg, "playwright", true)
	if !reflect.DeepEqual(cfg.DisabledMCPServers, []string{"playwright"}) {
		t.Fatalf("after add = %v", cfg.DisabledMCPServers)
	}
	// Idempotent add stays sorted & deduped.
	SetMCPServerDisabled(cfg, "browser", true)
	if !reflect.DeepEqual(cfg.DisabledMCPServers, []string{"browser", "playwright"}) {
		t.Fatalf("after second add = %v, want sorted", cfg.DisabledMCPServers)
	}
	SetMCPServerDisabled(cfg, "browser", true)
	if !reflect.DeepEqual(cfg.DisabledMCPServers, []string{"browser", "playwright"}) {
		t.Fatalf("duplicate add changed list = %v", cfg.DisabledMCPServers)
	}
	// Remove one.
	SetMCPServerDisabled(cfg, "browser", false)
	if !reflect.DeepEqual(cfg.DisabledMCPServers, []string{"playwright"}) {
		t.Fatalf("after remove = %v", cfg.DisabledMCPServers)
	}
	// Remove last → nil (so omitempty drops the field).
	SetMCPServerDisabled(cfg, "playwright", false)
	if cfg.DisabledMCPServers != nil {
		t.Fatalf("after removing last = %v, want nil", cfg.DisabledMCPServers)
	}
	// Empty name is ignored.
	SetMCPServerDisabled(cfg, "", true)
	if cfg.DisabledMCPServers != nil {
		t.Fatalf("empty name mutated list = %v", cfg.DisabledMCPServers)
	}
}
