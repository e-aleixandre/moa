package serve

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// The reconnect snapshot marks a task notice so a client whose recall request
// fails cannot put it back in the composer as if the owner had typed it.
func TestNoticeSnapshotMarksTaskNoticesNonRecallable(t *testing.T) {
	prov := newMockProvider(delayedResponseHandler(5*time.Second, "slow"))
	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_, n := startAndSteerNotice(t, srv, mgr, sess)
	if _, _, _, err := mgr.Send(sess.ID, "mine", nil, "q-owner", ""); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(buildInitData(sess, bus.StreamingAggregate{}, nil, "").PendingSteers)
	if err != nil {
		t.Fatal(err)
	}
	var projected []struct {
		ID            string `json:"id"`
		NonRecallable bool   `json:"non_recallable"`
	}
	if err := json.Unmarshal(data, &projected); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range projected {
		got[s.ID] = s.NonRecallable
	}
	if len(got) != 2 || !got[n.SteerID] || got["q-owner"] {
		t.Fatalf("snapshot steers = %+v (notice steer %s)", got, n.SteerID)
	}
}

// Stop with only a task notice queued leaves the composer's text alone.
func TestNoticeStopWithOnlyANoticeRecallsNothing(t *testing.T) {
	prov := newMockProvider(delayedResponseHandler(5*time.Second, "slow"))
	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	startAndSteerNotice(t, srv, mgr, sess)

	resp := mustAPI(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel-and-recall", "", http.StatusOK)
	out := decode[map[string][]string](t, resp)
	if ids := out["discarded_steer_ids"]; len(ids) != 0 {
		t.Fatalf("stop recalled %v, want nothing", ids)
	}
}
