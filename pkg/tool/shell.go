package tool

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

const defaultMaxOutput = 25 * 1024 // 25KB per stream

// ShellConfig configures a shell command execution.
type ShellConfig struct {
	Command   string
	Dir       string
	Timeout   time.Duration // 0 = no timeout (uses parent ctx deadline only)
	MaxOutput int           // per-stream max bytes (head+tail), default 25KB
}

// ShellResult holds the output of a shell command execution.
type ShellResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Elapsed  time.Duration
}

// RunShell executes a bash command with process group handling, WaitDelay,
// and head+tail output buffering. Returns a structured result rather than
// a core.Result so callers can format output however they need.
func RunShell(ctx context.Context, cfg ShellConfig) ShellResult {
	start := time.Now()

	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	maxOutput := cfg.MaxOutput
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutput
	}
	half := maxOutput / 2

	cmd := exec.CommandContext(ctx, "bash", "-c", cfg.Command)
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}
	setProcGroup(cmd)
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr headTailBuffer
	stdout.headMax = half
	stdout.tailMax = half
	stderr.headMax = half
	stderr.tailMax = half
	// No SpillDir — verify checks don't need disk spill.

	// Assign the buffers as io.Writers so os/exec owns the output copiers and
	// Wait() waits for them to drain before closing the pipes. Reading a
	// self-owned StdoutPipe concurrently with Wait() truncates output: with a
	// pipe exec has no copier to wait for, so Wait closes the read end the
	// instant the process is reaped, before our reader consumes the tail.
	// WaitDelay still bounds the wait if a grandchild keeps the pipes open.
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return ShellResult{Stderr: err.Error(), ExitCode: -1, Elapsed: time.Since(start)}
	}

	err := cmd.Wait()

	elapsed := time.Since(start)

	// Check context first — on timeout cmd.Wait may return an ExitError
	// (SIGTERM exit) masking the real cause.
	if ctx.Err() != nil {
		killProcGroup(cmd) // reap grandchildren that ignored SIGTERM
		stdout.Close()
		stderr.Close()
		return ShellResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: -1,
			TimedOut: true,
			Elapsed:  elapsed,
		}
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// ErrWaitDelay: the main process exited but a child kept the pipes
			// open past WaitDelay. Reap the group so no grandchild lingers.
			if errors.Is(err, exec.ErrWaitDelay) {
				killProcGroup(cmd)
			}
			stdout.Close()
			stderr.Close()
			return ShellResult{Stderr: err.Error(), ExitCode: -1, Elapsed: elapsed}
		}
	}

	stdout.Close()
	stderr.Close()

	return ShellResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Elapsed:  elapsed,
	}
}
