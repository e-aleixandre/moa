package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewEdit creates the edit tool.
func NewEdit(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "edit",
		Label:       "Edit",
		Description: "Edit a file by replacing text. Supports fuzzy matching for whitespace/indentation differences. Returns error if not found or ambiguous.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Path to the file to edit"
				},
				"oldText": {
					"type": "string",
					"description": "Text to find and replace"
				},
				"newText": {
					"type": "string",
					"description": "New text to replace the old text with"
				},
				"replaceAll": {
					"type": "boolean",
					"description": "Replace all occurrences of oldText (default false)"
				}
			},
			"required": ["path", "oldText", "newText"]
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
			oldText := getString(params, "oldText", "")
			newText := getString(params, "newText", "")
			replaceAll := getBool(params, "replaceAll", false)

			if path == "" {
				return core.ErrorResult("path is required"), nil
			}
			if oldText == "" {
				return core.ErrorResult("oldText is required"), nil
			}

			resolved, err := safePath(cfg, path)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			// Stale edit protection: warn if the agent hasn't read this file.
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

			content := string(data)
			newContent, matchMsg, editErr := applyEdit(content, oldText, newText, replaceAll)
			if editErr != nil {
				return core.ErrorResult(fmt.Sprintf("%v in %s", editErr, path)), nil
			}

			if cfg.BeforeWrite != nil {
				if err := cfg.BeforeWrite(resolved); err != nil {
					return core.ErrorResult(fmt.Sprintf("checkpoint: %v", err)), nil
				}
			}

			if err := os.WriteFile(resolved, []byte(newContent), 0o644); err != nil {
				return core.ErrorResult(fmt.Sprintf("write: %v", err)), nil
			}

			diff := unifiedDiff(content, newContent, 3)

			// Emit diff for TUI streaming display.
			if onUpdate != nil && diff != "" {
				onUpdate(core.TextResult(diff))
			}

			// Include diff and match info in result.
			label := fmt.Sprintf("Edited %s (%s)", path, matchMsg)
			if diff != "" {
				return core.TextResult(fmt.Sprintf("%s\n\n%s", label, diff)), nil
			}
			return core.TextResult(label), nil
		},
	}
}
