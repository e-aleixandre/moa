package usage

import (
	"context"
	"net/http"
	"sync"
)

// AccountPollers polls the plan usage of each account of a chain on its own
// cache, keyed by an opaque account identity. A poller is created on first
// use and only when somebody asks: nothing here runs on a timer, and the token
// functions it is built from never refresh a credential.
type AccountPollers struct {
	// New builds the poller for one account.
	New func(key string) *Poller

	mu      sync.Mutex
	pollers map[string]*Poller
}

// Get returns the account's usage snapshot, like Poller.Get.
func (a *AccountPollers) Get(ctx context.Context, key string) (*Snapshot, error) {
	if a == nil || a.New == nil {
		return nil, nil
	}
	a.mu.Lock()
	p := a.pollers[key]
	if p == nil {
		if a.pollers == nil {
			a.pollers = map[string]*Poller{}
		}
		p = a.New(key)
		a.pollers[key] = p
	}
	a.mu.Unlock()
	return p.Get(ctx)
}

// Retain forgets the pollers of accounts that are no longer in keep.
func (a *AccountPollers) Retain(keep []string) {
	if a == nil {
		return
	}
	live := make(map[string]bool, len(keep))
	for _, k := range keep {
		live[k] = true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.pollers {
		if !live[k] {
			delete(a.pollers, k)
		}
	}
}

// NewPollerWithClient is NewPoller with the HTTP client the usage endpoint is
// asked through; tests pass one that never leaves the process.
func NewPollerWithClient(tokenFn TokenFunc, client *http.Client) *Poller {
	p := NewPoller(tokenFn)
	if client != nil {
		p.client = client
	}
	return p
}
