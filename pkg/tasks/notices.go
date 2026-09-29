package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// NoticeSourceName is custom.source_name on a notice's transcript message.
const NoticeSourceName = "tasks"

// NoticeKind says which owner gesture a notice reports.
type NoticeKind string

const (
	NoticeAssigned     NoticeKind = "assigned"
	NoticeUpdated      NoticeKind = "updated"
	NoticeRequestDone  NoticeKind = "request_done"
	NoticeAgentDone    NoticeKind = "agent_done"
	NoticeAgentDeleted NoticeKind = "agent_deleted"
)

// Notice states. pending is waiting for the dispatcher (with a Reason when an
// attempt could not deliver it); held waits for its saved session to be
// opened; sent reserves a delivery attempt and waits to be seen in the
// persisted transcript; delivered is there; failed is terminal.
const (
	NoticePending   = "pending"
	NoticeHeld      = "held"
	NoticeSent      = "sent"
	NoticeDelivered = "delivered"
	NoticeFailed    = "failed"
)

// Reasons a notice is not delivered.
const (
	ReasonSessionDeleted  = "session_deleted"
	ReasonSessionLimit    = "session_limit"
	ReasonQuestionPending = "question_pending"
)

// Deliver modes for a recipient that is saved but not loaded: wake resumes it,
// hold waits until the owner opens it. Anything else means hold: a session is
// never woken without the owner choosing to.
const (
	DeliverWake = "wake"
	DeliverHold = "hold"
)

// Delivery methods. run starts (or steers into) a turn; append only adds the
// notice to the transcript, used after the owner discarded its steer.
const (
	MethodRun    = "run"
	MethodAppend = "append"
)

// Notice is one event for a session, written in the same transaction as the
// gesture that caused it. Title and Text are rendered at commit, so the notice
// still says what happened after its task is gone.
type Notice struct {
	ID                 string     `json:"id"`
	TaskID             int64      `json:"task_id"`
	Kind               NoticeKind `json:"kind"`
	RecipientSessionID string     `json:"recipient_session_id"`
	State              string     `json:"state"`
	Reason             string     `json:"reason,omitempty"`
	CreatedAt          int64      `json:"created_at"`
	DeliveredAt        int64      `json:"delivered_at,omitempty"`

	Deliver   string `json:"-"`
	Method    string `json:"-"`
	Title     string `json:"-"`
	Text      string `json:"-"`
	SteerID   string `json:"-"`
	UpdatedAt int64  `json:"-"`
}

func validDeliver(d string) error {
	if d != "" && d != DeliverWake && d != DeliverHold {
		return invalid("deliver must be %q or %q", DeliverWake, DeliverHold)
	}
	return nil
}

// ownerNotice decides what an owner write tells a session: at most one notice
// per gesture. before is nil for a create. Completion wins over assignment,
// and both win over an explicit notify, so "assign and notify" is one notice.
func ownerNotice(before *Record, after Record, notify bool) (NoticeKind, string) {
	if before != nil && after.Status == StatusDone && before.Status != StatusDone {
		switch {
		case after.Place == PlaceYou && after.RequesterSessionID != "":
			return NoticeRequestDone, after.RequesterSessionID
		case after.Place == PlaceAgent:
			return NoticeAgentDone, after.AssigneeSessionID
		}
	}
	if after.Place == PlaceAgent && after.AssigneeSessionID != "" &&
		(before == nil || before.Place != PlaceAgent || before.AssigneeSessionID != after.AssigneeSessionID) {
		return NoticeAssigned, after.AssigneeSessionID
	}
	if notify && before != nil {
		switch {
		case after.Place == PlaceAgent:
			return NoticeUpdated, after.AssigneeSessionID
		case after.Place == PlaceYou && after.RequesterSessionID != "":
			return NoticeUpdated, after.RequesterSessionID
		}
	}
	return "", ""
}

func newNoticeID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "tn_" + hex.EncodeToString(b[:])
}

// addNotice stores a notice for rec inside the gesture's transaction.
func (r *Repo) addNotice(ctx context.Context, tx *sql.Tx, kind NoticeKind, recipient, deliver string, rec Record) error {
	if kind == "" || recipient == "" {
		return nil
	}
	if deliver != DeliverWake {
		deliver = DeliverHold
	}
	now := r.now().UnixMilli()
	_, err := tx.ExecContext(ctx, `INSERT INTO task_notifications(id, task_id, kind, recipient_session_id, deliver, method,
		title, body, state, reason, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,'',?,?)`,
		newNoticeID(), rec.ID, string(kind), recipient, deliver, MethodRun,
		noticeTitle(kind, rec.ID), noticeText(kind, rec), NoticePending, now, now)
	return err
}

func noticeTitle(kind NoticeKind, id int64) string {
	switch kind {
	case NoticeAssigned:
		return fmt.Sprintf("Task #%d assigned", id)
	case NoticeUpdated:
		return fmt.Sprintf("Task #%d updated", id)
	case NoticeAgentDeleted:
		return fmt.Sprintf("Task #%d deleted", id)
	default:
		return fmt.Sprintf("Task #%d done", id)
	}
}

// noticeText is what the agent reads. What the owner wrote (title, description,
// note) is quoted between markers so it reads as data about the task, and a
// closing marker inside it cannot end the quote early.
func noticeText(kind NoticeKind, rec Record) string {
	var b strings.Builder
	title := oneLine(rec.Title)
	get := fmt.Sprintf("Use the tasks tool (action \"get\", id %d) to read it.", rec.ID)
	switch kind {
	case NoticeAssigned:
		fmt.Fprintf(&b, "The owner assigned you task #%d %q. It is on your checklist.\n", rec.ID, title)
		writeSnapshot(&b, rec)
		b.WriteString(get)
	case NoticeUpdated:
		fmt.Fprintf(&b, "The owner updated task #%d %q. This is what was saved:\n", rec.ID, title)
		writeSnapshot(&b, rec)
		b.WriteString(get)
	case NoticeRequestDone:
		fmt.Fprintf(&b, "The owner completed your request, task #%d %q.\n", rec.ID, title)
		if note := strings.TrimSpace(rec.CompletionNote); note != "" {
			b.WriteString("The owner's completion note, verbatim:\n")
			writeQuoted(&b, "owner_note", note)
		}
		b.WriteString(get)
	case NoticeAgentDone:
		fmt.Fprintf(&b, "The owner marked task #%d %q on your checklist as done. Do not keep working on it unless the owner asks.\n", rec.ID, title)
		b.WriteString(get)
	case NoticeAgentDeleted:
		fmt.Fprintf(&b, "The owner deleted task #%d %q from your checklist. It no longer exists; do not keep working on it unless the owner asks.", rec.ID, title)
	}
	return b.String()
}

func writeSnapshot(b *strings.Builder, rec Record) {
	fmt.Fprintf(b, "Status: %s.\n", rec.Status)
	if d := strings.TrimSpace(rec.Description); d != "" {
		b.WriteString("Description, verbatim:\n")
		writeQuoted(b, "task_description", d)
	}
	if len(rec.Subtasks) > 0 {
		b.WriteString("Subtasks:\n")
		for _, s := range rec.Subtasks {
			mark := " "
			if s.Done {
				mark = "x"
			}
			fmt.Fprintf(b, "- [%s] %s\n", mark, oneLine(s.Title))
		}
	}
}

func writeQuoted(b *strings.Builder, tag, text string) {
	closing := "</" + tag + ">"
	text = strings.ReplaceAll(text, closing, "")
	fmt.Fprintf(b, "<%s>\n%s\n%s\n", tag, text, closing)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

const noticeCols = `id, task_id, kind, recipient_session_id, deliver, method, title, body, state, reason,
	steer_id, created_at, updated_at, delivered_at`

func scanNotice(sc interface{ Scan(...any) error }) (Notice, error) {
	var n Notice
	var kind string
	var steer sql.NullString
	var delivered sql.NullInt64
	if err := sc.Scan(&n.ID, &n.TaskID, &kind, &n.RecipientSessionID, &n.Deliver, &n.Method, &n.Title, &n.Text,
		&n.State, &n.Reason, &steer, &n.CreatedAt, &n.UpdatedAt, &delivered); err != nil {
		return Notice{}, err
	}
	n.Kind = NoticeKind(kind)
	n.SteerID = steer.String
	n.DeliveredAt = delivered.Int64
	return n, nil
}

func (r *Repo) queryNotices(ctx context.Context, where string, args ...any) ([]Notice, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	rows, err := rd.QueryContext(ctx, "SELECT "+noticeCols+" FROM task_notifications WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Notice
	for rows.Next() {
		n, err := scanNotice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ErrNoticeNotFound is an unknown notice ID.
var ErrNoticeNotFound = fmt.Errorf("notice %w", ErrNotFound)

// Notice returns one notice.
func (r *Repo) Notice(ctx context.Context, id string) (Notice, error) {
	ns, err := r.queryNotices(ctx, "id = ?", id)
	if err != nil {
		return Notice{}, err
	}
	if len(ns) == 0 {
		return Notice{}, ErrNoticeNotFound
	}
	return ns[0], nil
}

// TaskNotices returns the latest notices of a task, newest first.
func (r *Repo) TaskNotices(ctx context.Context, taskID int64, limit int) ([]Notice, error) {
	return r.queryNotices(ctx, "task_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?", taskID, limit)
}

// OpenNotices returns every notice the dispatcher still has work for, oldest
// first so a session receives them in the order they happened.
func (r *Repo) OpenNotices(ctx context.Context) ([]Notice, error) {
	return r.queryNotices(ctx, "state IN ('pending','held','sent') ORDER BY created_at, rowid")
}

// NoticeChange is a state transition. From lists the states it may leave, so a
// transition decided on a stale read (delivered meanwhile, say) changes nothing.
type NoticeChange struct {
	From    []string
	State   string
	Reason  string
	Method  string // "" keeps the current one
	SteerID string
}

// SetNoticeState applies c and reports whether it matched. A change bumps the
// global revision, so /api/tasks/ws tells open task details to refresh.
func (r *Repo) SetNoticeState(ctx context.Context, id string, c NoticeChange) (bool, error) {
	var matched bool
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		now := r.now().UnixMilli()
		var delivered any
		if c.State == NoticeDelivered {
			delivered = now
		}
		args := []any{c.State, c.Reason, c.Method, c.Method, nullStr(c.SteerID), now, delivered, id}
		for _, f := range c.From {
			args = append(args, f)
		}
		res, err := tx.ExecContext(ctx, `UPDATE task_notifications SET state = ?, reason = ?,
			method = CASE WHEN ? = '' THEN method ELSE ? END, steer_id = ?, updated_at = ?,
			delivered_at = COALESCE(?, delivered_at)
			WHERE id = ? AND state IN (`+placeholders(len(c.From))+`)`, args...)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		matched = n > 0
		return matched, nil
	})
	return matched, err
}

// latestNoticeStates maps each task to the state of its newest notice.
func latestNoticeStates(ctx context.Context, q querier) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT task_id, state FROM task_notifications
		WHERE rowid IN (SELECT MAX(rowid) FROM task_notifications GROUP BY task_id)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		out[id] = state
	}
	return out, rows.Err()
}
