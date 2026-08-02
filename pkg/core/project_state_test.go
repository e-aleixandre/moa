package core

import (
	"os"
	"path/filepath"
	"testing"
)

// The whole point is that clicking "always allow" or switching a server off no
// longer edits a file that belongs to the repository.
func TestProjectState_NeverTouchesTheProject(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()

	if err := AddProjectAllowPattern(workspace, "Bash(git:*)"); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectMCPServerDisabled(workspace, "playwright", true); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(workspace, ".moa")); !os.IsNotExist(err) {
		t.Fatalf("nothing should have been written under the workspace, stat err = %v", err)
	}
	st, err := LoadProjectState(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PermissionAllow) != 1 || st.PermissionAllow[0] != "Bash(git:*)" {
		t.Errorf("PermissionAllow = %v", st.PermissionAllow)
	}
	if len(st.DisabledMCPServers) != 1 || st.DisabledMCPServers[0] != "playwright" {
		t.Errorf("DisabledMCPServers = %v", st.DisabledMCPServers)
	}
}

func TestProjectState_SeparatesWorkspaces(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	a, b := t.TempDir(), t.TempDir()

	if err := AddProjectAllowPattern(a, "Bash(git:*)"); err != nil {
		t.Fatal(err)
	}

	stB, err := LoadProjectState(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(stB.PermissionAllow) != 0 {
		t.Errorf("an approval in one workspace leaked into another: %v", stB.PermissionAllow)
	}
}

func TestProjectState_ApprovalsAreNotDuplicated(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()

	for range 3 {
		if err := AddProjectAllowPattern(workspace, "Bash(git:*)"); err != nil {
			t.Fatal(err)
		}
	}
	st, err := LoadProjectState(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PermissionAllow) != 1 {
		t.Errorf("PermissionAllow = %v, want one entry", st.PermissionAllow)
	}
}

func TestProjectState_VetoCanBeLifted(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()

	if err := SetProjectMCPServerDisabled(workspace, "playwright", true); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectMCPServerDisabled(workspace, "playwright", false); err != nil {
		t.Fatal(err)
	}
	st, err := LoadProjectState(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.DisabledMCPServers) != 0 {
		t.Errorf("DisabledMCPServers = %v, want empty after re-enabling", st.DisabledMCPServers)
	}
}

// Updating must not drop what a previous update stored: the config writer this
// replaces read the file back first, and losing that would silently discard
// approvals the moment a server was toggled.
func TestProjectState_UpdatesPreserveTheOtherFields(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()

	if err := AddProjectAllowPattern(workspace, "Bash(git:*)"); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectMCPServerDisabled(workspace, "playwright", true); err != nil {
		t.Fatal(err)
	}

	st, err := LoadProjectState(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PermissionAllow) != 1 {
		t.Errorf("toggling a server dropped the saved approvals: %v", st.PermissionAllow)
	}
}

// A missing file is a fresh project. Anything else must be reported: treating
// an unreadable file as "no approvals" would re-prompt for everything with no
// indication why.
func TestProjectState_MissingIsEmptyButUnreadableIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", dir)
	workspace := t.TempDir()

	if st, err := LoadProjectState(workspace); err != nil || len(st.PermissionAllow) != 0 {
		t.Fatalf("a fresh project should load empty, got %+v err=%v", st, err)
	}

	path := filepath.Join(ProjectStateDir(workspace), "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjectState(workspace); err == nil {
		t.Error("a corrupt state file should be reported, not read as empty")
	}
}

func TestProjectState_IsPrivateToTheUser(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()

	if err := AddProjectAllowPattern(workspace, "Bash(git:*)"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(ProjectStateDir(workspace), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("state file mode = %o, should not be readable by others", perm)
	}
}

// Memory already scopes per project by hashing the canonical path; both must
// agree or the same project ends up with two directories.
func TestProjectHash_FollowsTheCanonicalPath(t *testing.T) {
	workspace := t.TempDir()
	if ProjectHash(workspace) != ProjectHash(filepath.Join(workspace, ".")) {
		t.Error("equivalent paths should hash the same")
	}
	if ProjectHash(workspace) == ProjectHash(t.TempDir()) {
		t.Error("different workspaces should hash differently")
	}
}

// Allow patterns already sitting in a project's config.json keep working: they
// may have been put there deliberately as a team policy, and moa cannot tell
// those apart from ones a click generated, so nothing is migrated or dropped.
func TestProjectState_DoesNotSupersedeAllowPatternsInTheProjectFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOA_CONFIG_DIR", "")
	cwd := t.TempDir()

	writeConfigJSON(t, filepath.Join(home, ".config", "moa", "config.json"), MoaConfig{
		TrustedProjectPaths: []string{cwd},
	})
	writeConfigJSON(t, filepath.Join(cwd, ".moa", "config.json"), MoaConfig{
		Permissions: PermissionsConfig{Allow: []string{"Bash(make:*)"}},
	})

	cfg := LoadMoaConfig(cwd)
	if len(cfg.Permissions.Allow) != 1 || cfg.Permissions.Allow[0] != "Bash(make:*)" {
		t.Errorf("existing project allow patterns should still load, got %v", cfg.Permissions.Allow)
	}
}
