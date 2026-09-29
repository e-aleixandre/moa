package tasks

import (
	"context"
	"database/sql"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// CreateInput is a task written by the owner (UI, REST or CLI).
type CreateInput struct {
	Title       string
	Description string
	Place       Place
	Status      string // default pending

	// ProjectKey wins over ProjectCWD; with only a CWD the key is derived from
	// it (core.CodebaseKey), so worktrees of one repo share a project.
	ProjectKey string
	ProjectCWD string

	RequesterSessionID string // PlaceYou only: makes it a request from that session
	AssigneeSessionID  string // PlaceAgent only

	Subtasks []SubtaskInput
	WaitsFor []int64

	// Deliver is the owner's choice for a saved recipient: DeliverWake or
	// DeliverHold ("" means hold). A loaded recipient gets the notice at once.
	Deliver string
}

// Patch is a partial owner edit; nil fields are left alone.
type Patch struct {
	Title       *string
	Description *string
	Status      *string
	Place       *Place // a move: an explicit gesture, see applyMove

	ProjectKey        *string
	ProjectCWD        *string
	AssigneeSessionID *string
	CompletionNote    *string

	Subtasks *[]SubtaskInput
	WaitsFor *[]int64

	// Notify is "Save and notify": the saved task is sent to its assignee (or
	// to the requester of a request). Assigning or completing notifies anyway.
	Notify  bool
	Deliver string // see CreateInput.Deliver
}

func validStatus(s string) bool {
	return s == StatusPending || s == StatusInProgress || s == StatusDone
}

// checkPlace mirrors the table's CHECK constraints so the caller gets a
// message instead of a constraint failure.
func checkPlace(rec *Record) error {
	switch rec.Place {
	case PlaceBacklog:
		if rec.ProjectKey == "" {
			return invalid("a backlog task belongs to a project")
		}
		if rec.RequesterSessionID != "" || rec.AssigneeSessionID != "" {
			return invalid("a backlog task has no requester or assignee")
		}
	case PlaceAgent:
		if rec.AssigneeSessionID == "" {
			return invalid("an agent task needs a session")
		}
	case PlaceYou:
		if rec.AssigneeSessionID != "" {
			return invalid("a task for you has no assignee")
		}
	default:
		return invalid("unknown place %q", rec.Place)
	}
	return nil
}

func projectKeyFor(key, cwd string) string {
	if key != "" || cwd == "" {
		return key
	}
	return core.CodebaseKey(cwd)
}

// Create stores a task as the owner.
func (r *Repo) Create(ctx context.Context, in CreateInput) (Record, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Record{}, invalid("title is required")
	}
	status := in.Status
	if status == "" {
		status = StatusPending
	}
	if !validStatus(status) {
		return Record{}, invalid("unknown status %q", status)
	}
	if err := validDeliver(in.Deliver); err != nil {
		return Record{}, err
	}
	rec := Record{
		Title: title, Description: in.Description, Status: status, Place: in.Place,
		ProjectKey: projectKeyFor(in.ProjectKey, in.ProjectCWD), ProjectCWD: in.ProjectCWD,
		RequesterSessionID: in.RequesterSessionID, AssigneeSessionID: in.AssigneeSessionID,
	}
	if err := checkPlace(&rec); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		id, err := r.insertTask(ctx, tx, rec, in.Subtasks, in.WaitsFor)
		if err != nil {
			return false, err
		}
		out, err = getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		kind, to := ownerNotice(nil, out, false)
		return true, r.addNotice(ctx, tx, kind, to, in.Deliver, out)
	})
	return out, err
}

// insertTask inserts a task with its subtasks and waits. Shared by the owner
// and agent paths, which differ only in what they allow beforehand.
func (r *Repo) insertTask(ctx context.Context, tx *sql.Tx, rec Record, subs []SubtaskInput, waits []int64) (int64, error) {
	now := r.now().UnixMilli()
	var completed any
	if rec.Status == StatusDone {
		completed = now
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO tasks(title, description, status, place, project_key, project_cwd,
		requester_session_id, assignee_session_id, completion_note, created_at, updated_at, completed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.Title, rec.Description, rec.Status, string(rec.Place), nullStr(rec.ProjectKey), nullStr(rec.ProjectCWD),
		nullStr(rec.RequesterSessionID), nullStr(rec.AssigneeSessionID), rec.CompletionNote, now, now, completed)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if len(subs) > 0 {
		if err := replaceSubtasks(ctx, tx, id, subs); err != nil {
			return 0, err
		}
	}
	if len(waits) > 0 {
		if err := replaceWaits(ctx, tx, id, waits); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// applyMove changes a task's place. Moving is the only thing that clears the
// requester: a request never becomes visible backlog by an ordinary edit.
func applyMove(cur *Record, to Place, p Patch) error {
	cur.Place = to
	switch to {
	case PlaceBacklog:
		cur.RequesterSessionID, cur.AssigneeSessionID = "", ""
	case PlaceYou:
		cur.RequesterSessionID, cur.AssigneeSessionID = "", ""
	case PlaceAgent:
		cur.RequesterSessionID = ""
		cur.AssigneeSessionID = ""
		if p.AssigneeSessionID != nil {
			cur.AssigneeSessionID = *p.AssigneeSessionID
		}
	}
	return nil
}

// Update applies an owner edit. revision must be the one the caller last
// saw: a stale form gets a *ConflictError carrying the current task instead
// of overwriting a change the CLI or an agent made in between.
func (r *Repo) Update(ctx context.Context, id, revision int64, p Patch) (Record, error) {
	if revision <= 0 {
		return Record{}, invalid("revision is required")
	}
	if err := validDeliver(p.Deliver); err != nil {
		return Record{}, err
	}
	var out Record
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if cur.Revision != revision {
			return false, &ConflictError{Current: cur}
		}
		before := cur
		if err := r.applyPatch(&cur, p); err != nil {
			return false, err
		}
		if err := r.saveTask(ctx, tx, cur, revision); err != nil {
			return false, err
		}
		if p.Subtasks != nil {
			if err := replaceSubtasks(ctx, tx, id, *p.Subtasks); err != nil {
				return false, err
			}
		}
		if p.WaitsFor != nil {
			if err := replaceWaits(ctx, tx, id, *p.WaitsFor); err != nil {
				return false, err
			}
		}
		out, err = getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		kind, to := ownerNotice(&before, out, p.Notify)
		return true, r.addNotice(ctx, tx, kind, to, p.Deliver, out)
	})
	return out, err
}

func (r *Repo) applyPatch(cur *Record, p Patch) error {
	if p.Title != nil {
		t := strings.TrimSpace(*p.Title)
		if t == "" {
			return invalid("title is required")
		}
		cur.Title = t
	}
	if p.Description != nil {
		cur.Description = *p.Description
	}
	if p.ProjectCWD != nil {
		cur.ProjectCWD = *p.ProjectCWD
	}
	if p.ProjectKey != nil {
		cur.ProjectKey = *p.ProjectKey
	} else if p.ProjectCWD != nil {
		cur.ProjectKey = projectKeyFor("", *p.ProjectCWD)
	}
	if p.Place != nil && *p.Place != cur.Place {
		if err := applyMove(cur, *p.Place, p); err != nil {
			return err
		}
	} else if p.AssigneeSessionID != nil && cur.Place == PlaceAgent {
		cur.AssigneeSessionID = *p.AssigneeSessionID
	}
	if p.Status != nil {
		if !validStatus(*p.Status) {
			return invalid("unknown status %q", *p.Status)
		}
		r.applyStatus(cur, *p.Status)
	}
	if p.CompletionNote != nil {
		cur.CompletionNote = *p.CompletionNote
	}
	return checkPlace(cur)
}

// applyStatus sets the status and the dates that depend on it: completion
// stamps completed_at; reopening clears it, the archive mark and the note.
func (r *Repo) applyStatus(cur *Record, status string) {
	was := cur.Status
	cur.Status = status
	switch {
	case status == StatusDone && was != StatusDone:
		cur.CompletedAt = r.now().UnixMilli()
		cur.ArchivedAt = 0
	case status != StatusDone && was == StatusDone:
		cur.CompletedAt, cur.ArchivedAt = 0, 0
		cur.CompletionNote = ""
	}
}

func (r *Repo) saveTask(ctx context.Context, tx *sql.Tx, t Record, revision int64) error {
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET title=?, description=?, status=?, place=?, project_key=?, project_cwd=?,
		requester_session_id=?, assignee_session_id=?, completion_note=?, updated_at=?, completed_at=?, archived_at=?,
		revision = revision + 1 WHERE id=? AND revision=?`,
		t.Title, t.Description, t.Status, string(t.Place), nullStr(t.ProjectKey), nullStr(t.ProjectCWD),
		nullStr(t.RequesterSessionID), nullStr(t.AssigneeSessionID), t.CompletionNote, r.now().UnixMilli(),
		nullInt(t.CompletedAt), nullInt(t.ArchivedAt), t.ID, revision)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		cur, err := getRecord(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		return &ConflictError{Current: cur}
	}
	return nil
}

// Delete removes a task with its subtasks and dependency edges. Deleting an
// open request tells nobody: the owner decided that. Deleting a session's
// task tells that session; deliver is as in CreateInput.
func (r *Repo) Delete(ctx context.Context, id, revision int64, deliver string) error {
	if revision <= 0 {
		return invalid("revision is required")
	}
	if err := validDeliver(deliver); err != nil {
		return err
	}
	return r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if cur.Revision != revision {
			return false, &ConflictError{Current: cur}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", id); err != nil {
			return false, err
		}
		if cur.Place == PlaceAgent {
			return true, r.addNotice(ctx, tx, NoticeAgentDeleted, cur.AssigneeSessionID, deliver, cur)
		}
		return true, nil
	})
}

// Get returns one task with its relations, archived or not. The owner's view:
// agents go through Actor methods.
func (r *Repo) Get(ctx context.Context, id int64) (Record, error) {
	r.archiveDue(ctx)
	rd, err := r.reader()
	if err != nil {
		return Record{}, err
	}
	if rd == nil {
		return Record{}, notAvailable(id)
	}
	return getRecord(ctx, rd, id)
}

// Filter narrows an owner listing.
type Filter struct {
	// ProjectKey limits backlog and agent tasks to one project. Tasks in You
	// are the owner's own and are always returned.
	ProjectKey      string
	IncludeAgents   bool
	IncludeArchived bool
}

// Counts summarizes a listing. OpenRequests is the number the sidebar shows:
// only requests agents made that are still open.
type Counts struct {
	OpenRequests int `json:"open_requests"`
	You          int `json:"you"`
	Backlog      int `json:"backlog"`
	Agents       int `json:"agents"`
}

// ListResult is an owner listing with the global revision it was read at.
type ListResult struct {
	Tasks    []Record `json:"tasks"`
	Counts   Counts   `json:"counts"`
	Revision int64    `json:"revision"`
}

// List returns the owner's view. Archived tasks are left out unless asked
// for, and never counted.
func (r *Repo) List(ctx context.Context, f Filter) (ListResult, error) {
	r.archiveDue(ctx)
	res := ListResult{Tasks: []Record{}}
	rd, err := r.reader()
	if err != nil || rd == nil {
		return res, err
	}
	// One read transaction so tasks and revision are the same snapshot.
	tx, err := rd.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM tasks_meta WHERE id = 1").Scan(&res.Revision); err != nil {
		return res, err
	}
	where := []string{"1=1"}
	var args []any
	if !f.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if !f.IncludeAgents {
		where = append(where, "place <> 'agent'")
	}
	if f.ProjectKey != "" {
		where = append(where, "(place = 'you' OR project_key = ?)")
		args = append(args, f.ProjectKey)
	}
	recs, err := loadRecords(ctx, tx, strings.Join(where, " AND "), args...)
	if err != nil {
		return res, err
	}
	if recs != nil {
		res.Tasks = recs
	}
	states, err := latestNoticeStates(ctx, tx)
	if err != nil {
		return res, err
	}
	for i := range res.Tasks {
		// Only an undelivered outcome is worth a mark in the list; sent is on
		// its way and delivered is done.
		switch st := states[res.Tasks[i].ID]; st {
		case NoticeHeld, NoticePending, NoticeFailed:
			res.Tasks[i].NoticeState = st
		}
	}
	for _, t := range recs {
		if t.ArchivedAt != 0 || t.Status == StatusDone {
			continue
		}
		switch t.Place {
		case PlaceYou:
			res.Counts.You++
			if t.RequesterSessionID != "" {
				res.Counts.OpenRequests++
			}
		case PlaceBacklog:
			res.Counts.Backlog++
		case PlaceAgent:
			res.Counts.Agents++
		}
	}
	return res, nil
}

// Project is a project the owner can file tasks under.
type Project struct {
	Key string `json:"key"`
	CWD string `json:"cwd,omitempty"`
}

// TaskProjects lists the projects tasks already refer to, most recent first
// wins for the display path.
func (r *Repo) TaskProjects(ctx context.Context) ([]Project, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	rows, err := rd.QueryContext(ctx, `SELECT project_key, project_cwd FROM tasks
		WHERE project_key IS NOT NULL ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	var out []Project
	for rows.Next() {
		var key string
		var cwd sql.NullString
		if err := rows.Scan(&key, &cwd); err != nil {
			return nil, err
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Project{Key: key, CWD: cwd.String})
	}
	return out, rows.Err()
}
