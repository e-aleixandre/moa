package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/e-aleixandre/moa/pkg/core"
)

// NewTool returns the tasks tool for one session. Who is asking is the
// scope's identity, injected at bootstrap; the arguments cannot name another
// session or project. The one exception is send, offered only to an owner's
// conversation, which names another owner (never its session).
func NewTool(scope *Scope) core.Tool {
	return newTool(scope, scope.ownerSession)
}

// ChildVariant is the tasks tool as a subagent sees it: the owner's tool
// without send, whose schema and description are those of a plain session. A
// child inherits its parent's tools but does not speak for the owner. It
// reports false for any tool that is not an owner's tasks tool. The refusal
// itself lives in Execute (see actsForOwner), so selecting the tool by name
// cannot bypass this.
func ChildVariant(t core.Tool) (core.Tool, bool) {
	if t.Name != "tasks" || t.Execute == nil || !strings.Contains(string(t.Parameters), `"send"`) {
		return core.Tool{}, false
	}
	plain := newTool(nil, false)
	t.Description, t.Parameters = plain.Description, plain.Parameters
	return t, true
}

func newTool(scope *Scope, ownerSession bool) core.Tool {
	actions := `"create", "ask", "list", "get", "update", "done", "claim", "comment"`
	description := toolDescription
	if ownerSession {
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
					"description": "For done, required: 1-4 lines. The first says what is true now (\"Deployed X at 22:25 UTC\"); the next ones, how you checked it and the evidence (commit, PR, time). For a task another owner sent you: the result, or the reason for cant_do"
				},
				"search": {
					"type": "string",
					"description": "For list: only tasks whose title, description, subtasks or closing note contain this text (accents and case ignored). Also finds done and archived tasks"
				},
				"limit": {
					"type": "integer",
					"description": "For list: at most this many tasks (default 50, maximum 200)"
				},
				"archived": {
					"type": "boolean",
					"description": "For list: also include archived tasks (done more than 7 days ago); default false"
				},` + toParam(ownerSession) + `
				"status": {
					"type": "string",
					"enum": ["pending", "in_progress", "open", "done", "all"],
					"description": "For update: pending or in_progress (use the done action to complete). For list: open, done or all (omitted: open tasks plus those done in the last 24 hours, one line each)"
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
			if err := actsForOwner(ctx, repo, actor, action, params); err != nil {
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
				res, err = toolList(ctx, repo, actor, params)
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
	"list shows what is open and what was done in the last 24 hours, one line each; use status, search, limit or archived to look further and get for one task in full. " +
	"done needs a note of 1-4 lines: the first says what is true now, the next ones how you checked it and the evidence (commit, PR, time). " +
	"comment writes in the thread of any task on your checklist, of a task between owners or of your request: use it for milestones the user would want to read later (a decision, a deploy, a check), not every step. " +
	"A task another owner sent you is closed with done, an outcome and a note. " +
	"You only ever see your own checklist and requests plus the project's backlog and the tasks you scheduled."

const toolOwnerDescription = " send gives a task to another project's owner (to, title, description, subtasks); " +
	"without the user's permission it waits for them first. list shows what you sent under Sent."

// actsForOwner refuses what a subagent (a context tagged with an agent id)
// would do in its owner's name: sending a task to another owner, and
// answering or closing a task another owner sent. The rest of the tool works
// for a child as it does for any session.
func actsForOwner(ctx context.Context, repo *Repo, actor Actor, action string, params map[string]any) error {
	if core.AgentIDFromContext(ctx) == "" {
		return nil
	}
	switch action {
	case "send":
		return invalid("a subagent cannot send tasks to another owner: that is the owner's own decision")
	case "comment", "done":
		id, ok := toInt(params["id"])
		if !ok {
			return nil
		}
		if t, err := repo.AgentGet(ctx, actor, int64(id)); err == nil && t.HandoffState != "" {
			return invalid("task #%d is between owners: only the owner's own conversation can answer or close it", id)
		}
	}
	return nil
}

func toParam(owner bool) string {
	if !owner {
		return ""
	}
	return `
				"to": {
					"type": "string",
					"description": "For send: the other owner's name or ID"
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

const (
	listDefaultLimit = 50
	listMaxLimit     = 200
	listOpenDescCut  = 200
	listNoteCut      = 110
	listRecent       = 24 * time.Hour
)

// listQuery is list's optional filters. The zero value is the default view:
// what is open plus what was done in the last 24 hours.
type listQuery struct {
	status   string // "", "open", "done" or "all"
	search   string // folded; also reaches archived tasks
	limit    int
	archived bool
}

func parseListQuery(params map[string]any) (listQuery, error) {
	q := listQuery{limit: listDefaultLimit}
	if v, _ := params["status"].(string); strings.TrimSpace(v) != "" {
		q.status = strings.ToLower(strings.TrimSpace(v))
		switch q.status {
		case "open", "done", "all":
		case "pending", "in_progress":
			// update's values, which a strict-schema model fills in for
			// every action: no filter.
			q.status = ""
		default:
			return q, invalid("status must be %q, %q or %q (or omitted: open tasks plus those done in the last 24 hours)", "open", "done", "all")
		}
	}
	if v, _ := params["search"].(string); strings.TrimSpace(v) != "" {
		q.search = fold(v)
	}
	if n, ok := toInt(params["limit"]); ok && n > 0 {
		q.limit = min(n, listMaxLimit)
	}
	q.archived, _ = params["archived"].(bool)
	return q, nil
}

// fold lower-cases and strips accents, so "ALBARAN" finds "Albarán".
func fold(s string) string {
	var sb strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func (q listQuery) matches(t AgentTask) bool {
	if q.search == "" {
		return true
	}
	hay := []string{t.Title, t.Description, t.CompletionNote}
	for _, s := range t.Subtasks {
		hay = append(hay, s.Title)
	}
	return strings.Contains(fold(strings.Join(hay, "\n")), q.search)
}

// shows says whether a task of the checklist, requests or sent lists is in
// the view: now is the clock, for the 24-hour window of the default.
func (q listQuery) shows(t AgentTask, now time.Time) bool {
	open := t.Status != StatusDone
	if t.Archived && !q.archived && q.search == "" {
		return false
	}
	switch q.status {
	case "open":
		if !open {
			return false
		}
	case "done":
		if open {
			return false
		}
	case "":
		if q.search == "" && !open && now.UnixMilli()-t.CompletedAt > listRecent.Milliseconds() {
			return false
		}
	}
	return q.matches(t)
}

// cut shortens s to n runes with an ellipsis.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// noteSummary is the first line of a closing note, without markdown marks.
func noteSummary(note string) string {
	for _, l := range strings.Split(note, "\n") {
		l = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "").Replace(l))
		l = strings.TrimLeft(l, "#> ")
		if l != "" {
			return cut(l, listNoteCut)
		}
	}
	return ""
}

// writeDoneLine is a done task in list: one line, whatever it holds.
func writeDoneLine(sb *strings.Builder, t AgentTask) {
	fmt.Fprintf(sb, "\n☑ #%d: %s", t.ID, t.Title)
	if t.Archived {
		sb.WriteString(" [archived]")
	}
	if sum := noteSummary(t.CompletionNote); sum != "" {
		fmt.Fprintf(sb, " — %s", sum)
	}
	switch t.CommentCount {
	case 0:
	case 1:
		sb.WriteString(" (1 comment)")
	default:
		fmt.Fprintf(sb, " (%d comments)", t.CommentCount)
	}
}

func writeListed(sb *strings.Builder, t AgentTask) {
	if t.Status == StatusDone {
		writeDoneLine(sb, t)
		return
	}
	t.Description = cut(t.Description, listOpenDescCut)
	t.CompletionNote = ""
	writeTask(sb, t)
	if t.CommentCount > 0 {
		fmt.Fprintf(sb, "\n    (%d comment%s)", t.CommentCount, map[bool]string{true: "", false: "s"}[t.CommentCount == 1])
	}
}

func toolList(ctx context.Context, repo *Repo, actor Actor, params map[string]any) (core.Result, error) {
	q, err := parseListQuery(params)
	if err != nil {
		return core.Result{}, err
	}
	view, err := repo.agentList(ctx, actor, q.archived || q.search != "")
	if err != nil {
		return core.Result{}, err
	}
	if len(view.Checklist)+len(view.Requests)+len(view.Backlog)+len(view.Scheduled)+len(view.Sent) == 0 {
		return core.TextResult("No tasks yet."), nil
	}
	now := repo.now()
	hiddenDone := 0
	pick := func(list []AgentTask) []AgentTask {
		var out []AgentTask
		var done []AgentTask
		for _, t := range list {
			if q.shows(t, now) {
				if t.Status == StatusDone {
					done = append(done, t)
				} else {
					out = append(out, t)
				}
			} else if t.Status == StatusDone && !t.Archived && q.status == "" && q.search == "" {
				hiddenDone++
			}
		}
		sort.SliceStable(done, func(i, j int) bool { return done[i].CompletedAt > done[j].CompletedAt })
		return append(out, done...)
	}
	// Backlog and templates are never done: they show unless the view is
	// only done tasks, and a search narrows them like the rest.
	rest := func(list []AgentTask) []AgentTask {
		if q.status == "done" {
			return nil
		}
		var out []AgentTask
		for _, t := range list {
			if q.matches(t) {
				out = append(out, t)
			}
		}
		return out
	}
	checklist, requests, sent := pick(view.Checklist), pick(view.Requests), pick(view.Sent)
	backlog, scheduled := rest(view.Backlog), rest(view.Scheduled)

	var sb strings.Builder
	budget, over := q.limit, 0
	section := func(head string, list []AgentTask, write func(AgentTask)) {
		if len(list) == 0 {
			return
		}
		sb.WriteString(head)
		for _, t := range list {
			if budget == 0 {
				over++
				continue
			}
			budget--
			write(t)
		}
	}
	if len(checklist) > 0 {
		done, total := progress(view.Checklist)
		section(fmt.Sprintf("Tasks (%d/%d done):", done, total), checklist, func(t AgentTask) { writeListed(&sb, t) })
	}
	section("\n\nYour requests to the owner:", requests, func(t AgentTask) { writeListed(&sb, t) })
	section("\n\nBacklog you can claim (your project):", backlog, func(t AgentTask) { writeTask(&sb, t) })
	section("\n\nSent to other owners:", sent, func(t AgentTask) {
		fmt.Fprintf(&sb, "\n- #%d → %s: %s — %s", t.ID, t.To, t.Title, handoffStatus(t))
	})
	section("\n\nYour scheduled tasks (each run arrives here as a new task):", scheduled, func(t AgentTask) {
		fmt.Fprintf(&sb, "\n- #%d: %s — %s", t.ID, t.Title, describeSchedule(t))
	})
	if over > 0 {
		fmt.Fprintf(&sb, "\n\n%d more — narrow with search or status (limit is %d, at most %d).", over, q.limit, listMaxLimit)
	}
	if hiddenDone > 0 {
		fmt.Fprintf(&sb, "\n\n%d older done task%s hidden — list with status \"done\" or search to see %s; get shows one in full.",
			hiddenDone, map[bool]string{true: "", false: "s"}[hiddenDone == 1], map[bool]string{true: "it", false: "them"}[hiddenDone == 1])
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		out = "No tasks match."
	}
	return core.TextResult(out), nil
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
