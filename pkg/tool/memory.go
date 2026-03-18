package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/memory"
)

// NewMemory creates the memory tool for reading/updating cross-session project memory.
func NewMemory(cfg ToolConfig) core.Tool {
	store := cfg.MemoryStore
	root := cfg.WorkspaceRoot
	lockPath := store.FilePath(root)

	return core.Tool{
		Name:  "memory",
		Label: "Memory",
		Description: "Read or update persistent project memory. " +
			"Use to save important learnings, corrections, and project-specific preferences that should persist across sessions. " +
			"Memory is stored per-project and automatically loaded into context at the start of each session.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["read", "update"],
					"description": "read: return current memory content. update: replace memory with new content."
				},
				"content": {
					"type": "string",
					"description": "New memory content (only for update action). Markdown format. Keep concise and actionable."
				}
			},
			"required": ["action"]
		}`),
		Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string {
			return lockPath
		},
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			action := getString(params, "action", "")

			switch action {
			case "read":
				content, err := store.Load(root)
				if err != nil {
					return core.ErrorResult(fmt.Sprintf("failed to read memory: %v", err)), nil
				}
				if content == "" {
					return core.TextResult("No memory saved for this project yet."), nil
				}
				return core.TextResult(content), nil

			case "update":
				content := getString(params, "content", "")
				if content == "" {
					return core.ErrorResult("content is required for update action"), nil
				}
				if len(content) > memory.MaxSize {
					return core.ErrorResult(fmt.Sprintf("content exceeds %dKB limit (%d bytes)", memory.MaxSize/1024, len(content))), nil
				}

				if err := store.Save(root, content); err != nil {
					return core.ErrorResult(fmt.Sprintf("failed to save memory: %v", err)), nil
				}

				lineCount := strings.Count(content, "\n") + 1
				return core.TextResult(fmt.Sprintf("Memory updated (%d lines). Will be loaded automatically in future sessions.", lineCount)), nil

			default:
				return core.ErrorResult(fmt.Sprintf("unknown action %q — use \"read\" or \"update\"", action)), nil
			}
		},
	}
}
