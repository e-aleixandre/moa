package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// streamWriter feeds command output into a headTailBuffer and streams the bytes
// accepted into the visible head via onUpdate. Used as cmd.Stdout/Stderr so
// os/exec owns the copy goroutine and Wait() drains it (bounded by WaitDelay),
// rather than racing a self-owned pipe read against Wait closing the pipe.
type streamWriter struct {
	buf      *headTailBuffer
	onUpdate func(core.Result)
}

func (w *streamWriter) Write(p []byte) (int, error) {
	accepted := w.buf.Append(p)
	if w.onUpdate != nil && accepted > 0 {
		w.onUpdate(core.TextResult(string(p[:accepted])))
	}
	return len(p), nil
}

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
		Effect: core.EffectShell,
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
				validCwd, err := safePath(cfg, cwd)
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
			setProcGroup(cmd)
			// If the process doesn't exit within 5s of cancel signal, Go force-kills.
			cmd.WaitDelay = 5 * time.Second

			// Capture stdout and stderr, streaming via onUpdate.
			// Buffers keep head + tail to preserve both the start and end of output.
			var stdout, stderr headTailBuffer
			stdout.headMax = maxOutputBytes / 2
			stdout.tailMax = maxOutputBytes / 2
			stdout.SpillDir = spillOutputDir
			stderr.headMax = maxOutputBytes / 2
			stderr.tailMax = maxOutputBytes / 2
			stderr.SpillDir = spillOutputDir

			// Assign io.Writers so os/exec owns the output copiers and Wait()
			// drains them before closing the pipes. A self-owned StdoutPipe read
			// concurrently with Wait() truncates output — exec has no copier to
			// wait for, so Wait closes the read end the instant the process is
			// reaped, before our reader consumes the tail. WaitDelay still bounds
			// the wait if a grandchild keeps the pipes open past cancel.
			// streamWriter streams newly captured bytes live via onUpdate.
			cmd.Stdout = &streamWriter{buf: &stdout, onUpdate: onUpdate}
			cmd.Stderr = &streamWriter{buf: &stderr, onUpdate: onUpdate}

			if err := cmd.Start(); err != nil {
				return core.ErrorResult(fmt.Sprintf("failed to start: %v", err)), nil
			}

			err := cmd.Wait()

			// Check context FIRST — on timeout, cmd.Wait() may return
			// an ExitError (SIGTERM exit), masking the real cause.
			if ctx.Err() != nil {
				killProcGroup(cmd) // reap grandchildren that ignored SIGTERM
				stdout.Close()
				stderr.Close()
				return core.ErrorResult("command timed out"), nil
			}

			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					// ErrWaitDelay: the main process exited but a child kept the
					// pipes open past WaitDelay. Reap the group so no grandchild
					// lingers.
					if errors.Is(err, exec.ErrWaitDelay) {
						killProcGroup(cmd)
					}
					stdout.Close()
					stderr.Close()
					return core.ErrorResult(fmt.Sprintf("exec: %v", err)), nil
				}
			}

			stdout.Close()
			stderr.Close()

			out := stdout.String()
			errOut := stderr.String()

			var result strings.Builder
			if out != "" {
				result.WriteString(out)
			}
			if errOut != "" {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString("STDERR:\n")
				result.WriteString(errOut)
			}
			if exitCode != 0 {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				fmt.Fprintf(&result, "Exit code: %d", exitCode)
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


