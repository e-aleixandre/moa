package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ealeixandre/go-agent/pkg/core"
)

// NewBash creates the bash tool.
func NewBash(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "bash",
		Label:       "Bash",
		Description: "Execute a bash command. Returns stdout, stderr, and exit code. Output is truncated to 50KB.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {
					"type": "string",
					"description": "The bash command to execute"
				},
				"cwd": {
					"type": "string",
					"description": "Working directory (default: workspace root)"
				},
				"timeout": {
					"type": "integer",
					"description": "Timeout in seconds (default: 300)"
				}
			},
			"required": ["command"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			command := getString(params, "command", "")
			if command == "" {
				return core.ErrorResult("command is required"), nil
			}

			cwd := getString(params, "cwd", cfg.WorkspaceRoot)
			if cwd == "" {
				cwd = cfg.WorkspaceRoot
			}
			if cfg.WorkspaceRoot != "" {
				validCwd, err := safePath(cfg.WorkspaceRoot, cwd)
				if err != nil {
					return core.ErrorResult(fmt.Sprintf("invalid cwd: %v", err)), nil
				}
				cwd = validCwd
			}

			timeout := cfg.BashTimeout
			if timeout == 0 {
				timeout = 5 * time.Minute // fallback default
			}
			if t := getInt(params, "timeout", 0); t > 0 {
				timeout = secondsToDuration(t)
			}

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(ctx, "bash", "-c", command)
			cmd.Dir = cwd
			// Run in its own process group so we can kill the entire tree.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			// On cancel, send SIGTERM to the process group (not just the shell).
			// This gives children a chance to clean up before being killed.
			cmd.Cancel = func() error {
				if cmd.Process == nil {
					return nil
				}
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			}
			// If the process doesn't exit within 5s of SIGTERM, Go sends SIGKILL.
			cmd.WaitDelay = 5 * time.Second

			// Capture stdout and stderr, streaming via onUpdate.
			// Buffers are capped at maxOutputBytes to prevent OOM on huge output.
			var stdout, stderr cappedBuffer
			stdout.max = maxOutputBytes
			stderr.max = maxOutputBytes
			var mu sync.Mutex

			stdoutPipe, _ := cmd.StdoutPipe()
			stderrPipe, _ := cmd.StderrPipe()

			if err := cmd.Start(); err != nil {
				return core.ErrorResult(fmt.Sprintf("failed to start: %v", err)), nil
			}

			// Stream output
			var wg sync.WaitGroup
			wg.Add(2)

			streamReader := func(r io.Reader, buf *cappedBuffer) {
				defer wg.Done()
				tmp := make([]byte, 4096)
				for {
					n, err := r.Read(tmp)
					if n > 0 {
						chunk := string(tmp[:n])
						mu.Lock()
						buf.Write(tmp[:n])
						mu.Unlock()
						if onUpdate != nil {
							onUpdate(core.TextResult(chunk))
						}
					}
					if err != nil {
						break
					}
				}
			}

			go streamReader(stdoutPipe, &stdout)
			go streamReader(stderrPipe, &stderr)

			wg.Wait()
			err := cmd.Wait()

			// Check context FIRST — on timeout, cmd.Wait() may return
			// an ExitError (SIGTERM exit), masking the real cause.
			if ctx.Err() != nil {
				return core.ErrorResult("command timed out"), nil
			}

			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					return core.ErrorResult(fmt.Sprintf("exec: %v", err)), nil
				}
			}

			out := stdout.String()
			errOut := stderr.String()

			var result strings.Builder
			if out != "" {
				result.WriteString(out)
				if stdout.truncated {
					result.WriteString("\n\n[output truncated]")
				}
			}
			if errOut != "" {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString("STDERR:\n")
				result.WriteString(errOut)
				if stderr.truncated {
					result.WriteString("\n\n[output truncated]")
				}
			}
			if exitCode != 0 {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString(fmt.Sprintf("Exit code: %d", exitCode))
			}

			if result.Len() == 0 {
				result.WriteString("(no output)")
			}

			return core.TextResult(result.String()), nil
		},
	}
}

func secondsToDuration(s int) time.Duration {
	return time.Duration(s) * time.Second
}

// cappedBuffer accumulates up to max bytes. Once full, further writes are
// silently discarded. This prevents OOM when commands produce huge output
// (e.g., cat /dev/urandom). The truncated flag tells the caller to append a notice.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.truncated {
		return len(p), nil // accept but discard
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil // report full write to caller
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}
