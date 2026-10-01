package serve

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// startWithOwnerSteer starts a slow run and queues one owner steer behind it.
func startWithOwnerSteer(t *testing.T) (func(string) string, *http.Client, *ManagedSession, <-chan bus.SteersCanceled) {
	t.Helper()
	prov := newMockProvider(delayedResponseHandler(5*time.Second, "slow"))
	ts, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan bus.SteersCanceled, 4)
	sess.runtime.Bus.Subscribe(func(e bus.SteersCanceled) { ch <- e })
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool { return sessState(sess) == StateRunning })
	if _, id, _, err := mgr.Send(sess.ID, "mine", nil, "q-owner", ""); err != nil || id != "q-owner" {
		t.Fatalf("owner steer = %q, %v", id, err)
	}
	return func(p string) string { return ts.URL + p }, ts.Client(), sess, ch
}

func postOperation(t *testing.T, client *http.Client, url, body string, header http.Header) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s = %d", url, resp.StatusCode)
	}
	return decode[map[string]any](t, resp)
}

func wsSteersCanceled(t *testing.T, events <-chan bus.SteersCanceled) map[string]any {
	t.Helper()
	select {
	case e := <-events:
		ev, ok := wsEventFromBus(e)
		if !ok || ev.Type != "steers_canceled" {
			t.Fatalf("steers_canceled not forwarded: %+v", ev)
		}
		return ev.Data.(map[string]any)
	case <-time.After(5 * time.Second):
		t.Fatal("no steers_canceled")
		return nil
	}
}

// The client that stops tells its own discards from anyone else's by the
// stop_id it chose, echoed in the reply and in the broadcast.
func TestCancelAndRecallEchoesStopID(t *testing.T) {
	url, client, sess, events := startWithOwnerSteer(t)
	out := postOperation(t, client, url("/api/sessions/"+sess.ID+"/cancel-and-recall"), `{"stop_id":"stop-1"}`, nil)
	if out["stop_id"] != "stop-1" {
		t.Fatalf("reply stop_id = %v", out["stop_id"])
	}
	data := wsSteersCanceled(t, events)
	if data["stop_id"] != "stop-1" || data["recall_id"] != nil {
		t.Fatalf("broadcast = %+v, want stop_id stop-1", data)
	}
	if ids, _ := data["discarded_steer_ids"].([]string); len(ids) != 1 || ids[0] != "q-owner" {
		t.Fatalf("broadcast ids = %v", data["discarded_steer_ids"])
	}
}

func TestCancelSteersEchoesRecallID(t *testing.T) {
	url, client, sess, events := startWithOwnerSteer(t)
	out := postOperation(t, client, url("/api/sessions/"+sess.ID+"/steers/cancel"), `{"recall_id":"recall-1"}`,
		http.Header{"X-Moa-Steers-Cancel-Response": {"discarded"}})
	if out["recall_id"] != "recall-1" {
		t.Fatalf("reply recall_id = %v", out["recall_id"])
	}
	data := wsSteersCanceled(t, events)
	if data["recall_id"] != "recall-1" || data["stop_id"] != nil {
		t.Fatalf("broadcast = %+v, want recall_id recall-1", data)
	}
}

func TestSteerOperationIDIsBounded(t *testing.T) {
	url, client, sess, _ := startWithOwnerSteer(t)
	body := `{"stop_id":"` + strings.Repeat("x", maxSteerOperationID+1) + `"}`
	req, err := http.NewRequest(http.MethodPost, url("/api/sessions/"+sess.ID+"/cancel-and-recall"), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized stop_id = %d, want 400", resp.StatusCode)
	}
	if sessState(sess) != StateRunning {
		t.Fatalf("a rejected Stop changed the run: %s", sessState(sess))
	}
}
