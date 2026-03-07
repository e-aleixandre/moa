package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewEdit creates the edit tool.
func NewEdit(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "edit",
		Label:       "Edit",
		Description: "Edit a file by replacing exact text. The oldText must match exactly (including whitespace). Returns error if not found or multiple matches.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Path to the file to edit"
				},
				"oldText": {
					"type": "string",
					"description": "Exact text to find and replace"
				},
				"newText": {
					"type": "string",
					"description": "New text to replace the old text with"
				}
			},
			"required": ["path", "oldText", "newText"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			path := getString(params, "path", "")
			oldText := getString(params, "oldText", "")
			newText := getString(params, "newText", "")

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

			data, err := os.ReadFile(resolved)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("read: %v", err)), nil
			}

			content := string(data)
			count := strings.Count(content, oldText)

			if count == 0 {
				return core.ErrorResult(fmt.Sprintf("oldText not found in %s", path)), nil
			}
			if count > 1 {
				return core.ErrorResult(fmt.Sprintf("oldText matches %d locations in %s — be more specific", count, path)), nil
			}

			newContent := strings.Replace(content, oldText, newText, 1)
			if err := os.WriteFile(resolved, []byte(newContent), 0o644); err != nil {
				return core.ErrorResult(fmt.Sprintf("write: %v", err)), nil
			}

			// Emit diff for TUI display via onUpdate
			if onUpdate != nil {
				diff := unifiedDiff(content, newContent, 3)
				if diff != "" {
					onUpdate(core.TextResult(diff))
				}
			}

			return core.TextResult(fmt.Sprintf("Edited %s", path)), nil
		},
	}
}
