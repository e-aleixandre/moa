package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/e-aleixandre/moa/pkg/askuser"
)

// Questions an agent posted without waiting for the answer (asynchronous
// ask_user). A question is a durable object of its session, not a task: it
// shares the SQLite file and the notice outbox with tasks and nothing else.
// Posting is the commit of the row; answering is one transaction that closes
// the question and writes the question_answer notice the dispatcher delivers.

// NoticeQuestionAnswer is the kind of the outbox row that carries an answer to
// the session that asked. Its task_id is 0.
const NoticeQuestionAnswer NoticeKind = "question_answer"

// QuestionState is where a question is in its life. open is the only state
// that can change; the others are terminal.
type QuestionState string

const (
	QuestionOpen        QuestionState = "open"
	QuestionAnswered    QuestionState = "answered"
	QuestionWithdrawn   QuestionState = "withdrawn"
	QuestionDismissed   QuestionState = "dismissed"
	QuestionInvalidated QuestionState = "invalidated"
)

// Work says whether the answer's event still owes the model a continuation,
// apart from the transport state of its outbox row. due: the answer was
// accepted and the run that must read it has not finished; done: it has;
// stopped: Stop or a recall cancelled that run, the event is only appended.
const (
	WorkDue     = "due"
	WorkDone    = "done"
	WorkStopped = "stopped"
)

// Origin tools a question can come from.
const (
	OriginAskUser     = "ask_user"
	OriginRequestCall = "request_call"
)

// ReasonRewound closes a question whose origin the conversation left.
const ReasonRewound = "rewound"

// Question is one posted batch. Everything but State, Reason, Answers,
// Responder, EventID, Work and the times is immutable after the post.
type Question struct {
	ID        string
	SessionID string
	// OriginMsgID, OriginToolCallID and SessionID identify the invocation that
	// posted it: a retry of the same call finds the same question.
	OriginMsgID      string
	OriginToolCallID string
	OriginTool       string
	Questions        []askuser.Question
	RequiresTap      bool
	State            QuestionState
	Reason           string
	// Ack is exactly what the posting call returned, kept so a call whose
	// result never reached the transcript can be closed with the same result.
	Ack       QuestionAck
	Answers   []string
	Responder askuser.Responder
	EventID   string
	Work      string
	AskedAt   int64
	ClosedAt  int64
	UpdatedAt int64
}

// QuestionAck is the tool result of a post: text for the model, metadata for
// the transcript.
type QuestionAck struct {
	Text   string         `json:"text"`
	Custom map[string]any `json:"custom,omitempty"`
}

// Errors of the question store. They are typed so a caller can tell a stale
// request from a lost one without parsing messages.
var (
	// ErrQuestionNotFound is an unknown question, or one of another session.
	ErrQuestionNotFound = fmt.Errorf("question %w", ErrNotFound)
	// ErrAnswerCount is an answer whose length is not the question's.
	ErrAnswerCount = errors.New("wrong number of answers")
)

// ErrQuestionConflict means the question is already closed in a way that does
// not match the request.
type ErrQuestionConflict struct{ State QuestionState }

func (e *ErrQuestionConflict) Error() string { return "question already " + string(e.State) }

// ErrQuestionOpen means the session already has an open question (the bound of
// the first pilot is one per session). ID is that question.
type ErrQuestionOpen struct{ ID string }

func (e *ErrQuestionOpen) Error() string { return "this session already has an open question: " + e.ID }

// NewQuestionID mints a question ID. It is global: random, never a counter.
func NewQuestionID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "q_" + hex.EncodeToString(b[:])
}

const questionCols = `id, session_id, origin_msg_id, origin_tool_call_id, origin_tool, questions_json, requires_tap,
	state, reason, ack_json, answers_json, responder_json, event_id, work, asked_at, closed_at, updated_at`

func scanQuestion(sc interface{ Scan(...any) error }) (Question, error) {
	var q Question
	var qsJSON, ackJSON, ansJSON, respJSON, state string
	var tap int
	var closed sql.NullInt64
	if err := sc.Scan(&q.ID, &q.SessionID, &q.OriginMsgID, &q.OriginToolCallID, &q.OriginTool, &qsJSON, &tap,
		&state, &q.Reason, &ackJSON, &ansJSON, &respJSON, &q.EventID, &q.Work, &q.AskedAt, &closed, &q.UpdatedAt); err != nil {
		return Question{}, err
	}
	q.State = QuestionState(state)
	q.RequiresTap = tap != 0
	q.ClosedAt = closed.Int64
	if err := json.Unmarshal([]byte(qsJSON), &q.Questions); err != nil {
		return Question{}, fmt.Errorf("question %s: %w", q.ID, err)
	}
	if ackJSON != "" {
		if err := json.Unmarshal([]byte(ackJSON), &q.Ack); err != nil {
			return Question{}, fmt.Errorf("question %s ack: %w", q.ID, err)
		}
	}
	if ansJSON != "" {
		if err := json.Unmarshal([]byte(ansJSON), &q.Answers); err != nil {
			return Question{}, fmt.Errorf("question %s answers: %w", q.ID, err)
		}
	}
	if respJSON != "" {
		if err := json.Unmarshal([]byte(respJSON), &q.Responder); err != nil {
			return Question{}, fmt.Errorf("question %s responder: %w", q.ID, err)
		}
	}
	return q, nil
}

func (r *Repo) queryQuestions(ctx context.Context, q querier, where string, args ...any) ([]Question, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+questionCols+" FROM questions WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Question
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// readQuestions is queryQuestions on the read pool; a database that does not
// exist yet has no questions.
func (r *Repo) readQuestions(ctx context.Context, where string, args ...any) ([]Question, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return nil, err
	}
	return r.queryQuestions(ctx, rd, where, args...)
}

// OpenQuestions returns the session's open questions, oldest first.
func (r *Repo) OpenQuestions(ctx context.Context, sessionID string) ([]Question, error) {
	return r.readQuestions(ctx, "session_id = ? AND state = 'open' ORDER BY asked_at, rowid", sessionID)
}

// SessionQuestions returns every question of the session, oldest first.
func (r *Repo) SessionQuestions(ctx context.Context, sessionID string) ([]Question, error) {
	return r.readQuestions(ctx, "session_id = ? ORDER BY asked_at, rowid", sessionID)
}

// GetQuestion returns a question by ID, whatever its session.
func (r *Repo) GetQuestion(ctx context.Context, id string) (Question, error) {
	qs, err := r.readQuestions(ctx, "id = ?", id)
	if err != nil {
		return Question{}, err
	}
	if len(qs) == 0 {
		return Question{}, ErrQuestionNotFound
	}
	return qs[0], nil
}

// QuestionByEvent returns the question whose answer notice is eventID.
func (r *Repo) QuestionByEvent(ctx context.Context, eventID string) (Question, error) {
	if eventID == "" {
		return Question{}, ErrQuestionNotFound
	}
	qs, err := r.readQuestions(ctx, "event_id = ?", eventID)
	if err != nil {
		return Question{}, err
	}
	if len(qs) == 0 {
		return Question{}, ErrQuestionNotFound
	}
	return qs[0], nil
}

// QuestionByInvocation returns the question a tool invocation posted.
func (r *Repo) QuestionByInvocation(ctx context.Context, sessionID, msgID, toolCallID string) (Question, bool, error) {
	qs, err := r.readQuestions(ctx, "session_id = ? AND origin_msg_id = ? AND origin_tool_call_id = ?", sessionID, msgID, toolCallID)
	if err != nil || len(qs) == 0 {
		return Question{}, false, err
	}
	return qs[0], true, nil
}

// InsertQuestion commits a posted question. The invocation that already posted
// one gets it back (existed), so a retry is idempotent; a session at its bound
// gets *ErrQuestionOpen. q.ID, q.Ack and the origin are the caller's.
func (r *Repo) InsertQuestion(ctx context.Context, q Question) (Question, bool, error) {
	if q.ID == "" || q.SessionID == "" || q.OriginToolCallID == "" {
		return Question{}, false, invalid("a question needs an id, a session and an origin")
	}
	if q.OriginTool != OriginAskUser && q.OriginTool != OriginRequestCall {
		return Question{}, false, invalid("unknown origin tool %q", q.OriginTool)
	}
	qsJSON, err := json.Marshal(q.Questions)
	if err != nil {
		return Question{}, false, err
	}
	ackJSON, err := json.Marshal(q.Ack)
	if err != nil {
		return Question{}, false, err
	}
	var out Question
	var existed bool
	err = r.write(ctx, func(tx *sql.Tx) (bool, error) {
		existed = false
		found, err := r.queryQuestions(ctx, tx, "session_id = ? AND origin_msg_id = ? AND origin_tool_call_id = ?",
			q.SessionID, q.OriginMsgID, q.OriginToolCallID)
		if err != nil {
			return false, err
		}
		if len(found) > 0 {
			out, existed = found[0], true
			return false, nil
		}
		open, err := r.queryQuestions(ctx, tx, "session_id = ? AND state = 'open'", q.SessionID)
		if err != nil {
			return false, err
		}
		if len(open) > 0 {
			return false, &ErrQuestionOpen{ID: open[0].ID}
		}
		now := r.now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `INSERT INTO questions(id, session_id, origin_msg_id, origin_tool_call_id,
			origin_tool, questions_json, requires_tap, state, ack_json, asked_at, updated_at)
			VALUES (?,?,?,?,?,?,?,'open',?,?,?)`,
			q.ID, q.SessionID, q.OriginMsgID, q.OriginToolCallID, q.OriginTool, string(qsJSON), boolInt(q.RequiresTap),
			string(ackJSON), now, now); err != nil {
			return false, err
		}
		q.State, q.AskedAt, q.UpdatedAt = QuestionOpen, now, now
		out = q
		return true, nil
	})
	if err != nil {
		return Question{}, false, err
	}
	return out, existed, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// QuestionAnswerResult is what an accepted answer produced.
type QuestionAnswerResult struct {
	Question Question
	// Duplicate: the same responder had already given the same answers; nothing
	// was written.
	Duplicate bool
}

// RenderAnswer turns an accepted answer into the title and the text the
// session will read. It runs inside the answer's transaction.
type RenderAnswer func(q Question) (title, text string)

// sameResponder compares who answered, ignoring the evidence text a voice
// responder carries.
func sameResponder(a, b askuser.Responder) bool {
	return a.Kind == b.Kind && a.Via == b.Via && a.ID == b.ID && a.CallID == b.CallID
}

// AnswerQuestion accepts the answer to an open question of sessionID in one
// transaction: the question closes as answered and its notice enters the
// outbox, or neither happens. A requires_tap question accepts only a human
// (ResponderUser) and is left untouched otherwise. The same responder
// repeating the same answers is a Duplicate and writes nothing.
func (r *Repo) AnswerQuestion(ctx context.Context, sessionID, id string, answers []string, responder askuser.Responder, render RenderAnswer) (QuestionAnswerResult, error) {
	var res QuestionAnswerResult
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		res = QuestionAnswerResult{}
		qs, err := r.queryQuestions(ctx, tx, "id = ?", id)
		if err != nil {
			return false, err
		}
		if len(qs) == 0 || qs[0].SessionID != sessionID {
			return false, ErrQuestionNotFound
		}
		q := qs[0]
		switch q.State {
		case QuestionOpen:
		case QuestionAnswered:
			if reflect.DeepEqual(q.Answers, answers) && sameResponder(q.Responder, responder) {
				res = QuestionAnswerResult{Question: q, Duplicate: true}
				return false, nil
			}
			return false, &ErrQuestionConflict{State: q.State}
		default:
			return false, &ErrQuestionConflict{State: q.State}
		}
		// Authority first, before anything is consumed: a tap question is the
		// human's alone, whatever the kind says it is.
		if q.RequiresTap && responder.Kind != askuser.ResponderUser {
			return false, askuser.ErrRequiresTap
		}
		if len(answers) != len(q.Questions) {
			return false, fmt.Errorf("%w: expected %d answers, got %d", ErrAnswerCount, len(q.Questions), len(answers))
		}
		now := r.now().UnixMilli()
		eventID := newNoticeID()
		q.State, q.Answers, q.Responder, q.ClosedAt, q.UpdatedAt = QuestionAnswered, answers, responder, now, now
		q.EventID, q.Work = eventID, WorkDue
		title, text := render(q)
		if r.hookBeforeQuestionEvent != nil {
			if err := r.hookBeforeQuestionEvent(); err != nil {
				return false, err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_notifications(id, task_id, kind, recipient_session_id, deliver,
			method, title, body, state, reason, created_at, updated_at) VALUES (?,0,?,?,?,?,?,?,?,'',?,?)`,
			eventID, string(NoticeQuestionAnswer), sessionID, DeliverWake, MethodRun, title, text, NoticePending, now, now); err != nil {
			return false, err
		}
		ansJSON, err := json.Marshal(answers)
		if err != nil {
			return false, err
		}
		respJSON, err := json.Marshal(responder)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE questions SET state = 'answered', answers_json = ?, responder_json = ?,
			event_id = ?, work = 'due', closed_at = ?, updated_at = ? WHERE id = ? AND state = 'open'`,
			string(ansJSON), string(respJSON), eventID, now, now, id); err != nil {
			return false, err
		}
		res = QuestionAnswerResult{Question: q}
		return true, nil
	})
	if err != nil {
		return QuestionAnswerResult{}, err
	}
	return res, nil
}

// CloseQuestion ends an open question without an answer: to is withdrawn,
// dismissed or invalidated. Closing it again the same way is a no-op that
// reports changed=false; a question closed any other way, answered included,
// is a *ErrQuestionConflict and stays as it was.
func (r *Repo) CloseQuestion(ctx context.Context, sessionID, id string, to QuestionState, reason string) (Question, bool, error) {
	if to != QuestionWithdrawn && to != QuestionDismissed && to != QuestionInvalidated {
		return Question{}, false, invalid("a question cannot close as %q", to)
	}
	var out Question
	var changed bool
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		changed = false
		qs, err := r.queryQuestions(ctx, tx, "id = ?", id)
		if err != nil {
			return false, err
		}
		if len(qs) == 0 || qs[0].SessionID != sessionID {
			return false, ErrQuestionNotFound
		}
		out = qs[0]
		switch {
		case out.State == to:
			return false, nil
		case out.State != QuestionOpen:
			return false, &ErrQuestionConflict{State: out.State}
		}
		now := r.now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `UPDATE questions SET state = ?, reason = ?, closed_at = ?, updated_at = ?
			WHERE id = ? AND state = 'open'`, string(to), reason, now, now, id); err != nil {
			return false, err
		}
		out.State, out.Reason, out.ClosedAt, out.UpdatedAt = to, reason, now, now
		changed = true
		return true, nil
	})
	return out, changed, err
}

// settleDeletedQuestions is the question half of SettleSessionDeleted: the
// session is gone, so its open questions can never be answered, the answers
// not yet admitted can never be delivered, and no continuation is owed.
func settleDeletedQuestions(ctx context.Context, tx *sql.Tx, sessionID string, now int64) (bool, error) {
	changed := false
	run := func(query string, args ...any) error {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		changed = changed || n > 0
		return nil
	}
	if err := run(`UPDATE questions SET state = 'invalidated', reason = ?, closed_at = ?, updated_at = ?
		WHERE session_id = ? AND state = 'open'`, ReasonSessionDeleted, now, now, sessionID); err != nil {
		return false, err
	}
	if err := run(`UPDATE task_notifications SET state = 'failed', reason = ?, updated_at = ?
		WHERE kind = 'question_answer' AND recipient_session_id = ? AND state IN ('pending','held','sent')`,
		ReasonSessionDeleted, now, sessionID); err != nil {
		return false, err
	}
	if err := run(`UPDATE questions SET work = 'stopped', updated_at = ? WHERE session_id = ? AND work = 'due'`,
		now, sessionID); err != nil {
		return false, err
	}
	return changed, nil
}

// AbandonQuestionAnswer ends an answer whose conversation branch was left
// before it was admitted: the event fails (reason) and the run it would have
// caused is stopped, in one transaction. An answer already admitted is left
// alone; it reports false.
func (r *Repo) AbandonQuestionAnswer(ctx context.Context, eventID, reason string) (bool, error) {
	var abandoned bool
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		now := r.now().UnixMilli()
		res, err := tx.ExecContext(ctx, `UPDATE task_notifications SET state = 'failed', reason = ?, updated_at = ?
			WHERE id = ? AND kind = 'question_answer' AND state IN ('pending','held','sent')`, reason, now, eventID)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		abandoned = n > 0
		if _, err := tx.ExecContext(ctx, `UPDATE questions SET work = 'stopped', updated_at = ? WHERE event_id = ? AND work = 'due'`,
			now, eventID); err != nil {
			return false, err
		}
		return abandoned, nil
	})
	return abandoned, err
}

// FenceQuestionAnswers commits, before a Stop or a recall is confirmed, what
// happens to answers already accepted: an answer not yet admitted stays in
// the outbox as an append (it joins the transcript, it starts nothing), and
// with stopRun the continuation an admitted one still owes is cancelled too.
// It is the durable order the dispatcher re-reads before admitting, so a Stop
// that lands between "accepted" and "admitted" cannot be raced by a reserved
// attempt. It returns how many rows it changed.
func (r *Repo) FenceQuestionAnswers(ctx context.Context, sessionID string, stopRun bool) (int, error) {
	var n int
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		now := r.now().UnixMilli()
		res, err := tx.ExecContext(ctx, `UPDATE task_notifications SET method = 'append', updated_at = ?
			WHERE kind = 'question_answer' AND recipient_session_id = ? AND state IN ('pending','held','sent') AND method <> 'append'`,
			now, sessionID)
		if err != nil {
			return false, err
		}
		fenced, _ := res.RowsAffected()
		// Work owed by an answer that never reached the session is stopped
		// either way: an append owes no run.
		q := `UPDATE questions SET work = 'stopped', updated_at = ? WHERE session_id = ? AND work = 'due'
			AND event_id IN (SELECT id FROM task_notifications WHERE state IN ('pending','held','sent'))`
		if stopRun {
			q = `UPDATE questions SET work = 'stopped', updated_at = ? WHERE session_id = ? AND work = 'due'`
		}
		res, err = tx.ExecContext(ctx, q, now, sessionID)
		if err != nil {
			return false, err
		}
		stopped, _ := res.RowsAffected()
		n = int(fenced + stopped)
		return n > 0, nil
	})
	return n, err
}

// SetQuestionWork moves an answer's owed work from one disposition to another.
func (r *Repo) SetQuestionWork(ctx context.Context, eventID, from, to string) (bool, error) {
	var matched bool
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		res, err := tx.ExecContext(ctx, `UPDATE questions SET work = ?, updated_at = ? WHERE event_id = ? AND work = ?`,
			to, r.now().UnixMilli(), eventID, from)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		matched = n > 0
		return matched, nil
	})
	return matched, err
}

// DueQuestionWork returns the answers whose continuation is still owed,
// oldest first: accepted, not stopped, and not yet seen to finish.
func (r *Repo) DueQuestionWork(ctx context.Context) ([]Question, error) {
	return r.readQuestions(ctx, "work = 'due' ORDER BY closed_at, rowid")
}

// SessionOwesWork reports whether a session has an open question or an answer
// whose continuation is still due: it is waiting on something, so a goal must
// not read its silence as a result.
func (r *Repo) SessionOwesWork(ctx context.Context, sessionID string) (bool, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return false, err
	}
	var owes bool
	err = rd.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM questions WHERE session_id = ? AND (state = 'open' OR work = 'due'))`,
		sessionID).Scan(&owes)
	return owes, err
}
