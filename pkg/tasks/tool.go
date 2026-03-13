package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewTool returns the tasks tool backed by the given store.
func NewTool(store *Store) core.Tool {
	return core.Tool{
		Name:        "tasks",
		Label:       "Tasks",
		Description: "Manage implementation tasks. Create, update, mark done, or list tasks for tracking progress.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["create", "update", "done", "list", "get"],
					"description": "Action to perform"
				},
				"id": {
					"type": "integer",
					"description": "Task ID (required for update/done/get)"
				},
				"title": {
					"type": "string",
					"description": "Task title (required for create, optional for update)"
				},
				"description": {
					"type": "string",
					"description": "Task description (optional)"
				},
				"depends_on": {
					"type": "array",
					"items": { "type": "integer" },
					"description": "IDs of tasks this depends on (optional for create/update)"
				}
			},
			"required": ["action"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			action, _ := params["action"].(string)
			switch action {
			case "create":
				return toolCreate(store, params)
			case "update":
				return toolUpdate(store, params)
			case "done":
				return toolDone(store, params)
			case "list":
				return toolList(store)
			case "get":
				return toolGet(store, params)
			default:
				return core.ErrorResult(fmt.Sprintf("unknown action: %s", action)), nil
			}
		},
	}
}

func toolCreate(s *Store, params map[string]any) (core.Result, error) {
	title, _ := params["title"].(string)
	if strings.TrimSpace(title) == "" {
		return core.ErrorResult("title is required for create"), nil
	}
	desc, _ := params["description"].(string)
	var deps []int
	if raw, ok := params["depends_on"].([]any); ok {
		for _, d := range raw {
			if id, ok := toInt(d); ok {
				deps = append(deps, id)
			}
		}
	}
	t := s.Create(title, desc, deps)
	return core.TextResult(fmt.Sprintf("Created task #%d: %s", t.ID, t.Title)), nil
}

func toolUpdate(s *Store, params map[string]any) (core.Result, error) {
	id, ok := toInt(params["id"])
	if !ok {
		return core.ErrorResult("id is required for update"), nil
	}
	var title, desc *string
	if v, ok := params["title"].(string); ok && v != "" {
		title = &v
	}
	if v, ok := params["description"].(string); ok {
		desc = &v
	}
	var deps *[]int
	if raw, ok := params["depends_on"].([]any); ok {
		d := make([]int, 0, len(raw))
		for _, v := range raw {
			if n, ok := toInt(v); ok {
				d = append(d, n)
			}
		}
		deps = &d
	}
	if !s.Update(id, title, desc, deps) {
		return core.ErrorResult(fmt.Sprintf("task #%d not found", id)), nil
	}
	t, _ := s.Get(id)
	return core.TextResult(fmt.Sprintf("Updated task #%d: %s", t.ID, t.Title)), nil
}

func toolDone(s *Store, params map[string]any) (core.Result, error) {
	id, ok := toInt(params["id"])
	if !ok {
		return core.ErrorResult("id is required for done"), nil
	}
	if !s.MarkDone(id) {
		return core.ErrorResult(fmt.Sprintf("task #%d not found", id)), nil
	}
	done, total := s.Progress()
	msg := fmt.Sprintf("Marked task #%d as done (%d/%d complete)", id, done, total)
	if done == total && total > 0 {
		msg += "\n\nAll tasks complete!"
	}
	return core.TextResult(msg), nil
}

func toolList(s *Store) (core.Result, error) {
	tasks := s.Tasks()
	if len(tasks) == 0 {
		return core.TextResult("No tasks yet."), nil
	}
	var sb strings.Builder
	done, total := s.Progress()
	sb.WriteString(fmt.Sprintf("Tasks (%d/%d done):\n", done, total))
	for _, t := range tasks {
		icon := "☐"
		if t.Status == "done" {
			icon = "☑"
		} else if t.Status == "in_progress" {
			icon = "▶"
		}
		sb.WriteString(fmt.Sprintf("\n%s #%d: %s", icon, t.ID, t.Title))
		if t.Description != "" {
			sb.WriteString(fmt.Sprintf("\n    %s", t.Description))
		}
	}
	return core.TextResult(sb.String()), nil
}

func toolGet(s *Store, params map[string]any) (core.Result, error) {
	id, ok := toInt(params["id"])
	if !ok {
		return core.ErrorResult("id is required for get"), nil
	}
	task, found := s.Get(id)
	if !found {
		return core.ErrorResult(fmt.Sprintf("task #%d not found", id)), nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Task #%d: %s\nStatus: %s", task.ID, task.Title, task.Status))
	if task.Description != "" {
		sb.WriteString(fmt.Sprintf("\nDescription: %s", task.Description))
	}
	if len(task.DependsOn) > 0 {
		sb.WriteString(fmt.Sprintf("\nDepends on: %v", task.DependsOn))
	}
	return core.TextResult(sb.String()), nil
}

// toInt converts a JSON number (float64 or json.Number) to int.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}
