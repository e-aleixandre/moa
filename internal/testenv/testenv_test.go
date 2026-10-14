package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestMain(m *testing.M) { os.Exit(Run(m)) }

func protect(t *testing.T, roots ...string) {
	t.Helper()
	mu.RLock()
	old := protected
	mu.RUnlock()
	install(roots)
	t.Cleanup(func() { install(old) })
}

func mustPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s: no panic", what)
		}
		if !strings.Contains(fmt.Sprint(r), "testenv:") {
			t.Fatalf("%s: unclear diagnostic %v", what, r)
		}
	}()
	f()
}

func TestRunSandboxesTheEnvironment(t *testing.T) {
	if got := os.Getenv("MOA_CONFIG_DIR"); got != "" {
		t.Errorf("MOA_CONFIG_DIR = %q, want unset", got)
	}
	home := os.Getenv("HOME")
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		t.Fatalf("sandbox HOME must exist as a directory: %v", err)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); !strings.HasPrefix(xdg, filepath.Dir(home)) {
		t.Errorf("XDG_CONFIG_HOME %q not beside HOME %q", xdg, home)
	}
	if got, want := core.ConfigDir(), filepath.Join(home, ".config", "moa"); got != want {
		t.Errorf("ConfigDir = %q, want %q", got, want)
	}
	if tmp := os.TempDir(); !within(filepath.Dir(home), tmp) || within(home, tmp) {
		t.Errorf("temporary directory %q must be owned by the sandbox, outside its HOME %q", tmp, home)
	}
}

func TestGuardRejectsInheritedHomeBeforeIO(t *testing.T) {
	fake := t.TempDir()
	protect(t, filepath.Join(fake, ".config", "moa"))
	t.Setenv("HOME", fake)
	t.Setenv("MOA_CONFIG_DIR", "")
	mustPanic(t, "ConfigDir", func() { core.ConfigDir() })
	mustPanic(t, "ConfigSubdir", func() { core.ConfigSubdir("sessions") })
	if _, err := os.Stat(filepath.Join(fake, ".config")); !os.IsNotExist(err) {
		t.Errorf("guard must not create anything: %v", err)
	}
}

func TestGuardRejectsInheritedOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "prod")
	protect(t, override)
	t.Setenv("MOA_CONFIG_DIR", override)
	mustPanic(t, "ConfigDir", func() { core.ConfigDir() })
	mustPanic(t, "ConfigSubdir", func() { core.ConfigSubdir("tasks.sqlite") })
}

func TestGuardRejectsSubdirTraversalIntoProtected(t *testing.T) {
	base := t.TempDir()
	prod := filepath.Join(base, "prod")
	protect(t, prod)
	t.Setenv("MOA_CONFIG_DIR", filepath.Join(base, "temp"))
	if got := core.ConfigDir(); got != filepath.Join(base, "temp") {
		t.Fatalf("genuine override rejected: %q", got)
	}
	mustPanic(t, "traversal", func() { core.ConfigSubdir("..", "prod", "sessions") })
}

func TestGuardAllowsGenuineTempAndSiblingPrefix(t *testing.T) {
	base := t.TempDir()
	prod := filepath.Join(base, "moa")
	protect(t, prod)
	for _, dir := range []string{filepath.Join(base, "moa-other"), filepath.Join(base, "moa2", "x"), t.TempDir()} {
		t.Setenv("MOA_CONFIG_DIR", dir)
		if got := core.ConfigSubdir("sessions"); got != filepath.Join(dir, "sessions") {
			t.Errorf("ConfigSubdir under %s = %q", dir, got)
		}
	}
}

func TestGuardFollowsSymlinkAlias(t *testing.T) {
	base := t.TempDir()
	prod := filepath.Join(base, "prod")
	if err := os.Mkdir(prod, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(prod, alias); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	protect(t, prod)
	t.Setenv("MOA_CONFIG_DIR", alias)
	mustPanic(t, "alias", func() { core.ConfigSubdir("x", "y") })
}

func TestAssertNotProtectedFailsTheTest(t *testing.T) {
	base := t.TempDir()
	protect(t, filepath.Join(base, "prod"))
	AssertNotProtected(t, filepath.Join(base, "prod-copy"))
	ft := &recorder{TB: t}
	func() {
		defer func() { _ = recover() }() // recorder.Fatalf aborts like testing does
		AssertNotProtected(ft, filepath.Join(base, "prod", "sessions"))
	}()
	if !ft.failed {
		t.Fatal("protected path accepted")
	}
}

type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Fatalf(string, ...any) { r.failed = true; panic("fatal") }

func TestGuardRejectsAnyPathUnderProtectedHome(t *testing.T) {
	fake := t.TempDir()
	protect(t, fake)
	t.Setenv("MOA_CONFIG_DIR", filepath.Join(fake, "alternate-moa"))
	mustPanic(t, "ConfigDir", func() { core.ConfigDir() })
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	mustPanic(t, "traversal", func() { core.ConfigSubdir("..", filepath.Base(fake), "x") })
}

const probeEnv = "MOA_TESTENV_PROBE"

// TestProbeHelper runs in a child started by the tests below: after the
// child's own TestMain captured its incoming environment and the test changes
// directory, the probe path must still be refused.
func TestProbeHelper(t *testing.T) {
	probe := os.Getenv(probeEnv)
	if probe == "" {
		t.Skip("subprocess helper")
	}
	if err := os.Chdir(os.TempDir()); err != nil {
		t.Fatal(err)
	}
	mustPanic(t, "probe", func() { Check(probe) })
}

func runProbe(t *testing.T, dir string, env ...string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestProbeHelper$", "-test.count=1")
	cmd.Dir = dir
	cmd.Env = append([]string{"TMPDIR=" + t.TempDir()}, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestRunIgnoresAnEmptyOrForgedMarker(t *testing.T) {
	home := t.TempDir()
	override := t.TempDir()
	for _, marker := range []string{"", "/nonexistent"} {
		runProbe(t, t.TempDir(), "HOME="+home, probeEnv+"="+filepath.Join(home, "x", "y"), protectedEnv+"="+marker)
		runProbe(t, t.TempDir(), "HOME="+t.TempDir(), "MOA_CONFIG_DIR="+override,
			probeEnv+"="+filepath.Join(override, "tasks.sqlite"), protectedEnv+"="+marker)
	}
}

func TestRunKeepsARelativeOverrideAnchoredAtStartup(t *testing.T) {
	base := t.TempDir()
	runProbe(t, base, "HOME="+t.TempDir(), "MOA_CONFIG_DIR=prod",
		probeEnv+"="+filepath.Join(base, "prod", "sessions"))
}

func TestInheritedMarkerOnlyAddsRoots(t *testing.T) {
	extra := t.TempDir()
	runProbe(t, t.TempDir(), "HOME="+t.TempDir(), protectedEnv+"="+extra, probeEnv+"="+filepath.Join(extra, "a"))
}

// isolated runs scripts/test-isolated.sh from a throwaway HOME so that the
// developer's real configuration is never involved.
func isolated(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	fake := t.TempDir()
	cmd := exec.Command("sh", append([]string{filepath.Join(moduleRoot(t), "scripts", "test-isolated.sh")}, args...)...)
	cmd.Dir = fake
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + fake, "XDG_CONFIG_HOME=" + filepath.Join(fake, "xdg"),
		"MOA_CONFIG_DIR=", "GOTOOLCHAIN=local"}, env...)
	out, err := cmd.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

const printEnv = `printf '%s\n' "$HOME" "$MOA_CONFIG_DIR" "$XDG_CONFIG_HOME" "$TMPDIR" "$GOPATH" "$GOMODCACHE" "$GOCACHE"`

func TestIsolatedScriptKeepsExplicitGoCaches(t *testing.T) {
	out, code := isolated(t, []string{"GOPATH=/x/gopath", "GOMODCACHE=/x/mod", "GOCACHE=/x/cache"}, "sh", "-c", printEnv)
	got := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(got) != 7 || got[4] != "/x/gopath" || got[5] != "/x/mod" || got[6] != "/x/cache" {
		t.Fatalf("code %d, env %q", code, got)
	}
	root := filepath.Dir(got[0])
	for _, p := range got[:4] {
		if filepath.Dir(p) != root || !strings.HasPrefix(root, "/tmp/moa-isolated.") {
			t.Errorf("%q not owned by the /tmp sandbox %q", p, root)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("sandbox %q survived the command (%v)", root, err)
	}
}

func TestIsolatedScriptResolvesGoDefaultsBeforeChangingHome(t *testing.T) {
	fake := t.TempDir()
	env := []string{"HOME=" + fake, "GOPATH=", "GOMODCACHE=", "GOCACHE=", "XDG_CACHE_HOME="}
	out, _ := isolated(t, env, "sh", "-c", printEnv)
	got := strings.Split(strings.TrimSpace(out), "\n")
	if len(got) != 7 {
		t.Fatalf("unexpected output %q", out)
	}
	if want := filepath.Join(fake, "go"); got[4] != want {
		t.Errorf("GOPATH = %q, want %q", got[4], want)
	}
	for i, p := range got[4:] {
		if strings.HasPrefix(p, "/tmp/moa-isolated.") || p == "" {
			t.Errorf("Go location %d = %q points into the sandbox or is empty", i, p)
		}
	}
}

func TestIsolatedScriptRemovesReadOnlyFilesAndKeepsExitStatus(t *testing.T) {
	out, code := isolated(t, nil, "sh", "-c", `mkdir "$HOME/ro" && echo x > "$HOME/ro/f" && chmod -R a-w "$HOME/ro"; echo "$HOME"; exit 7`)
	if code != 7 {
		t.Fatalf("exit status %d, want 7", code)
	}
	if home := strings.TrimSpace(out); home == "" {
		t.Fatal("command did not run")
	} else if _, err := os.Stat(filepath.Dir(home)); !os.IsNotExist(err) {
		t.Errorf("sandbox %q survived (%v)", filepath.Dir(home), err)
	}
}

func TestIsolatedScriptWithoutArgsPrintsUsageAndCreatesNothing(t *testing.T) {
	before, _ := filepath.Glob("/tmp/moa-isolated.*")
	out, code := isolated(t, nil)
	after, _ := filepath.Glob("/tmp/moa-isolated.*")
	if code != 2 || out != "" || len(after) > len(before) {
		t.Fatalf("code %d, stdout %q, sandboxes %d -> %d", code, out, len(before), len(after))
	}
}

// Run must leave the Go cache variables untouched: they are not config.
func TestRunLeavesGoCacheVariablesAlone(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestCacheEnvHelper$", "-test.count=1")
	cmd.Env = []string{"TMPDIR=" + t.TempDir(), "HOME=" + t.TempDir(), "GOCACHE=/x/cache", "GOMODCACHE=/x/mod", "GOPATH=/x/gopath", "GOFLAGS_UNSET_CHECK=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestCacheEnvHelper(t *testing.T) {
	if os.Getenv("GOFLAGS_UNSET_CHECK") == "" {
		t.Skip("subprocess helper")
	}
	for k, v := range map[string]string{"GOCACHE": "/x/cache", "GOMODCACHE": "/x/mod", "GOPATH": "/x/gopath"} {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
