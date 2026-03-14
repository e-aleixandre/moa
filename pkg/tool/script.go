package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"path/filepath"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// ScriptDef defines a user-provided script tool loaded from JSON.
type ScriptDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Timeout     int    `json:"timeout"` // seconds, 0 = 60s default
}

// LoadScriptTools discovers and loads tool definitions from .moa/tools/*.json.
// Returns nil (no error) if the directory doesn't exist.
func LoadScriptTools(cwd string) ([]ScriptDef, error) {
	dir := filepath.Join(cwd, ".moa", "tools")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("script tools: %w", err)
	}

	var defs []ScriptDef
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var d ScriptDef
		if err := json.Unmarshal(data, &d); err != nil {
			continue
		}
		if d.Name == "" || d.Command == "" {
			continue
		}
		if d.Description == "" {
			d.Description = "Run " + d.Command
		}
		defs = append(defs, d)
	}
	return defs, nil
}

// RegisterScriptTools registers script tools into the registry.
// Tools that collide with already-registered names are skipped with a warning
// to prevent untrusted repos from shadowing builtins.
func RegisterScriptTools(reg *core.Registry, cwd string) error {
	defs, err := LoadScriptTools(cwd)
	if err != nil {
		return err
	}
	for _, d := range defs {
		if _, exists := reg.Get(d.Name); exists {
			fmt.Fprintf(os.Stderr, "warning: script tool %q skipped (name already registered)\n", d.Name)
			continue
		}
		t := newScriptTool(d, cwd)
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("script tool %q: %w", d.Name, err)
		}
	}
	return nil
}

func newScriptTool(d ScriptDef, cwd string) core.Tool {
	timeout := time.Duration(d.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	return core.Tool{
		Name:        d.Name,
		Description: d.Description,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"args": {
					"type": "string",
					"description": "Arguments to pass to the command (optional)"
				}
			}
		}`),
		Effect: core.EffectShell,
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			args, _ := params["args"].(string)

			cmdCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// Pass args positionally via $1 to prevent shell injection.
			// The command can reference them as "$1", "$@", etc.
			var cmd *exec.Cmd
			if args != "" {
				cmd = exec.CommandContext(cmdCtx, "bash", "-c", d.Command+` "$@"`, "_", args)
			} else {
				cmd = exec.CommandContext(cmdCtx, "bash", "-c", d.Command)
			}
			cmd.Dir = cwd

			out, err := cmd.CombinedOutput()
			output := string(out)
			if len(output) > 50000 {
				output = output[:25000] + "\n\n... (truncated) ...\n\n" + output[len(output)-25000:]
			}

			if err != nil {
				return core.TextResult(fmt.Sprintf("exit error: %v\n\n%s", err, output)), nil
			}
			if output == "" {
				output = "(no output)"
			}
			return core.TextResult(output), nil
		},
	}
}
