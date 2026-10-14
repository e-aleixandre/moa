package tasks

import (
	"encoding/json"
	"testing"
)

// The flat session projection is what the session's pinned request line and
// /tasks read: it must say a request waits for permission and what happened
// to a task that was sent, without any session id of the other owner.
func TestSessionProjectionCarriesHandoff(t *testing.T) {
	r := newRepo(t)
	moa, infra := testOwners()
	asking := mustSend(t, r, moa, infra, false)
	sent := mustSend(t, r, moa, infra, true)
	if _, err := r.HumanComment(bg, sent.ID, "Is 2 GB fine?"); err != nil {
		t.Fatal(err)
	}
	cant, note := OutcomeCantDo, "No RAM"
	st := StatusDone
	if _, err := r.Update(bg, sent.ID, sent.Revision, Patch{Status: &st, Outcome: &cant, CompletionNote: &note}); err != nil {
		t.Fatal(err)
	}
	reqs, err := r.Requests(bg, moa.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	checklist, err := r.Checklist(bg, infra.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	raw := func(v any) []map[string]any {
		b, _ := json.Marshal(v)
		var out []map[string]any
		_ = json.Unmarshal(b, &out)
		return out
	}
	rm := raw(reqs)
	if len(rm) != 1 || rm[0]["id"] != float64(asking.ID) {
		t.Fatalf("requests = %v", rm)
	}
	h, _ := rm[0]["handoff"].(map[string]any)
	if h["state"] != HandoffAwaiting || h["from_owner_name"] != "moa" || h["to_owner_name"] != "Infra" {
		t.Fatalf("request handoff = %v", rm[0]["handoff"])
	}
	cm := raw(checklist)
	if len(cm) != 1 || cm[0]["comment_count"] != float64(1) {
		t.Fatalf("checklist = %v", cm)
	}
	h, _ = cm[0]["handoff"].(map[string]any)
	if h["state"] != HandoffSent || h["outcome"] != OutcomeCantDo || h["from_owner_name"] != "moa" {
		t.Fatalf("checklist handoff = %v", cm[0]["handoff"])
	}
	for _, m := range append(rm, cm...) {
		b, _ := json.Marshal(m)
		if id := leaks(string(b), moa); id != "" && id != moa.ID {
			t.Fatalf("projection leaks %q: %s", id, b)
		}
		if id := leaks(string(b), infra); id != "" && id != infra.ID {
			t.Fatalf("projection leaks %q: %s", id, b)
		}
	}
	// An ordinary task keeps the JSON installed clients have always parsed.
	plain := mustCreate(t, r, CreateInput{Title: "plain", Place: PlaceAgent, AssigneeSessionID: "s9", ProjectKey: "p"})
	list, _ := r.Checklist(bg, "s9")
	b, _ := json.Marshal(list)
	var got []map[string]any
	_ = json.Unmarshal(b, &got)
	if len(got) != 1 || got[0]["id"] != float64(plain.ID) || got[0]["handoff"] != nil || got[0]["comment_count"] != nil {
		t.Fatalf("plain task projection changed: %s", b)
	}
}
