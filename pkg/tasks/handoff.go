package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// OwnerRef is a project owner as a task between owners records it: its
// identity, its conversation and the project a task sent to it belongs to.
type OwnerRef struct {
	ID         string
	Name       string
	SessionID  string
	ProjectKey string
	ProjectCWD string
}

// OwnerDirectory resolves owners for the tasks tool. It is implemented
// outside this package, over owner.json, and read on every call: a
// permission revoked in Edit owner applies to the very next send.
type OwnerDirectory interface {
	// OwnerOf returns the owner whose conversation is sessionID.
	OwnerOf(sessionID string) (OwnerRef, bool, error)
	// Find resolves an owner by ID or by name.
	Find(nameOrID string) (OwnerRef, bool, error)
	// CanSend reports whether fromID may send tasks to toID without asking
	// the user. One-way.
	CanSend(fromID, toID string) (bool, error)
}

// Handoff states. awaiting_permission is a request in You (the user decides);
// sent is a task on the recipient's checklist; declined is closed unsent.
const (
	HandoffAwaiting = "awaiting_permission"
	HandoffSent     = "sent"
	HandoffDeclined = "declined"
)

// Outcomes of a closed task between owners.
const (
	OutcomeDone   = "done"
	OutcomeCantDo = "cant_do"
)

// The user's answers to a request awaiting permission. Always allow is send
// plus a permission the caller stores in owner.json first.
const (
	HandoffActionSend    = "send"
	HandoffActionDecline = "decline"
)

// DeclinedNote is the completion note of a declined request.
const DeclinedNote = "Declined by the owner"

// Notice kinds of tasks between owners and of threads.
const (
	NoticeHandoff         NoticeKind = "handoff"
	NoticeComment         NoticeKind = "comment"
	NoticeHandoffClosed   NoticeKind = "handoff_closed"
	NoticeHandoffDeclined NoticeKind = "handoff_declined"
)

// Comment authors.
const (
	AuthorHuman   = "human"
	AuthorSession = "session"
)

// Handoff is what makes a task one owner's task for another. Names are
// copied when it is sent, so the task still says who asked after a rename or
// a deleted owner. Session IDs stay inside the package.
type Handoff struct {
	FromOwnerID   string `json:"from_owner_id"`
	FromOwnerName string `json:"from_owner_name"`
	ToOwnerID     string `json:"to_owner_id"`
	ToOwnerName   string `json:"to_owner_name"`
	State         string `json:"state"`
	Outcome       string `json:"outcome,omitempty"`

	fromSession, toSession string
}

// Comment is one entry of a task's thread.
type Comment struct {
	ID              int64  `json:"id"`
	TaskID          int64  `json:"task_id"`
	AuthorKind      string `json:"author_kind"`
	AuthorSessionID string `json:"author_session_id,omitempty"`
	AuthorName      string `json:"author_name,omitempty"`
	Body            string `json:"body"`
	CreatedAt       int64  `json:"created_at"`
}

func attachHandoffs(ctx context.Context, q querier, recs []Record) error {
	idx := map[int64]int{}
	var ids []int64
	for i := range recs {
		if recs[i].handoff {
			idx[recs[i].ID] = i
			ids = append(ids, recs[i].ID)
		}
	}
	return inChunks(ids, func(chunk []int64) error {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT task_id, from_owner_id, from_owner_name, from_session_id,
			to_owner_id, to_owner_name, to_session_id, state, outcome
			FROM task_handoffs WHERE task_id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			var h Handoff
			var outcome sql.NullString
			if err := rows.Scan(&id, &h.FromOwnerID, &h.FromOwnerName, &h.fromSession,
				&h.ToOwnerID, &h.ToOwnerName, &h.toSession, &h.State, &outcome); err != nil {
				return err
			}
			rec := &recs[idx[id]]
			// An outcome belongs to a closed task: a binary that reopens it does
			// not know to clear it, and it must not come back on the next Done.
			if rec.Status == StatusDone {
				h.Outcome = outcome.String
			}
			// The sender is who the handoff says. tasks.requester_session_id
			// is a shared column that a binary without handoffs can clear (a
			// move), and the sender must not be lost with it. Not in the
			// backlog, which has no requester: one moved there by such a
			// binary has left the sender's hands.
			if rec.Place != PlaceBacklog {
				rec.RequesterSessionID = h.fromSession
			}
			rec.Handoff = &h
		}
		return rows.Err()
	})
}

func (r *Repo) setHandoff(ctx context.Context, tx *sql.Tx, id int64, state, outcome string) error {
	_, err := tx.ExecContext(ctx, `UPDATE task_handoffs SET state = ?, outcome = ?, updated_at = ? WHERE task_id = ?`,
		state, nullStr(outcome), r.now().UnixMilli(), id)
	return err
}

// SendHandoff files a task from one owner to another. allowed is the
// sender's standing permission: with it the task lands on the recipient's
// checklist and the recipient is woken; without it, it is a request in You
// and nothing reaches the recipient until the user answers (ResolveHandoff).
func (r *Repo) SendHandoff(ctx context.Context, from, to OwnerRef, allowed bool, in AgentInput) (Record, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Record{}, invalid("title is required")
	}
	if from.ID == "" || from.SessionID == "" {
		return Record{}, invalid("only a project owner can send a task to another owner")
	}
	if to.ID == from.ID || to.SessionID == from.SessionID {
		return Record{}, invalid("an owner cannot send a task to itself")
	}
	if to.ID == "" || to.SessionID == "" {
		return Record{}, invalid("owner %q has no conversation to receive the task", to.Name)
	}
	rec := Record{Title: title, Description: in.Description, Status: StatusPending, Place: PlaceYou,
		ProjectKey: to.ProjectKey, ProjectCWD: to.ProjectCWD, RequesterSessionID: from.SessionID}
	state := HandoffAwaiting
	if allowed {
		rec.Place, rec.AssigneeSessionID, state = PlaceAgent, to.SessionID, HandoffSent
	}
	var out Record
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		id, err := r.insertTask(ctx, tx, rec, in.Subtasks, nil)
		if err != nil {
			return false, err
		}
		now := r.now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_handoffs(task_id, from_owner_id, from_owner_name, from_session_id,
			to_owner_id, to_owner_name, to_session_id, state, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, from.ID, from.Name, from.SessionID, to.ID, to.Name, to.SessionID, state, now, now); err != nil {
			return false, err
		}
		if out, err = getRecord(ctx, tx, id); err != nil {
			return false, err
		}
		if allowed {
			return true, r.handoffNotice(ctx, tx, NoticeHandoff, to.SessionID, out, "", "")
		}
		return true, nil
	})
	return out, err
}

// ResolveHandoff is the user's answer to a request awaiting permission: send
// it to the recipient, or decline it (closed as Can't do; the sender hears).
func (r *Repo) ResolveHandoff(ctx context.Context, id, revision int64, action string) (Record, error) {
	if revision <= 0 {
		return Record{}, invalid("revision is required")
	}
	if action != HandoffActionSend && action != HandoffActionDecline {
		return Record{}, invalid("action must be %q or %q", HandoffActionSend, HandoffActionDecline)
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
		h := cur.Handoff
		if h == nil {
			return false, invalid("task #%d is not a task between owners", id)
		}
		if h.State != HandoffAwaiting || cur.Place != PlaceYou {
			return false, invalid("task #%d is not waiting for permission", id)
		}
		var kind NoticeKind
		var to string
		if action == HandoffActionSend {
			cur.Place, cur.AssigneeSessionID = PlaceAgent, h.toSession
			if err := r.saveTask(ctx, tx, cur, revision); err != nil {
				return false, err
			}
			if err := r.setHandoff(ctx, tx, id, HandoffSent, ""); err != nil {
				return false, err
			}
			kind, to = NoticeHandoff, h.toSession
		} else {
			r.applyStatus(&cur, StatusDone)
			cur.CompletionNote = DeclinedNote
			if err := r.saveTask(ctx, tx, cur, revision); err != nil {
				return false, err
			}
			if err := r.setHandoff(ctx, tx, id, HandoffDeclined, OutcomeCantDo); err != nil {
				return false, err
			}
			kind, to = NoticeHandoffDeclined, cur.RequesterSessionID
		}
		if out, err = getRecord(ctx, tx, id); err != nil {
			return false, err
		}
		return true, r.handoffNotice(ctx, tx, kind, to, out, "", "")
	})
	return out, err
}

// threadParties are the sessions a thread event concerns: whoever asked
// (the sender of a task between owners, the agent of a request) and whoever
// has it on their checklist. Taken from the task as it is now.
func threadParties(t *Record) []string {
	var out []string
	if t.RequesterSessionID != "" {
		out = append(out, t.RequesterSessionID)
	}
	if t.Place == PlaceAgent && t.AssigneeSessionID != "" && t.AssigneeSessionID != t.RequesterSessionID {
		out = append(out, t.AssigneeSessionID)
	}
	return out
}

// hasThread says whether a task takes comments: tasks between owners and an
// agent's requests to the user. Checklists, assignments and notes do not.
func hasThread(t *Record) bool {
	return t.Handoff != nil || (t.Place == PlaceYou && t.RequesterSessionID != "" && !t.template)
}

// AgentComment writes in the thread of a task the agent reads and takes part
// in. The other party hears it; the author never does.
func (r *Repo) AgentComment(ctx context.Context, a Actor, id int64, body string) (Comment, error) {
	return r.comment(ctx, id, body, func(cur *Record) (Comment, error) {
		if !a.reads(cur) {
			return Comment{}, notAvailable(id)
		}
		if !hasThread(cur) {
			return Comment{}, invalid("comments are for tasks between owners and for your requests to the user")
		}
		c := Comment{AuthorKind: AuthorSession, AuthorSessionID: a.SessionID}
		if h := cur.Handoff; h != nil {
			c.AuthorName = h.ToOwnerName
			if a.SessionID == cur.RequesterSessionID {
				c.AuthorName = h.FromOwnerName
			}
		}
		return c, nil
	})
}

// HumanComment is the user writing in a thread: every party hears it.
func (r *Repo) HumanComment(ctx context.Context, id int64, body string) (Comment, error) {
	return r.comment(ctx, id, body, func(cur *Record) (Comment, error) {
		if !hasThread(cur) {
			return Comment{}, invalid("comments are for tasks between owners and for requests from agents")
		}
		return Comment{AuthorKind: AuthorHuman}, nil
	})
}

func (r *Repo) comment(ctx context.Context, id int64, body string, author func(*Record) (Comment, error)) (Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Comment{}, invalid("a comment needs a body")
	}
	var out Comment
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		c, err := author(&cur)
		if err != nil {
			return false, err
		}
		c.TaskID, c.Body, c.CreatedAt = id, body, r.now().UnixMilli()
		res, err := tx.ExecContext(ctx, `INSERT INTO task_comments(task_id, author_kind, author_session_id, author_name, body, created_at)
			VALUES (?,?,?,?,?,?)`, id, c.AuthorKind, nullStr(c.AuthorSessionID), c.AuthorName, c.Body, c.CreatedAt)
		if err != nil {
			return false, err
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return false, err
		}
		for _, to := range threadParties(&cur) {
			if to == c.AuthorSessionID {
				continue
			}
			if err := r.handoffNotice(ctx, tx, NoticeComment, to, cur, authorFor(c), body); err != nil {
				return false, err
			}
		}
		out = c
		return true, nil
	})
	return out, err
}

// authorFor is how a notice names a comment's author.
func authorFor(c Comment) string {
	if c.AuthorKind == AuthorHuman {
		return "The user"
	}
	if c.AuthorName != "" {
		return c.AuthorName
	}
	return "The session that asked"
}

// Comments is a task's thread, oldest first. The owner's view.
func (r *Repo) Comments(ctx context.Context, id int64) ([]Comment, error) {
	rd, err := r.reader()
	if err != nil {
		return nil, err
	}
	if rd == nil {
		return nil, notAvailable(id)
	}
	recs, err := loadBare(ctx, rd, "id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, notAvailable(id)
	}
	return loadComments(ctx, rd, id)
}

func loadComments(ctx context.Context, q querier, id int64) ([]Comment, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, task_id, author_kind, author_session_id, author_name, body, created_at
		FROM task_comments WHERE task_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		var sess sql.NullString
		if err := rows.Scan(&c.ID, &c.TaskID, &c.AuthorKind, &sess, &c.AuthorName, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.AuthorSessionID = sess.String
		out = append(out, c)
	}
	return out, rows.Err()
}

// agentThread is the thread as the agent reads it: authors by role or name,
// never by session.
func agentThread(ctx context.Context, q querier, a Actor, id int64) ([]ThreadEntry, error) {
	cs, err := loadComments(ctx, q, id)
	if err != nil {
		return nil, err
	}
	var out []ThreadEntry
	for _, c := range cs {
		author := c.AuthorName
		switch {
		case c.AuthorKind == AuthorHuman:
			author = "the user"
		case c.AuthorSessionID == a.SessionID:
			author = "you"
		case author == "":
			author = "another session"
		}
		out = append(out, ThreadEntry{Author: author, Body: c.Body, At: c.CreatedAt})
	}
	return out, nil
}

// AgentClose completes a task on the agent's checklist. A task another owner
// sent needs an outcome (done by default) and a note, which the sender reads;
// an outcome on anything else is refused. The sender of a task can read it
// but never close it.
func (r *Repo) AgentClose(ctx context.Context, a Actor, id int64, outcome, note string) (AgentTask, error) {
	note = strings.TrimSpace(note)
	var out AgentTask
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if !a.reads(&cur) {
			return false, notAvailable(id)
		}
		if cur.Place != PlaceAgent || cur.AssigneeSessionID != a.SessionID {
			return false, ErrForbidden
		}
		if cur.Handoff != nil {
			if outcome == "" {
				outcome = OutcomeDone
			}
			if outcome != OutcomeDone && outcome != OutcomeCantDo {
				return false, invalid("outcome must be %q or %q", OutcomeDone, OutcomeCantDo)
			}
			if note == "" {
				return false, invalid("task #%d was sent by %s: close it with a note (the result for done, the reason for cant_do)",
					id, cur.Handoff.FromOwnerName)
			}
		} else if outcome != "" {
			return false, invalid("outcome is only for tasks sent by another owner")
		}
		changed := cur.Status != StatusDone
		if changed {
			r.applyStatus(&cur, StatusDone)
			if note != "" {
				cur.CompletionNote = note
			}
			if err := r.saveTask(ctx, tx, cur, cur.Revision); err != nil {
				return false, err
			}
			if cur.Handoff != nil {
				if err := r.setHandoff(ctx, tx, id, cur.Handoff.State, outcome); err != nil {
					return false, err
				}
				rec, err := getRecord(ctx, tx, id)
				if err != nil {
					return false, err
				}
				if to := cur.RequesterSessionID; to != "" && to != a.SessionID {
					if err := r.handoffNotice(ctx, tx, NoticeHandoffClosed, to, rec, cur.Handoff.ToOwnerName, ""); err != nil {
						return false, err
					}
				}
			}
		}
		out, err = r.agentSnapshot(ctx, tx, a, id)
		return changed, err
	})
	return out, err
}

// userClosesHandoff applies a user's edit to a task between owners (from
// Tasks or from a session's /tasks): the outcome, its note rule, and the
// sender's notice. before and after are the task around the save.
func (r *Repo) userClosesHandoff(ctx context.Context, tx *sql.Tx, before, after Record, outcome *string) error {
	h := before.Handoff
	if h == nil {
		if outcome != nil {
			return invalid("outcome is only for tasks between owners")
		}
		return nil
	}
	statusChanged := after.Status != before.Status
	switch {
	case after.Place != before.Place || after.AssigneeSessionID != before.AssigneeSessionID:
		// Moving it would drop the sender (the requester) or bypass the
		// permission answer: a task between owners stays where it was sent.
		return invalid("task #%d is a task between owners; it cannot be moved or reassigned", before.ID)
	case h.State == HandoffAwaiting && statusChanged:
		return invalid("task #%d waits for your permission: send it, always allow, or decline it", before.ID)
	case h.State == HandoffDeclined && statusChanged:
		return invalid("task #%d was declined; it stays closed", before.ID)
	}
	if after.Status != StatusDone {
		if outcome != nil {
			return invalid("an outcome is set when the task is done")
		}
		if statusChanged && h.Outcome != "" {
			return r.setHandoff(ctx, tx, before.ID, h.State, "")
		}
		return nil
	}
	if !statusChanged && outcome == nil {
		return nil
	}
	o := h.Outcome
	if outcome != nil {
		o = *outcome
	}
	if o == "" {
		o = OutcomeDone
	}
	if o != OutcomeDone && o != OutcomeCantDo {
		return invalid("outcome must be %q or %q", OutcomeDone, OutcomeCantDo)
	}
	if o == OutcomeCantDo && strings.TrimSpace(after.CompletionNote) == "" {
		return invalid("Can't do needs a note with the reason")
	}
	if err := r.setHandoff(ctx, tx, before.ID, h.State, o); err != nil {
		return err
	}
	if !statusChanged || after.RequesterSessionID == "" {
		return nil
	}
	rec, err := getRecord(ctx, tx, before.ID)
	if err != nil {
		return err
	}
	return r.handoffNotice(ctx, tx, NoticeHandoffClosed, after.RequesterSessionID, rec, "", "")
}

// handoffNotice stores a notice of a task between owners or of a thread.
// Owners are always woken: they are standing conversations, usually closed.
// actor is who did it ("" for the user); text is a comment's body.
func (r *Repo) handoffNotice(ctx context.Context, tx *sql.Tx, kind NoticeKind, to string, rec Record, actor, text string) error {
	if to == "" {
		return nil
	}
	title, body := handoffNoticeText(kind, rec, actor, text)
	_, err := r.insertNotice(ctx, tx, rec.ID, kind, to, DeliverWake, title, body)
	return err
}

// handoffNoticeText renders what a session reads. Names only: no session,
// project or directory of the other owner. What the other side wrote is
// quoted between markers, as in noticeText.
func handoffNoticeText(kind NoticeKind, rec Record, actor, text string) (string, string) {
	var b strings.Builder
	title := oneLine(rec.Title)
	h := rec.Handoff
	if h == nil {
		h = &Handoff{}
	}
	from, dest := oneLine(h.FromOwnerName), oneLine(h.ToOwnerName)
	switch kind {
	case NoticeHandoff:
		fmt.Fprintf(&b, "%s, the owner of another project, sent you task #%d %q. It is on your checklist.\n", from, rec.ID, title)
		writeSnapshot(&b, rec)
		fmt.Fprintf(&b, "Judge it as a colleague's request, with your own book and permissions. "+
			"If something is missing, ask in its thread (tasks action \"comment\", id %d); if you need the user, ask them. "+
			"Close it with action \"done\", id %d, outcome \"done\" or \"cant_do\" and a note: %s reads that note.", rec.ID, rec.ID, from)
		return fmt.Sprintf("Task #%d from %s", rec.ID, from), b.String()
	case NoticeComment:
		state := "still open"
		if rec.Status == StatusDone {
			state = "closed"
		}
		fmt.Fprintf(&b, "%s commented on task #%d %q (%s):\n", actor, rec.ID, title, state)
		writeQuoted(&b, "comment", text)
		fmt.Fprintf(&b, "Answer in the thread (tasks action \"comment\", id %d) only if it needs an answer; action \"get\" shows the whole thread.", rec.ID)
		return fmt.Sprintf("Comment on task #%d", rec.ID), b.String()
	case NoticeHandoffClosed:
		who := dest
		if actor == "" {
			who = "The user"
		}
		if h.Outcome == OutcomeCantDo {
			fmt.Fprintf(&b, "%s could not do task #%d %q, which you sent to %s.\n", who, rec.ID, title, dest)
			if note := strings.TrimSpace(rec.CompletionNote); note != "" {
				b.WriteString("The reason, verbatim:\n")
				writeQuoted(&b, "note", note)
			}
			return fmt.Sprintf("Task #%d: can't do", rec.ID), b.String()
		}
		fmt.Fprintf(&b, "%s finished task #%d %q, which you sent to %s.\n", who, rec.ID, title, dest)
		if note := strings.TrimSpace(rec.CompletionNote); note != "" {
			b.WriteString("The result, verbatim:\n")
			writeQuoted(&b, "note", note)
		}
		return fmt.Sprintf("Task #%d done", rec.ID), b.String()
	case NoticeHandoffDeclined:
		fmt.Fprintf(&b, "The user declined task #%d %q, which you asked to send to %s. It was not sent and is closed.", rec.ID, title, dest)
		return fmt.Sprintf("Task #%d declined", rec.ID), b.String()
	}
	return fmt.Sprintf("Task #%d", rec.ID), ""
}
