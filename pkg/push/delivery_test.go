package push

import (
	"context"
	"sync"
	"testing"
	"time"
)

// gateSender lets a test hold every delivery open and see how they overlap.
type gateSender struct {
	mu        sync.Mutex
	started   []Notification
	finished  []Notification
	active    int
	maxActive int
	release   chan struct{} // closed to let every held delivery finish
	hold      func(Notification) bool
}

func newGateSender(hold func(Notification) bool) *gateSender {
	return &gateSender{release: make(chan struct{}), hold: hold}
}

func (g *gateSender) Notify(ctx context.Context, n Notification) {
	g.mu.Lock()
	g.started = append(g.started, n)
	g.active++
	if g.active > g.maxActive {
		g.maxActive = g.active
	}
	g.mu.Unlock()
	if g.hold == nil || g.hold(n) {
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	g.mu.Lock()
	g.active--
	if ctx.Err() == nil {
		g.finished = append(g.finished, n)
	}
	g.mu.Unlock()
}

func (g *gateSender) snapshot() (started, finished []Notification, maxActive int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Notification(nil), g.started...), append([]Notification(nil), g.finished...), g.maxActive
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func quiet(tag, body string) Notification {
	return Notification{Tag: tag, Body: body, Level: LevelPassive}
}

func TestATagIsNeverInFlightTwiceAndTheLatestWins(t *testing.T) {
	g := newGateSender(nil)
	d := newDeliverer(g)
	defer d.close()
	d.submit(quiet("run:s", "old"))
	waitUntil(t, "the old one in flight", func() bool { s, _, _ := g.snapshot(); return len(s) == 1 })
	d.submit(quiet("run:s", "new"))
	d.submit(quiet("run:s", "newest")) // replaces "new" while it waits
	time.Sleep(30 * time.Millisecond)
	if s, _, _ := g.snapshot(); len(s) != 1 {
		t.Fatalf("a notification of a tag already in flight went in parallel: %+v", s)
	}
	close(g.release)
	waitUntil(t, "both deliveries", func() bool { _, f, _ := g.snapshot(); return len(f) == 2 })
	_, f, _ := g.snapshot()
	if f[0].Body != "old" || f[1].Body != "newest" {
		t.Fatalf("finished in order %q → %q, want old → newest (the intermediate one is replaced)", f[0].Body, f[1].Body)
	}
}

func TestConcurrencyIsBoundedAndQuietQueueIsBoundedButUrgentIsKept(t *testing.T) {
	g := newGateSender(nil)
	d := newDeliverer(g)
	for i := 0; i < 200; i++ {
		d.submit(quiet("q"+string(rune('A'+i%26))+time.Duration(i).String(), "quiet"))
	}
	const urgentCount = 64
	for i := 0; i < urgentCount; i++ {
		d.submit(Notification{Tag: "req:" + time.Duration(i).String(), Level: LevelUrgent})
	}
	time.Sleep(50 * time.Millisecond)
	if _, _, max := g.snapshot(); max > deliveryWorkers {
		t.Fatalf("%d deliveries at once, want at most %d", max, deliveryWorkers)
	}
	close(g.release)
	waitUntil(t, "the queue to drain", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.pending) == 0 && len(d.busy) == 0
	})
	started, _, max := g.snapshot()
	urgent, quietSent := 0, 0
	for _, n := range started {
		if n.Level == LevelUrgent {
			urgent++
		} else {
			quietSent++
		}
	}
	if urgent != urgentCount {
		t.Fatalf("%d of %d urgent notifications were delivered: urgent ones are never dropped", urgent, urgentCount)
	}
	if quietSent > maxQuietQueued+deliveryWorkers {
		t.Fatalf("%d quiet deliveries from 200 submitted: the quiet queue must be bounded", quietSent)
	}
	if max > deliveryWorkers {
		t.Fatalf("%d deliveries at once, want at most %d", max, deliveryWorkers)
	}
	d.close()
}

func TestUrgentDoesNotWaitBehindHungQuietDeliveries(t *testing.T) {
	g := newGateSender(func(n Notification) bool { return n.Level != LevelUrgent })
	d := newDeliverer(g)
	defer d.close()
	defer close(g.release)
	for i := 0; i < 3*deliveryWorkers; i++ {
		d.submit(quiet("q"+time.Duration(i).String(), "quiet"))
	}
	waitUntil(t, "every general worker stuck on a quiet delivery", func() bool { s, _, _ := g.snapshot(); return len(s) == deliveryWorkers-1 })
	d.submit(Notification{Tag: "req:s", Body: "question", Level: LevelUrgent})
	waitUntil(t, "the question to go out", func() bool {
		_, f, _ := g.snapshot()
		for _, n := range f {
			if n.Body == "question" {
				return true
			}
		}
		return false
	})
}

func TestCloseDropsWhatWaitsAndCancelsWhatIsBeingSent(t *testing.T) {
	g := newGateSender(nil)
	d := newDeliverer(g)
	for i := 0; i < deliveryWorkers+5; i++ {
		d.submit(Notification{Tag: "req:" + time.Duration(i).String(), Level: LevelUrgent})
	}
	waitUntil(t, "workers busy", func() bool { s, _, _ := g.snapshot(); return len(s) == deliveryWorkers })
	d.close()
	waitUntil(t, "in-flight deliveries to see the cancellation", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.active == 0
	})
	d.submit(Notification{Tag: "late", Level: LevelUrgent})
	time.Sleep(20 * time.Millisecond)
	if s, _, _ := g.snapshot(); len(s) != deliveryWorkers {
		t.Fatalf("%d deliveries started, want only the %d admitted before Close", len(s), deliveryWorkers)
	}
}
