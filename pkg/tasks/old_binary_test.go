package tasks

import (
	"database/sql"
	"testing"
)

// oldExec runs, against the repository's database, the statement a binary
// without tasks between owners would issue: it knows neither task_handoffs
// nor outcome, so these are the writes it can make to the shared columns.
func oldExec(t *testing.T, r *Repo, query string, args ...any) {
	t.Helper()
	if _, err := r.Revision(bg); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn(r.Path(), true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

const oldReopen = `UPDATE tasks SET status='pending', completed_at=NULL, updated_at=updated_at+1, revision=revision+1 WHERE id=?`

func closedCantDo(t *testing.T, r *Repo) (Record, OwnerRef, OwnerRef) {
	t.Helper()
	moa, infra := testOwners()
	sent := mustSend(t, r, moa, infra, true)
	if _, err := r.AgentClose(bg, ownerActor(infra), sent.ID, OutcomeCantDo, "missing RAM"); err != nil {
		t.Fatal(err)
	}
	return sent, moa, infra
}

// Reopening is not a stale cant_do: the binary that reopens does not know the
// outcome, so the next close must not inherit it.
func TestReopenedByOldBinaryDoesNotKeepOutcome(t *testing.T) {
	r := newRepo(t)
	sent, _, _ := closedCantDo(t, r)
	oldExec(t, r, oldReopen, sent.ID)
	rec, err := r.Get(bg, sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Handoff.Outcome != "" {
		t.Fatalf("open task reports outcome %q", rec.Handoff.Outcome)
	}
	done := StatusDone
	closed, err := r.Update(bg, rec.ID, rec.Revision, Patch{Status: &done})
	if err != nil {
		t.Fatalf("ordinary Done after an old reopen: %v", err)
	}
	if closed.Handoff.Outcome != OutcomeDone {
		t.Fatalf("outcome = %q, want done", closed.Handoff.Outcome)
	}
}

func TestReopenedByUserOrAgentClearsOutcome(t *testing.T) {
	r := newRepo(t)
	sent, _, infra := closedCantDo(t, r)
	pending := StatusPending
	got, err := r.Get(bg, sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err = r.Update(bg, sent.ID, got.Revision, Patch{Status: &pending})
	if err != nil {
		t.Fatal(err)
	}
	if got.Handoff.Outcome != "" {
		t.Fatalf("reopened by the user keeps %q", got.Handoff.Outcome)
	}
	var n sql.NullString
	db, _ := sql.Open("sqlite", dsn(r.Path(), false))
	defer func() { _ = db.Close() }()
	if err := db.QueryRow(`SELECT outcome FROM task_handoffs WHERE task_id=?`, sent.ID).Scan(&n); err != nil || n.Valid {
		t.Fatalf("stored outcome = %v, %v", n, err)
	}

	// The recipient's agent closes cant_do again, then reopens its own task.
	if _, err := r.AgentClose(bg, ownerActor(infra), sent.ID, OutcomeCantDo, "still no RAM"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AgentUpdate(bg, ownerActor(infra), sent.ID, AgentPatch{Status: &pending}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT outcome FROM task_handoffs WHERE task_id=?`, sent.ID).Scan(&n); err != nil || n.Valid {
		t.Fatalf("stored outcome after agent reopen = %v, %v", n, err)
	}
}
