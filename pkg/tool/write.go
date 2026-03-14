package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewWrite creates the write tool.
func NewWrite(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "write",
		Label:       "Write",
		Description: "Create or overwrite a file. Automatically creates parent directories.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Path to the file to write"
				},
				"content": {
					"type": "string",
					"description": "Content to write to the file"
				}
			},
			"required": ["path", "content"]
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
			content := getString(params, "content", "")
			if path == "" {
				return core.ErrorResult("path is required"), nil
			}

			resolved, err := safePath(cfg, path)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			// Create parent directories
			dir := filepath.Dir(resolved)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return core.ErrorResult(fmt.Sprintf("mkdir: %v", err)), nil
			}

			if cfg.BeforeWrite != nil {
				if err := cfg.BeforeWrite(resolved); err != nil {
					return core.ErrorResult(fmt.Sprintf("checkpoint: %v", err)), nil
				}
			}

			if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
				return core.ErrorResult(fmt.Sprintf("write: %v", err)), nil
			}

			return core.TextResult(fmt.Sprintf("Wrote %d bytes to %s", len(content), path)), nil
		},
	}
}
