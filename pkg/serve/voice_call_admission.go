package serve

import (
	"sync"
	"time"
)

// Budgets for opening voice calls: concurrent opens, opens per minute across
// the server, and calls that opened per principal per minute.
const (
	voiceMaxInFlight   = 2
	voiceGlobalRate    = 8
	voicePrincipalRate = 2
)

// voiceOpenBudget limits call starts rather than connections: an upstream
// session continues billing after the create request has returned, so
// releasing the request slot cannot be treated as releasing the cost of a call.
//
// Only calls that ESTABLISH spend a principal's allowance. A failed attempt
// costs nothing upstream, and charging for it punishes exactly the behaviour a
// human has when something does not start — retrying — by locking him out of
// the feature. The in-flight cap and the global rate still count attempts:
// those two exist against a runaway client loop, which fails in a tight cycle
// and would otherwise be unbounded.
type voiceOpenBudget struct {
	mu        sync.Mutex
	now       func() time.Time
	active    int
	global    []time.Time
	principal map[string][]time.Time
}

func newVoiceOpenBudget() *voiceOpenBudget {
	return &voiceOpenBudget{now: time.Now, principal: make(map[string][]time.Time)}
}

func (a *voiceOpenBudget) acquire(principal string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now().UTC()
	cutoff := now.Add(-time.Minute)
	a.global = pruneTimes(a.global, cutoff)
	a.prune(cutoff)
	if a.active >= voiceMaxInFlight || len(a.global) >= voiceGlobalRate || len(a.principal[principal]) >= voicePrincipalRate {
		return retryAfter(now, append(a.global, a.principal[principal]...)), false
	}
	a.active++
	a.global = append(a.global, now)
	return 0, true
}

// established records the only thing the per-principal allowance counts: a
// session that exists upstream and is billing.
func (a *voiceOpenBudget) established(principal string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now().UTC()
	a.prune(now.Add(-time.Minute))
	a.principal[principal] = append(a.principal[principal], now)
}

// prune drops expired timestamps and the principals left with none, so a
// long-lived server does not keep a map entry per identity ever seen.
func (a *voiceOpenBudget) prune(cutoff time.Time) {
	for id, times := range a.principal {
		times = pruneTimes(times, cutoff)
		if len(times) == 0 {
			delete(a.principal, id)
			continue
		}
		a.principal[id] = times
	}
}

func (a *voiceOpenBudget) release() { a.mu.Lock(); a.active--; a.mu.Unlock() }
