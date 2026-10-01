package serve

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

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

// The stopped run's own unwind finds the queue already claimed by Stop. It
// must not announce that empty discard: clients would receive a bare
// steers_canceled that clears the chips before the Stop's correlated one,
// whose text a client with a lost reply can only recover from those chips.
// abortAfterUnwind holds Stop until the unwind has run, the adverse order.
func TestEmptyUnwindDoesNotPrecedeTheStopDiscard(t *testing.T) {
	ts, mgr := newNoticeTestServer(t, newMockProvider(delayedResponseHandler(time.Minute, "unused")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	var once sync.Once
	unsub := sess.runtime.Bus.Subscribe(func(bus.RunEnded) { once.Do(func() { close(ended) }) })
	defer unsub()
	sctx := sess.runtime.Context()
	sctx.Agent = &abortAfterUnwind{AgentController: sctx.Agent, ended: ended}
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "running", func() bool { return sessState(sess) == StateRunning })
	for _, id := range []string{"q-first", "q-second"} {
		if _, _, _, err := mgr.Send(sess.ID, id+" text", nil, id, ""); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, ts.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	var init Event
	if err := wsjson.Read(ctx, conn, &init); err != nil || init.Type != "init" {
		t.Fatalf("first event = %+v, %v", init, err)
	}

	postOperation(t, ts.Client(), ts.URL+"/api/sessions/"+sess.ID+"/cancel-and-recall", `{"stop_id":"unwind-stop"}`, nil)
	for {
		var e Event
		if err := wsjson.Read(ctx, conn, &e); err != nil {
			t.Fatal(err)
		}
		if e.Type != "steers_canceled" {
			continue
		}
		data := e.Data.(map[string]any)
		if data["stop_id"] != "unwind-stop" {
			t.Fatalf("an uncorrelated discard came before the Stop's: %v", data)
		}
		if !reflect.DeepEqual(data["discarded_steer_ids"], []any{"q-first", "q-second"}) {
			t.Fatalf("Stop discard = %v, want both steers in queue order", data["discarded_steer_ids"])
		}
		return
	}
}
