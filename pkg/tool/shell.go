package tool

import (
	"context"
	"io"
	"os/exec"
	"sync"
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

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return ShellResult{Stderr: err.Error(), ExitCode: -1, Elapsed: time.Since(start)}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return ShellResult{Stderr: err.Error(), ExitCode: -1, Elapsed: time.Since(start)}
	}

	if err := cmd.Start(); err != nil {
		return ShellResult{Stderr: err.Error(), ExitCode: -1, Elapsed: time.Since(start)}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	drain := func(r io.Reader, buf *headTailBuffer) {
		defer wg.Done()
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				_, _ = buf.Write(tmp[:n])
			}
			if err != nil {
				return
			}
		}
	}
	go drain(stdoutPipe, &stdout)
	go drain(stderrPipe, &stderr)

	wg.Wait()
	err = cmd.Wait()

	elapsed := time.Since(start)

	// Check context first — on timeout cmd.Wait may return an ExitError
	// (SIGTERM exit) masking the real cause.
	if ctx.Err() != nil {
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
