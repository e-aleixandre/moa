package core

import (
	"os"
	"path/filepath"
)

// ConfigDir returns the directory holding moa's own state: config.json,
// credentials, sessions, skills, memory and attachments.
//
// MOA_CONFIG_DIR overrides it. That knob existed before this function but was
// only honored by some of the call sites, so setting it produced a half-moved
// instance: credentials in the new directory, config and history still in the
// home one. Everything that resolves moa state must go through here, so the
// override either moves all of it or none of it.
//
// It deliberately does not cover paths that merely happen to live under the
// home directory, such as expanding "~" in a path the user typed.
func ConfigDir() string {
	if dir := os.Getenv("MOA_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "moa")
}

// ConfigSubdir returns a path inside ConfigDir, or "" if it cannot be
// resolved — callers already treat an empty path as "this feature is
// unavailable" rather than writing to a relative directory.
func ConfigSubdir(parts ...string) string {
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(append([]string{dir}, parts...)...)
}
