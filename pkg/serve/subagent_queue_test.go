package serve

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// A subagent view shows its child's queue like the session's own: the
// endpoints act on the child's whole queue, the events reach the clients
// wrapped in subagent_event, and a reconnect snapshot carries the queue.
func TestSubagentQueueEndpoints(t *testing.T) {
	p := &daybreakRecorder{started: make(chan struct{}, 1), release: make(chan struct{})}
	m := daybreakManager(t, p, t.TempDir(), t.TempDir(), false)
	s, err := m.CreateSession(CreateOpts{Model: "sol"})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var frames []Event
	unsub := s.runtime.Bus.Subscribe(func(e bus.SubagentEvent) {
		if ev, ok := wsEventFromBus(e); ok {
			mu.Lock()
			frames = append(frames, ev)
			mu.Unlock()
		}
	})
	defer unsub()
	jobID, finish := startHeldChild(t, s, p)
	srv := NewServer(m)
	base := "/api/sessions/" + s.ID + "/subagents/" + jobID

	for _, body := range []string{`{"text":"first"}`, `{"text":"second"}`} {
		if w := daybreakHTTP(srv, http.MethodPost, base+"/steer", body); w.Code != http.StatusOK {
			t.Fatalf("steer %d %s", w.Code, w.Body.String())
		}
	}

	// Reconnect snapshot.
	snaps, err := bus.QueryTyped[bus.GetSubagents, []bus.SubagentSnapshot](s.runtime.Bus, bus.GetSubagents{})
	if err != nil {
		t.Fatal(err)
	}
	init := liveSubagentInitData(snaps)
	if len(init) != 1 || len(init[0].PendingSteers) != 2 || init[0].PendingSteers[0].Text != "first" {
		t.Fatalf("init subagents = %+v, want the two queued messages", init)
	}

	w := daybreakHTTP(srv, http.MethodPost, base+"/steers/redirect", "")
	if w.Code != http.StatusOK {
		t.Fatalf("redirect %d %s", w.Code, w.Body.String())
	}
	var redirected struct {
		IDs []string `json:"redirected_steer_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &redirected); err != nil || len(redirected.IDs) != 2 {
		t.Fatalf("redirect body %s, want both ids", w.Body.String())
	}
	snaps, _ = bus.QueryTyped[bus.GetSubagents, []bus.SubagentSnapshot](s.runtime.Bus, bus.GetSubagents{})
	if init := liveSubagentInitData(snaps); !init[0].PendingSteers[0].Redirect || !init[0].PendingSteers[1].Redirect {
		t.Fatalf("snapshot after redirect = %+v, want both marked", init[0].PendingSteers)
	}

	w = daybreakHTTP(srv, http.MethodPost, base+"/steers/cancel", `{"recall_id":"recall-x"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel %d %s", w.Code, w.Body.String())
	}
	var recalled struct {
		IDs      []string           `json:"discarded_steer_ids"`
		Steers   []PendingSteerData `json:"discarded_steers"`
		RecallID string             `json:"recall_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &recalled); err != nil || len(recalled.Steers) != 2 ||
		recalled.Steers[0].Text != "first" || recalled.Steers[1].Text != "second" || recalled.RecallID != "recall-x" {
		t.Fatalf("cancel body %s, want both messages in order with the recall id", w.Body.String())
	}
	if init := liveSubagentInitData(mustSubagents(t, s)); len(init[0].PendingSteers) != 0 {
		t.Fatalf("snapshot after recall = %+v, want empty", init[0].PendingSteers)
	}

	for _, path := range []string{"/steers/redirect", "/steers/cancel"} {
		if w := daybreakHTTP(srv, http.MethodPost, "/api/sessions/"+s.ID+"/subagents/sa-nope"+path, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s on unknown job = %d, want 404", path, w.Code)
		}
	}
	finish()

	// Subscribers are delivered asynchronously: wait for the four frames.
	queueTypes := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var types []string
		for _, f := range frames {
			if f.Type != "subagent_event" {
				continue
			}
			inner := f.Data.(SubagentEventData).Event
			switch inner.Type {
			case "steer_queued":
				if d := inner.Data.(SteerQueuedData); d.NonRecallable {
					t.Errorf("child steer_queued %+v must be recallable", d)
				}
			case "steers_redirected", "steers_canceled":
			default:
				continue
			}
			types = append(types, inner.Type)
		}
		return types
	}
	want := []string{"steer_queued", "steer_queued", "steers_redirected", "steers_canceled"}
	deadline := time.Now().Add(3 * time.Second)
	types := queueTypes()
	for len(types) < len(want) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		types = queueTypes()
	}
	if len(types) != len(want) {
		t.Fatalf("child queue frames = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("child queue frames = %v, want %v", types, want)
		}
	}
}

func mustSubagents(t *testing.T, s *ManagedSession) []bus.SubagentSnapshot {
	t.Helper()
	snaps, err := bus.QueryTyped[bus.GetSubagents, []bus.SubagentSnapshot](s.runtime.Bus, bus.GetSubagents{})
	if err != nil {
		t.Fatal(err)
	}
	return snaps
}
