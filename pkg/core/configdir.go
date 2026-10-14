package core

import (
	"os"
	"path/filepath"
	"sync/atomic"
)

// configPathGuard is nil in production, and production code never installs one:
// path resolution is unchanged there. Test binaries install the hook through
// internal/testenv so that resolving a path inside the developer's real config
// directory fails loudly before anything is read or written there.
var configPathGuard atomic.Pointer[func(path string)]

// SetConfigPathGuardForTests installs (or, with nil, removes) a hook called with
// every path ConfigDir and ConfigSubdir are about to return. Only test
// harnesses should use it.
func SetConfigPathGuardForTests(guard func(path string)) {
	if guard == nil {
		configPathGuard.Store(nil)
		return
	}
	configPathGuard.Store(&guard)
}

func guardConfigPath(path string) {
	if g := configPathGuard.Load(); g != nil && path != "" {
		(*g)(path)
	}
}

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
		guardConfigPath(dir)
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".config", "moa")
	guardConfigPath(dir)
	return dir
}

// ConfigSubdir returns a path inside ConfigDir, or "" if it cannot be
// resolved — callers already treat an empty path as "this feature is
// unavailable" rather than writing to a relative directory.
func ConfigSubdir(parts ...string) string {
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(append([]string{dir}, parts...)...)
	guardConfigPath(path)
	return path
}
