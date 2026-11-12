package tasks

import (
	"context"
	"database/sql"
	"strings"
)

// ExecutionLink says which session is doing an owner's checklist task. The
// platform writes the facts it knows (delegated, started, went quiet); what
// they mean stays the owner's decision, so nothing here completes a task.
// Times are Unix milliseconds; zero is "not yet".
type ExecutionLink struct {
	SessionID string `json:"session_id"`
	LinkedAt  int64  `json:"linked_at"`
	// StartedAt is the executor's first run under this link.
	StartedAt int64 `json:"started_at,omitempty"`
	// StoppedAt is when the owner was told the executor went quiet; zero
	// while it runs. It is said once per stop, never because of age.
	StoppedAt int64 `json:"stopped_at,omitempty"`
}

// attachExecutions fills Execution on the records that have a link.
func attachExecutions(ctx context.Context, q querier, recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	idx := make(map[int64]int, len(recs))
	ids := make([]int64, 0, len(recs))
	for i := range recs {
		idx[recs[i].ID] = i
		ids = append(ids, recs[i].ID)
	}
	return inChunks(ids, func(chunk []int64) error {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT task_id, session_id, linked_at, started_at, stopped_at
			FROM task_execution_links WHERE task_id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			var ex ExecutionLink
			var started, stopped sql.NullInt64
			if err := rows.Scan(&id, &ex.SessionID, &ex.LinkedAt, &started, &stopped); err != nil {
				return err
			}
			ex.StartedAt, ex.StoppedAt = started.Int64, stopped.Int64
			recs[idx[id]].Execution = &ex
		}
		return rows.Err()
	})
}

// linkable says why an owner cannot hand a task to an executor, "" when it can.
func (a Actor) linkable(t *Record) string {
	switch {
	case t.Place == PlaceBacklog:
		return "it is an idea in the backlog: promote it with tasks claim once the user decided it, then pass task again"
	case t.Place != PlaceAgent || t.AssigneeSessionID != a.SessionID:
		return "it is not on your checklist: create the task with tasks create, then pass its id"
	case t.Status == StatusDone:
		return "it is done: reopen it with tasks update status=in_progress, or create a new task for the new work"
	}
	return ""
}

func (a Actor) mayLink() error {
	if !a.Owner || a.SessionID == "" {
		return invalid("only the project owner's conversation links tasks to sessions")
	}
	return nil
}

func (a Actor) checkLink(t *Record) error {
	if t.ArchivedAt != 0 || !a.readsArchived(t) {
		return notAvailable(t.ID)
	}
	if why := a.linkable(t); why != "" {
		return invalid("task #%d cannot be linked: %s", t.ID, why)
	}
	return nil
}

// LinkExecution records that executor is doing the owner's task id. Only the
// owner's own conversation links, and only an open task of its own checklist.
// Linking another session replaces the attempt; linking the same one again
// (a correction, a "continue") keeps it.
func (r *Repo) LinkExecution(ctx context.Context, a Actor, id int64, executor string) (Record, error) {
	executor = strings.TrimSpace(executor)
	if err := a.mayLink(); err != nil {
		return Record{}, err
	}
	if executor == "" {
		return Record{}, invalid("no session to link task #%d to", id)
	}
	var out Record
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if err := a.checkLink(&cur); err != nil {
			return false, err
		}
		changed := false
		if cur.Execution == nil || cur.Execution.SessionID != executor {
			if _, err := tx.ExecContext(ctx, `INSERT INTO task_execution_links(task_id, session_id, linked_at) VALUES (?,?,?)
				ON CONFLICT(task_id) DO UPDATE SET session_id = excluded.session_id, linked_at = excluded.linked_at,
				started_at = NULL, stopped_at = NULL`, id, executor, r.now().UnixMilli()); err != nil {
				return false, err
			}
			changed = true
		}
		out, err = getRecord(ctx, tx, id)
		return changed, err
	})
	return out, err
}

// MarkExecutionStarted is work of sessionID: its linked tasks that were still
// pending are now in progress, the first start is kept, and a previous stop is
// cleared so the next one is said again.
func (r *Repo) MarkExecutionStarted(ctx context.Context, sessionID string) error {
	return r.MarkExecutionStartedExcept(ctx, sessionID, nil)
}

// MarkExecutionStartedExcept is MarkExecutionStarted leaving alone the tasks
// in queued: their instruction is still waiting for the session.
func (r *Repo) MarkExecutionStartedExcept(ctx context.Context, sessionID string, queued []int64) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	if ok, err := r.hasLinks(ctx, sessionID); err != nil || !ok {
		return err
	}
	except, args := "", []any{}
	if len(queued) > 0 {
		except = ` AND task_id NOT IN (` + placeholders(len(queued)) + `)`
		for _, id := range queued {
			args = append(args, id)
		}
	}
	return r.write(ctx, func(tx *sql.Tx) (bool, error) {
		now := r.now().UnixMilli()
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'in_progress', updated_at = ?, revision = revision + 1
			WHERE status = 'pending' AND place = 'agent' AND archived_at IS NULL
			AND id IN (SELECT task_id FROM task_execution_links WHERE session_id = ?`+except+`)`, append([]any{now, sessionID}, args...)...)
		if err != nil {
			return false, err
		}
		tasks, _ := res.RowsAffected()
		res, err = tx.ExecContext(ctx, `UPDATE task_execution_links SET started_at = COALESCE(started_at, ?), stopped_at = NULL
			WHERE session_id = ? AND (started_at IS NULL OR stopped_at IS NOT NULL)`+except+`
			AND task_id IN (SELECT id FROM tasks WHERE status <> 'done' AND archived_at IS NULL)`, append([]any{now, sessionID}, args...)...)
		if err != nil {
			return false, err
		}
		links, _ := res.RowsAffected()
		return tasks+links > 0, nil
	})
}

// MarkExecutionStopped records that sessionID went quiet and returns the open
// tasks it is the current executor of that were not told so yet. A second
// call for the same stop returns nothing: only a new start rearms it.
func (r *Repo) MarkExecutionStopped(ctx context.Context, sessionID string) ([]Record, error) {
	return r.MarkExecutionsStopped(ctx, sessionID, nil)
}

// MarkExecutionsStopped is MarkExecutionStopped for only the tasks a report
// named (all of them when only is empty).
func (r *Repo) MarkExecutionsStopped(ctx context.Context, sessionID string, only []int64) ([]Record, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	if ok, err := r.hasLinks(ctx, sessionID); err != nil || !ok {
		return nil, err
	}
	var out []Record
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		where, args := `status <> 'done' AND place = 'agent' AND archived_at IS NULL
			AND id IN (SELECT task_id FROM task_execution_links WHERE session_id = ? AND stopped_at IS NULL)`, []any{sessionID}
		if len(only) > 0 {
			where += ` AND id IN (` + placeholders(len(only)) + `)`
			for _, id := range only {
				args = append(args, id)
			}
		}
		recs, err := loadRecords(ctx, tx, where, args...)
		if err != nil || len(recs) == 0 {
			return false, err
		}
		now := r.now().UnixMilli()
		for i := range recs {
			if _, err := tx.ExecContext(ctx, `UPDATE task_execution_links SET stopped_at = ? WHERE task_id = ?`, now, recs[i].ID); err != nil {
				return false, err
			}
			recs[i].Execution.StoppedAt = now
		}
		out = recs
		return true, nil
	})
	return out, err
}

// InterruptedExecutions are the open tasks whose executor started and was
// never seen stopping: after a crash, the ones whose session is not working
// any more are what the owner must hear about. Read only.
func (r *Repo) InterruptedExecutions(ctx context.Context) ([]Record, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	return loadRecords(ctx, rd, `status <> 'done' AND place = 'agent' AND archived_at IS NULL
		AND id IN (SELECT task_id FROM task_execution_links WHERE started_at IS NOT NULL AND stopped_at IS NULL)`)
}

// UnwarnedExecutions are the open tasks sessionID is the current executor of
// whose stop the owner was not told yet: what a report of that session, made
// when it goes quiet, must name.
func (r *Repo) UnwarnedExecutions(ctx context.Context, sessionID string) ([]Record, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	if ok, err := r.hasLinks(ctx, sessionID); err != nil || !ok {
		return nil, err
	}
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	return loadRecords(ctx, rd, `status <> 'done' AND place = 'agent' AND archived_at IS NULL
		AND id IN (SELECT task_id FROM task_execution_links WHERE session_id = ? AND stopped_at IS NULL)`, sessionID)
}

// OwnerExecutions are the open tasks of ownerSessionID's checklist that have
// an executor.
func (r *Repo) OwnerExecutions(ctx context.Context, ownerSessionID string) ([]Record, error) {
	rd, err := r.reader()
	if err != nil || rd == nil || strings.TrimSpace(ownerSessionID) == "" {
		return nil, err
	}
	return loadRecords(ctx, rd, `status <> 'done' AND place = 'agent' AND archived_at IS NULL AND assignee_session_id = ?
		AND id IN (SELECT task_id FROM task_execution_links)`, ownerSessionID)
}

// CheckExecutionLink says whether LinkExecution would accept the task, without
// writing: the sessions tool asks before it creates a session or sends.
func (r *Repo) CheckExecutionLink(ctx context.Context, a Actor, id int64) error {
	if err := a.mayLink(); err != nil {
		return err
	}
	rd, err := r.reader()
	if err != nil {
		return err
	}
	if rd == nil {
		return notAvailable(id)
	}
	cur, err := getRecord(ctx, rd, id)
	if err != nil {
		return err
	}
	return a.checkLink(&cur)
}

// hasLinks is the cheap read that keeps every run start of an unlinked
// session (almost all of them) away from the writer.
func (r *Repo) hasLinks(ctx context.Context, sessionID string) (bool, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return false, err
	}
	var ok bool
	err = rd.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_execution_links WHERE session_id = ?)`, sessionID).Scan(&ok)
	return ok, err
}
