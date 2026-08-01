package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// NewTool creates the verify tool. sessionCWD is the workspace root used to
// locate .moa/verify.json when a call does not name a directory; allowed
// bounds which other directories a call may target (nil means no bound).
func NewTool(sessionCWD string, allowed PathChecker) core.Tool {
	return core.Tool{
		Name:  "verify",
		Label: "Verify",
		Description: "Run project verification checks (build, test, lint) defined in .moa/verify.json. " +
			"Call this after completing coding tasks to validate your work. " +
			"Pass cwd to verify a different repository or worktree — its own .moa/verify.json is used.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"checks": {
					"type": "array",
					"items": { "type": "string" },
					"description": "Run only these named checks (default: all)"
				},
				"cwd": {
					"type": "string",
					"description": "Directory whose .moa/verify.json to run (default: the session's working directory). Use it when the code you changed lives in another repository or worktree; relative paths resolve against the session directory."
				}
			}
		}`),
		Effect: core.EffectShell,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			dir, err := ResolveWorkDir(sessionCWD, stringParam(params, "cwd"), allowed)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			cfg, err := LoadConfig(dir)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("verify config error: %v", err)), nil
			}
			if cfg == nil {
				return core.ErrorResult(fmt.Sprintf(
					"no .moa/verify.json in %s — create one to define verification checks, "+
						"or pass cwd to verify a directory that has one", dir)), nil
			}

			// Filter checks if the "checks" param is set.
			if raw, ok := params["checks"]; ok {
				if arr, ok := raw.([]any); ok && len(arr) > 0 {
					filtered, filterErr := filterChecks(cfg.Checks, arr)
					if filterErr != nil {
						return core.ErrorResult(filterErr.Error()), nil
					}
					cfg.Checks = filtered
				}
			}

			// Preamble: list what will be executed.
			var preamble strings.Builder
			fmt.Fprintf(&preamble, "Running %d checks", len(cfg.Checks))
			// Naming the directory only when it differs from the session's keeps
			// the common case terse and multi-repo runs unambiguous. Compare
			// resolved paths: dir comes back canonical, so a session CWD holding
			// a symlink or ".." would otherwise look like a different directory.
			if sessionReal, err := filepath.EvalSymlinks(sessionCWD); err != nil || dir != sessionReal {
				fmt.Fprintf(&preamble, " in %s", dir)
			}
			preamble.WriteString(":\n")
			for _, ch := range cfg.Checks {
				fmt.Fprintf(&preamble, "  %s: %s\n", ch.Name, ch.Command)
			}
			preamble.WriteString("\n")

			result := Run(ctx, dir, *cfg)
			formatted := FormatResult(result)
			output := preamble.String() + formatted

			r := core.TextResult(output)
			if !result.AllPass {
				r.IsError = true
			}
			return r, nil
		},
	}
}

// stringParam reads an optional string argument, tolerating the absent or null
// values models emit for optional fields.
func stringParam(params map[string]any, name string) string {
	if v, ok := params[name].(string); ok {
		return v
	}
	return ""
}

// filterChecks keeps only checks whose names appear in the requested list.
// Returns an error listing valid names if any requested name is unknown.
func filterChecks(checks []Check, requested []any) ([]Check, error) {
	available := make(map[string]Check, len(checks))
	for _, ch := range checks {
		available[ch.Name] = ch
	}

	var filtered []Check
	var unknown []string
	for _, v := range requested {
		name, ok := v.(string)
		if !ok {
			continue
		}
		ch, found := available[name]
		if !found {
			unknown = append(unknown, name)
			continue
		}
		filtered = append(filtered, ch)
	}

	if len(unknown) > 0 {
		var names []string
		for _, ch := range checks {
			names = append(names, ch.Name)
		}
		return nil, fmt.Errorf("unknown checks: %s (available: %s)",
			strings.Join(unknown, ", "), strings.Join(names, ", "))
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no matching checks found")
	}
	return filtered, nil
}
