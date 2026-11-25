package antigravity

// REVIEW3 test for 306ce6c9 (worktree only).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Invariant: the AfterFunc of a finished run never touches a later run's turn.
// context.AfterFunc runs f in its own goroutine after cancellation, so the
// end-of-run cancel of run 1 can be observed after run 2 started its prompt.
// Here that ordering is forced: run 2 starts, then run 1's ctx is cancelled.
func TestReview3StaleAfterFuncStopsNextRunsTurn(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "second-silent")
	a := core.NewUserMessage("A")
	ctx1, cancel1 := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx1, request("late1", a))
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := drain(t, ch) // run 1 ends normally (turn closed)

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ch, err = p.Stream(ctx2, request("late1", a, *m1, core.NewUserMessage("B")))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // B's turn is running
	cancel1()                          // run 1's AfterFunc goroutine fires late
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(readWire(wire), `"session/cancel"`) {
		t.Errorf("run 1's AfterFunc cancelled run 2's turn")
	}
	if !strings.Contains(readWire(wire), `"session/cancel"`) {
		return // run 2's turn is untouched (it would end with run 2)
	}
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok || ev.Type == core.ProviderEventDone || ev.Type == core.ProviderEventError {
				t.Logf("run 2 ended: closed=%v type=%v err=%v", !ok, ev.Type, ev.Error)
				return
			}
		case <-timeout:
			t.Errorf("run 2's stream is stuck: no terminal event 3s after its turn was cancelled")
			return
		}
	}
}
