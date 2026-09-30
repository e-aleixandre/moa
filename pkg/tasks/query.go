package tasks

import (
	"context"
	"database/sql"
	"strings"
)

// querier is what *sql.DB and *sql.Tx share, so reads work in or out of a
// write transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// The last four columns come from the scheduling tables: whether the task is
// a template (and which session created it), and the occurrence a run's child
// belongs to. Every read of a task knows what it is, so no path can treat a
// template or a scheduled child as an ordinary task by forgetting a join.
const taskCols = `id, title, description, status, place, project_key, project_cwd,
	requester_session_id, assignee_session_id, completion_note,
	created_at, updated_at, completed_at, archived_at, revision,
	EXISTS(SELECT 1 FROM task_schedules s WHERE s.task_id = tasks.id),
	(SELECT s.created_by_session_id FROM task_schedules s WHERE s.task_id = tasks.id),
	(SELECT o.id FROM task_occurrences o WHERE o.child_task_id = tasks.id),
	(SELECT o.schedule_task_id FROM task_occurrences o WHERE o.child_task_id = tasks.id)`

func scanRecord(sc interface{ Scan(...any) error }) (Record, error) {
	var (
		r                           Record
		place                       string
		projKey, projCWD, requester sql.NullString
		assignee                    sql.NullString
		completedAt, archivedAt     sql.NullInt64
		template                    bool
		creator                     sql.NullString
		occID, parentID             sql.NullInt64
	)
	if err := sc.Scan(&r.ID, &r.Title, &r.Description, &r.Status, &place, &projKey, &projCWD,
		&requester, &assignee, &r.CompletionNote,
		&r.CreatedAt, &r.UpdatedAt, &completedAt, &archivedAt, &r.Revision,
		&template, &creator, &occID, &parentID); err != nil {
		return Record{}, err
	}
	r.template, r.CreatedBySessionID = template, creator.String
	r.OccurrenceID, r.ParentTaskID = occID.Int64, parentID.Int64
	r.Place = Place(place)
	r.ProjectKey, r.ProjectCWD = projKey.String, projCWD.String
	r.RequesterSessionID, r.AssigneeSessionID = requester.String, assignee.String
	r.CompletedAt, r.ArchivedAt = completedAt.Int64, archivedAt.Int64
	return r, nil
}

// loadBare reads tasks without their relations.
func loadBare(ctx context.Context, q querier, where string, args ...any) ([]Record, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+taskCols+" FROM tasks WHERE "+where+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadRecords reads tasks with subtasks and dependencies attached.
func loadRecords(ctx context.Context, q querier, where string, args ...any) ([]Record, error) {
	recs, err := loadBare(ctx, q, where, args...)
	if err != nil {
		return nil, err
	}
	if err := attachRelations(ctx, q, recs); err != nil {
		return nil, err
	}
	return recs, attachSchedules(ctx, q, recs)
}

func getRecord(ctx context.Context, q querier, id int64) (Record, error) {
	recs, err := loadRecords(ctx, q, "id = ?", id)
	if err != nil {
		return Record{}, err
	}
	if len(recs) == 0 {
		return Record{}, notAvailable(id)
	}
	return recs[0], nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// inChunks runs fn over ids in slices small enough for SQLite's variable cap.
func inChunks(ids []int64, fn func(chunk []int64) error) error {
	const size = 400
	for len(ids) > 0 {
		n := min(size, len(ids))
		if err := fn(ids[:n]); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

func attachRelations(ctx context.Context, q querier, recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	idx := make(map[int64]int, len(recs))
	ids := make([]int64, 0, len(recs))
	for i, r := range recs {
		idx[r.ID] = i
		ids = append(ids, r.ID)
	}
	return inChunks(ids, func(chunk []int64) error {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		ph := placeholders(len(chunk))
		inChunk := make(map[int64]bool, len(chunk))
		for _, id := range chunk {
			inChunk[id] = true
		}
		rows, err := q.QueryContext(ctx, `SELECT id, task_id, title, done FROM task_subtasks
			WHERE task_id IN (`+ph+`) ORDER BY task_id, position, id`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st Subtask
			var taskID int64
			var done int
			if err := rows.Scan(&st.ID, &taskID, &st.Title, &done); err != nil {
				_ = rows.Close()
				return err
			}
			st.Done = done == 1
			recs[idx[taskID]].Subtasks = append(recs[idx[taskID]].Subtasks, st)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()

		rows, err = q.QueryContext(ctx, `SELECT task_id, waits_for_id FROM task_dependencies
			WHERE task_id IN (`+ph+`) OR waits_for_id IN (`+ph+`) ORDER BY task_id, waits_for_id`, append(args, args...)...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var task, waits int64
			if err := rows.Scan(&task, &waits); err != nil {
				return err
			}
			// An edge whose ends fall in different chunks is returned by both
			// queries: each end is filled only by the chunk that owns it.
			if i, ok := idx[task]; ok && inChunk[task] {
				recs[i].WaitsFor = append(recs[i].WaitsFor, waits)
			}
			if i, ok := idx[waits]; ok && inChunk[waits] {
				recs[i].Unblocks = append(recs[i].Unblocks, task)
			}
		}
		return rows.Err()
	})
}

// replaceSubtasks stores the whole list. A subtask is a title and a done flag,
// so there is no way to write a second level.
func replaceSubtasks(ctx context.Context, tx querier, taskID int64, subs []SubtaskInput) error {
	const maxSubtasks = 200
	if len(subs) > maxSubtasks {
		return invalid("at most %d subtasks", maxSubtasks)
	}
	for _, s := range subs {
		if strings.TrimSpace(s.Title) == "" {
			return invalid("a subtask needs a title")
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_subtasks WHERE task_id = ?", taskID); err != nil {
		return err
	}
	for i, s := range subs {
		done := 0
		if s.Done {
			done = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_subtasks(task_id, title, done, position) VALUES (?,?,?,?)`,
			taskID, strings.TrimSpace(s.Title), done, i); err != nil {
			return err
		}
	}
	return nil
}

// replaceWaits stores the tasks taskID waits for, then rejects the result if
// it closed a cycle. It runs in the same transaction as the write it belongs
// to, so a cycle can never be committed.
func replaceWaits(ctx context.Context, tx querier, taskID int64, waits []int64) error {
	seen := map[int64]bool{}
	uniq := waits[:0:0]
	for _, w := range waits {
		if w == taskID {
			return invalid("a task cannot wait for itself")
		}
		if !seen[w] {
			seen[w] = true
			uniq = append(uniq, w)
		}
	}
	for _, w := range uniq {
		var one int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM tasks WHERE id = ?", w).Scan(&one); err != nil {
			if err == sql.ErrNoRows {
				return notAvailable(w)
			}
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependencies WHERE task_id = ?", taskID); err != nil {
		return err
	}
	for _, w := range uniq {
		if _, err := tx.ExecContext(ctx, "INSERT INTO task_dependencies(task_id, waits_for_id) VALUES (?,?)", taskID, w); err != nil {
			return err
		}
	}
	for _, w := range uniq {
		var cyc int
		err := tx.QueryRowContext(ctx, `WITH RECURSIVE reach(id) AS (
			SELECT waits_for_id FROM task_dependencies WHERE task_id = ?
			UNION
			SELECT d.waits_for_id FROM task_dependencies d JOIN reach r ON d.task_id = r.id
		) SELECT 1 FROM reach WHERE id = ? LIMIT 1`, w, taskID).Scan(&cyc)
		if err == nil {
			return invalid("waiting for #%d would close a dependency cycle", w)
		}
		if err != sql.ErrNoRows {
			return err
		}
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
