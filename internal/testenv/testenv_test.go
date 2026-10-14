package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
	cmd := exec.Command(filepath.Join(moduleRoot(t), "scripts", "test-isolated.sh"), args...)
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
	out, code := isolated(t, env, "sh", "-c", printEnv)
	got := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(got) != 7 {
		t.Fatalf("code %d, output %q", code, out)
	}
	want := []string{filepath.Join(fake, "go"), filepath.Join(fake, "go", "pkg", "mod"), filepath.Join(fake, ".cache", "go-build")}
	for i, w := range want {
		if got[4+i] != w {
			t.Errorf("Go location %d = %q, want %q", i, got[4+i], w)
		}
	}
}

func TestIsolatedScriptDerivesModCacheFromFirstGOPATHEntryAndHonoursXDG(t *testing.T) {
	fake := t.TempDir()
	env := []string{"HOME=" + fake, "GOPATH=/x/a:/x/b", "GOMODCACHE=", "GOCACHE=", "XDG_CACHE_HOME=/x/xdg"}
	out, _ := isolated(t, env, "sh", "-c", printEnv)
	got := strings.Split(strings.TrimSpace(out), "\n")
	if len(got) != 7 || got[4] != "/x/a:/x/b" || got[5] != "/x/a/pkg/mod" || got[6] != "/x/xdg/go-build" {
		t.Fatalf("env %q", got)
	}
}

func TestIsolatedScriptLeavesGOCACHEOffAlone(t *testing.T) {
	out, _ := isolated(t, []string{"GOCACHE=off"}, "sh", "-c", printEnv)
	got := strings.Split(strings.TrimSpace(out), "\n")
	if len(got) != 7 || got[6] != "off" {
		t.Fatalf("env %q", got)
	}
}

// A wrapper run inside another wrapper, with the caches left to Go's defaults,
// must still resolve them from the outermost HOME.
func TestIsolatedScriptNestsWithoutPointingCachesIntoAWrapper(t *testing.T) {
	fake := t.TempDir()
	script := filepath.Join(moduleRoot(t), "scripts", "test-isolated.sh")
	env := []string{"HOME=" + fake, "GOPATH=", "GOMODCACHE=", "GOCACHE=", "XDG_CACHE_HOME="}
	out, code := isolated(t, env, script, "sh", "-c", printEnv)
	got := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(got) != 7 {
		t.Fatalf("code %d, output %q", code, out)
	}
	want := []string{filepath.Join(fake, "go"), filepath.Join(fake, "go", "pkg", "mod"), filepath.Join(fake, ".cache", "go-build")}
	for i, p := range got[4:] {
		if p != want[i] {
			t.Errorf("nested Go location %d = %q, want %q", i, p, want[i])
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

// A signal sent only to the wrapper must stop the literal long-lived command
// and remove the sandbox.
func TestIsolatedScriptForwardsSignalsToTheCommand(t *testing.T) {
	if _, err := os.Stat("/proc/self/task"); err != nil {
		t.Skip("needs /proc")
	}
	for _, tc := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGTERM, 143}, {syscall.SIGINT, 130}} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			cmd := exec.Command(filepath.Join(moduleRoot(t), "scripts", "test-isolated.sh"), "sleep", "60")
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GOPATH=/x", "GOCACHE=/x"}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			wrapper := cmd.Process.Pid
			var child int
			var sandbox string
			defer func() {
				_ = syscall.Kill(-wrapper, syscall.SIGKILL) // no leftovers if the test fails
				if child != 0 {
					// The command runs in its own process group.
					_ = syscall.Kill(-child, syscall.SIGKILL)
					_ = syscall.Kill(child, syscall.SIGKILL)
				}
				_ = cmd.Wait()
				if sandbox != "" {
					_ = os.RemoveAll(sandbox)
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for child == 0 && time.Now().Before(deadline) {
				kids, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", wrapper, wrapper))
				// mktemp and mkdir are transient children; wait for the sleep.
				if f := strings.Fields(string(kids)); len(f) == 1 {
					comm, _ := os.ReadFile(fmt.Sprintf("/proc/%s/comm", f[0]))
					if strings.TrimSpace(string(comm)) != "sleep" {
						time.Sleep(10 * time.Millisecond)
						continue
					}
					child, _ = strconv.Atoi(f[0])
					if env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", child)); err == nil {
						for _, kv := range strings.Split(string(env), "\x00") {
							if v, ok := strings.CutPrefix(kv, "HOME="); ok {
								sandbox = filepath.Dir(v)
							}
						}
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if child == 0 || sandbox == "" {
				t.Fatalf("wrapper %d never started its command (child %d, sandbox %q)", wrapper, child, sandbox)
			}
			if err := syscall.Kill(wrapper, tc.sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != tc.code {
					t.Errorf("wrapper exit = %v, want %d", err, tc.code)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("wrapper still running 5s after %v", tc.sig)
			}
			if err := syscall.Kill(child, 0); err == nil {
				t.Errorf("command %d survived", child)
			}
			if _, err := os.Stat(sandbox); !os.IsNotExist(err) {
				t.Errorf("sandbox %q survived (%v)", sandbox, err)
			}
		})
	}
}

// A signal sent only to the wrapper must reach the whole process group of the
// command: a shell and the sleep it is waiting on both stop, and the wrapper
// exits promptly with the signal's status.
func TestIsolatedScriptSignalsTheWholeCommandGroup(t *testing.T) {
	if _, err := os.Stat("/proc/self/task"); err != nil {
		t.Skip("needs /proc")
	}
	for _, tc := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGTERM, 143}, {syscall.SIGINT, 130}} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "sh.pid")
			// The trailing ":" keeps sh from exec'ing the sleep.
			cmd := exec.Command(filepath.Join(moduleRoot(t), "scripts", "test-isolated.sh"),
				"sh", "-c", `echo $$ > "$1"; sleep 30; :`, "sh", pidFile)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GOPATH=/x", "GOCACHE=/x"}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			wrapper := cmd.Process.Pid
			var shell, sleeper int
			var sandbox string
			defer func() {
				// Only PIDs and groups recorded by this test.
				_ = syscall.Kill(-wrapper, syscall.SIGKILL)
				if shell != 0 {
					_ = syscall.Kill(-shell, syscall.SIGKILL)
					_ = syscall.Kill(shell, syscall.SIGKILL)
				}
				if sleeper != 0 {
					_ = syscall.Kill(sleeper, syscall.SIGKILL)
				}
				_ = cmd.Wait()
				if sandbox != "" {
					_ = os.RemoveAll(sandbox)
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for sleeper == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
				b, _ := os.ReadFile(pidFile)
				pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
				if err != nil {
					continue
				}
				shell = pid
				kids, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", shell, shell))
				for _, f := range strings.Fields(string(kids)) {
					if comm, _ := os.ReadFile("/proc/" + f + "/comm"); strings.TrimSpace(string(comm)) == "sleep" {
						sleeper, _ = strconv.Atoi(f)
					}
				}
			}
			if sleeper == 0 {
				t.Fatalf("wrapper %d never started the shell's sleep (shell %d)", wrapper, shell)
			}
			if env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", shell)); err == nil {
				for _, kv := range strings.Split(string(env), "\x00") {
					if v, ok := strings.CutPrefix(kv, "HOME="); ok {
						sandbox = filepath.Dir(v)
					}
				}
			}
			if pg, err := syscall.Getpgid(shell); err != nil || pg == wrapper {
				t.Errorf("command group %d (%v) must differ from the wrapper %d", pg, err, wrapper)
			}
			if err := syscall.Kill(wrapper, tc.sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != tc.code {
					t.Errorf("wrapper exit = %v, want %d", err, tc.code)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("wrapper still running 5s after %v", tc.sig)
			}
			for name, pid := range map[string]int{"shell": shell, "sleep": sleeper} {
				if st, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); len(st) > 0 {
					if i := strings.LastIndex(string(st), ") "); i >= 0 && string(st)[i+2] != 'Z' {
						t.Errorf("%s %d still running after %v", name, pid, tc.sig)
					}
				}
			}
			if sandbox != "" {
				if _, err := os.Stat(sandbox); !os.IsNotExist(err) {
					t.Errorf("sandbox %q survived (%v)", sandbox, err)
				}
			}
		})
	}
}

const cleanupEnv = "MOA_TESTENV_CLEANUP"

func runCleanupChild(t *testing.T, mode string) (parent string, out []byte, err error) {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	parent = t.TempDir()
	cmd := exec.Command(exe, "-test.run=^TestCleanupHelper$", "-test.count=1")
	cmd.Env = []string{"TMPDIR=" + parent, "HOME=" + t.TempDir(), cleanupEnv + "=" + mode, cleanupEnv + "_PARENT=" + parent}
	out, err = cmd.CombinedOutput()
	return parent, out, err
}

// TestCleanupHelper runs in a child: it leaves read-only content in the
// sandbox ("readonly"), or makes the sandbox's parent unwritable so that it
// cannot be removed at all ("stuck").
func TestCleanupHelper(t *testing.T) {
	mode := os.Getenv(cleanupEnv)
	if mode == "" {
		t.Skip("subprocess helper")
	}
	switch mode {
	case "readonly":
		dir := filepath.Join(os.Getenv("HOME"), "ro")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
	case "stuck":
		if err := os.Chmod(os.Getenv(cleanupEnv+"_PARENT"), 0o500); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunRemovesReadOnlySandboxContent(t *testing.T) {
	parent, out, err := runCleanupChild(t, "readonly")
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if left, _ := os.ReadDir(parent); len(left) != 0 {
		t.Errorf("sandbox left behind: %v", left)
	}
}

func TestRunFailsWhenTheSandboxCannotBeRemoved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := ""
	defer func() {
		if parent != "" {
			_ = os.Chmod(parent, 0o700) // let t.TempDir clean up
		}
	}()
	parent, out, err := runCleanupChild(t, "stuck")
	_ = os.Chmod(parent, 0o700)
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() == 0 {
		t.Errorf("child exit = %v, want non-zero\n%s", err, out)
	}
	if !strings.Contains(string(out), "testenv: cannot remove sandbox") {
		t.Errorf("no diagnostic in output:\n%s", out)
	}
}

// Unset Go cache variables must resolve against the incoming HOME, not the
// sandbox one; explicit values stay as given.
func TestRunPinsGoDefaultsFromIncomingHome(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestGoDefaultsHelper$", "-test.count=1")
	cmd.Env = []string{"TMPDIR=" + t.TempDir(), "HOME=" + home, "GOCACHE_PIN_CHECK=" + home, "GOMODCACHE=/x/mod",
		"GOCACHE_PIN_EXPECTED=" + filepath.Join(cacheDir, "go-build"), "GOCACHE_PIN_GO=" + goTool, "GOTOOLCHAIN=local"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	cmd = exec.Command(exe, "-test.run=^TestGoDefaultsHelper$", "-test.count=1")
	cmd.Env = append([]string{"TMPDIR=" + t.TempDir(), "HOME=" + home}, "GOCACHE=off", "GOPATH=/x/a"+string(os.PathListSeparator)+"/x/b")
	cmd.Env = append(cmd.Env, "GOCACHE_PIN_EXPLICIT=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("explicit child: %v\n%s", err, out)
	}
}

func TestGoDefaultsHelper(t *testing.T) {
	if os.Getenv("GOCACHE_PIN_EXPLICIT") != "" {
		if os.Getenv("GOCACHE") != "off" || os.Getenv("GOPATH") != "/x/a"+string(os.PathListSeparator)+"/x/b" ||
			os.Getenv("GOMODCACHE") != "/x/a/pkg/mod" {
			t.Errorf("explicit values changed: GOCACHE=%q GOPATH=%q GOMODCACHE=%q",
				os.Getenv("GOCACHE"), os.Getenv("GOPATH"), os.Getenv("GOMODCACHE"))
		}
		return
	}
	home := os.Getenv("GOCACHE_PIN_CHECK")
	if home == "" {
		t.Skip("subprocess helper")
	}
	cache, err := os.UserCacheDir() // HOME is the sandbox here
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "go"); os.Getenv("GOPATH") != want {
		t.Errorf("GOPATH = %q, want %q", os.Getenv("GOPATH"), want)
	}
	if os.Getenv("GOMODCACHE") != "/x/mod" {
		t.Errorf("explicit GOMODCACHE changed: %q", os.Getenv("GOMODCACHE"))
	}
	if got, want := os.Getenv("GOCACHE"), os.Getenv("GOCACHE_PIN_EXPECTED"); got != want || strings.HasPrefix(got, cache) {
		t.Errorf("GOCACHE = %q, want %q", got, want)
	}
	out, err := exec.Command(os.Getenv("GOCACHE_PIN_GO"), "env", "GOPATH", "GOMODCACHE", "GOCACHE").CombinedOutput()
	if err != nil {
		t.Fatalf("go env: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	want := []string{os.Getenv("GOPATH"), os.Getenv("GOMODCACHE"), os.Getenv("GOCACHE")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("child go env = %q, want shared locations %q", got, want)
	}
}

// Settings persisted with go env -w must win over the built-in defaults, and a
// single go invocation resolves all three locations.
func TestRunPinsGoLocationsFromPersistedConfig(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("needs go on PATH")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := t.TempDir()
	goenv := filepath.Join(t.TempDir(), "go.env")
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + fake, "GOENV=" + goenv, "GOTOOLCHAIN=local", "GOPROXY=off"}
	want := []string{"/x/pa" + string(os.PathListSeparator) + "/x/pb", "/x/mod", "/x/cache"}
	write := exec.Command(goTool, "env", "-w", "GOPATH="+want[0], "GOMODCACHE="+want[1], "GOCACHE="+want[2])
	write.Env = base
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("go env -w: %v\n%s", err, out)
	}
	cmd := exec.Command(exe, "-test.run=^TestPersistedGoEnvHelper$", "-test.count=1")
	cmd.Env = append(append([]string{}, base...), "TMPDIR="+t.TempDir(), "PERSISTED_GO_WANT="+strings.Join(want, "\n"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestPersistedGoEnvHelper(t *testing.T) {
	want := os.Getenv("PERSISTED_GO_WANT")
	if want == "" {
		t.Skip("subprocess helper")
	}
	got := []string{os.Getenv("GOPATH"), os.Getenv("GOMODCACHE"), os.Getenv("GOCACHE")}
	if !reflect.DeepEqual(got, strings.Split(want, "\n")) {
		t.Errorf("pinned %q, want %q", got, strings.Split(want, "\n"))
	}
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output() // HOME is the sandbox here
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	if g := strings.Split(strings.TrimSpace(string(out)), "\n"); !reflect.DeepEqual(g, got) {
		t.Errorf("child go env = %q, want %q", g, got)
	}
}

// With a go on PATH, one query supplies all three values; when it fails the
// test binary stops with a clear message instead of guessing.
func TestRunQueriesGoOnceAndFailsWhenTheQueryFails(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"works", `printf '/g/path\n/g/mod\noff\n'`, true},
		{"fails", `echo boom >&2; exit 3`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, log := t.TempDir(), filepath.Join(t.TempDir(), "calls")
			script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" + tc.body + "\n"
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestFakeGoHelper$", "-test.count=1")
			cmd.Env = []string{"PATH=" + bin, "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "FAKE_GO_CHECK=1"}
			out, err := cmd.CombinedOutput()
			if tc.ok && err != nil {
				t.Fatalf("child: %v\n%s", err, out)
			}
			if !tc.ok && (err == nil || !strings.Contains(string(out), "testenv: cannot resolve Go locations")) {
				t.Fatalf("child err %v, output:\n%s", err, out)
			}
			if calls, _ := os.ReadFile(log); string(calls) != "env GOPATH GOMODCACHE GOCACHE\n" {
				t.Errorf("go invocations: %q", calls)
			}
		})
	}
}

func TestFakeGoHelper(t *testing.T) {
	if os.Getenv("FAKE_GO_CHECK") == "" {
		t.Skip("subprocess helper")
	}
	if got := []string{os.Getenv("GOPATH"), os.Getenv("GOMODCACHE"), os.Getenv("GOCACHE")}; !reflect.DeepEqual(got, []string{"/g/path", "/g/mod", "off"}) {
		t.Errorf("pinned %q", got)
	}
}
