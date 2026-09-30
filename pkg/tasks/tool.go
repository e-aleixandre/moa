package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// NewTool returns the tasks tool for one session. Who is asking is the
// scope's identity, injected at bootstrap; the arguments cannot name another
// session or project.
func NewTool(scope *Scope) core.Tool {
	return core.Tool{
		Name:  "tasks",
		Label: "Tasks",
		Description: "Your task checklist, your requests to the owner, and your project's shared backlog. " +
			"create adds to your checklist; ask files a request to the owner without blocking you (ask_user does block); " +
			"claim takes a pending backlog task of your project; list before creating to avoid duplicates. " +
			"create with when schedules the task to run later or repeat, in this session only: when is plain text such as " +
			"\"in 20m\", \"tomorrow at 09:00\", \"monday at 09:00\", \"2026-10-05 15:30\" or \"every weekday at 08:30\" " +
			"(use 09:00-style hours: a bare \"at 9\" is ambiguous and nothing is scheduled). Each run arrives in this session as a new task. " +
			"You only ever see your own checklist and requests plus the project's backlog and the tasks you scheduled.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["create", "ask", "list", "get", "update", "done", "claim"],
					"description": "Action to perform"
				},
				"id": {
					"type": "integer",
					"description": "Task ID (required for update/done/get/claim)"
				},
				"title": {
					"type": "string",
					"description": "Task title (required for create/ask, optional for update)"
				},
				"description": {
					"type": "string",
					"description": "Task description (optional)"
				},
				"when": {
					"type": "string",
					"description": "create only: when the task runs, in plain text (\"in 20m\", \"tomorrow at 09:00\", \"every monday at 09:00\"). Read in the session's timezone (UTC when unknown). Omit for an ordinary task"
				},
				"target": {
					"type": "string",
					"enum": ["this session"],
					"description": "create with when only: the session that receives each run. Always this session; omit it"
				},
				"status": {
					"type": "string",
					"enum": ["pending", "in_progress"],
					"description": "New status for update; use done to complete"
				},
				"depends_on": {
					"type": "array",
					"items": { "type": "integer" },
					"description": "IDs of tasks this waits for (optional for create/update; replaces the ones you can see)"
				},
				"subtasks": {
					"type": "array",
					"items": {
						"type": "object",
						"properties": {
							"title": { "type": "string" },
							"done": { "type": "boolean" }
						},
						"required": ["title"]
					},
					"description": "One level of subtasks (optional for create/ask/update; replaces the list)"
				}
			},
			"required": ["action"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			action, _ := params["action"].(string)
			repo, actor := scope.Repo(), scope.Actor()
			if err := checkScheduleParams(action, params); err != nil {
				return core.ErrorResult(toolErrorText(err)), nil
			}
			var (
				res core.Result
				err error
			)
			switch action {
			case "create", "ask":
				res, err = toolCreate(ctx, repo, actor, action == "ask", params)
			case "update":
				res, err = toolUpdate(ctx, repo, actor, params)
			case "done":
				res, err = toolDone(ctx, repo, actor, params)
			case "claim":
				res, err = toolClaim(ctx, repo, actor, params)
			case "list":
				res, err = toolList(ctx, repo, actor)
			case "get":
				res, err = toolGet(ctx, repo, actor, params)
			default:
				return core.ErrorResult(fmt.Sprintf("unknown action: %s", action)), nil
			}
			if err != nil {
				return core.ErrorResult(toolErrorText(err)), nil
			}
			return res, nil
		},
	}
}

func toolErrorText(err error) string {
	switch {
	case errors.Is(err, ErrForbidden):
		return "that task is not yours to change"
	case errors.Is(err, ErrClaimed):
		return "that task was already claimed by another session"
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrInvalid), errors.Is(err, ErrSchemaTooNew):
		return err.Error()
	}
	return "tasks unavailable: " + err.Error()
}

func idParam(params map[string]any, action string) (int64, error) {
	id, ok := toInt(params["id"])
	if !ok {
		return 0, invalid("id is required for %s", action)
	}
	return int64(id), nil
}

func intList(v any) ([]int64, bool) {
	raw, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]int64, 0, len(raw))
	for _, d := range raw {
		if id, ok := toInt(d); ok {
			out = append(out, int64(id))
		}
	}
	return out, true
}

// parseSubtasks accepts objects {title, done} or bare strings. A subtask that
// carries anything else, in particular its own subtasks, is refused: the
// hierarchy is one level.
func parseSubtasks(v any) (*[]SubtaskInput, error) {
	raw, ok := v.([]any)
	if !ok {
		return nil, nil
	}
	out := make([]SubtaskInput, 0, len(raw))
	for _, item := range raw {
		switch it := item.(type) {
		case string:
			out = append(out, SubtaskInput{Title: it})
		case map[string]any:
			for k := range it {
				if k != "title" && k != "done" {
					return nil, invalid("a subtask has only title and done; subtasks cannot have subtasks")
				}
			}
			title, _ := it["title"].(string)
			done, _ := it["done"].(bool)
			out = append(out, SubtaskInput{Title: title, Done: done})
		default:
			return nil, invalid("a subtask is a title or {title, done}")
		}
	}
	return &out, nil
}

func toolCreate(ctx context.Context, repo *Repo, actor Actor, ask bool, params map[string]any) (core.Result, error) {
	title, _ := params["title"].(string)
	if strings.TrimSpace(title) == "" {
		return core.Result{}, invalid("title is required for create")
	}
	in := AgentInput{Title: title}
	in.Description, _ = params["description"].(string)
	in.DependsOn, _ = intList(params["depends_on"])
	if subs, err := parseSubtasks(params["subtasks"]); err != nil {
		return core.Result{}, err
	} else if subs != nil {
		in.Subtasks = *subs
	}
	if text, ok := params["when"].(string); ok {
		return toolSchedule(ctx, repo, actor, in, text)
	}
	var (
		t   AgentTask
		err error
	)
	if ask {
		t, err = repo.AgentAsk(ctx, actor, in)
	} else {
		t, err = repo.AgentCreate(ctx, actor, in)
	}
	if err != nil {
		return core.Result{}, err
	}
	if ask {
		return core.TextResult(fmt.Sprintf("Asked the owner, request #%d: %s. Keep working; the answer arrives later.", t.ID, t.Title)), nil
	}
	msg := fmt.Sprintf("Created task #%d: %s", t.ID, t.Title)
	if view, err := repo.AgentList(ctx, actor); err == nil {
		for _, other := range view.Checklist {
			if other.ID != t.ID && other.Status != StatusDone && strings.EqualFold(strings.TrimSpace(other.Title), strings.TrimSpace(t.Title)) {
				msg += fmt.Sprintf("\nNote: open task #%d has the same title; use update on it instead of duplicating.", other.ID)
				break
			}
		}
	}
	return core.TextResult(msg), nil
}

func toolUpdate(ctx context.Context, repo *Repo, actor Actor, params map[string]any) (core.Result, error) {
	id, err := idParam(params, "update")
	if err != nil {
		return core.Result{}, err
	}
	var p AgentPatch
	if v, ok := params["title"].(string); ok && v != "" {
		p.Title = &v
	}
	if v, ok := params["description"].(string); ok {
		p.Description = &v
	}
	if v, ok := params["status"].(string); ok && v != "" {
		p.Status = &v
	}
	if deps, ok := intList(params["depends_on"]); ok {
		p.DependsOn = &deps
	}
	if subs, err := parseSubtasks(params["subtasks"]); err != nil {
		return core.Result{}, err
	} else {
		p.Subtasks = subs
	}
	t, err := repo.AgentUpdate(ctx, actor, id, p)
	if err != nil {
		return core.Result{}, err
	}
	return core.TextResult(fmt.Sprintf("Updated task #%d: %s", t.ID, t.Title)), nil
}

func toolDone(ctx context.Context, repo *Repo, actor Actor, params map[string]any) (core.Result, error) {
	id, err := idParam(params, "done")
	if err != nil {
		return core.Result{}, err
	}
	if _, err := repo.AgentDone(ctx, actor, id); err != nil {
		return core.Result{}, err
	}
	view, err := repo.AgentList(ctx, actor)
	if err != nil {
		return core.Result{}, err
	}
	done, total := progress(view.Checklist)
	msg := fmt.Sprintf("Marked task #%d as done (%d/%d complete)", id, done, total)
	if done == total && total > 0 {
		msg += "\n\nAll tasks complete!"
	}
	return core.TextResult(msg), nil
}

func toolClaim(ctx context.Context, repo *Repo, actor Actor, params map[string]any) (core.Result, error) {
	id, err := idParam(params, "claim")
	if err != nil {
		return core.Result{}, err
	}
	t, err := repo.AgentClaim(ctx, actor, id)
	if err != nil {
		return core.Result{}, err
	}
	return core.TextResult(fmt.Sprintf("Claimed task #%d: %s. It is now in your checklist.", t.ID, t.Title)), nil
}

func progress(list []AgentTask) (done, total int) {
	for _, t := range list {
		total++
		if t.Status == StatusDone {
			done++
		}
	}
	return
}

func icon(status string) string {
	switch status {
	case StatusDone:
		return "☑"
	case StatusInProgress:
		return "▶"
	}
	return "☐"
}

func writeTask(sb *strings.Builder, t AgentTask) {
	fmt.Fprintf(sb, "\n%s #%d: %s", icon(t.Status), t.ID, t.Title)
	if t.Description != "" {
		fmt.Fprintf(sb, "\n    %s", t.Description)
	}
	for _, s := range t.Subtasks {
		box := "☐"
		if s.Done {
			box = "☑"
		}
		fmt.Fprintf(sb, "\n      %s %s", box, s.Title)
	}
	if len(t.WaitsFor) > 0 {
		fmt.Fprintf(sb, "\n    waits for: %s", joinIDs(t.WaitsFor))
	}
	if t.PrivateBlockers > 0 {
		sb.WriteString("\n    Blocked by a private task")
	}
	if t.CompletionNote != "" {
		fmt.Fprintf(sb, "\n    note: %s", t.CompletionNote)
	}
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(parts, ", ")
}

func toolList(ctx context.Context, repo *Repo, actor Actor) (core.Result, error) {
	view, err := repo.AgentList(ctx, actor)
	if err != nil {
		return core.Result{}, err
	}
	if len(view.Checklist)+len(view.Requests)+len(view.Backlog)+len(view.Scheduled) == 0 {
		return core.TextResult("No tasks yet."), nil
	}
	var sb strings.Builder
	if len(view.Checklist) > 0 {
		done, total := progress(view.Checklist)
		fmt.Fprintf(&sb, "Tasks (%d/%d done):\n", done, total)
		for _, t := range view.Checklist {
			writeTask(&sb, t)
		}
	}
	if len(view.Requests) > 0 {
		sb.WriteString("\n\nYour requests to the owner:")
		for _, t := range view.Requests {
			writeTask(&sb, t)
		}
	}
	if len(view.Backlog) > 0 {
		sb.WriteString("\n\nBacklog you can claim (your project):")
		for _, t := range view.Backlog {
			writeTask(&sb, t)
		}
	}
	if len(view.Scheduled) > 0 {
		sb.WriteString("\n\nYour scheduled tasks (each run arrives here as a new task):")
		for _, t := range view.Scheduled {
			fmt.Fprintf(&sb, "\n- #%d: %s — %s", t.ID, t.Title, describeSchedule(t))
		}
	}
	return core.TextResult(strings.TrimSpace(sb.String())), nil
}

func toolGet(ctx context.Context, repo *Repo, actor Actor, params map[string]any) (core.Result, error) {
	id, err := idParam(params, "get")
	if err != nil {
		return core.Result{}, err
	}
	t, err := repo.AgentGet(ctx, actor, id)
	if err != nil {
		return core.Result{}, err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Task #%d: %s\nStatus: %s\nPlace: %s", t.ID, t.Title, t.Status, t.Place)
	if t.Description != "" {
		fmt.Fprintf(&sb, "\nDescription: %s", t.Description)
	}
	if t.When != nil {
		fmt.Fprintf(&sb, "\nSchedule: %s", describeSchedule(t))
	}
	if len(t.WaitsFor) > 0 {
		fmt.Fprintf(&sb, "\nDepends on: %s", joinIDs(t.WaitsFor))
	}
	if t.PrivateBlockers > 0 {
		sb.WriteString("\nBlocked by a private task")
	}
	if len(t.Unblocks) > 0 {
		fmt.Fprintf(&sb, "\nUnblocks: %s", joinIDs(t.Unblocks))
	}
	for _, s := range t.Subtasks {
		box := "☐"
		if s.Done {
			box = "☑"
		}
		fmt.Fprintf(&sb, "\n  %s %s", box, s.Title)
	}
	if t.CompletionNote != "" {
		fmt.Fprintf(&sb, "\nNote: %s", t.CompletionNote)
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
