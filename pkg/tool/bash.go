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

			// Capture stdout and stderr, streaming via onUpdate
			var stdout, stderr bytes.Buffer
			var mu sync.Mutex

			stdoutPipe, _ := cmd.StdoutPipe()
			stderrPipe, _ := cmd.StderrPipe()

			if err := cmd.Start(); err != nil {
				return core.ErrorResult(fmt.Sprintf("failed to start: %v", err)), nil
			}

			// Stream output
			var wg sync.WaitGroup
			wg.Add(2)

			streamReader := func(r io.Reader, buf *bytes.Buffer, label string) {
				defer wg.Done()
				tmp := make([]byte, 4096)
				for {
					n, err := r.Read(tmp)
					if n > 0 {
						chunk := string(tmp[:n])
						mu.Lock()
						buf.WriteString(chunk)
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

			go streamReader(stdoutPipe, &stdout, "stdout")
			go streamReader(stderrPipe, &stderr, "stderr")

			wg.Wait()
			err := cmd.Wait()

			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else if ctx.Err() != nil {
					return core.ErrorResult("command timed out"), nil
				}
			}

			out := stdout.String()
			errOut := stderr.String()

			var result strings.Builder
			if out != "" {
				result.WriteString(truncateOutput(out, maxOutputBytes))
			}
			if errOut != "" {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString("STDERR:\n")
				result.WriteString(truncateOutput(errOut, maxOutputBytes))
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
