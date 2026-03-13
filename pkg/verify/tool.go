package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewTool creates the verify tool. cwd is the workspace root used to
// locate .moa/verify.json.
func NewTool(cwd string) core.Tool {
	return core.Tool{
		Name:  "verify",
		Label: "Verify",
		Description: "Run project verification checks (build, test, lint) defined in .moa/verify.json. " +
			"Call this after completing coding tasks to validate your work.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"checks": {
					"type": "array",
					"items": { "type": "string" },
					"description": "Run only these named checks (default: all)"
				}
			}
		}`),
		Effect: core.EffectShell,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			cfg, err := LoadConfig(cwd)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("verify config error: %v", err)), nil
			}
			if cfg == nil {
				return core.ErrorResult("no .moa/verify.json found — create one to define verification checks"), nil
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
			fmt.Fprintf(&preamble, "Running %d checks:\n", len(cfg.Checks))
			for _, ch := range cfg.Checks {
				fmt.Fprintf(&preamble, "  %s: %s\n", ch.Name, ch.Command)
			}
			preamble.WriteString("\n")

			result := Run(ctx, cwd, *cfg)
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
