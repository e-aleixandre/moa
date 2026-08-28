package core

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
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

// Two sessions in the same project update this concurrently. Without a lock
// around the read-modify-write, each one saves what it read and the last
// rename wins — silently dropping an approval, or re-enabling a server the
// user had switched off.
func TestProjectState_ConcurrentUpdatesAllSurvive(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()

	const writers = 40
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := AddProjectAllowPattern(workspace, fmt.Sprintf("Bash(cmd%02d:*)", i)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent update: %v", err)
	}

	st, err := LoadProjectState(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PermissionAllow) != writers {
		t.Errorf("saved %d approvals, want %d — concurrent updates were lost",
			len(st.PermissionAllow), writers)
	}
}

// Settings you want in one project but not in its repository: a turn limit you
// prefer here. Global config is for every project, and
// the project's own file is committed and shared — neither fits.
func TestProjectState_ConfigAppliesToThisProjectOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOA_CONFIG_DIR", "")
	cwd := t.TempDir()

	writeConfigJSON(t, filepath.Join(home, ".config", "moa", "config.json"), MoaConfig{
		MaxTurns: 100,
	})
	if err := UpdateProjectState(cwd, func(st *ProjectState) {
		st.Config = &MoaConfig{MaxTurns: 40}
	}); err != nil {
		t.Fatal(err)
	}

	cfg := LoadMoaConfig(cwd)
	if cfg.MaxTurns != 40 {
		t.Errorf("MaxTurns = %d, want your project setting (40)", cfg.MaxTurns)
	}
	// Another workspace keeps the global value.
	if other := LoadMoaConfig(t.TempDir()); other.MaxTurns != 100 {
		t.Errorf("MaxTurns leaked into another project: %d", other.MaxTurns)
	}
}

// The merge rules are the existing ones: limits can be tightened, never
// relaxed. Your own file is no exception — otherwise it would be a way to
// quietly undo a guardrail set globally.
func TestProjectState_ConfigCannotRelaxAGlobalLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOA_CONFIG_DIR", "")
	cwd := t.TempDir()

	writeConfigJSON(t, filepath.Join(home, ".config", "moa", "config.json"), MoaConfig{
		MaxTurns: 50,
	})
	if err := UpdateProjectState(cwd, func(st *ProjectState) {
		st.Config = &MoaConfig{MaxTurns: 500}
	}); err != nil {
		t.Fatal(err)
	}

	if got := LoadMoaConfig(cwd).MaxTurns; got != 50 {
		t.Errorf("MaxTurns = %d, want the tighter global limit (50)", got)
	}
}

// It applies without trusting the project: the file is yours, not the
// repository's, so there is nothing about the checkout to vet.
func TestProjectState_ConfigNeedsNoProjectTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOA_CONFIG_DIR", "")
	cwd := t.TempDir()

	writeConfigJSON(t, filepath.Join(home, ".config", "moa", "config.json"), MoaConfig{})
	writeConfigJSON(t, filepath.Join(cwd, ".moa", "config.json"), MoaConfig{
		MaxTurns: 999, // untrusted project: must be ignored
	})
	if err := UpdateProjectState(cwd, func(st *ProjectState) {
		st.Config = &MoaConfig{MaxTurns: 40}
	}); err != nil {
		t.Fatal(err)
	}

	if got := LoadMoaConfig(cwd).MaxTurns; got != 40 {
		t.Errorf("MaxTurns = %d, want your own setting (40)", got)
	}
}

// Moa writes approvals and vetoes into the same file, so a hand-edited config
// block must survive that.
func TestProjectState_ConfigSurvivesAnApproval(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir()

	if err := UpdateProjectState(cwd, func(st *ProjectState) {
		st.Config = &MoaConfig{MaxTurns: 40}
	}); err != nil {
		t.Fatal(err)
	}
	if err := AddProjectAllowPattern(cwd, "Bash(git:*)"); err != nil {
		t.Fatal(err)
	}

	st, err := LoadProjectState(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if st.Config == nil || st.Config.MaxTurns != 40 {
		t.Errorf("approving a permission dropped your settings: %+v", st.Config)
	}
}

// A veto written by hand in the config block has to reach the effective MCP
// policy. The block is documented as taking the same fields, so a setting that
// shows up in the merged config but never stops the server would be a trap.
func TestProjectState_ConfigVetoReachesTheMCPPolicy(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir()

	if err := UpdateProjectState(cwd, func(st *ProjectState) {
		st.Config = &MoaConfig{DisabledMCPServers: []string{"db"}}
	}); err != nil {
		t.Fatal(err)
	}

	if got := LoadMoaConfigResolved(cwd).MCPDisabled.Project; !slices.Contains(got, "db") {
		t.Errorf("the veto never reaches the effective policy: %v", got)
	}
}

// A mistyped key parses fine and does nothing, so it is worth a warning rather
// than silence. Unknown fields must not stop the rest of the file working.
func TestProjectState_UnknownFieldDoesNotDiscardTheRest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", dir)
	cwd := t.TempDir()

	if err := UpdateProjectState(cwd, func(st *ProjectState) {
		st.PermissionAllow = []string{"Bash(git:*)"}
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ProjectStateDir(cwd), "state.json")
	if err := os.WriteFile(path,
		[]byte(`{"permission_allow":["Bash(git:*)"],"max_turms":40}`), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := LoadProjectState(cwd)
	if err != nil {
		t.Fatalf("an unknown field must not make the state unreadable: %v", err)
	}
	if !slices.Contains(st.PermissionAllow, "Bash(git:*)") {
		t.Errorf("the rest of the file was discarded: %+v", st)
	}
}
