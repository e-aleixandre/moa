package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "events.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestAddMintsIdentityAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ev, created, err := store.Add(Event{Source: "ci", Project: "/work", Title: "build failed"})
	if err != nil || !created {
		t.Fatalf("Add: created=%v err=%v", created, err)
	}
	if ev.ID == "" || ev.State != StateNew || ev.Created.IsZero() {
		t.Fatalf("Add left an incomplete event: %+v", ev)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store permissions = %v, want 0600", perm)
	}

	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, ok := reopened.Get(ev.ID); !ok || got.Title != "build failed" {
		t.Fatalf("reopened store lost the event: %+v ok=%v", got, ok)
	}
}

func TestAddDeduplicatesByKey(t *testing.T) {
	store := newTestStore(t)
	first, created, err := store.Add(Event{Key: "agentmail:m1", Source: "agentmail", Title: "Re: design"})
	if err != nil || !created {
		t.Fatalf("first Add: created=%v err=%v", created, err)
	}
	// The redelivery carries a different title on purpose: the stored event
	// must come back untouched, so nothing downstream re-injects or re-pushes.
	again, created, err := store.Add(Event{Key: "agentmail:m1", Source: "agentmail", Title: "Re: design (retry)"})
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if created {
		t.Fatal("a repeated idempotency key created a second event")
	}
	if again.ID != first.ID || again.Title != first.Title {
		t.Fatalf("redelivery returned %+v, want the stored %+v", again, first)
	}
	if got := len(store.List("")); got != 1 {
		t.Fatalf("store holds %d events, want 1", got)
	}
}

func TestAddDeduplicatesBySourceAndKey(t *testing.T) {
	store := newTestStore(t)
	if _, created, err := store.Add(Event{Key: "m1", Source: "agentmail", Title: "first"}); err != nil || !created {
		t.Fatalf("first Add: created=%v err=%v", created, err)
	}
	if _, created, err := store.Add(Event{Key: "m1", Source: "ci", Title: "second"}); err != nil || !created {
		t.Fatalf("same key from another source: created=%v err=%v", created, err)
	}
	if got := len(store.List("")); got != 2 {
		t.Fatalf("store holds %d events, want 2", got)
	}
}

func TestAddKeepsUnkeyedEventsSeparate(t *testing.T) {
	store := newTestStore(t)
	if _, _, err := store.Add(Event{Source: "ci", Title: "one"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, created, err := store.Add(Event{Source: "ci", Title: "two"}); err != nil || !created {
		t.Fatalf("Add: created=%v err=%v", created, err)
	}
	if got := len(store.List("")); got != 2 {
		t.Fatalf("store holds %d events, want 2", got)
	}
}

func TestMarkRoutedSettlesOnce(t *testing.T) {
	store := newTestStore(t)
	ev, _, err := store.Add(Event{Source: "ci", Title: "build failed"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	routed, err := store.MarkRouted(ev.ID, "sess-1")
	if err != nil {
		t.Fatalf("MarkRouted: %v", err)
	}
	if routed.State != StateRouted || routed.RoutedTo != "sess-1" || routed.RoutedAt.IsZero() {
		t.Fatalf("MarkRouted produced %+v", routed)
	}
	if _, err := store.MarkRouted(ev.ID, "sess-2"); err != ErrSettled {
		t.Fatalf("second MarkRouted err = %v, want ErrSettled", err)
	}
	if _, err := store.MarkDismissed(ev.ID); err != ErrSettled {
		t.Fatalf("dismiss after route err = %v, want ErrSettled", err)
	}
	if got := len(store.List(StateNew)); got != 0 {
		t.Fatalf("a routed event is still listed in the inbox (%d)", got)
	}
}

func TestMarkDismissedSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ev, _, err := store.Add(Event{Source: "ci", Title: "build failed"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.MarkDismissed(ev.ID); err != nil {
		t.Fatalf("MarkDismissed: %v", err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(reopened.List(StateNew)); got != 0 {
		t.Fatalf("a dismissed event came back after a restart (%d in the inbox)", got)
	}
	got, _ := reopened.Get(ev.ID)
	if got.State != StateDismissed {
		t.Fatalf("state after reopen = %q, want %q", got.State, StateDismissed)
	}
}

func TestSettleUnknownEvent(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.MarkRouted("ev_missing", "sess-1"); err != ErrNotFound {
		t.Fatalf("MarkRouted err = %v, want ErrNotFound", err)
	}
}

func TestListNewestFirstAndFiltered(t *testing.T) {
	store := newTestStore(t)
	old, _, _ := store.Add(Event{Source: "ci", Title: "old", Created: time.Now().Add(-time.Hour)})
	recent, _, _ := store.Add(Event{Source: "ci", Title: "recent"})
	list := store.List(StateNew)
	if len(list) != 2 || list[0].ID != recent.ID || list[1].ID != old.ID {
		t.Fatalf("List order = %v", list)
	}
	if _, err := store.MarkDismissed(old.ID); err != nil {
		t.Fatalf("MarkDismissed: %v", err)
	}
	if got := store.List(StateDismissed); len(got) != 1 || got[0].ID != old.ID {
		t.Fatalf("dismissed list = %v", got)
	}
}

func TestPruneDropsSettledEventsButKeepsPending(t *testing.T) {
	store := newTestStore(t)
	stale := Event{Source: "ci", Title: "stale", Created: time.Now().Add(-30 * 24 * time.Hour)}
	settled, _, err := store.Add(stale)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.MarkDismissed(settled.ID); err != nil {
		t.Fatalf("MarkDismissed: %v", err)
	}
	pending, _, err := store.Add(Event{Source: "ci", Title: "old but pending", Created: time.Now().Add(-30 * 24 * time.Hour)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// The Add above ran the prune: the aged dismissed event is gone, the aged
	// pending one is not (it is still waiting for a decision).
	if _, ok := store.Get(settled.ID); ok {
		t.Fatal("an aged dismissed event survived the prune")
	}
	if _, ok := store.Get(pending.ID); !ok {
		t.Fatal("an aged pending event was pruned")
	}
}

func TestPruneDeletesEventBody(t *testing.T) {
	store := newTestStore(t)
	stale, _, err := store.Add(Event{Source: "ci", Title: "stale", Created: time.Now().Add(-30 * 24 * time.Hour)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	bodyPath := filepath.Join(filepath.Dir(store.path), "event-bodies", stale.ID+".txt")
	if err := os.MkdirAll(filepath.Dir(bodyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bodyPath, []byte("full body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDismissed(stale.ID); err != nil {
		t.Fatalf("MarkDismissed: %v", err)
	}
	if _, _, err := store.Add(Event{Source: "ci", Title: "trigger prune"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
		t.Fatalf("pruned event body stat err = %v, want not exist", err)
	}
}

func TestWriteFailureRollsBackMemory(t *testing.T) {
	store := newTestStore(t)
	ev, _, err := store.Add(Event{Source: "ci", Title: "pending"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	store.path = t.TempDir() // a directory cannot replace the event index
	if _, created, err := store.Add(Event{Source: "ci", Title: "not persisted"}); err == nil || created {
		t.Fatalf("Add after write failure: created=%v err=%v", created, err)
	}
	if got := len(store.List("")); got != 1 {
		t.Fatalf("Add failure left %d events in memory, want 1", got)
	}
	if _, err := store.MarkRouted(ev.ID, "sess-1"); err == nil {
		t.Fatal("MarkRouted unexpectedly persisted to a directory")
	}
	if got, _ := store.Get(ev.ID); got.State != StateNew {
		t.Fatalf("failed settle left state %q, want %q", got.State, StateNew)
	}
}

func TestSetSuggestedOnlyTouchesPendingEvents(t *testing.T) {
	store := newTestStore(t)
	ev, _, _ := store.Add(Event{Source: "ci", Title: "build failed"})
	if err := store.SetSuggested(ev.ID, "sess-1"); err != nil {
		t.Fatalf("SetSuggested: %v", err)
	}
	if got, _ := store.Get(ev.ID); got.Suggested != "sess-1" {
		t.Fatalf("suggested = %q, want sess-1", got.Suggested)
	}
	if _, err := store.MarkRouted(ev.ID, "sess-1"); err != nil {
		t.Fatalf("MarkRouted: %v", err)
	}
	if err := store.SetSuggested(ev.ID, "sess-2"); err != nil {
		t.Fatalf("SetSuggested after routing: %v", err)
	}
	if got, _ := store.Get(ev.ID); got.Suggested != "sess-1" {
		t.Fatalf("a settled event's suggestion changed to %q", got.Suggested)
	}
}

func TestPayloadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	store, _ := NewStore(path)
	if _, _, err := store.Add(Event{Source: "ci", Title: "t", Payload: json.RawMessage(`{"job":42}`)}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.List("")[0]
	// The store re-indents raw JSON when it writes, so compare decoded values.
	var decoded map[string]int
	if err := json.Unmarshal(got.Payload, &decoded); err != nil {
		t.Fatalf("payload %s: %v", got.Payload, err)
	}
	if decoded["job"] != 42 {
		t.Fatalf("payload = %s", got.Payload)
	}
}
