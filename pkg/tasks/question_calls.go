package tasks

import (
	"context"
	"database/sql"
	"strings"
)

// A question can escalate to a call: when it is still open at call_due_at,
// the server's scheduler claims it and its owner calls the user about it once.
// The row is the only record that survives a restart, so every transition is a
// conditional update on it and the order is the contract:
//
//   - the claim (pending -> calling, with the attempt's id) commits before the
//     call is announced, and only takes an open question that is due;
//   - answering, closing or Stop moves pending or calling to cancelled in the
//     same transaction as the change that made the call pointless, and hands
//     back the attempt so a ringing call is withdrawn;
//   - a call the user answered stays calling until the call itself ends, then
//     done: while it lasts, Stop and a restart still see it;
//   - a call that ends unanswered is missed, terminal: never rearmed;
//   - a call claimed by a process that died is interrupted, terminal: whether
//     it rang is unknown, so it is never replayed.

// CallState is where a question's escalation is.
type CallState string

const (
	CallNone        CallState = ""
	CallPending     CallState = "pending"
	CallCalling     CallState = "calling"
	CallMissed      CallState = "missed"
	CallInterrupted CallState = "interrupted"
	CallCancelled   CallState = "cancelled"
	CallDone        CallState = "done"
)

// cancelCallSet is the SET fragment that cancels a live escalation together
// with the change that ends its question.
const cancelCallSet = `call_state = CASE WHEN call_state IN ('pending','calling') THEN 'cancelled' ELSE call_state END`

func cancelledCall(s CallState) CallState {
	if s == CallPending || s == CallCalling {
		return CallCancelled
	}
	return s
}

// DueCallEscalations returns the open questions whose call is due at now,
// oldest due first.
func (r *Repo) DueCallEscalations(ctx context.Context, now int64) ([]Question, error) {
	return r.readQuestions(ctx, "state = 'open' AND call_state = 'pending' AND call_due_at <= ? ORDER BY call_due_at, rowid", now)
}

// NextCallDueAt is the earliest pending call of an open question, 0 if none.
func (r *Repo) NextCallDueAt(ctx context.Context) (int64, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return 0, err
	}
	var next sql.NullInt64
	err = rd.QueryRowContext(ctx, `SELECT MIN(call_due_at) FROM questions WHERE state = 'open' AND call_state = 'pending'`).Scan(&next)
	return next.Int64, err
}

// callUpdate runs one conditional update of a question's escalation and
// reports whether it matched.
func (r *Repo) callUpdate(ctx context.Context, query string, args ...any) (bool, error) {
	var matched bool
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		matched = n > 0
		return matched, nil
	})
	return matched, err
}

// ClaimCallEscalation takes a due escalation for one attempt: it commits
// calling with attempt before anything rings. It reports false when the
// question was answered or closed, the escalation cancelled or claimed, or it
// is not due yet.
func (r *Repo) ClaimCallEscalation(ctx context.Context, id, attempt string) (bool, error) {
	if id == "" || attempt == "" {
		return false, invalid("a claim needs a question and an attempt")
	}
	if r.hookBeforeCallClaim != nil {
		r.hookBeforeCallClaim()
	}
	now := r.now().UnixMilli()
	return r.callUpdate(ctx, `UPDATE questions SET call_state = 'calling', call_attempt = ?, updated_at = ?
		WHERE id = ? AND state = 'open' AND call_state = 'pending' AND call_due_at <= ?`, attempt, now, id, now)
}

// DeferCallEscalation puts a claimed attempt that did not ring back to
// pending, due at dueAt (outside the allowed hours, paused, the line busy).
func (r *Repo) DeferCallEscalation(ctx context.Context, id, attempt string, dueAt int64) (bool, error) {
	return r.callUpdate(ctx, `UPDATE questions SET call_state = 'pending', call_due_at = ?, updated_at = ?
		WHERE id = ? AND call_attempt = ? AND call_state = 'calling' AND state = 'open'`, dueAt, r.now().UnixMilli(), id, attempt)
}

// FinishCallEscalation ends a claimed attempt as missed, done or cancelled.
func (r *Repo) FinishCallEscalation(ctx context.Context, id, attempt string, to CallState) (bool, error) {
	if to != CallMissed && to != CallDone && to != CallCancelled {
		return false, invalid("an attempt cannot end as %q", to)
	}
	return r.callUpdate(ctx, `UPDATE questions SET call_state = ?, updated_at = ?
		WHERE id = ? AND call_attempt = ? AND call_state = 'calling'`, string(to), r.now().UnixMilli(), id, attempt)
}

// CancelPendingCall cancels the escalation of one question that has not been
// claimed (its owner can no longer call, or never could).
func (r *Repo) CancelPendingCall(ctx context.Context, id string) (bool, error) {
	return r.callUpdate(ctx, `UPDATE questions SET call_state = 'cancelled', updated_at = ?
		WHERE id = ? AND call_state = 'pending'`, r.now().UnixMilli(), id)
}

// CancelSessionCalls is Stop: every live escalation of the session's open
// questions is cancelled, the questions stay open. It returns the attempts
// that were calling, for the caller to withdraw, and whether it cancelled any.
func (r *Repo) CancelSessionCalls(ctx context.Context, sessionID string) ([]string, bool, error) {
	var attempts []string
	cancelled := false
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		attempts, cancelled = nil, false
		rows, err := tx.QueryContext(ctx, `SELECT call_attempt FROM questions
			WHERE session_id = ? AND state = 'open' AND call_state = 'calling'`, sessionID)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				_ = rows.Close()
				return false, err
			}
			attempts = append(attempts, a)
		}
		_ = rows.Close()
		res, err := tx.ExecContext(ctx, `UPDATE questions SET call_state = 'cancelled', updated_at = ?
			WHERE session_id = ? AND state = 'open' AND call_state IN ('pending','calling')`, r.now().UnixMilli(), sessionID)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		cancelled = n > 0
		return cancelled, nil
	})
	return attempts, cancelled, err
}

// InterruptCallEscalations marks interrupted every attempt still calling. Only
// the process that claimed an attempt can know how it ended, so this runs when
// a process starts, before it claims anything: those attempts belonged to one
// that died. It returns the questions it marked.
func (r *Repo) InterruptCallEscalations(ctx context.Context) ([]string, error) {
	var ids []string
	err := r.write(ctx, func(tx *sql.Tx) (bool, error) {
		ids = nil
		rows, err := tx.QueryContext(ctx, `SELECT id FROM questions WHERE call_state = 'calling'`)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return false, err
			}
			ids = append(ids, id)
		}
		_ = rows.Close()
		if len(ids) == 0 {
			return false, nil
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := []any{r.now().UnixMilli()}
		for _, id := range ids {
			args = append(args, id)
		}
		_, err = tx.ExecContext(ctx, `UPDATE questions SET call_state = 'interrupted', updated_at = ?
			WHERE call_state = 'calling' AND id IN (`+marks+`)`, args...)
		return err == nil, err
	})
	return ids, err
}
