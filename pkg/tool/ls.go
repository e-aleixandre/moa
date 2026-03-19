package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// listDirectory lists the contents of a resolved directory path.
// If prefix is non-empty it is prepended to the output (used by read to add a hint).
func listDirectory(resolved, workspaceRoot, prefix string) (core.Result, error) {
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot read %s: %v", resolved, err)), nil
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}

	sort.Strings(names)

	maxEntries := 500
	truncated := false
	if len(names) > maxEntries {
		names = names[:maxEntries]
		truncated = true
	}

	result := strings.Join(names, "\n")

	// Show relative path context
	relPath, _ := filepath.Rel(workspaceRoot, resolved)
	if relPath == "" || relPath == "." {
		relPath = resolved
	}

	header := fmt.Sprintf("Directory: %s (%d entries)\n\n", relPath, len(entries))
	result = prefix + header + result

	if truncated {
		result += fmt.Sprintf("\n\n[truncated — showing %d of %d entries]", maxEntries, len(entries))
	}

	return core.TextResult(result), nil
}

// NewLs creates the ls tool.
func NewLs(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "ls",
		Label:       "List",
		Description: "List directory contents. Sorted alphabetically. Directories have '/' suffix. Includes dotfiles. Truncated to 500 entries.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Directory to list (default: workspace root)"
				}
			}
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			path := getString(params, "path", ".")
			resolved, err := safePath(cfg, path)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			info, err := os.Stat(resolved)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("cannot access %s: %v", path, err)), nil
			}
			if !info.IsDir() {
				return core.ErrorResult(fmt.Sprintf("%s is not a directory", path)), nil
			}

			return listDirectory(resolved, cfg.WorkspaceRoot, "")
		},
	}
}
