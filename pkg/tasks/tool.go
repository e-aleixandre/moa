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
// session or project. The one exception is send, offered only to an owner's
// conversation, which names another owner (never its session).
func NewTool(scope *Scope) core.Tool {
	actions := `"create", "ask", "list", "get", "update", "done", "claim", "comment"`
	description := toolDescription
	if scope.ownerSession {
		actions += `, "send"`
		description += toolOwnerDescription
	}
	return core.Tool{
		Name:        "tasks",
		Label:       "Tasks",
		Description: description,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": [` + actions + `],
					"description": "Action to perform"
				},
				"id": {
					"type": "integer",
					"description": "Task ID (required for update/done/get/claim/comment)"
				},
				"title": {
					"type": "string",
					"description": "Task title (required for create/ask, optional for update; empty or omitted = unchanged)"
				},
				"description": {
					"type": "string",
					"description": "Task description (optional; empty or omitted on update = unchanged)"
				},
				"when": {
					"type": ["string", "null"],
					"description": "Optional, create only. Omit it, or leave it empty or null, unless the user asks to schedule the task. Then it is plain text (\"in 20m\", \"tomorrow at 09:00\", \"every monday at 09:00\"), read in the session's timezone (UTC when unknown)"
				},
				"target": {
					"type": ["string", "null"],
					"enum": ["this session", "", null],
					"description": "Optional. Omit it, or leave it empty or null, unless the user asks to schedule the task: it is always this session"
				},
				"body": {
					"type": "string",
					"description": "Comment text (required for comment)"
				},
				"outcome": {
					"type": "string",
					"enum": ["done", "cant_do"],
					"description": "For done on a task another owner sent you: done (default) or cant_do"
				},
				"note": {
					"type": "string",
					"description": "For done: the result, or the reason for cant_do. Required on a task another owner sent you"
				},` + toParam(scope.ownerSession) + `
				"status": {
					"type": "string",
					"enum": ["pending", "in_progress"],
					"description": "New status for update; use done to complete"
				},
				"depends_on": {
					"type": "array",
					"items": { "type": "integer" },
					"description": "IDs of tasks this waits for (optional for create/update; replaces the ones you can see; empty or omitted on update = unchanged)"
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
					"description": "One level of subtasks (optional for create/ask/update; replaces the list; empty or omitted on update = unchanged)"
				}
			},
			"required": ["action"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			action, _ := params["action"].(string)
			repo, actor := scope.Repo(), scope.Actor()
			// A nullable optional arrives as null from strict-schema models.
			for k, v := range params {
				if v == nil {
					delete(params, k)
				}
			}
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
			case "comment":
				res, err = toolComment(ctx, repo, actor, params)
			case "send":
				res, err = toolSend(ctx, scope, params)
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

const toolDescription = "Your task checklist, your requests to the owner, and your project's shared backlog. " +
	"create adds to your checklist; ask files a request to the owner without blocking you (ask_user does block); " +
	"claim takes a pending backlog task of your project; list before creating to avoid duplicates. " +
	"create with when schedules the task to run later or repeat, in this session only; only when the user asks to schedule, otherwise omit when. when is plain text such as " +
	"\"in 20m\", \"tomorrow at 09:00\", \"monday at 09:00\", \"2026-10-05 15:30\" or \"every weekday at 08:30\" " +
	"(use 09:00-style hours: a bare \"at 9\" is ambiguous and nothing is scheduled). Each run arrives in this session as a new task. " +
	"comment writes in the thread of a task between owners or of your request; a task another owner sent you is closed with done, an outcome and a note. " +
	"You only ever see your own checklist and requests plus the project's backlog and the tasks you scheduled."

const toolOwnerDescription = " send gives a task to another project's owner (to, title, description, subtasks); " +
	"without the user's permission it waits for them first. list shows what you sent under Sent."

func toParam(owner bool) string {
	if !owner {
		return ""
	}
	return `
				"to": {
					"type": "string",
					"description": "For send: the other owner's name"
				},`
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
	if v, ok := params["description"].(string); ok && v != "" {
		p.Description = &v
	}
	if v, ok := params["status"].(string); ok && v != "" {
		p.Status = &v
	}
	if deps, ok := intList(params["depends_on"]); ok && len(deps) > 0 {
		p.DependsOn = &deps
	}
	if subs, err := parseSubtasks(params["subtasks"]); err != nil {
		return core.Result{}, err
	} else {
		if subs != nil && len(*subs) > 0 {
			p.Subtasks = subs
		}
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
	outcome, _ := params["outcome"].(string)
	note, _ := params["note"].(string)
	if _, err := repo.AgentClose(ctx, actor, id, outcome, note); err != nil {
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

func toolComment(ctx context.Context, repo *Repo, actor Actor, params map[string]any) (core.Result, error) {
	id, err := idParam(params, "comment")
	if err != nil {
		return core.Result{}, err
	}
	body, _ := params["body"].(string)
	if _, err := repo.AgentComment(ctx, actor, id, body); err != nil {
		return core.Result{}, err
	}
	return core.TextResult(fmt.Sprintf("Commented on task #%d. The other side hears it; you will hear their answer.", id)), nil
}

// toolSend is the only action that names another owner. Who sends is the
// scope's session, checked against the directory on every call: only an
// owner's own conversation sends, and the permission is read fresh.
func toolSend(ctx context.Context, scope *Scope, params map[string]any) (core.Result, error) {
	if !scope.ownerSession || scope.owners == nil {
		return core.Result{}, invalid("only a project owner can send tasks to another owner")
	}
	from, ok, err := scope.owners.OwnerOf(scope.sessionID)
	if err != nil {
		return core.Result{}, err
	}
	if !ok {
		return core.Result{}, invalid("only a project owner can send tasks to another owner")
	}
	name, _ := params["to"].(string)
	if strings.TrimSpace(name) == "" {
		return core.Result{}, invalid("to is required for send: the other owner's name")
	}
	to, ok, err := scope.owners.Find(strings.TrimSpace(name))
	if err != nil {
		return core.Result{}, err
	}
	if !ok {
		return core.Result{}, invalid("there is no owner named %q", name)
	}
	if to.ID == from.ID {
		return core.Result{}, invalid("an owner cannot send a task to itself")
	}
	allowed, err := scope.owners.CanSend(from.ID, to.ID)
	if err != nil {
		return core.Result{}, err
	}
	in := AgentInput{}
	in.Title, _ = params["title"].(string)
	in.Description, _ = params["description"].(string)
	if subs, err := parseSubtasks(params["subtasks"]); err != nil {
		return core.Result{}, err
	} else if subs != nil {
		in.Subtasks = *subs
	}
	rec, err := scope.repo.SendHandoff(ctx, from, to, allowed, in)
	if err != nil {
		return core.Result{}, err
	}
	if allowed {
		return core.TextResult(fmt.Sprintf("Sent task #%d to %s: %s. Its answer and its closing note arrive as notices; it is under Sent in list.",
			rec.ID, to.Name, rec.Title)), nil
	}
	return core.TextResult(fmt.Sprintf("You have no permission to send tasks to %s, so task #%d waits for the user: they send it, always allow it, or decline it. "+
		"Keep working; you will hear the answer.", to.Name, rec.ID)), nil
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
	if len(view.Checklist)+len(view.Requests)+len(view.Backlog)+len(view.Scheduled)+len(view.Sent) == 0 {
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
	if len(view.Sent) > 0 {
		sb.WriteString("\n\nSent to other owners:")
		for _, t := range view.Sent {
			fmt.Fprintf(&sb, "\n- #%d → %s: %s — %s", t.ID, t.To, t.Title, handoffStatus(t))
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

func handoffStatus(t AgentTask) string {
	switch {
	case t.HandoffState == HandoffAwaiting:
		return "waiting for the user's permission"
	case t.HandoffState == HandoffDeclined:
		return "declined by the user"
	case t.Status == StatusDone && t.Outcome == OutcomeCantDo:
		return "can't do"
	case t.Status == StatusDone:
		return "done"
	case t.Status == StatusInProgress:
		return "in progress"
	}
	return "open"
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
	if t.From != "" {
		fmt.Fprintf(&sb, "\nFrom: %s, the owner of another project", t.From)
	}
	if t.To != "" {
		fmt.Fprintf(&sb, "\nSent to: %s (%s)", t.To, handoffStatus(t))
	}
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
	if len(t.Thread) > 0 {
		sb.WriteString("\nThread:")
		for _, c := range t.Thread {
			fmt.Fprintf(&sb, "\n  %s: %s", c.Author, c.Body)
		}
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
