package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
)

// ProjectHash identifies a workspace by its canonical path. Memory already
// scopes per project this way; sharing the function keeps a project from
// hashing to two different directories depending on who asks.
func ProjectHash(workspaceRoot string) string {
	canonical, err := CanonicalizePath(workspaceRoot)
	if err != nil {
		canonical = filepath.Clean(workspaceRoot)
	}
	h := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(h[:8])
}

// ProjectStateDir returns where this user's state for a workspace lives, or ""
// when the config directory cannot be resolved.
func ProjectStateDir(workspaceRoot string) string {
	return ConfigSubdir("projects", ProjectHash(workspaceRoot))
}

// ProjectState is what moa decides on its own while the user works in a
// project: approvals they granted, MCP servers they switched off.
//
// It is deliberately not a MoaConfig. <project>/.moa/config.json describes the
// project — which MCP servers it uses, what its limits are — and is meant to be
// committed and shared. What one person clicked is not that: it followed the
// user's machine into the repository, showed up as a diff nobody wanted, and on
// a shared checkout the first writer's private permissions locked everyone else
// out of the directory. So it lives with the user instead.
type ProjectState struct {
	// PermissionAllow holds patterns approved with "always allow".
	PermissionAllow []string `json:"permission_allow,omitempty"`
	// DisabledMCPServers holds servers this user switched off for this project.
	DisabledMCPServers []string `json:"disabled_mcp_servers,omitempty"`
	// Config is settings you want in this project but not in the repository —
	// a turn limit you prefer here, a review model, your own budget. It is
	// hand-edited: moa never writes it. Merged after the project's own config
	// and before session flags, and subject to the same rules, so it can
	// tighten a limit the project set but never relax one.
	Config *MoaConfig `json:"config,omitempty"`
	// LegacyVetoImported records that vetoes an older moa wrote into the
	// project's own config were carried over. It has to be remembered: without
	// it the import would run again after the user switches a server back on,
	// and the toggle would appear to do nothing.
	LegacyVetoImported bool `json:"legacy_veto_imported,omitempty"`
}

func projectStatePath(workspaceRoot string) string {
	dir := ProjectStateDir(workspaceRoot)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "state.json")
}

// LoadProjectState reads this user's state for a workspace. A missing file is
// an empty state; anything else is an error, because silently treating an
// unreadable file as "no approvals" would re-prompt for everything with no
// indication why.
func LoadProjectState(workspaceRoot string) (ProjectState, error) {
	path := projectStatePath(workspaceRoot)
	if path == "" {
		return ProjectState{}, errors.New("cannot resolve the moa config directory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ProjectState{}, nil
		}
		return ProjectState{}, err
	}
	var st ProjectState
	if err := json.Unmarshal(data, &st); err != nil {
		return ProjectState{}, fmt.Errorf("corrupt project state %s: %w", path, err)
	}
	// The config block is hand-edited, so a typo is likely and silence is the
	// worst answer: "max_turms" would parse fine and do nothing at all. It only
	// warns, since a state written by a newer moa may legitimately carry fields
	// this build does not know.
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(new(ProjectState)); err != nil {
		slog.Warn("project state: unrecognized setting, check for a typo", "file", path, "error", err)
	}
	return st, nil
}

// UpdateProjectState applies update to the stored state and writes it back.
//
// update must not call back into UpdateProjectState or the lock deadlocks, and
// it should not read the state either: it receives the current one, and a
// LoadProjectState from inside would return the version before its own edits.
//
// The whole read-modify-write is under an advisory lock, and the write goes
// through a uniquely named temporary file. Both matter: the project config
// writer this replaces used a fixed temp name and no lock, so two moa
// processes could clobber each other — reachable with one user running two
// sessions in the same project, which would silently drop an approval or
// re-enable a server they had switched off.
func UpdateProjectState(workspaceRoot string, update func(*ProjectState)) error {
	path := projectStatePath(workspaceRoot)
	if path == "" {
		return errors.New("cannot resolve the moa config directory")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := acquireStateLock(path + ".lock")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	current, err := LoadProjectState(workspaceRoot)
	if err != nil {
		return err
	}
	update(&current)

	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, "state-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// AddProjectAllowPattern records an "always allow" approval for a workspace.
func AddProjectAllowPattern(workspaceRoot, pattern string) error {
	return UpdateProjectState(workspaceRoot, func(st *ProjectState) {
		if !slices.Contains(st.PermissionAllow, pattern) {
			st.PermissionAllow = append(st.PermissionAllow, pattern)
		}
	})
}

// ImportLegacyProjectVeto carries vetoes an older moa wrote into a trusted
// project's own config over to this user's state, once.
//
// Merging them on every read instead would leave the MCP panel in a state the
// user cannot get out of: the veto would show under the project scope, but
// switching the server back on writes to the state, and the next session would
// read the project file again and turn it off. Importing makes the toggle mean
// what it says, and the flag stops the import from undoing that later.
func ImportLegacyProjectVeto(cwd string) error {
	global := loadConfigFile(globalConfigPath())
	if !IsProjectPathTrusted(global, cwd) {
		return nil
	}
	state, err := LoadProjectState(cwd)
	if err != nil || state.LegacyVetoImported {
		return err
	}
	legacy := loadConfigFile(filepath.Join(cwd, ".moa", "config.json")).DisabledMCPServers

	return UpdateProjectState(cwd, func(st *ProjectState) {
		if st.LegacyVetoImported {
			return // another session won the race
		}
		st.LegacyVetoImported = true
		for _, server := range legacy {
			if !slices.Contains(st.DisabledMCPServers, server) {
				st.DisabledMCPServers = append(st.DisabledMCPServers, server)
			}
		}
	})
}

// SetProjectMCPServerDisabled records this user's veto for a workspace.
func SetProjectMCPServerDisabled(workspaceRoot, server string, disabled bool) error {
	return UpdateProjectState(workspaceRoot, func(st *ProjectState) {
		has := slices.Contains(st.DisabledMCPServers, server)
		switch {
		case disabled && !has:
			st.DisabledMCPServers = append(st.DisabledMCPServers, server)
		case !disabled && has:
			st.DisabledMCPServers = slices.DeleteFunc(st.DisabledMCPServers, func(s string) bool {
				return s == server
			})
		}
	})
}
