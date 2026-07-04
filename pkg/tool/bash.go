package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	description := "Execute a bash command. Returns stdout, stderr, and exit code. Output is truncated to 50KB."
	if cfg.BashState != nil {
		description = "Execute a bash command. Working directory and exported environment variables persist across calls (cd, export, venv activation carry over). Returns stdout, stderr, and exit code. Output is truncated to 50KB."
	}
	return core.Tool{
		Name:        "bash",
		Label:       "Bash",
		Description: description,
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

			// Persisted shell state (nil = stateless, byte-identical to before).
			// The default cwd becomes the persisted cwd (validated) when present;
			// an explicit cwd param still overrides it. persistedEnv, when set,
			// is applied to cmd.Env below.
			var agentID string
			var persistedEnv []string
			defaultCwd := cfg.WorkspaceRoot
			if cfg.BashState != nil {
				agentID = core.AgentIDFromContext(ctx)
				var persistedCwd string
				persistedCwd, persistedEnv = cfg.BashState.Snapshot(agentID)
				if persistedCwd != "" {
					if fi, err := os.Stat(persistedCwd); err == nil && fi.IsDir() {
						if cfg.WorkspaceRoot == "" {
							defaultCwd = persistedCwd
						} else if validated, err := safePath(cfg, persistedCwd); err == nil {
							defaultCwd = validated
						}
					}
				}
			}

			cwd := getString(params, "cwd", defaultCwd)
			if cwd == "" {
				cwd = defaultCwd
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

			// When persisting state, prepend an EXIT trap that dumps the final
			// cwd and exported env to temp files, then re-apply them on the next
			// call via cmd.Dir/Env. The trap runs on `exit N` and normal
			// completion but NOT on death by signal (timeout/kill), so a killed
			// command never corrupts the snapshot. Paths come from MkdirTemp and
			// are %q-quoted, and the model's command sits on its own line — no
			// injection surface. `builtin pwd` / `command env` can't be shadowed
			// by a same-name function the command defines (which would corrupt
			// the capture). Startup/control vars (BASH_ENV, BASH_FUNC_*) are
			// stripped from the persisted env in parseNullSepEnv so a persisted
			// export can't run code on the next call before the trap installs.
			runCommand := command
			var cwdFile, envFile string
			if cfg.BashState != nil {
				if stateDir, err := os.MkdirTemp("", "moa-bash-state-"); err == nil {
					defer func() { _ = os.RemoveAll(stateDir) }()
					cwdFile = filepath.Join(stateDir, "cwd")
					envFile = filepath.Join(stateDir, "env")
					runCommand = fmt.Sprintf("trap '{ builtin pwd > %q; command env -0 > %q; } 2>/dev/null' EXIT\n%s",
						cwdFile, envFile, command)
				}
				// MkdirTemp failure => run without persistence this call (best effort).
			}

			cmd := exec.CommandContext(ctx, "bash", "-c", runCommand)
			cmd.Dir = cwd
			if persistedEnv != nil {
				cmd.Env = persistedEnv
			}
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

			// Capture the new cwd+env. Only reachable when the command did NOT
			// time out and Wait returned success or an ExitError (the trap ran),
			// so `exit N` still updates state. cwd+env update atomically.
			if cfg.BashState != nil && cwdFile != "" {
				captureShellState(cfg.BashState, agentID, cwdFile, envFile)
			}

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

// captureShellState reads the cwd/env dumped by the EXIT trap and updates the
// agent's snapshot. It is a no-op unless both a non-empty cwd and a non-empty,
// within-cap env were captured — cwd and env are always persisted together, so
// a missing or truncated file leaves the prior snapshot intact.
func captureShellState(st *BashState, agentID, cwdFile, envFile string) {
	cwdRaw, err := os.ReadFile(cwdFile)
	if err != nil {
		return
	}
	// Strip only the single terminator `pwd` writes, not every trailing
	// newline — a directory whose name legitimately ends in "\n" must survive.
	newCwd := strings.TrimSuffix(string(cwdRaw), "\n")
	if newCwd == "" {
		return
	}
	envRaw, err := os.ReadFile(envFile)
	if err != nil || len(envRaw) == 0 || len(envRaw) > maxEnvCapture {
		return
	}
	st.Update(agentID, newCwd, parseNullSepEnv(envRaw))
}

func secondsToDuration(s int) time.Duration {
	return time.Duration(s) * time.Second
}


