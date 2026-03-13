//go:build !windows

package tool

import (
	"context"
	"strings"
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
