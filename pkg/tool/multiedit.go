package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewMultiEdit creates the multiedit tool for atomic batch edits to a single file.
func NewMultiEdit(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:  "multiedit",
		Label: "MultiEdit",
		Description: "Make multiple edits to a single file atomically. " +
			"Edits are applied sequentially; if any edit fails, none are written. " +
			"Prefer over edit when making several changes to the same file.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Path to the file to modify"
				},
				"edits": {
					"type": "array",
					"items": {
						"type": "object",
						"properties": {
							"oldText": {
								"type": "string",
								"description": "Text to find"
							},
							"newText": {
								"type": "string",
								"description": "Replacement text"
							},
							"replaceAll": {
								"type": "boolean",
								"description": "Replace all occurrences (default false)"
							}
						},
						"required": ["oldText", "newText"]
					},
					"description": "Array of edit operations applied sequentially"
				}
			},
			"required": ["path", "edits"]
		}`),
		Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string {
			p := getString(args, "path", "")
			if p == "" {
				return ""
			}
			resolved, err := safePath(cfg, p)
			if err != nil {
				return ""
			}
			return resolved
		},
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			path := getString(params, "path", "")
			if path == "" {
				return core.ErrorResult("path is required"), nil
			}

			editsRaw, ok := params["edits"]
			if !ok {
				return core.ErrorResult("edits is required"), nil
			}

			edits, err := parseEdits(editsRaw)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			if len(edits) == 0 {
				return core.ErrorResult("edits array is empty"), nil
			}

			resolved, err := safePath(cfg, path)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			// Stale edit protection
			if cfg.FileTracker != nil && !cfg.FileTracker.WasRead(resolved) {
				return core.ErrorResult(fmt.Sprintf(
					"You haven't read %s yet. Read the file first to see its current content before editing.",
					path,
				)), nil
			}

			data, err := os.ReadFile(resolved)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("read: %v", err)), nil
			}

			original := string(data)
			content := original

			// Apply edits sequentially — abort on first failure (atomic)
			for i, e := range edits {
				newContent, _, editErr := applyEdit(content, e.oldText, e.newText, e.replaceAll)
				if editErr != nil {
					return core.ErrorResult(fmt.Sprintf("edit #%d: %v in %s", i+1, editErr, path)), nil
				}
				content = newContent
			}

			// All edits succeeded — write once
			if cfg.BeforeWrite != nil {
				if err := cfg.BeforeWrite(resolved); err != nil {
					return core.ErrorResult(fmt.Sprintf("checkpoint: %v", err)), nil
				}
			}

			if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
				return core.ErrorResult(fmt.Sprintf("write: %v", err)), nil
			}

			diff := unifiedDiff(original, content, 3)

			if onUpdate != nil && diff != "" {
				onUpdate(core.TextResult(diff))
			}

			label := fmt.Sprintf("Applied %d edits to %s", len(edits), path)
			if diff != "" {
				return core.TextResult(fmt.Sprintf("%s\n\n%s", label, diff)), nil
			}
			return core.TextResult(label), nil
		},
	}
}

// editOp represents a single edit operation within a multiedit.
type editOp struct {
	oldText    string
	newText    string
	replaceAll bool
}

// parseEdits extracts the edits array from the raw parameter value.
func parseEdits(raw any) ([]editOp, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("edits must be an array")
	}

	edits := make([]editOp, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edit #%d: must be an object", i+1)
		}
		old, _ := m["oldText"].(string)
		new, _ := m["newText"].(string)
		if old == "" {
			return nil, fmt.Errorf("edit #%d: oldText is required", i+1)
		}
		ra, _ := m["replaceAll"].(bool)
		edits = append(edits, editOp{oldText: old, newText: new, replaceAll: ra})
	}
	return edits, nil
}
