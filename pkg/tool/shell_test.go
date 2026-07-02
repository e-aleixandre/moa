//go:build !windows

package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunShell_Success(t *testing.T) {
	r := RunShell(context.Background(), ShellConfig{Command: "echo hello"})
	if r.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", r.ExitCode)
	}
	if strings.TrimSpace(r.Stdout) != "hello" {
		t.Fatalf("expected 'hello', got %q", r.Stdout)
	}
	if r.TimedOut {
		t.Fatal("unexpected timeout")
	}
}

func TestRunShell_Failure(t *testing.T) {
	r := RunShell(context.Background(), ShellConfig{Command: "exit 42"})
	if r.ExitCode != 42 {
		t.Fatalf("expected exit 42, got %d", r.ExitCode)
	}
	if r.TimedOut {
		t.Fatal("unexpected timeout")
	}
}

func TestRunShell_Stderr(t *testing.T) {
	r := RunShell(context.Background(), ShellConfig{Command: "echo err >&2"})
	if r.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", r.ExitCode)
	}
	if !strings.Contains(r.Stderr, "err") {
		t.Fatalf("expected stderr to contain 'err', got %q", r.Stderr)
	}
}

func TestRunShell_Timeout(t *testing.T) {
	r := RunShell(context.Background(), ShellConfig{
		Command: "sleep 60",
		Timeout: 100 * time.Millisecond,
	})
	if !r.TimedOut {
		t.Fatal("expected timeout")
	}
	if r.ExitCode != -1 {
		t.Fatalf("expected exit -1 on timeout, got %d", r.ExitCode)
	}
}

func TestRunShell_NoOutputLostOnManySmallWrites(t *testing.T) {
	// A fast-exiting command that writes a lot in small chunks must not lose
	// its tail. A distinctive final marker must always survive. Loop to catch
	// scheduling races between the process exit and output capture.
	const lines = 20000
	for iter := 0; iter < 20; iter++ {
		r := RunShell(context.Background(), ShellConfig{
			Command:   fmt.Sprintf("for i in $(seq 1 %d); do echo line$i; done; echo FINAL_MARKER", lines),
			MaxOutput: 8 * 1024 * 1024, // large enough to keep everything
		})
		if r.ExitCode != 0 {
			t.Fatalf("iter %d: exit %d, stderr=%q", iter, r.ExitCode, r.Stderr)
		}
		if !strings.HasSuffix(strings.TrimRight(r.Stdout, "\n"), "FINAL_MARKER") {
			tail := r.Stdout
			if len(tail) > 120 {
				tail = tail[len(tail)-120:]
			}
			t.Fatalf("iter %d: final marker lost — output truncated. tail=%q", iter, tail)
		}
		if got := strings.Count(r.Stdout, "line"); got != lines {
			t.Fatalf("iter %d: expected %d 'line' entries, got %d", iter, lines, got)
		}
	}
}

func TestRunShell_NoOutputLostOnBurstAtExit(t *testing.T) {
	// Regression: a command that dumps a large buffered burst and exits fast
	// leaves unread bytes in the kernel pipe at the moment the process is
	// reaped. Capturing via cmd.Stdout (exec-owned copier) must drain it before
	// closing the pipe. A self-owned StdoutPipe read concurrently with Wait()
	// truncated this (often to zero bytes) because Wait closed the pipe first.
	// CPU contention starves the copier's scheduling to expose the race.
	dir := t.TempDir()
	blob := filepath.Join(dir, "blob")
	full := strings.Repeat("x", 60*1024) + "END_MARKER"
	if err := os.WriteFile(blob, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			x := 0
			for {
				select {
				case <-stop:
					return
				default:
					x++
					_ = x * x
				}
			}
		}()
	}
	defer close(stop)

	for iter := 0; iter < 200; iter++ {
		r := RunShell(context.Background(), ShellConfig{
			Command:   "cat " + blob,
			MaxOutput: 4 * 1024 * 1024,
		})
		if r.ExitCode != 0 {
			t.Fatalf("iter %d: exit %d stderr=%q", iter, r.ExitCode, r.Stderr)
		}
		if !strings.HasSuffix(r.Stdout, "END_MARKER") {
			t.Fatalf("iter %d: output truncated — got %d of %d bytes", iter, len(r.Stdout), len(full))
		}
	}
}

func TestKillProcGroup_KillsBackgroundChild(t *testing.T) {
	// killProcGroup must SIGKILL the whole group, not just the leader, so a
	// backgrounded child (that a plain kill or WaitDelay would leave running)
	// is reaped. The child writes its PID so we can confirm it dies.
	cmd := exec.CommandContext(context.Background(), "bash", "-c", "sleep 30 & echo $!; wait")
	setProcGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Read the child PID (first line of stdout) via the pipe — reading a
	// bytes.Buffer while exec's copier writes it would be a data race.
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading child PID: %v", err)
	}
	var childPID int
	if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &childPID); err != nil || childPID <= 0 {
		t.Fatalf("bad child PID %q: %v", line, err)
	}

	killProcGroup(cmd)
	_ = cmd.Wait()

	// The backgrounded child must be gone. signal 0 probes for existence.
	dead := false
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(childPID, 0); err != nil {
			dead = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !dead {
		_ = syscall.Kill(childPID, syscall.SIGKILL) // cleanup if the fix regressed
		t.Fatalf("background child %d survived killProcGroup", childPID)
	}
}

func TestRunShell_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	r := RunShell(ctx, ShellConfig{Command: "sleep 60"})
	// Either TimedOut (if cmd started then got cancelled) or ExitCode == -1
	// (if cmd.Start failed because ctx was already done).
	if r.ExitCode == 0 {
		t.Fatal("expected non-zero exit on cancelled context")
	}
}

func TestRunShell_OutputTruncation(t *testing.T) {
	// Generate more output than MaxOutput
	r := RunShell(context.Background(), ShellConfig{
		Command:   "yes hello | head -c 20000",
		MaxOutput: 1024,
	})
	if r.ExitCode != 0 {
		// yes | head can exit with SIGPIPE on some systems; just check output
		if r.Stdout == "" {
			t.Fatal("expected stdout output")
		}
	}
	// Head+tail buffer should truncate and include a notice
	if len(r.Stdout) > 2048 { // some overhead from truncation notice is OK
		t.Fatalf("expected truncated output, got %d bytes", len(r.Stdout))
	}
	if !strings.Contains(r.Stdout, "truncated") {
		t.Fatalf("expected truncation notice in output: %q", r.Stdout[:min(200, len(r.Stdout))])
	}
}

func TestRunShell_Dir(t *testing.T) {
	r := RunShell(context.Background(), ShellConfig{
		Command: "pwd",
		Dir:     "/tmp",
	})
	if r.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", r.ExitCode)
	}
	// On macOS /tmp is a symlink to /private/tmp
	out := strings.TrimSpace(r.Stdout)
	if out != "/tmp" && out != "/private/tmp" {
		t.Fatalf("expected /tmp or /private/tmp, got %q", out)
	}
}

func TestRunShell_Elapsed(t *testing.T) {
	r := RunShell(context.Background(), ShellConfig{Command: "echo fast"})
	if r.Elapsed <= 0 {
		t.Fatalf("expected positive elapsed, got %v", r.Elapsed)
	}
}
