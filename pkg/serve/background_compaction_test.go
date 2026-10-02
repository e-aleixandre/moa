package serve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// bgServeProvider holds the summary request (no tools) until release, and
// answers every ordinary request at once.
type bgServeProvider struct {
	entered  chan struct{}
	once     sync.Once
	release  chan struct{}
	ordinary atomic.Int32
}

func (p *bgServeProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if isTestAutoTitleRequest(req) {
		return simpleResponse("title"), nil
	}
	if len(req.Tools) == 0 {
		p.once.Do(func() { close(p.entered) })
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return simpleResponse("BG SUMMARY"), nil
	}
	p.ordinary.Add(1)
	return simpleResponse("ok"), nil
}

// The client's Stop calls cancel-and-recall. On an idle session that is still
// summarizing in the background it discards that summary without a run; with
// nothing pending it keeps its legacy "not running" refusal.
func TestCancelAndRecallStopsIdleBackgroundCompaction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &bgServeProvider{entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-prov.release:
		default:
			close(prov.release)
		}
	}()
	dir := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, prov, dir)
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	rt := sess.runtime
	ag := rt.Context().Agent
	model := ag.Model()
	model.MaxInput = 200_000
	if err := ag.Reconfigure(nil, model, "", 50_000); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		for _, role := range []string{"user", "assistant"} {
			msg := core.WrapMessage(core.Message{Role: role, Content: []core.Content{core.TextContent(strings.Repeat("history ", 4000))}})
			msg.EnsureMsgID()
			if err := ag.AppendMessage(msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	var runs atomic.Int32
	rt.Bus.Subscribe(func(bus.RunStarted) { runs.Add(1) })
	var bgEnds atomic.Int32
	rt.Bus.Subscribe(func(e bus.CompactionEnded) {
		if e.Background {
			bgEnds.Add(1)
		}
	})

	if _, _, _, err := mgr.Send(sess.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-prov.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background summary never started")
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if !rt.WaitSettled(wctx) {
		t.Fatal("run did not settle")
	}
	rt.Bus.Drain(5 * time.Second)
	if s, _ := bus.QueryTyped[bus.GetBackgroundCompaction, core.BackgroundCompactionState](rt.Bus, bus.GetBackgroundCompaction{}); !s.Active {
		t.Fatalf("no background compaction pending at idle: %+v", s)
	}
	before := runs.Load()

	discarded, err := mgr.CancelWithDiscardedSteers(sess.ID, "stop-1")
	if err != nil || len(discarded) != 0 {
		t.Fatalf("idle stop: discarded=%v err=%v", discarded, err)
	}
	// The state reaches the session through the agent's event stream.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		s, _ := bus.QueryTyped[bus.GetBackgroundCompaction, core.BackgroundCompactionState](rt.Bus, bus.GetBackgroundCompaction{})
		if !s.Active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background compaction still pending after Stop: %+v", s)
		}
	}
	close(prov.release)
	time.Sleep(200 * time.Millisecond) // a discarded summary has nothing to deliver
	rt.Bus.Drain(5 * time.Second)
	if runs.Load() != before || rt.State.Current() != bus.StateIdle {
		t.Fatalf("idle Stop started a run: runs %d -> %d, state %s", before, runs.Load(), rt.State.Current())
	}
	if bgEnds.Load() != 0 {
		t.Fatal("discarded summary was adopted")
	}
	for _, m := range ag.Messages() {
		if m.Role == "compaction_summary" {
			t.Fatal("discarded summary reached the conversation")
		}
	}
	if _, err := mgr.CancelWithDiscardedSteers(sess.ID, ""); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("stop with nothing pending: %v, want the legacy not-running refusal", err)
	}
}

// Wire contract: the state event's Data is the state object itself, the
// snapshot carries the same object, and a background completion is marked.
func TestBackgroundCompactionWireContract(t *testing.T) {
	state := core.BackgroundCompactionState{JobID: 7, Revision: 9, Active: true, Waiting: true}
	ev, ok := wsEventFromBus(bus.BackgroundCompactionChanged{State: state})
	if !ok || ev.Type != "background_compaction_state" {
		t.Fatalf("event=%+v ok=%v", ev, ok)
	}
	b, _ := json.Marshal(ev.Data)
	if string(b) != `{"job_id":7,"revision":9,"active":true,"waiting":true}` {
		t.Fatalf("state data=%s", b)
	}
	init, _ := json.Marshal(InitData{BackgroundCompaction: state})
	if !strings.Contains(string(init), `"background_compaction":{"job_id":7,"revision":9,"active":true,"waiting":true}`) {
		t.Fatalf("snapshot=%s", init)
	}
	end, _ := wsEventFromBus(bus.CompactionEnded{Background: true, JobID: 7, Err: errors.New("disk full")})
	b, _ = json.Marshal(end.Data)
	if end.Type != "compaction_end" || string(b) != `{"background":true,"error":"disk full"}` {
		t.Fatalf("background end=%s %s", end.Type, b)
	}
	fg, _ := wsEventFromBus(bus.CompactionEnded{Err: errors.New("x")})
	b, _ = json.Marshal(fg.Data)
	if string(b) != `{}` {
		t.Fatalf("foreground end changed shape: %s", b)
	}
}
