package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathPolicy_Scope(t *testing.T) {
	tests := []struct {
		name         string
		unrestricted bool
		allowed      []string
		want         string
	}{
		{"workspace only", false, nil, "workspace"},
		{"unrestricted", true, nil, "unrestricted"},
		{"ws+1", false, []string{"/extra"}, "ws+1"},
		{"ws+3", false, []string{"/a", "/b", "/c"}, "ws+3"},
		{"unrestricted with allowed", true, []string{"/a"}, "unrestricted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPathPolicy("/workspace", tt.allowed, tt.unrestricted)
			if got := p.Scope(); got != tt.want {
				t.Errorf("Scope() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPathPolicy_AddRemove(t *testing.T) {
	p := NewPathPolicy("/workspace", nil, false)
	if got := p.Scope(); got != "workspace" {
		t.Fatalf("initial scope = %q, want workspace", got)
	}

	extra := t.TempDir()
	if err := p.AddPath(extra); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if got := p.Scope(); got != "ws+1" {
		t.Fatalf("after add = %q, want ws+1", got)
	}
	paths := p.AllowedPaths()
	if len(paths) != 1 || paths[0] != extra {
		t.Fatalf("AllowedPaths = %v", paths)
	}

	// Duplicate add is a no-op
	if err := p.AddPath(extra); err != nil {
		t.Fatalf("duplicate AddPath: %v", err)
	}
	if len(p.AllowedPaths()) != 1 {
		t.Fatal("duplicate add should be no-op")
	}

	// Remove
	if !p.RemovePath(extra) {
		t.Fatal("RemovePath should return true")
	}
	if got := p.Scope(); got != "workspace" {
		t.Fatalf("after remove = %q, want workspace", got)
	}

	// Remove non-existent
	if p.RemovePath("/nonexistent") {
		t.Fatal("RemovePath of absent path should return false")
	}
}

func TestPathPolicy_AddPath_Validation(t *testing.T) {
	p := NewPathPolicy("/workspace", nil, false)

	// Non-existent path
	if err := p.AddPath("/nonexistent/path/xyz"); err == nil {
		t.Fatal("should reject non-existent path")
	}

	// File, not directory
	f := filepath.Join(t.TempDir(), "file.txt")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	if err := p.AddPath(f); err == nil {
		t.Fatal("should reject file (not directory)")
	}

	// Valid directory
	dir := t.TempDir()
	if err := p.AddPath(dir); err != nil {
		t.Fatalf("should accept valid directory: %v", err)
	}
}

func TestPathPolicy_SetUnrestricted(t *testing.T) {
	p := NewPathPolicy("/workspace", nil, false)
	if p.Unrestricted() {
		t.Fatal("should start restricted")
	}
	p.SetUnrestricted(true)
	if !p.Unrestricted() {
		t.Fatal("should be unrestricted")
	}
	if p.Scope() != "unrestricted" {
		t.Fatal("scope should reflect unrestricted")
	}
}

func TestPathPolicy_IsAllowed(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	// Resolve symlinks (macOS /var → /private/var)
	tmp, _ = filepath.EvalSymlinks(tmp)
	outside, _ = filepath.EvalSymlinks(outside)
	p := NewPathPolicy(tmp, nil, false)

	// Inside workspace
	if !p.IsAllowed(tmp + "/subdir/file.txt") {
		t.Error("should allow paths inside workspace")
	}

	// Workspace root itself
	if !p.IsAllowed(tmp) {
		t.Error("should allow workspace root")
	}

	// Outside workspace
	if p.IsAllowed(outside + "/file.txt") {
		t.Error("should reject paths outside workspace")
	}

	// Add allowed path
	if err := p.AddPath(outside); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if !p.IsAllowed(outside + "/file.txt") {
		t.Error("should allow paths in added directory")
	}

	// Unrestricted allows everything
	p.SetUnrestricted(true)
	if !p.IsAllowed("/anywhere/at/all") {
		t.Error("unrestricted should allow any path")
	}
}

func TestPathPolicy_WorkspaceRoot(t *testing.T) {
	p := NewPathPolicy("/my/project", nil, false)
	if got := p.WorkspaceRoot(); got != "/my/project" {
		t.Fatalf("WorkspaceRoot() = %q, want /my/project", got)
	}
}

func TestPathPolicy_AllowedPathsCopy(t *testing.T) {
	p := NewPathPolicy("/workspace", []string{"/a", "/b"}, false)
	paths := p.AllowedPaths()
	paths[0] = "/mutated"
	// Original should be unaffected
	if p.AllowedPaths()[0] != "/a" {
		t.Fatal("AllowedPaths should return a copy")
	}
}

// --- safePath with PathPolicy tests ---

func TestSafePath_WithPathPolicy_Unrestricted(t *testing.T) {
	tmp := t.TempDir()
	policy := NewPathPolicy(tmp, nil, true)
	cfg := ToolConfig{WorkspaceRoot: tmp, PathPolicy: policy}

	p, err := safePath(cfg, "/etc/passwd")
	if err != nil {
		t.Fatal("PathPolicy unrestricted should allow any path")
	}
	if p != "/etc/passwd" {
		t.Fatalf("expected /etc/passwd, got %s", p)
	}
}

func TestSafePath_WithPathPolicy_WorkspaceOnly(t *testing.T) {
	tmp := t.TempDir()
	policy := NewPathPolicy(tmp, nil, false)
	cfg := ToolConfig{WorkspaceRoot: tmp, PathPolicy: policy}

	// Inside workspace works
	p, err := safePath(cfg, "subdir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if p != tmp+"/subdir/file.txt" {
		t.Fatalf("expected %s/subdir/file.txt, got %s", tmp, p)
	}

	// Outside workspace rejected
	_, err = safePath(cfg, "/etc/passwd")
	if err == nil {
		t.Fatal("should reject paths outside workspace")
	}
}

func TestSafePath_WithPathPolicy_AllowedPath(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	policy := NewPathPolicy(tmp, []string{outside}, false)
	cfg := ToolConfig{WorkspaceRoot: tmp, PathPolicy: policy}

	target := outside + "/file.txt"
	p, err := safePath(cfg, target)
	if err != nil {
		t.Fatalf("should allow path in allowed dir: %v", err)
	}
	if p != target {
		t.Fatalf("expected %s, got %s", target, p)
	}
}

func TestSafePath_WithPathPolicy_RuntimeAdd(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	policy := NewPathPolicy(tmp, nil, false)
	cfg := ToolConfig{WorkspaceRoot: tmp, PathPolicy: policy}

	target := outside + "/file.txt"

	// Initially rejected
	_, err := safePath(cfg, target)
	if err == nil {
		t.Fatal("should reject before AddPath")
	}

	// Add at runtime
	if err := policy.AddPath(outside); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	// Now allowed
	p, err := safePath(cfg, target)
	if err != nil {
		t.Fatalf("should allow after AddPath: %v", err)
	}
	if p != target {
		t.Fatalf("expected %s, got %s", target, p)
	}
}

func TestSafePath_WithPathPolicy_OverridesLegacy(t *testing.T) {
	tmp := t.TempDir()
	// Legacy DisableSandbox=true, but PathPolicy says restricted
	policy := NewPathPolicy(tmp, nil, false)
	cfg := ToolConfig{
		WorkspaceRoot:  tmp,
		DisableSandbox: true, // legacy says unrestricted
		PathPolicy:     policy, // policy says restricted
	}

	_, err := safePath(cfg, "/etc/passwd")
	if err == nil {
		t.Fatal("PathPolicy should override legacy DisableSandbox")
	}
}

// --- Integration test: full flow through PathPolicy → ToolConfig → safePath ---

func TestIntegration_PathPolicy_ToolConfig_SafePath(t *testing.T) {
	workspace := t.TempDir()
	outside1 := t.TempDir()
	outside2 := t.TempDir()
	// Resolve symlinks (macOS /var → /private/var)
	workspace, _ = filepath.EvalSymlinks(workspace)
	outside1, _ = filepath.EvalSymlinks(outside1)
	outside2, _ = filepath.EvalSymlinks(outside2)

	// Step 1: Create a PathPolicy with workspace-only scope.
	policy := NewPathPolicy(workspace, nil, false)

	// Step 2: Wire into ToolConfig (mirrors what bootstrap does).
	cfg := ToolConfig{
		WorkspaceRoot: workspace,
		PathPolicy:    policy,
	}

	// Step 3: Workspace paths allowed, outside paths rejected.
	if _, err := safePath(cfg, "src/main.go"); err != nil {
		t.Fatalf("workspace-relative path should be allowed: %v", err)
	}
	if _, err := safePath(cfg, outside1+"/file.txt"); err == nil {
		t.Fatal("outside1 should be rejected initially")
	}
	if _, err := safePath(cfg, outside2+"/file.txt"); err == nil {
		t.Fatal("outside2 should be rejected initially")
	}

	// Step 4: Runtime mutation — add outside1 via policy (simulates /path add).
	if err := policy.AddPath(outside1); err != nil {
		t.Fatalf("AddPath outside1: %v", err)
	}
	if _, err := safePath(cfg, outside1+"/file.txt"); err != nil {
		t.Fatalf("outside1 should be allowed after AddPath: %v", err)
	}
	// outside2 still rejected
	if _, err := safePath(cfg, outside2+"/file.txt"); err == nil {
		t.Fatal("outside2 should still be rejected")
	}

	// Step 5: Runtime mutation — switch to unrestricted (simulates /path unrestricted).
	policy.SetUnrestricted(true)
	if _, err := safePath(cfg, "/anywhere/at/all"); err != nil {
		t.Fatalf("unrestricted should allow any path: %v", err)
	}

	// Step 6: Switch back to restricted — outside2 still not in allowed list.
	policy.SetUnrestricted(false)
	if _, err := safePath(cfg, outside2+"/file.txt"); err == nil {
		t.Fatal("outside2 should be rejected after reverting unrestricted")
	}
	// outside1 still allowed (was added in step 4)
	if _, err := safePath(cfg, outside1+"/deep/nested/file.txt"); err != nil {
		t.Fatalf("outside1 nested path should still be allowed: %v", err)
	}

	// Step 7: Remove outside1 — now it's rejected again.
	policy.RemovePath(outside1)
	if _, err := safePath(cfg, outside1+"/file.txt"); err == nil {
		t.Fatal("outside1 should be rejected after RemovePath")
	}

	// Step 8: Verify scope reflects current state.
	if got := policy.Scope(); got != "workspace" {
		t.Fatalf("Scope() = %q after all mutations, want workspace", got)
	}
}

func TestSafePath_ActionableError(t *testing.T) {
	tmp := t.TempDir()
	cfg := ToolConfig{WorkspaceRoot: tmp}

	_, err := safePath(cfg, "/foo/bar/baz.txt")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "outside workspace") {
		t.Errorf("error should mention 'outside workspace': %s", msg)
	}
	if !strings.Contains(msg, "/path add") {
		t.Errorf("error should suggest /path add: %s", msg)
	}
	if !strings.Contains(msg, "--allow-path") {
		t.Errorf("error should suggest --allow-path: %s", msg)
	}
	if !strings.Contains(msg, "/foo/bar") {
		t.Errorf("error should suggest parent dir /foo/bar: %s", msg)
	}
}
