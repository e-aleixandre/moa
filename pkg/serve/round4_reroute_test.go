package serve

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

// Rerouting a failed run that a newer one already replaced would leave two
// waiting candidates of one recurrence: it is refused with 409.
func TestRound4APIRerouteOfSupersededRunConflicts(t *testing.T) {
	a := newSchedAPI(t, "2026-09-28T08:00:00Z")
	schedReviewStopWorkers(t, a.h.mgr)
	sid := a.h.savedSession()
	r := a.h.repo()
	tmpl := mkTemplate(t, r, "API reroute is a candidate", dailyAt(9, 0, "UTC", toSession(sid), tasks.Delivery{Saved: tasks.DeliverHold, Late: tasks.LateRun}))
	a.h.clock.Set(mustUTC("2026-09-28T09:00:00Z"))
	ds, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(ds) != 1 {
		t.Fatalf("T0: %+v %v", ds, err)
	}
	old, err := r.FailOccurrence(bgc, ds[0].ID, tasks.ReasonSessionDeleted, "failed original recipient")
	if err != nil {
		t.Fatal(err)
	}
	a.h.clock.Set(mustUTC("2026-09-29T09:00:00Z"))
	ds, err = r.MaterializeDue(bgc, 10)
	if err != nil || len(ds) != 1 {
		t.Fatalf("latest T0: %+v %v", ds, err)
	}
	if _, err := r.AssignOccurrence(bgc, ds[0].ID, tasks.Destination{SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	code, body := a.do("POST", fmt.Sprintf("/api/tasks/occurrences/%d/reroute", old.ID), map[string]any{"revision": old.Revision, "session_id": sid})
	pending := sqlCount(t, a.h.dbPath(), "SELECT count(*) FROM task_occurrences WHERE schedule_task_id=? AND state IN ('ready','late','assigned') AND admitted_at IS NULL", tmpl.ID)
	t.Logf("HTTP reroute status=%d body=%s pending=%d", code, body, pending)
	if pending > 1 {
		t.Fatalf("public reroute admits two waiting candidates: %d", pending)
	}
	if code != http.StatusConflict || !strings.Contains(string(body), "A newer run replaced this one") {
		t.Fatalf("reroute of a superseded run: %d %s, want 409 with a clear message", code, body)
	}
}
