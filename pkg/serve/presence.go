package serve

import (
	"encoding/json"
	"sync"
	"time"
)

// Presence: who is actually looking at a session. Push asks it to decide
// whether a notification is worth sending — a question somebody has open on
// another device does not need to buzz a phone.
//
// Each WebSocket viewer reports {"type":"presence","visible":bool} when it
// connects, when its tab/app becomes visible or hidden, and periodically while
// visible. A report expires: a phone that was put away without a last message
// stops counting after presenceTTL, it does not count until the socket dies.

const (
	presenceTTL = 45 * time.Second
	// presenceMaxMessage bounds what a client may send on the socket.
	presenceMaxMessage = 1024
)

type viewer struct {
	mu           sync.Mutex
	visibleUntil time.Time
}

// apply reads one client message; anything that is not a presence report is ignored.
func (v *viewer) apply(data []byte) {
	var msg struct {
		Type    string `json:"type"`
		Visible bool   `json:"visible"`
	}
	if json.Unmarshal(data, &msg) != nil || msg.Type != "presence" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if msg.Visible {
		v.visibleUntil = time.Now().Add(presenceTTL)
	} else {
		v.visibleUntil = time.Time{}
	}
}

func (v *viewer) visible(now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return now.Before(v.visibleUntil)
}

// presenceSet is the viewers of one session. The zero value is ready to use.
type presenceSet struct {
	mu      sync.Mutex
	viewers map[*viewer]struct{}
}

func (p *presenceSet) join() *viewer {
	v := &viewer{}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.viewers == nil {
		p.viewers = make(map[*viewer]struct{})
	}
	p.viewers[v] = struct{}{}
	return v
}

func (p *presenceSet) leave(v *viewer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.viewers, v)
}

// watched reports whether any viewer has the session visible right now.
func (p *presenceSet) watched() bool {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for v := range p.viewers {
		if v.visible(now) {
			return true
		}
	}
	return false
}
