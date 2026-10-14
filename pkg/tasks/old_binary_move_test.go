package tasks

import "testing"

// A binary that moves a task between owners (a gesture the current one
// refuses) clears the requester: the sender stays the one in the handoff.
func TestMovedByOldBinaryKeepsSender(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	sent := mustSend(t, r, moa, infra, true)
	oldExec(t, r, `UPDATE tasks SET place='you', requester_session_id=NULL, assignee_session_id=NULL, revision=revision+1 WHERE id=?`, sent.ID)

	view, err := r.AgentList(bg, ownerActor(moa))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range view.Sent {
		found = found || s.ID == sent.ID
	}
	if !found {
		t.Fatalf("sender lost Sent after an old move: %+v", view.Sent)
	}
	if _, err := r.AgentGet(bg, ownerActor(moa), sent.ID); err != nil {
		t.Fatalf("sender lost read access: %v", err)
	}
	if _, err := r.AgentComment(bg, ownerActor(moa), sent.ID, "still mine"); err != nil {
		t.Fatalf("sender cannot comment: %v", err)
	}
	// Whoever the thread concerns hears the sender's side: a human comment
	// reaches the sender.
	if _, err := r.HumanComment(bg, sent.ID, "from the user"); err != nil {
		t.Fatal(err)
	}
	if len(noticesTo(notices(t, r, sent.ID), moa.SessionID)) == 0 {
		t.Fatal("sender hears nothing from the thread")
	}
}

// An older binary can also move a task between owners to the backlog, where
// a requester is not allowed: the user must still be able to edit it.
func TestMovedToBacklogByOldBinaryStaysEditable(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	sent := mustSend(t, r, moa, infra, true)
	oldExec(t, r, `UPDATE tasks SET place='backlog', requester_session_id=NULL, assignee_session_id=NULL, revision=revision+1 WHERE id=?`, sent.ID)
	cur, err := r.Get(bg, sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	title := "edited after an old move"
	if _, err := r.Update(bg, sent.ID, cur.Revision, Patch{Title: &title}); err != nil {
		t.Fatalf("old-moved backlog task is not editable: %v", err)
	}
}
