// Package testenv keeps test binaries away from the developer's real moa
// configuration. A package that can reach pkg/core calls Run from TestMain.
package testenv

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// protectedEnv carries protected roots to child test binaries (crash helpers
// re-executing os.Args[0]). A child always builds its own sandbox and captures
// its own incoming roots; the inherited list can only add to them.
const protectedEnv = "MOA_TESTENV_PROTECTED"

var (
	mu        sync.RWMutex
	protected []string
)

// Run replaces HOME, XDG_CONFIG_HOME and MOA_CONFIG_DIR with a private
// sandbox, makes core refuse to resolve any config path inside the incoming
// HOME or MOA_CONFIG_DIR, runs the tests and removes the sandbox. The
// environment is not restored afterwards: work that outlives a test stays
// sandboxed too.
func Run(m *testing.M) (code int) {
	// Roots are made absolute now: a later chdir must not move them.
	var roots []string
	if dir := os.Getenv("MOA_CONFIG_DIR"); dir != "" {
		roots = append(roots, resolve(dir))
	}
	if home := os.Getenv("HOME"); home != "" {
		roots = append(roots, resolve(home))
	}
	roots = append(roots, splitList(os.Getenv(protectedEnv))...)
	sandbox, err := os.MkdirTemp("", "moa-testenv-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv: cannot create sandbox:", err)
		return 1
	}
	defer func() {
		if err := removeSandbox(sandbox); err != nil {
			fmt.Fprintln(os.Stderr, "testenv: cannot remove sandbox:", err)
			if code == 0 {
				code = 1
			}
		}
	}()
	for _, root := range roots {
		if within(root, sandbox) {
			fmt.Fprintf(os.Stderr, "testenv: sandbox %s is inside protected config %s\n", sandbox, root)
			return 1
		}
	}
	home := filepath.Join(sandbox, "home")
	tmp := filepath.Join(sandbox, "tmp")
	for _, dir := range []string{home, tmp} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "testenv: cannot create sandbox directory:", err)
			return 1
		}
	}
	pinGoLocations()
	// Child sandboxes stay owned by this run even if a crash test kills them.
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(sandbox, "xdg"),
		"TMPDIR":          tmp,
		protectedEnv:      strings.Join(roots, string(os.PathListSeparator)),
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			return 1
		}
	}
	_ = os.Unsetenv("MOA_CONFIG_DIR")
	install(roots)
	return m.Run()
}

// removeSandbox deletes dir. Tests may leave read-only content behind (module
// caches are, for one), which RemoveAll cannot delete, so on failure the
// owner's write bit is restored without following symlinks and removal is
// retried once.
func removeSandbox(dir string) error {
	if os.RemoveAll(dir) == nil {
		return nil
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if d == nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := d.Info(); err == nil {
			perm := os.FileMode(0o200)
			if d.IsDir() {
				perm = 0o700 // also needed to list and enter it
			}
			_ = os.Chmod(path, info.Mode().Perm()|perm)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// pinGoLocations fixes the Go defaults that derive from the incoming HOME, so
// go commands started by tests keep using the shared caches instead of
// downloading and rebuilding inside the sandbox. Explicit values (including
// GOCACHE=off) are left alone, and a default that cannot be computed stays unset.
func pinGoLocations() {
	pin := func(key string, value func() (string, bool)) {
		if os.Getenv(key) != "" {
			return
		}
		if v, ok := value(); ok {
			_ = os.Setenv(key, v)
		}
	}
	pin("GOPATH", func() (string, bool) {
		home := os.Getenv("HOME")
		return filepath.Join(home, "go"), home != ""
	})
	pin("GOMODCACHE", func() (string, bool) {
		first := filepath.SplitList(os.Getenv("GOPATH"))
		if len(first) == 0 {
			return "", false
		}
		return filepath.Join(first[0], "pkg", "mod"), true
	})
	pin("GOCACHE", func() (string, bool) {
		dir, err := os.UserCacheDir()
		return filepath.Join(dir, "go-build"), err == nil
	})
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return filepath.SplitList(s)
}

func install(roots []string) {
	resolved := make([]string, len(roots))
	for i, r := range roots {
		resolved[i] = resolve(r)
	}
	mu.Lock()
	protected = resolved
	mu.Unlock()
	core.SetConfigPathGuardForTests(func(path string) { Check(path) })
}

// Check panics when path is, or is inside, a config location the harness
// protects.
func Check(path string) {
	mu.RLock()
	roots := protected
	mu.RUnlock()
	for _, root := range roots {
		if within(root, path) {
			panic(fmt.Sprintf("testenv: test code resolved %q inside the real moa config %q; "+
				"point MOA_CONFIG_DIR at t.TempDir() instead", path, root))
		}
	}
}

// AssertNotProtected stops a test that was pointed at the real config by an
// environment variable before it reads or writes there.
func AssertNotProtected(tb testing.TB, path string) {
	tb.Helper()
	defer func() {
		if r := recover(); r != nil {
			tb.Fatalf("%v", r)
		}
	}()
	Check(path)
}

// within reports whether path is root or lies below it, comparing whole path
// elements (so /a/moa-x is not inside /a/moa) and following symlinks of the
// parts that exist.
func within(root, path string) bool {
	r, p := resolve(root), resolve(path)
	rel, err := filepath.Rel(r, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolve makes path absolute and clean, and resolves symlinks in its longest
// existing prefix; only metadata is read, never file contents.
func resolve(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	rest := ""
	for cur := abs; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
