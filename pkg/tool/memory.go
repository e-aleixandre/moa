package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/memory"
)

// NewMemory creates the memory tool for managing cross-session memory as typed,
// single-fact files (list/read/write/delete).
func NewMemory(cfg ToolConfig) core.Tool {
	store := cfg.MemoryStore
	lockKey := "memory:" + store.ProjectDir()

	return core.Tool{
		Name:  "memory",
		Label: "Memory",
		Description: "Manage persistent memory as small, single-fact notes. Only the index (one line per " +
			"fact) is in your context; read a fact's full text on demand. Each fact has a type that decides " +
			"its scope: user/feedback are global (all projects); project/reference are scoped to this project. " +
			"Save durable, non-obvious facts (user preferences, corrections, project constraints); update the " +
			"existing fact instead of duplicating, and delete facts that become wrong. Refer to a fact by its " +
			"canonical id from the index (e.g. \"project/uses-docker\" or \"global/prefers-tabs\").",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["list", "read", "write", "delete"],
					"description": "list: show the index of all facts. read: return one fact's full text. write: create/overwrite one fact. delete: remove one fact."
				},
				"id": {
					"type": "string",
					"description": "Canonical id for read/delete: \"project/<name>\", \"global/<name>\", or a bare \"<name>\" if unambiguous."
				},
				"name": {
					"type": "string",
					"description": "Fact name for write: kebab-case ascii (e.g. \"uses-docker\"). The file is named after it."
				},
				"description": {
					"type": "string",
					"description": "One-line hook shown in the index (for write). Required."
				},
				"type": {
					"type": "string",
					"enum": ["user", "feedback", "project", "reference"],
					"description": "Fact type (for write). Decides scope: user/feedback → global, project/reference → this project."
				},
				"content": {
					"type": "string",
					"description": "The full fact body in markdown (for write)."
				}
			},
			"required": ["action"]
		}`),
		Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string {
			return lockKey
		},
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			switch getString(params, "action", "") {
			case "list":
				mems := store.List()
				if len(mems) == 0 {
					return core.TextResult("No memories saved yet."), nil
				}
				var sb strings.Builder
				for _, m := range mems {
					sb.WriteString("- ")
					sb.WriteString(m.ID())
					sb.WriteString(" (")
					sb.WriteString(string(m.Type))
					sb.WriteString(") — ")
					sb.WriteString(m.Description)
					sb.WriteString("\n")
				}
				return core.TextResult(sb.String()), nil

			case "read":
				id := getString(params, "id", "")
				if id == "" {
					return core.ErrorResult("id is required for read (e.g. \"project/uses-docker\")"), nil
				}
				m, ok, err := store.Read(id)
				if err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				if !ok {
					return core.ErrorResult(fmt.Sprintf("memory %q not found", id)), nil
				}
				return core.TextResult(m.Body), nil

			case "write":
				m := memory.Memory{
					Name:        getString(params, "name", ""),
					Description: getString(params, "description", ""),
					Type:        memory.Type(getString(params, "type", "")),
					Body:        getString(params, "content", ""),
				}
				if err := store.Write(m); err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				m.Scope = memory.ScopeForType(m.Type)
				return core.TextResult(fmt.Sprintf("Saved memory %q. Read it later with: read %s", m.Name, m.ID())), nil

			case "delete":
				id := getString(params, "id", "")
				if id == "" {
					return core.ErrorResult("id is required for delete (e.g. \"project/uses-docker\")"), nil
				}
				if err := store.Delete(id); err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				return core.TextResult(fmt.Sprintf("Deleted memory %q.", id)), nil

			default:
				return core.ErrorResult(fmt.Sprintf("unknown action %q — use \"list\", \"read\", \"write\" or \"delete\"", getString(params, "action", ""))), nil
			}
		},
	}
}
