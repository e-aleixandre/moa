package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// scheduleRow is a template's definition as stored.
type scheduleRow struct {
	TaskID    int64
	Def       ScheduleDef
	Enabled   bool
	NextDueAt int64 // 0: nothing left to consume
	whenJSON  string
}

const scheduleCols = `task_id, when_json, tz, target_json, busy, saved, late, enabled, next_due_at, created_by_session_id`

func scanSchedule(sc interface{ Scan(...any) error }) (scheduleRow, error) {
	var (
		s                 scheduleRow
		whenJSON, tgtJSON string
		enabled           int
		next              sql.NullInt64
		creator           sql.NullString
		busy, saved, late string
	)
	if err := sc.Scan(&s.TaskID, &whenJSON, &s.Def.TZ, &tgtJSON, &busy, &saved, &late, &enabled, &next, &creator); err != nil {
		return scheduleRow{}, err
	}
	if err := json.Unmarshal([]byte(whenJSON), &s.Def.When); err != nil {
		return scheduleRow{}, fmt.Errorf("schedule #%d when: %w", s.TaskID, err)
	}
	if err := json.Unmarshal([]byte(tgtJSON), &s.Def.Target); err != nil {
		return scheduleRow{}, fmt.Errorf("schedule #%d target: %w", s.TaskID, err)
	}
	s.Def.Delivery = Delivery{Busy: busy, Saved: saved, Late: late}
	s.Def.CreatedBySessionID = creator.String
	s.Enabled, s.NextDueAt, s.whenJSON = enabled == 1, next.Int64, whenJSON
	return s, nil
}

func getSchedule(ctx context.Context, q querier, taskID int64) (scheduleRow, bool, error) {
	s, err := scanSchedule(q.QueryRowContext(ctx, "SELECT "+scheduleCols+" FROM task_schedules WHERE task_id = ?", taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return scheduleRow{}, false, nil
	}
	return s, err == nil, err
}

func saveSchedule(ctx context.Context, tx *sql.Tx, s scheduleRow) error {
	enabled := 0
	if s.Enabled {
		enabled = 1
	}
	d := s.Def
	_, err := tx.ExecContext(ctx, `INSERT INTO task_schedules(task_id, when_json, tz, target_json, busy, saved, late, enabled,
		next_due_at, created_by_session_id) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(task_id) DO UPDATE SET when_json=excluded.when_json, tz=excluded.tz, target_json=excluded.target_json,
		busy=excluded.busy, saved=excluded.saved, late=excluded.late, enabled=excluded.enabled, next_due_at=excluded.next_due_at`,
		s.TaskID, mustJSON(d.When), d.TZ, mustJSON(d.Target), d.Delivery.Busy, d.Delivery.Saved, d.Delivery.Late, enabled,
		nullInt(s.NextDueAt), nullStr(d.CreatedBySessionID))
	return err
}

const occCols = `id, schedule_task_id, due_at, definition_revision, spec_json, trigger, observed_at, state, reason, note,
	confirmed_at, resolved_session_id, child_task_id, notice_id, admitted_at, completed_at, missed_count,
	created_at, updated_at, revision`

func scanOccurrence(sc interface{ Scan(...any) error }) (Occurrence, error) {
	var (
		o                                    Occurrence
		spec                                 string
		confirmed, child, admitted, complete sql.NullInt64
		session, notice                      sql.NullString
	)
	if err := sc.Scan(&o.ID, &o.ScheduleTaskID, &o.DueAt, &o.DefinitionRevision, &spec, &o.Trigger, &o.ObservedAt,
		&o.State, &o.Reason, &o.Note, &confirmed, &session, &child, &notice, &admitted, &complete, &o.MissedCount,
		&o.CreatedAt, &o.UpdatedAt, &o.Revision); err != nil {
		return Occurrence{}, err
	}
	if err := json.Unmarshal([]byte(spec), &o.Spec); err != nil {
		return Occurrence{}, fmt.Errorf("run #%d spec: %w", o.ID, err)
	}
	o.ConfirmedAt, o.ChildTaskID, o.AdmittedAt, o.CompletedAt = confirmed.Int64, child.Int64, admitted.Int64, complete.Int64
	o.SessionID, o.NoticeID = session.String, notice.String
	return o, nil
}

func queryOccurrences(ctx context.Context, q querier, where string, args ...any) ([]Occurrence, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+occCols+" FROM task_occurrences WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Occurrence
	for rows.Next() {
		o, err := scanOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func getOccurrence(ctx context.Context, q querier, id int64) (Occurrence, error) {
	os, err := queryOccurrences(ctx, q, "id = ?", id)
	if err != nil {
		return Occurrence{}, err
	}
	if len(os) == 0 {
		return Occurrence{}, fmt.Errorf("run #%d: %w", id, ErrOccurrenceNotFound)
	}
	return os[0], nil
}

// saveOccurrence is a CAS write of an occurrence's mutable columns.
func (r *Repo) saveOccurrence(ctx context.Context, tx *sql.Tx, o Occurrence) error {
	res, err := tx.ExecContext(ctx, `UPDATE task_occurrences SET state=?, reason=?, note=?, confirmed_at=?, resolved_session_id=?,
		child_task_id=?, notice_id=?, admitted_at=?, completed_at=?, updated_at=?, revision = revision + 1
		WHERE id = ? AND revision = ?`,
		o.State, o.Reason, o.Note, nullInt(o.ConfirmedAt), nullStr(o.SessionID), nullInt(o.ChildTaskID), nullStr(o.NoticeID),
		nullInt(o.AdmittedAt), nullInt(o.CompletedAt), r.now().UnixMilli(), o.ID, o.Revision)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		cur, err := getOccurrence(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		return &OccurrenceConflictError{Current: cur}
	}
	return nil
}

// attachSchedules fills the template fields of the templates among recs.
func attachSchedules(ctx context.Context, q querier, recs []Record) error {
	idx := map[int64]int{}
	var ids []int64
	for i := range recs {
		if recs[i].template {
			idx[recs[i].ID] = i
			ids = append(ids, recs[i].ID)
		}
	}
	return inChunks(ids, func(chunk []int64) error {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		ph := placeholders(len(chunk))
		rows, err := q.QueryContext(ctx, "SELECT "+scheduleCols+" FROM task_schedules WHERE task_id IN ("+ph+")", args...)
		if err != nil {
			return err
		}
		enabled := map[int64]bool{}
		for rows.Next() {
			s, err := scanSchedule(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			rec := &recs[idx[s.TaskID]]
			w, tgt, del := s.Def.When, s.Def.Target, s.Def.Delivery
			rec.When, rec.TZ, rec.Target, rec.Delivery, rec.Next = &w, s.Def.TZ, &tgt, &del, s.NextDueAt
			enabled[s.TaskID] = s.Enabled
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		// latest: no later run of the same template exists. A recurring
		// template is failed only while its most recent run is; a later run
		// supersedes an older failure.
		rows, err = q.QueryContext(ctx, `SELECT o.schedule_task_id, o.state, o.id, o.reason, o.note,
			NOT EXISTS (SELECT 1 FROM task_occurrences l WHERE l.schedule_task_id = o.schedule_task_id AND l.due_at > o.due_at)
			FROM task_occurrences o
			WHERE o.state IN ('late','failed') AND o.schedule_task_id IN (`+ph+`) ORDER BY o.due_at, o.id`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var parent, id int64
			var state, reason, note string
			var latest bool
			if err := rows.Scan(&parent, &state, &id, &reason, &note, &latest); err != nil {
				return err
			}
			rec := &recs[idx[parent]]
			switch {
			case state == OccLate:
				rec.LateCount++
			case latest || rec.When == nil || rec.When.Kind == WhenOnce:
				rec.Failure = &RunFailure{OccurrenceID: id, Reason: reason, Note: note}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range chunk {
			rec := &recs[idx[id]]
			switch {
			case rec.Status == StatusDone:
			case !enabled[id]:
				rec.ScheduleState = "paused"
			case rec.LateCount > 0:
				rec.ScheduleState = OccLate
			case rec.Failure != nil:
				rec.ScheduleState = OccFailed
			default:
				rec.ScheduleState = "scheduled"
			}
		}
		return nil
	})
}

// createSchedule writes the definition of a template inserted in tx.
func (r *Repo) createSchedule(ctx context.Context, tx *sql.Tx, taskID int64, def ScheduleDef, next int64) error {
	return saveSchedule(ctx, tx, scheduleRow{TaskID: taskID, Def: def, Enabled: true, NextDueAt: next})
}

// checkTemplateInput validates an owner create of a template and returns its
// first cursor.
func (r *Repo) checkTemplateInput(in *CreateInput, rec *Record) (int64, error) {
	if in.Place != "" && in.Place != PlaceYou {
		return 0, invalid("a scheduled task is yours until it runs")
	}
	if in.RequesterSessionID != "" || in.AssigneeSessionID != "" || in.Deliver != "" {
		return 0, invalid("a scheduled task has no requester or assignee")
	}
	if rec.Status != StatusPending {
		return 0, invalid("a scheduled task starts pending")
	}
	now := r.now()
	loc, err := in.Schedule.normalize(now, false)
	if err != nil {
		return 0, err
	}
	rec.Place = PlaceYou
	return in.Schedule.firstDue(now, loc)
}

func specOf(rec Record, def ScheduleDef) OccurrenceSpec {
	sp := OccurrenceSpec{V: 1, Title: rec.Title, Description: rec.Description, WaitsFor: rec.WaitsFor,
		ProjectKey: rec.ProjectKey, ProjectCWD: rec.ProjectCWD, When: def.When, TZ: def.TZ, Target: def.Target,
		Delivery: def.Delivery, CreatedBySessionID: def.CreatedBySessionID}
	for _, s := range rec.Subtasks {
		sp.Subtasks = append(sp.Subtasks, SpecSubtask{Title: s.Title})
	}
	return sp
}

// slotPlan is what consuming a template's cursor produces.
type slotPlan struct {
	due, next int64 // next 0: nothing left
	missed    int
}

// planSlot computes the slot to consume from cursor. A cursor already past is
// caught up by coalescing: only the latest slot at or before now is kept and
// the earlier ones are counted. A cursor still in the future (Run now, Skip
// next) is consumed early and the rule continues after it.
func planSlot(def ScheduleDef, cursor int64, now time.Time) (slotPlan, error) {
	if def.When.Kind == WhenOnce {
		return slotPlan{due: cursor}, nil
	}
	loc, err := LoadZone(def.TZ)
	if err != nil {
		return slotPlan{}, err
	}
	p := slotPlan{due: cursor}
	for i := 0; ; i++ {
		n, err := NextSlot(*def.When.Rule, time.UnixMilli(p.due), loc)
		if err != nil {
			return slotPlan{}, err
		}
		if n.After(now) || cursor > now.UnixMilli() {
			p.next = n.UnixMilli()
			return p, nil
		}
		if i > 200000 {
			return slotPlan{}, fmt.Errorf("schedule catch-up does not end")
		}
		p.due, p.missed = n.UnixMilli(), p.missed+1
	}
}

// errSlotGone reports that a consumption decided on a stale read no longer
// applies: another writer consumed, edited, paused or deleted first.
var errSlotGone = errors.New("slot no longer due")

// consume is one slot consumption (T0): the occurrence INSERT, the cursor
// move and the template's summary commit together. expectRev 0 skips the
// template revision check (the timer); an owner control passes the revision
// and due time it saw, so a retry never consumes the following slot.
func (r *Repo) consume(ctx context.Context, taskID, expectRev, expectDue int64, trigger string) (Occurrence, error) {
	now := r.now()
	rd, err := r.reader()
	if err != nil {
		return Occurrence{}, err
	}
	if rd == nil {
		return Occurrence{}, notAvailable(taskID)
	}
	// The calendar is walked outside the writer lock; the transaction below
	// checks the definition it was computed from is still the current one.
	seen, ok, err := getSchedule(ctx, rd, taskID)
	if err != nil {
		return Occurrence{}, err
	}
	if !ok {
		if expectRev == 0 {
			return Occurrence{}, errSlotGone
		}
		return Occurrence{}, r.controlTarget(ctx, rd, taskID)
	}
	plan, err := planSlot(seen.Def, expectDue, now)
	if err != nil {
		return Occurrence{}, err
	}
	if r.hookAfterPlan != nil {
		r.hookAfterPlan()
	}
	var out Occurrence
	err = r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, taskID)
		if err != nil {
			if expectRev == 0 && errors.Is(err, ErrNotFound) {
				return false, errSlotGone
			}
			return false, err
		}
		s, ok, err := getSchedule(ctx, tx, taskID)
		if err != nil {
			return false, err
		}
		if expectRev != 0 {
			if cur.Revision != expectRev {
				return false, &ConflictError{Current: cur}
			}
			if ok && !s.Enabled {
				return false, invalid("the schedule is paused; resume it first")
			}
		}
		stale := !ok || !s.Enabled || s.NextDueAt != expectDue || s.whenJSON != seen.whenJSON || s.Def.TZ != seen.Def.TZ ||
			cur.Status == StatusDone || cur.ArchivedAt != 0
		if stale {
			if expectRev == 0 {
				return false, errSlotGone
			}
			return false, &ConflictError{Current: cur}
		}
		o := Occurrence{ScheduleTaskID: taskID, DueAt: plan.due, DefinitionRevision: cur.Revision,
			Spec: specOf(cur, s.Def), Trigger: trigger, ObservedAt: now.UnixMilli(), MissedCount: plan.missed}
		switch {
		case trigger == TriggerRunNow:
			o.State = OccReady
		case trigger == TriggerSkipNext:
			o.State, o.Reason = OccSkipped, ReasonOwnerSkip
		case now.UnixMilli()-plan.due < LateAfter.Milliseconds():
			o.State = OccReady
		case s.Def.Delivery.Late == LateRun:
			o.State = OccReady
		case s.Def.Delivery.Late == LateSkip:
			o.State, o.Reason = OccSkipped, ReasonLateSkip
		default:
			o.State = OccLate
		}
		if o.ID, err = r.insertOccurrence(ctx, tx, o); err != nil {
			return false, err
		}
		res, err := tx.ExecContext(ctx, "UPDATE task_schedules SET next_due_at = ? WHERE task_id = ? AND next_due_at = ?",
			nullInt(plan.next), taskID, expectDue)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return false, fmt.Errorf("schedule #%d cursor moved inside its transaction", taskID)
		}
		if err := r.touchTemplate(ctx, tx, cur, o); err != nil {
			return false, err
		}
		out, err = getOccurrence(ctx, tx, o.ID)
		return true, err
	})
	return out, err
}

// controlTarget is the error for an owner control on a task that is not an
// active template.
func (r *Repo) controlTarget(ctx context.Context, q querier, taskID int64) error {
	if _, err := getRecord(ctx, q, taskID); err != nil {
		return err
	}
	return invalid("task #%d is not scheduled", taskID)
}

func (r *Repo) insertOccurrence(ctx context.Context, tx *sql.Tx, o Occurrence) (int64, error) {
	now := r.now().UnixMilli()
	res, err := tx.ExecContext(ctx, `INSERT INTO task_occurrences(schedule_task_id, due_at, definition_revision, spec_json,
		trigger, observed_at, state, reason, note, confirmed_at, missed_count, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.ScheduleTaskID, o.DueAt, o.DefinitionRevision, mustJSON(o.Spec), o.Trigger, o.ObservedAt, o.State, o.Reason,
		o.Note, nullInt(o.ConfirmedAt), o.MissedCount, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// touchTemplate records on the template that one of its runs changed: its
// revision moves (so stale owner forms conflict), and a once template whose
// run was skipped or finished follows it to Done.
func (r *Repo) touchTemplate(ctx context.Context, tx *sql.Tx, tmpl Record, o Occurrence) error {
	once := o.Spec.When.Kind == WhenOnce
	switch {
	case once && o.State == OccSkipped && tmpl.Status != StatusDone:
		r.applyStatus(&tmpl, StatusDone)
		tmpl.CompletionNote = skippedNote
	case once && o.State == OccDone && tmpl.Status != StatusDone:
		r.applyStatus(&tmpl, StatusDone)
		tmpl.CompletionNote = o.Note
	case once && o.State != OccDone && o.State != OccSkipped && tmpl.Status == StatusDone:
		r.applyStatus(&tmpl, StatusPending)
	}
	return r.saveTask(ctx, tx, tmpl, tmpl.Revision)
}

// touchTemplateByID is touchTemplate for a template that may have been
// deleted: authorized runs outlive it.
func (r *Repo) touchTemplateByID(ctx context.Context, tx *sql.Tx, o Occurrence) error {
	tmpl, err := getRecord(ctx, tx, o.ScheduleTaskID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.touchTemplate(ctx, tx, tmpl, o)
}

// MaterializeDue consumes every enabled template whose cursor is at or before
// now (at most limit of them), and returns the occurrences it created. It is
// the scheduler's T0: safe to repeat, and safe against another process doing
// the same, because each consumption checks the cursor it read.
func (r *Repo) MaterializeDue(ctx context.Context, limit int) ([]Occurrence, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	rows, err := rd.QueryContext(ctx, `SELECT task_id, next_due_at FROM task_schedules
		WHERE enabled = 1 AND next_due_at IS NOT NULL AND next_due_at <= ? ORDER BY next_due_at, task_id LIMIT ?`,
		r.now().UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	type due struct{ id, at int64 }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.at); err != nil {
			_ = rows.Close()
			return nil, err
		}
		list = append(list, d)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	var out []Occurrence
	for _, d := range list {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		o, err := r.consume(ctx, d.id, 0, d.at, TriggerTimer)
		if errors.Is(err, errSlotGone) {
			continue
		}
		if err != nil {
			return out, err
		}
		out = append(out, o)
	}
	return out, nil
}

// NextDueAt is the earliest cursor of an enabled template, 0 when none.
func (r *Repo) NextDueAt(ctx context.Context) (int64, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return 0, err
	}
	var next sql.NullInt64
	err = rd.QueryRowContext(ctx, `SELECT MIN(next_due_at) FROM task_schedules WHERE enabled = 1 AND next_due_at IS NOT NULL`).Scan(&next)
	return next.Int64, err
}

// RunNow consumes the template's next slot now, as an authorized run: it is
// that slot early, not an extra run. dueAt and revision are what the owner
// saw, so a retried request cannot consume the following slot.
func (r *Repo) RunNow(ctx context.Context, taskID, revision, dueAt int64) (Occurrence, error) {
	if revision <= 0 || dueAt <= 0 {
		return Occurrence{}, invalid("revision and due time are required")
	}
	return r.consume(ctx, taskID, revision, dueAt, TriggerRunNow)
}

// SkipNext records the next slot of an active recurring template as skipped
// and moves on to the following one.
func (r *Repo) SkipNext(ctx context.Context, taskID, revision, dueAt int64) (Occurrence, error) {
	if revision <= 0 || dueAt <= 0 {
		return Occurrence{}, invalid("revision and due time are required")
	}
	if err := r.requireRepeat(ctx, taskID); err != nil {
		return Occurrence{}, err
	}
	return r.consume(ctx, taskID, revision, dueAt, TriggerSkipNext)
}

func (r *Repo) requireRepeat(ctx context.Context, taskID int64) error {
	rd, err := r.reader()
	if err != nil {
		return err
	}
	if rd == nil {
		return notAvailable(taskID)
	}
	s, ok, err := getSchedule(ctx, rd, taskID)
	if err != nil {
		return err
	}
	if !ok {
		return r.controlTarget(ctx, rd, taskID)
	}
	if s.Def.When.Kind != WhenRepeat {
		return invalid("only a recurring task can skip its next run")
	}
	return nil
}

// Pause stops a recurring template from consuming slots. Runs already
// authorized continue; late decisions stay answerable.
func (r *Repo) Pause(ctx context.Context, taskID, revision int64) (Record, error) {
	return r.setEnabled(ctx, taskID, revision, false)
}

// Resume re-enables a recurring template. The paused dates are not caught
// up: the next slot is strictly after now.
func (r *Repo) Resume(ctx context.Context, taskID, revision int64) (Record, error) {
	return r.setEnabled(ctx, taskID, revision, true)
}

func (r *Repo) setEnabled(ctx context.Context, taskID, revision int64, enabled bool) (Record, error) {
	if revision <= 0 {
		return Record{}, invalid("revision is required")
	}
	var out Record
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, taskID)
		if err != nil {
			return false, err
		}
		s, ok, err := getSchedule(ctx, tx, taskID)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, invalid("task #%d is not scheduled", taskID)
		}
		if s.Def.When.Kind != WhenRepeat {
			return false, invalid("only a recurring task can be paused")
		}
		if cur.Revision != revision {
			return false, &ConflictError{Current: cur}
		}
		if s.Enabled == enabled {
			out = cur
			return false, nil
		}
		s.Enabled = enabled
		if enabled {
			if s.NextDueAt, err = r.cursorAfterEdit(ctx, tx, taskID, s.Def); err != nil {
				return false, err
			}
		}
		if err := saveSchedule(ctx, tx, s); err != nil {
			return false, err
		}
		if err := r.saveTask(ctx, tx, cur, revision); err != nil {
			return false, err
		}
		out, err = getRecord(ctx, tx, taskID)
		return true, err
	})
	return out, err
}

// cursorAfterEdit is a recurring template's next slot after a rule change or
// a resume: strictly after now, and after every slot already consumed (a
// slot taken early by Run now is not offered again).
func (r *Repo) cursorAfterEdit(ctx context.Context, q querier, taskID int64, def ScheduleDef) (int64, error) {
	loc, err := LoadZone(def.TZ)
	if err != nil {
		return 0, err
	}
	var last sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT MAX(due_at) FROM task_occurrences WHERE schedule_task_id = ?", taskID).Scan(&last); err != nil {
		return 0, err
	}
	after := r.now()
	if last.Valid && time.UnixMilli(last.Int64).After(after) {
		after = time.UnixMilli(last.Int64)
	}
	t, err := NextSlot(*def.When.Rule, after, loc)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

// editSchedule applies an owner's new definition to a template, from its
// next unconsumed slot on: occurrences already taken keep their snapshot.
func (r *Repo) editSchedule(ctx context.Context, tx *sql.Tx, cur Record, def ScheduleDef) error {
	s, ok, err := getSchedule(ctx, tx, cur.ID)
	if err != nil {
		return err
	}
	if !ok {
		return invalid("task #%d is not scheduled", cur.ID)
	}
	if cur.Status == StatusDone {
		return invalid("a finished scheduled task cannot be rescheduled")
	}
	def.CreatedBySessionID = s.Def.CreatedBySessionID
	sameOnce := def.When.Kind == WhenOnce && s.Def.When.Kind == WhenOnce && def.When.At == s.Def.When.At
	if _, err := def.normalize(r.now(), sameOnce); err != nil {
		return err
	}
	var runs int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM task_occurrences WHERE schedule_task_id = ?", cur.ID).Scan(&runs); err != nil {
		return err
	}
	if def.When.Kind != s.Def.When.Kind && runs > 0 {
		return invalid("a task that already ran cannot change between once and recurring")
	}
	calendarChanged := mustJSON(def.When) != s.whenJSON || def.TZ != s.Def.TZ
	s.Def = def
	if calendarChanged {
		if def.When.Kind == WhenOnce {
			var taken int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM task_occurrences WHERE schedule_task_id = ? AND due_at = ?",
				cur.ID, def.When.At).Scan(&taken); err != nil {
				return err
			}
			if taken > 0 {
				return invalid("that time already ran")
			}
			s.NextDueAt = def.When.At
		} else {
			if s.NextDueAt, err = r.cursorAfterEdit(ctx, tx, cur.ID, def); err != nil {
				return err
			}
		}
	}
	return saveSchedule(ctx, tx, s)
}

// ConfirmOccurrence answers a late run: "run" authorizes it (once), "skip"
// settles it. revision is the occurrence's; of two answers one wins.
func (r *Repo) ConfirmOccurrence(ctx context.Context, id, revision int64, action string) (Occurrence, error) {
	if action != LateRun && action != LateSkip {
		return Occurrence{}, invalid("action must be %q or %q", LateRun, LateSkip)
	}
	var out Occurrence
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		o, err := getOccurrence(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if o.Revision != revision || o.State != OccLate {
			return false, &OccurrenceConflictError{Current: o}
		}
		if action == LateRun {
			o.State, o.ConfirmedAt = OccReady, r.now().UnixMilli()
		} else {
			o.State, o.Reason = OccSkipped, ReasonOwnerSkip
		}
		if err := r.saveOccurrence(ctx, tx, o); err != nil {
			return false, err
		}
		if err := r.touchTemplateByID(ctx, tx, o); err != nil {
			return false, err
		}
		out, err = getOccurrence(ctx, tx, id)
		return true, err
	})
	return out, err
}

// ReadyOccurrences lists the authorized runs still waiting for their child
// and assignment, oldest first.
func (r *Repo) ReadyOccurrences(ctx context.Context) ([]Occurrence, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	return queryOccurrences(ctx, rd, "state = 'ready' ORDER BY due_at, id")
}

// Occurrence returns one run.
func (r *Repo) Occurrence(ctx context.Context, id int64) (Occurrence, error) {
	rd, err := r.reader()
	if err != nil {
		return Occurrence{}, err
	}
	if rd == nil {
		return Occurrence{}, fmt.Errorf("run #%d: %w", id, ErrOccurrenceNotFound)
	}
	return getOccurrence(ctx, rd, id)
}

// OccurrenceForNotice returns the run whose current assignment is noticeID.
// The dispatcher uses it to apply the run's frozen delivery policy (busy
// wait). A superseded notice of a rerouted run maps to nothing.
func (r *Repo) OccurrenceForNotice(ctx context.Context, noticeID string) (Occurrence, bool, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return Occurrence{}, false, err
	}
	os, err := queryOccurrences(ctx, rd, "notice_id = ?", noticeID)
	if err != nil || len(os) == 0 {
		return Occurrence{}, false, err
	}
	return os[0], true, nil
}

// Runs returns a template's runs newest first, before beforeID when it is not 0.
func (r *Repo) Runs(ctx context.Context, taskID, beforeID int64, limit int) ([]Occurrence, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if beforeID > 0 {
		return queryOccurrences(ctx, rd, "schedule_task_id = ? AND id < ? ORDER BY id DESC LIMIT ?", taskID, beforeID, limit)
	}
	return queryOccurrences(ctx, rd, "schedule_task_id = ? ORDER BY id DESC LIMIT ?", taskID, limit)
}

// AssignOccurrence is the scheduler's T1: for a ready run, the child task on
// dest's checklist, its one assignment notice and the links commit together.
// An already assigned or finished run is returned as it is, so a retry after
// a lost response creates nothing. A snapshotted dependency that no longer
// exists fails the run (template_invalid) instead of being dropped.
func (r *Repo) AssignOccurrence(ctx context.Context, id int64, dest Destination) (Occurrence, error) {
	if dest.SessionID == "" {
		return Occurrence{}, invalid("a run needs a session")
	}
	var out Occurrence
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		o, err := getOccurrence(ctx, tx, id)
		if err != nil {
			return false, err
		}
		switch o.State {
		case OccAssigned, OccDone:
			out = o
			return false, nil
		case OccReady:
		default:
			return false, &OccurrenceConflictError{Current: o}
		}
		for _, w := range o.Spec.WaitsFor {
			var one int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM tasks WHERE id = ?", w).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				o.State, o.Reason, o.Note = OccFailed, ReasonTemplateInvalid, fmt.Sprintf("task #%d it waits for no longer exists", w)
				if err := r.saveOccurrence(ctx, tx, o); err != nil {
					return false, err
				}
				if err := r.touchTemplateByID(ctx, tx, o); err != nil {
					return false, err
				}
				out, err = getOccurrence(ctx, tx, id)
				return true, err
			}
			if err != nil {
				return false, err
			}
		}
		if err := r.deliverOccurrence(ctx, tx, &o, dest); err != nil {
			return false, err
		}
		out, err = getOccurrence(ctx, tx, id)
		return true, err
	})
	return out, err
}

// deliverOccurrence gives o a child on dest (reusing an existing one) and a
// new current assignment notice, and saves it as assigned.
func (r *Repo) deliverOccurrence(ctx context.Context, tx *sql.Tx, o *Occurrence, dest Destination) error {
	key := projectKeyFor(dest.ProjectKey, dest.ProjectCWD)
	if o.ChildTaskID == 0 {
		child := Record{Title: o.Spec.Title, Description: o.Spec.Description, Status: StatusPending, Place: PlaceAgent,
			ProjectKey: key, ProjectCWD: dest.ProjectCWD, AssigneeSessionID: dest.SessionID}
		subs := make([]SubtaskInput, 0, len(o.Spec.Subtasks))
		for _, s := range o.Spec.Subtasks {
			subs = append(subs, SubtaskInput{Title: s.Title})
		}
		id, err := r.insertTask(ctx, tx, child, subs, o.Spec.WaitsFor)
		if err != nil {
			return err
		}
		o.ChildTaskID = id
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET assignee_session_id = ?, project_key = ?, project_cwd = ?,
			updated_at = ?, revision = revision + 1 WHERE id = ? AND place = 'agent'`,
			dest.SessionID, nullStr(key), nullStr(dest.ProjectCWD), r.now().UnixMilli(), o.ChildTaskID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return invalid("run #%d lost its task #%d", o.ID, o.ChildTaskID)
		}
	}
	child, err := getRecord(ctx, tx, o.ChildTaskID)
	if err != nil {
		return err
	}
	noticeID, err := r.insertNotice(ctx, tx, child.ID, NoticeAssigned, dest.SessionID, o.Spec.Delivery.Saved,
		fmt.Sprintf("Scheduled task #%d", child.ID), scheduledText(*o, child))
	if err != nil {
		return err
	}
	o.State, o.Reason, o.Note = OccAssigned, "", ""
	o.NoticeID, o.SessionID = noticeID, dest.SessionID
	if err := r.saveOccurrence(ctx, tx, *o); err != nil {
		return err
	}
	return r.touchTemplateByID(ctx, tx, *o)
}

// scheduledText is the assignment an agent reads for a run. The owner's text
// is quoted as data, like every notice.
func scheduledText(o Occurrence, child Record) string {
	var b strings.Builder
	due := time.UnixMilli(o.DueAt).UTC()
	if loc, err := LoadZone(o.Spec.TZ); err == nil {
		due = due.In(loc)
	}
	fmt.Fprintf(&b, "Scheduled task #%d %q is due (%s, %s). It is on your checklist as task #%d.\n",
		o.ScheduleTaskID, oneLine(child.Title), due.Format("2006-01-02 15:04"), o.Spec.TZ, child.ID)
	writeSnapshot(&b, child)
	fmt.Fprintf(&b, "When you finish, mark it done with the tasks tool (action \"done\", id %d).", child.ID)
	return b.String()
}

// FailOccurrence marks a ready run as not sent: its target could not be
// resolved or provisioned. The owner decides what happens next.
func (r *Repo) FailOccurrence(ctx context.Context, id int64, reason, note string) (Occurrence, error) {
	if reason == "" {
		return Occurrence{}, invalid("a failure needs a reason")
	}
	var out Occurrence
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		o, err := getOccurrence(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if o.State != OccReady {
			return false, &OccurrenceConflictError{Current: o}
		}
		o.State, o.Reason, o.Note = OccFailed, reason, note
		if err := r.saveOccurrence(ctx, tx, o); err != nil {
			return false, err
		}
		if err := r.touchTemplateByID(ctx, tx, o); err != nil {
			return false, err
		}
		out, err = getOccurrence(ctx, tx, id)
		return true, err
	})
	return out, err
}

// RerouteOccurrence sends a failed, never admitted run to another existing
// session: the same child moves there and gets one new assignment notice.
// The old notice stays failed and is no longer the run's.
func (r *Repo) RerouteOccurrence(ctx context.Context, id, revision int64, dest Destination) (Occurrence, error) {
	if dest.SessionID == "" {
		return Occurrence{}, invalid("choose a session")
	}
	var out Occurrence
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		o, err := getOccurrence(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if o.Revision != revision || o.State != OccFailed || o.AdmittedAt != 0 {
			return false, &OccurrenceConflictError{Current: o}
		}
		if o.NoticeID != "" {
			var state string
			if err := tx.QueryRowContext(ctx, "SELECT state FROM task_notifications WHERE id = ?", o.NoticeID).Scan(&state); err != nil {
				return false, err
			}
			if state != NoticeFailed {
				return false, &OccurrenceConflictError{Current: o}
			}
		}
		if err := r.deliverOccurrence(ctx, tx, &o, dest); err != nil {
			return false, err
		}
		out, err = getOccurrence(ctx, tx, id)
		return true, err
	})
	return out, err
}

// settleChildStatus keeps a run in step with its child task inside the same
// transaction as the child's status write: completing the child completes
// the run (and a once template); reopening it reopens them, with no new
// assignment. A completion also withdraws an assignment notice that was
// never reserved, so finished work never wakes a session.
func (r *Repo) settleChildStatus(ctx context.Context, tx *sql.Tx, child Record) error {
	o, err := getOccurrence(ctx, tx, child.OccurrenceID)
	if err != nil {
		return err
	}
	if child.Status == StatusDone {
		if o.State == OccDone {
			if o.Note == child.CompletionNote {
				return nil
			}
			o.Note = child.CompletionNote
			return r.saveOccurrence(ctx, tx, o)
		}
		if o.State == OccLate {
			return nil
		}
		o.State, o.Reason, o.Note, o.CompletedAt = OccDone, "", child.CompletionNote, child.CompletedAt
		if o.NoticeID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE task_notifications SET state = 'failed', reason = ?, updated_at = ?
				WHERE id = ? AND state IN ('pending','held')`, ReasonChildDone, r.now().UnixMilli(), o.NoticeID); err != nil {
				return err
			}
		}
	} else {
		if o.State != OccDone {
			return nil
		}
		o.State, o.Note, o.CompletedAt = OccAssigned, "", 0
	}
	if err := r.saveOccurrence(ctx, tx, o); err != nil {
		return err
	}
	return r.touchTemplateByID(ctx, tx, o)
}

// assignmentWithdrawn reports whether a run's current assignment was
// withdrawn by its completion before any reservation.
func assignmentWithdrawn(ctx context.Context, q querier, occurrenceID int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_occurrences o JOIN task_notifications n ON n.id = o.notice_id
		WHERE o.id = ? AND n.state = 'failed' AND n.reason = ?`, occurrenceID, ReasonChildDone).Scan(&n)
	return n > 0, err
}

// settleRemovedChild records that a run's child task is being deleted: the
// run is skipped (removed; after delivery its outcome is unknown) and its
// assignment can no longer be delivered. A finished run keeps its history.
// It reports whether the session may have received the assignment: a
// deletion is only worth telling a session that could have seen the task.
func (r *Repo) settleRemovedChild(ctx context.Context, tx *sql.Tx, occurrenceID int64) (bool, error) {
	o, err := getOccurrence(ctx, tx, occurrenceID)
	if err != nil {
		return false, err
	}
	if o.State == OccDone || o.State == OccSkipped {
		return true, nil
	}
	delivered := o.AdmittedAt != 0
	if o.NoticeID != "" {
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM task_notifications WHERE id = ?", o.NoticeID).Scan(&state); err != nil {
			return false, err
		}
		delivered = delivered || state == NoticeSent || state == NoticeDelivered
		if _, err := tx.ExecContext(ctx, `UPDATE task_notifications SET state = 'failed', reason = ?, updated_at = ?
			WHERE id = ? AND state IN ('pending','held','sent')`, ReasonChildRemoved, r.now().UnixMilli(), o.NoticeID); err != nil {
			return false, err
		}
	}
	o.State, o.Reason = OccSkipped, ReasonChildRemoved
	o.Note = "Removed before delivery"
	if delivered {
		o.Note = "Removed after delivery; outcome unknown"
	}
	if err := r.saveOccurrence(ctx, tx, o); err != nil {
		return false, err
	}
	return delivered, r.touchTemplateByID(ctx, tx, o)
}

// settleDeletedTemplate settles the late decisions of a template being
// deleted: nobody can answer them any more. Authorized runs continue.
func (r *Repo) settleDeletedTemplate(ctx context.Context, tx *sql.Tx, taskID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE task_occurrences SET state = 'skipped', reason = ?, updated_at = ?, revision = revision + 1
		WHERE schedule_task_id = ? AND state = 'late'`, ReasonScheduleDeleted, r.now().UnixMilli(), taskID)
	return err
}

// settleNotice keeps a run in step with a transition of its current notice,
// in the notice's transaction. Delivered means admitted. A failure before
// any reservation is "not sent"; one after a reservation or admission is a
// removal whose outcome is unknown, never "not sent".
func (r *Repo) settleNotice(ctx context.Context, tx *sql.Tx, noticeID, prevState string, c NoticeChange) error {
	os, err := queryOccurrences(ctx, tx, "notice_id = ?", noticeID)
	if err != nil || len(os) == 0 {
		return err
	}
	o := os[0]
	now := r.now().UnixMilli()
	switch {
	case c.State == NoticeDelivered || c.Admitted:
		if o.AdmittedAt != 0 {
			return nil
		}
		o.AdmittedAt = now
	case c.State == NoticeFailed && o.State == OccAssigned:
		deleted := c.Reason == ReasonSessionDeleted
		switch {
		case o.AdmittedAt != 0 || prevState == NoticeDelivered:
			o.State, o.Reason, o.Note = OccSkipped, c.Reason, "Failed after delivery; outcome unknown"
			if deleted {
				o.Reason, o.Note = ReasonDeletedAfterDeliver, "Removed after delivery; outcome unknown"
			}
		case prevState == NoticeSent:
			o.State, o.Reason, o.Note = OccSkipped, c.Reason, "Failed during delivery; outcome unknown"
			if deleted {
				o.Reason, o.Note = ReasonDeletedDuringDeliver, "Removed during delivery; outcome unknown"
			}
		default:
			o.State, o.Reason = OccFailed, c.Reason
		}
	default:
		return nil
	}
	if err := r.saveOccurrence(ctx, tx, o); err != nil {
		return err
	}
	return r.touchTemplateByID(ctx, tx, o)
}

// RegateOnRestart re-applies the late policy, at startup recovery, to runs
// that were authorized on time but never reached their session and whose due
// time is now ten minutes or more ago. ask returns them to late (their
// undelivered child and notice are withdrawn, so Run goes through T1 again),
// skip settles them, run lets them continue. Runs the owner confirmed or ran
// by hand, and runs already reserved or admitted, are never re-gated.
func (r *Repo) RegateOnRestart(ctx context.Context) (int, error) {
	var n int
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		n = 0
		now := r.now().UnixMilli()
		cands, err := queryOccurrences(ctx, tx, `state IN ('ready','assigned') AND admitted_at IS NULL AND confirmed_at IS NULL
			AND trigger <> 'run_now' AND due_at <= ? ORDER BY id`, now-LateAfter.Milliseconds())
		if err != nil {
			return false, err
		}
		for _, o := range cands {
			if o.Spec.Delivery.Late == LateRun {
				continue
			}
			if o.NoticeID != "" {
				var state string
				if err := tx.QueryRowContext(ctx, "SELECT state FROM task_notifications WHERE id = ?", o.NoticeID).Scan(&state); err != nil {
					return false, err
				}
				if state != NoticePending && state != NoticeHeld {
					continue
				}
				if _, err := tx.ExecContext(ctx, `UPDATE task_notifications SET state = 'failed', reason = ?, updated_at = ?
					WHERE id = ?`, ReasonRegated, now, o.NoticeID); err != nil {
					return false, err
				}
			}
			if o.ChildTaskID != 0 {
				if _, err := tx.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", o.ChildTaskID); err != nil {
					return false, err
				}
			}
			o.ChildTaskID, o.NoticeID, o.SessionID = 0, "", ""
			if o.Spec.Delivery.Late == LateSkip {
				o.State, o.Reason = OccSkipped, ReasonLateSkip
			} else {
				o.State, o.Reason = OccLate, ReasonRegated
			}
			if err := r.saveOccurrence(ctx, tx, o); err != nil {
				return false, err
			}
			if err := r.touchTemplateByID(ctx, tx, o); err != nil {
				return false, err
			}
			n++
		}
		return n > 0, nil
	})
	return n, err
}

// SettleSessionDeleted settles, before a session's file is removed, every
// unfinished run bound to it, plus markerOccurrenceID (a run whose new
// session was created but not yet bound; 0 for none). Undelivered work fails
// (session_deleted) and can be sent elsewhere; work reserved or admitted is
// skipped with an unknown outcome. Finished history is unchanged.
func (r *Repo) SettleSessionDeleted(ctx context.Context, sessionID string, markerOccurrenceID int64) (int, error) {
	var n int
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		n = 0
		now := r.now().UnixMilli()
		os, err := queryOccurrences(ctx, tx, `(resolved_session_id = ? AND state IN ('ready','assigned'))
			OR (id = ? AND state = 'ready') ORDER BY id`, sessionID, markerOccurrenceID)
		if err != nil {
			return false, err
		}
		for _, o := range os {
			state := ""
			if o.NoticeID != "" {
				if err := tx.QueryRowContext(ctx, "SELECT state FROM task_notifications WHERE id = ?", o.NoticeID).Scan(&state); err != nil {
					return false, err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE task_notifications SET state = 'failed', reason = ?, updated_at = ?
					WHERE id = ? AND state IN ('pending','held','sent')`, ReasonSessionDeleted, now, o.NoticeID); err != nil {
					return false, err
				}
			}
			switch {
			case o.AdmittedAt != 0 || state == NoticeDelivered:
				o.State, o.Reason, o.Note = OccSkipped, ReasonDeletedAfterDeliver, "Removed after delivery; outcome unknown"
			case state == NoticeSent:
				o.State, o.Reason, o.Note = OccSkipped, ReasonDeletedDuringDeliver, "Removed during delivery; outcome unknown"
			default:
				o.State, o.Reason = OccFailed, ReasonSessionDeleted
			}
			if err := r.saveOccurrence(ctx, tx, o); err != nil {
				return false, err
			}
			if err := r.touchTemplateByID(ctx, tx, o); err != nil {
				return false, err
			}
			n++
		}
		return n > 0, nil
	})
	return n, err
}

// LegacySchedule is one pending record of the old schedules.json.
type LegacySchedule struct {
	ID        string
	SessionID string
	Text      string
	DueAt     int64 // Unix ms
	TZ        string
	CreatedAt int64 // Unix ms, 0 for now
}

// ImportLegacySchedules turns every pending legacy record into a once
// template for its session, all in one transaction with the installation's
// import flag. A record already due becomes a late run straight away,
// however little it is overdue, so a prompt the old scheduler may have been
// sending when it stopped is never re-sent without the owner's OK. It
// returns false when the import had already happened.
func (r *Repo) ImportLegacySchedules(ctx context.Context, items []LegacySchedule) (bool, error) {
	var imported bool
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		imported = false
		var done int
		if err := tx.QueryRowContext(ctx, "SELECT legacy_schedules_imported FROM tasks_meta WHERE id = 1").Scan(&done); err != nil {
			return false, err
		}
		if done == 1 {
			return false, nil
		}
		now := r.now()
		for _, it := range items {
			if it.SessionID == "" || strings.TrimSpace(it.Text) == "" || it.DueAt <= 0 || it.DueAt > maxInstant {
				return false, invalid("legacy schedule %q is incomplete", it.ID)
			}
			tz := it.TZ
			if _, err := LoadZone(tz); err != nil {
				tz = "UTC"
			}
			def := ScheduleDef{When: When{Kind: WhenOnce, At: it.DueAt}, TZ: tz, Target: Target{Kind: TargetSession, ID: it.SessionID}}
			if _, err := def.normalize(now, true); err != nil {
				return false, err
			}
			rec := Record{Title: legacyTitle(it.Text), Description: it.Text, Status: StatusPending, Place: PlaceYou}
			id, err := r.insertTask(ctx, tx, rec, nil, nil)
			if err != nil {
				return false, err
			}
			if it.CreatedAt > 0 {
				if _, err := tx.ExecContext(ctx, "UPDATE tasks SET created_at = ? WHERE id = ?", it.CreatedAt, id); err != nil {
					return false, err
				}
			}
			next := it.DueAt
			if it.DueAt <= now.UnixMilli() {
				next = 0
			}
			if err := r.createSchedule(ctx, tx, id, def, next); err != nil {
				return false, err
			}
			if next == 0 {
				cur, err := getRecord(ctx, tx, id)
				if err != nil {
					return false, err
				}
				o := Occurrence{ScheduleTaskID: id, DueAt: it.DueAt, DefinitionRevision: cur.Revision, Spec: specOf(cur, def),
					Trigger: TriggerLegacy, ObservedAt: now.UnixMilli(), State: OccLate, Note: "Imported from /schedule " + it.ID}
				if _, err := r.insertOccurrence(ctx, tx, o); err != nil {
					return false, err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tasks_meta SET legacy_schedules_imported = 1 WHERE id = 1"); err != nil {
			return false, err
		}
		imported = true
		return true, nil
	})
	return imported, err
}

// LegacySchedulesImported reports whether the one-time import has happened.
func (r *Repo) LegacySchedulesImported(ctx context.Context) (bool, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return false, err
	}
	var done int
	err = rd.QueryRowContext(ctx, "SELECT legacy_schedules_imported FROM tasks_meta WHERE id = 1").Scan(&done)
	return done == 1, err
}

func legacyTitle(text string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	if r := []rune(line); len(r) > 90 {
		line = strings.TrimSpace(string(r[:89])) + "…"
	}
	return line
}
