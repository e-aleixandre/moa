package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
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
			// Buffers keep head + tail to preserve both the start and end of output.
			var stdout, stderr headTailBuffer
			stdout.headMax = maxOutputBytes / 2
			stdout.tailMax = maxOutputBytes / 2
			stdout.SpillDir = spillOutputDir
			stderr.headMax = maxOutputBytes / 2
			stderr.tailMax = maxOutputBytes / 2
			stderr.SpillDir = spillOutputDir

			stdoutPipe, err := cmd.StdoutPipe()
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("stdout pipe: %v", err)), nil
			}
			stderrPipe, err := cmd.StderrPipe()
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("stderr pipe: %v", err)), nil
			}

			if err := cmd.Start(); err != nil {
				return core.ErrorResult(fmt.Sprintf("failed to start: %v", err)), nil
			}

			// Stream output
			var wg sync.WaitGroup
			wg.Add(2)

			streamReader := func(r io.Reader, buf *headTailBuffer, live bool) {
				defer wg.Done()
				tmp := make([]byte, 4096)
				for {
					n, err := r.Read(tmp)
					if n > 0 {
						accepted, _ := buf.Write(tmp[:n])
						if live && onUpdate != nil && accepted > 0 {
							onUpdate(core.TextResult(string(tmp[:accepted])))
						}
					}
					if err != nil {
						break
					}
				}
			}

			go streamReader(stdoutPipe, &stdout, true)
			go streamReader(stderrPipe, &stderr, true)

			wg.Wait()
			err = cmd.Wait()

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


