package push

import (
	"context"
	"fmt"
	"sync"
)

// The delivery stage sits between the policy, which decides in order, and the
// transport, which may take seconds. It keeps three promises:
//
//   - the policy never waits for a transport;
//   - a tag is never in flight twice, and a newer notification of a tag replaces
//     an older one that has not gone yet, so the device cannot end up showing the
//     old one after the new one;
//   - a fixed, small number of workers do the sending, and one of them only
//     takes urgent notifications, so questions never queue behind quiet ones.
//
// Everything is in memory. The queue of quiet notifications is bounded (the
// oldest is dropped); urgent ones are never dropped.
const (
	deliveryWorkers = 4 // the last one is urgent-only
	maxQuietQueued  = 64
)

type deliverer struct {
	sender Sender
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cond    *sync.Cond
	closed  bool
	seq     int
	pending map[string]Notification
	urgent  []string // keys of pending urgent notifications, oldest first
	quiet   []string
	busy    map[string]bool // keys being sent right now
}

func newDeliverer(sender Sender) *deliverer {
	ctx, cancel := context.WithCancel(context.Background())
	d := &deliverer{
		sender:  sender,
		ctx:     ctx,
		cancel:  cancel,
		pending: make(map[string]Notification),
		busy:    make(map[string]bool),
	}
	d.cond = sync.NewCond(&d.mu)
	for i := 0; i < deliveryWorkers; i++ {
		go d.work(i == deliveryWorkers-1)
	}
	return d
}

// submit queues n. It never blocks.
func (d *deliverer) submit(n Notification) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	key := n.Tag
	if key == "" {
		d.seq++
		key = fmt.Sprintf("\x00untagged-%d", d.seq) // nothing to replace, nothing to serialize
	}
	if _, waiting := d.pending[key]; waiting {
		d.pending[key] = n // the latest of a tag wins, keeping its place in line
		return
	}
	d.pending[key] = n
	if n.Level == LevelUrgent {
		d.urgent = append(d.urgent, key)
	} else {
		d.quiet = append(d.quiet, key)
		if len(d.quiet) > maxQuietQueued {
			delete(d.pending, d.quiet[0])
			d.quiet = d.quiet[1:]
		}
	}
	d.cond.Broadcast()
}

// take removes and returns the next sendable key of a lane: the first whose tag
// is not already in flight.
func (d *deliverer) take(lane *[]string) (string, bool) {
	for i, key := range *lane {
		if !d.busy[key] {
			*lane = append((*lane)[:i], (*lane)[i+1:]...)
			return key, true
		}
	}
	return "", false
}

func (d *deliverer) work(urgentOnly bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for !d.closed {
		key, ok := d.take(&d.urgent)
		if !ok && !urgentOnly {
			key, ok = d.take(&d.quiet)
		}
		if !ok {
			d.cond.Wait()
			continue
		}
		n := d.pending[key]
		delete(d.pending, key)
		d.busy[key] = true
		d.mu.Unlock()
		d.sender.Notify(d.ctx, n)
		d.mu.Lock()
		delete(d.busy, key)
		d.cond.Broadcast() // a newer notification of this tag may now go
	}
}

// close drops what is queued and cancels what is being sent.
func (d *deliverer) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	d.pending, d.urgent, d.quiet = nil, nil, nil
	d.cancel()
	d.cond.Broadcast()
}
