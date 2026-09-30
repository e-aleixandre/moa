package tasks

import (
	"context"
	"database/sql"
	"strings"
)

// AgentTask is what an agent may know about a task: never who else is
// involved, and never the identity of a task it cannot read. A dependency on
// such a task is only a count.
type AgentTask struct {
	ID             int64     `json:"id"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	Status         string    `json:"status"`
	Place          Place     `json:"place"`
	CompletionNote string    `json:"completion_note,omitempty"`
	Subtasks       []Subtask `json:"subtasks,omitempty"`
	// WaitsFor lists readable tasks this one waits for.
	WaitsFor []int64 `json:"waits_for,omitempty"`
	// PrivateBlockers counts open tasks this one waits for that the agent
	// cannot read: shown as "Blocked by a private task", with no title, note or
	// usable ID.
	PrivateBlockers int     `json:"private_blockers,omitempty"`
	Unblocks        []int64 `json:"unblocks,omitempty"`

	// A template the agent created for itself: when it runs.
	When          *When  `json:"when,omitempty"`
	TZ            string `json:"tz,omitempty"`
	Next          int64  `json:"next,omitempty"`
	ScheduleState string `json:"schedule_state,omitempty"`
}

// AgentView is the default listing of an agent.
type AgentView struct {
	Checklist []AgentTask `json:"checklist"`
	Requests  []AgentTask `json:"requests"`
	Backlog   []AgentTask `json:"backlog"`
	// Scheduled are the templates this session created for itself. They are
	// not on its checklist: each run arrives as its own task.
	Scheduled []AgentTask `json:"scheduled,omitempty"`
}

// AgentInput creates a checklist task or a request.
type AgentInput struct {
	Title       string
	Description string
	DependsOn   []int64
	Subtasks    []SubtaskInput
}

// AgentPatch is a partial agent edit of a task it may edit.
type AgentPatch struct {
	Title       *string
	Description *string
	Status      *string // pending or in_progress; completing goes through Done
	DependsOn   *[]int64
	Subtasks    *[]SubtaskInput
}

// reads reports whether the actor may read the task. The rule is here, in one
// place, and applied inside the same transaction as the operation.
func (a Actor) reads(t *Record) bool {
	if t.ArchivedAt != 0 || a.SessionID == "" {
		return false
	}
	switch t.Place {
	case PlaceAgent:
		return t.AssigneeSessionID == a.SessionID
	case PlaceYou:
		// A template is readable by the session that scheduled it for itself.
		if t.template {
			return t.CreatedBySessionID != "" && t.CreatedBySessionID == a.SessionID
		}
		// A private note has no requester and matches nobody.
		return t.RequesterSessionID != "" && t.RequesterSessionID == a.SessionID
	case PlaceBacklog:
		return a.ProjectKey != "" && t.ProjectKey == a.ProjectKey
	}
	return false
}

// edits is narrower than reads: a checklist task, or a request still open
// (to correct it). Backlog is only ever written through Claim.
func (a Actor) edits(t *Record) bool {
	// A template changes only through the owner's schedule controls.
	if !a.reads(t) || t.template {
		return false
	}
	switch t.Place {
	case PlaceAgent:
		return true
	case PlaceYou:
		return t.Status != StatusDone
	}
	return false
}

func (a Actor) project(ctx context.Context, q querier, recs []Record) ([]AgentTask, error) {
	known := make(map[int64]*Record, len(recs))
	for i := range recs {
		known[recs[i].ID] = &recs[i]
	}
	var need []int64
	for _, t := range recs {
		for _, id := range t.WaitsFor {
			if known[id] == nil {
				need = append(need, id)
			}
		}
		for _, id := range t.Unblocks {
			if known[id] == nil {
				need = append(need, id)
			}
		}
	}
	err := inChunks(need, func(chunk []int64) error {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		extra, err := loadBare(ctx, q, "id IN ("+placeholders(len(chunk))+")", args...)
		if err != nil {
			return err
		}
		for i := range extra {
			known[extra[i].ID] = &extra[i]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]AgentTask, 0, len(recs))
	for _, t := range recs {
		at := AgentTask{
			ID: t.ID, Title: t.Title, Description: t.Description, Status: t.Status, Place: t.Place,
			CompletionNote: t.CompletionNote, Subtasks: t.Subtasks,
			When: t.When, TZ: t.TZ, Next: t.Next, ScheduleState: t.ScheduleState,
		}
		for _, id := range t.WaitsFor {
			w := known[id]
			switch {
			case w != nil && a.reads(w):
				at.WaitsFor = append(at.WaitsFor, id)
			case w != nil && w.Status != StatusDone:
				at.PrivateBlockers++
			}
		}
		for _, id := range t.Unblocks {
			if w := known[id]; w != nil && a.reads(w) {
				at.Unblocks = append(at.Unblocks, id)
			}
		}
		out = append(out, at)
	}
	return out, nil
}

// AgentList returns the agent's checklist (done ones included until they are
// archived), its own requests to the owner, and the pending backlog of its
// project. Nothing else.
func (r *Repo) AgentList(ctx context.Context, a Actor) (AgentView, error) {
	view := AgentView{Checklist: []AgentTask{}, Requests: []AgentTask{}, Backlog: []AgentTask{}}
	if a.SessionID == "" {
		return view, nil
	}
	r.archiveDue(ctx)
	rd, err := r.reader()
	if err != nil || rd == nil {
		return view, err
	}
	tx, err := rd.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return view, err
	}
	defer func() { _ = tx.Rollback() }()
	load := func(where string, args ...any) ([]AgentTask, error) {
		recs, err := loadRecords(ctx, tx, where, args...)
		if err != nil {
			return nil, err
		}
		return a.project(ctx, tx, recs)
	}
	if view.Checklist, err = load("place = 'agent' AND assignee_session_id = ? AND archived_at IS NULL", a.SessionID); err != nil {
		return view, err
	}
	if view.Requests, err = load("place = 'you' AND requester_session_id = ? AND archived_at IS NULL", a.SessionID); err != nil {
		return view, err
	}
	if a.ProjectKey != "" {
		if view.Backlog, err = load("place = 'backlog' AND project_key = ? AND status = 'pending' AND archived_at IS NULL", a.ProjectKey); err != nil {
			return view, err
		}
	}
	if view.Scheduled, err = load(`place = 'you' AND archived_at IS NULL
		AND id IN (SELECT task_id FROM task_schedules WHERE created_by_session_id = ?)`, a.SessionID); err != nil {
		return view, err
	}
	return view, nil
}

// AgentGet returns one task the agent can read.
func (r *Repo) AgentGet(ctx context.Context, a Actor, id int64) (AgentTask, error) {
	r.archiveDue(ctx)
	rd, err := r.reader()
	if err != nil {
		return AgentTask{}, err
	}
	if rd == nil {
		return AgentTask{}, notAvailable(id)
	}
	rec, err := getRecord(ctx, rd, id)
	if err != nil {
		return AgentTask{}, err
	}
	if !a.reads(&rec) {
		return AgentTask{}, notAvailable(id)
	}
	pr, err := a.project(ctx, rd, []Record{rec})
	if err != nil {
		return AgentTask{}, err
	}
	return pr[0], nil
}

// visibleWaits checks that every dependency the agent names is one it can
// read: it cannot build an edge to a task it should not know exists.
func (a Actor) visibleWaits(ctx context.Context, q querier, ids []int64) error {
	for _, id := range ids {
		recs, err := loadBare(ctx, q, "id = ?", id)
		if err != nil {
			return err
		}
		if len(recs) == 0 || !a.reads(&recs[0]) {
			return notAvailable(id)
		}
	}
	return nil
}

func (r *Repo) agentInsert(ctx context.Context, a Actor, place Place, in AgentInput) (AgentTask, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return AgentTask{}, invalid("title is required")
	}
	if a.SessionID == "" {
		return AgentTask{}, invalid("no session identity")
	}
	rec := Record{Title: title, Description: in.Description, Status: StatusPending, Place: place,
		ProjectKey: a.ProjectKey, ProjectCWD: a.ProjectCWD}
	if place == PlaceAgent {
		rec.AssigneeSessionID = a.SessionID
	} else {
		rec.RequesterSessionID = a.SessionID
	}
	var out AgentTask
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		if err := a.visibleWaits(ctx, tx, in.DependsOn); err != nil {
			return false, err
		}
		id, err := r.insertTask(ctx, tx, rec, in.Subtasks, in.DependsOn)
		if err != nil {
			return false, err
		}
		out, err = r.agentSnapshot(ctx, tx, a, id)
		return true, err
	})
	return out, err
}

func (r *Repo) agentSnapshot(ctx context.Context, q querier, a Actor, id int64) (AgentTask, error) {
	rec, err := getRecord(ctx, q, id)
	if err != nil {
		return AgentTask{}, err
	}
	pr, err := a.project(ctx, q, []Record{rec})
	if err != nil {
		return AgentTask{}, err
	}
	return pr[0], nil
}

// AgentCreate adds a task to the agent's own checklist.
func (r *Repo) AgentCreate(ctx context.Context, a Actor, in AgentInput) (AgentTask, error) {
	return r.agentInsert(ctx, a, PlaceAgent, in)
}

// AgentAsk files a request to the owner. It does not block the agent.
func (r *Repo) AgentAsk(ctx context.Context, a Actor, in AgentInput) (AgentTask, error) {
	return r.agentInsert(ctx, a, PlaceYou, in)
}

// AgentUpdate edits a checklist task or a still-open request of the agent.
func (r *Repo) AgentUpdate(ctx context.Context, a Actor, id int64, p AgentPatch) (AgentTask, error) {
	var out AgentTask
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if !a.reads(&cur) {
			return false, notAvailable(id)
		}
		if !a.edits(&cur) {
			return false, ErrForbidden
		}
		if p.Title != nil {
			t := strings.TrimSpace(*p.Title)
			if t == "" {
				return false, invalid("title is required")
			}
			cur.Title = t
		}
		if p.Description != nil {
			cur.Description = *p.Description
		}
		if p.Status != nil {
			if *p.Status != StatusPending && *p.Status != StatusInProgress {
				return false, invalid("status must be pending or in_progress; use done to complete")
			}
			r.applyStatus(&cur, *p.Status)
		}
		if err := r.saveTask(ctx, tx, cur, cur.Revision); err != nil {
			return false, err
		}
		if p.Subtasks != nil {
			if err := replaceSubtasks(ctx, tx, id, *p.Subtasks); err != nil {
				return false, err
			}
		}
		if p.DependsOn != nil {
			if err := a.visibleWaits(ctx, tx, *p.DependsOn); err != nil {
				return false, err
			}
			// Edges the agent cannot see (an owner's dependency on a private
			// note) are not its to drop: it replaces only what it can read.
			keep, err := hiddenWaits(ctx, tx, a, id)
			if err != nil {
				return false, err
			}
			if err := replaceWaits(ctx, tx, id, append(keep, *p.DependsOn...)); err != nil {
				return false, err
			}
		}
		out, err = r.agentSnapshot(ctx, tx, a, id)
		return true, err
	})
	return out, err
}

func hiddenWaits(ctx context.Context, q querier, a Actor, id int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT waits_for_id FROM task_dependencies WHERE task_id = ?", id)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var w int64
		if err := rows.Scan(&w); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, w)
	}
	_ = rows.Close()
	var hidden []int64
	for _, w := range ids {
		recs, err := loadBare(ctx, q, "id = ?", w)
		if err != nil {
			return nil, err
		}
		if len(recs) == 0 || !a.reads(&recs[0]) {
			hidden = append(hidden, w)
		}
	}
	return hidden, nil
}

// AgentDone completes a task of the agent's own checklist. Not a backlog
// task it merely sees, and never a request to the owner: only the owner
// answers those.
func (r *Repo) AgentDone(ctx context.Context, a Actor, id int64) (AgentTask, error) {
	var out AgentTask
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if !a.reads(&cur) {
			return false, notAvailable(id)
		}
		if cur.Place != PlaceAgent {
			return false, ErrForbidden
		}
		changed := cur.Status != StatusDone
		if changed {
			r.applyStatus(&cur, StatusDone)
			if err := r.saveTask(ctx, tx, cur, cur.Revision); err != nil {
				return false, err
			}
		}
		out, err = r.agentSnapshot(ctx, tx, a, id)
		return changed, err
	})
	return out, err
}

// AgentClaim takes a pending backlog task of the agent's own project. It is
// the only write an agent has on the backlog. Of two sessions claiming at
// once, one wins and the others get "not available", the same answer as for
// a task assigned directly to someone else.
func (r *Repo) AgentClaim(ctx context.Context, a Actor, id int64) (AgentTask, error) {
	var out AgentTask
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		// A private note, a request or another project's task: not available.
		sameProject := a.ProjectKey != "" && cur.ProjectKey == a.ProjectKey
		if cur.ArchivedAt != 0 || !sameProject || (cur.Place != PlaceBacklog && cur.Place != PlaceAgent) {
			return false, notAvailable(id)
		}
		if cur.Place == PlaceAgent {
			if cur.AssigneeSessionID == a.SessionID {
				return false, invalid("task #%d is already yours", id)
			}
			// Indistinguishable from a task that was never claimable: telling a
			// race loser "claimed" would reveal that another session's direct
			// assignment exists.
			return false, notAvailable(id)
		}
		if cur.Status != StatusPending {
			return false, invalid("task #%d is not open", id)
		}
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET place = 'agent', assignee_session_id = ?,
			requester_session_id = NULL, updated_at = ?, revision = revision + 1
			WHERE id = ? AND place = 'backlog' AND status = 'pending' AND archived_at IS NULL AND project_key = ?`,
			a.SessionID, r.now().UnixMilli(), id, a.ProjectKey)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return false, ErrClaimed
		}
		out, err = r.agentSnapshot(ctx, tx, a, id)
		return true, err
	})
	return out, err
}

// Checklist is the flat projection of the agent's checklist that the session
// WebSocket and /tasks carry.
func (r *Repo) Checklist(ctx context.Context, sessionID string) ([]Task, error) {
	return r.flat(ctx, "place = 'agent' AND assignee_session_id = ? AND archived_at IS NULL", sessionID)
}

// Requests is the flat projection of the session's open and answered
// requests to the owner.
func (r *Repo) Requests(ctx context.Context, sessionID string) ([]Task, error) {
	return r.flat(ctx, "place = 'you' AND requester_session_id = ? AND archived_at IS NULL", sessionID)
}

func (r *Repo) flat(ctx context.Context, where, sessionID string) ([]Task, error) {
	if sessionID == "" {
		return nil, nil
	}
	r.archiveDue(ctx)
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	recs, err := loadRecords(ctx, rd, where, sessionID)
	if err != nil {
		return nil, err
	}
	// The session WebSocket and /tasks are the agent's view: an edge to a
	// task the session cannot read is not projected, as in AgentGet.
	known := map[int64]*Record{}
	var need []int64
	for i := range recs {
		known[recs[i].ID] = &recs[i]
	}
	for _, t := range recs {
		for _, w := range t.WaitsFor {
			if known[w] == nil {
				need = append(need, w)
			}
		}
	}
	err = inChunks(need, func(chunk []int64) error {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		extra, err := loadBare(ctx, rd, "id IN ("+placeholders(len(chunk))+")", args...)
		for i := range extra {
			known[extra[i].ID] = &extra[i]
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(recs))
	for _, t := range recs {
		a := Actor{SessionID: sessionID, ProjectKey: t.ProjectKey}
		ft := Task{ID: int(t.ID), Title: t.Title, Description: t.Description, Status: t.Status,
			CreatedAt: t.CreatedAt, CompletedAt: t.CompletedAt}
		for _, w := range t.WaitsFor {
			if k := known[w]; k != nil && a.reads(k) {
				ft.DependsOn = append(ft.DependsOn, int(w))
			}
		}
		out = append(out, ft)
	}
	return out, nil
}

// CompleteForSession is the owner completing, from that session's /tasks, a
// task the session owns or asked for. It reaches nothing outside the session.
func (r *Repo) CompleteForSession(ctx context.Context, sessionID string, id int64) error {
	return r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		mine := cur.ArchivedAt == 0 &&
			(cur.Place == PlaceAgent && cur.AssigneeSessionID == sessionID ||
				cur.Place == PlaceYou && cur.RequesterSessionID != "" && cur.RequesterSessionID == sessionID)
		if !mine {
			return false, notAvailable(id)
		}
		if cur.Status == StatusDone {
			return false, nil
		}
		r.applyStatus(&cur, StatusDone)
		return true, r.saveTask(ctx, tx, cur, cur.Revision)
	})
}

// ResetChecklist deletes the session's checklist. Its requests, the backlog
// and the owner's notes are untouched.
func (r *Repo) ResetChecklist(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	rd, err := r.reader()
	if err != nil || rd == nil {
		return err
	}
	return r.write(ctx, func(tx *sql.Tx) (bool, error) {
		// Runs whose child is on this checklist are settled first: a bulk
		// delete must not leave an assignment that can still be delivered.
		rows, err := tx.QueryContext(ctx, `SELECT o.id FROM task_occurrences o JOIN tasks t ON t.id = o.child_task_id
			WHERE t.place = 'agent' AND t.assignee_session_id = ?`, sessionID)
		if err != nil {
			return false, err
		}
		var runs []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return false, err
			}
			runs = append(runs, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return false, err
		}
		for _, id := range runs {
			if _, err := r.settleRemovedChild(ctx, tx, id); err != nil {
				return false, err
			}
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM tasks WHERE place = 'agent' AND assignee_session_id = ?", sessionID)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	})
}

// AgentSchedule schedules a task for the agent's own session: a template the
// owner owns and sees, which this session can read and which runs here. The
// target and the creator are the actor, never something the model says.
func (r *Repo) AgentSchedule(ctx context.Context, a Actor, in AgentInput, when When, tz string) (Record, error) {
	if a.SessionID == "" {
		return Record{}, invalid("no session identity")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Record{}, invalid("title is required")
	}
	def := ScheduleDef{When: when, TZ: tz, Target: Target{Kind: TargetSession, ID: a.SessionID}, CreatedBySessionID: a.SessionID}
	cin := CreateInput{Schedule: &def}
	rec := Record{Title: title, Description: in.Description, Status: StatusPending,
		ProjectKey: a.ProjectKey, ProjectCWD: a.ProjectCWD}
	first, err := r.checkTemplateInput(&cin, &rec)
	if err != nil {
		return Record{}, err
	}
	var out Record
	err = r.write(ctx, func(tx *sql.Tx) (bool, error) {
		if err := a.visibleWaits(ctx, tx, in.DependsOn); err != nil {
			return false, err
		}
		id, err := r.insertTask(ctx, tx, rec, in.Subtasks, in.DependsOn)
		if err != nil {
			return false, err
		}
		if err := r.createSchedule(ctx, tx, id, *cin.Schedule, first); err != nil {
			return false, err
		}
		out, err = getRecord(ctx, tx, id)
		return true, err
	})
	return out, err
}
