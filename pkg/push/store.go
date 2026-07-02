package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Store is a thread-safe, disk-backed set of Web Push subscriptions keyed by
// endpoint. Single-user personal use over the tailnet — no per-user identity;
// a subscription is one browser/device that opted in.
type Store struct {
	path string
	mu   sync.RWMutex
	subs map[string]webpush.Subscription // endpoint -> subscription
}

// NewStore loads subscriptions from path (empty if the file does not exist yet).
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, subs: map[string]webpush.Subscription{}}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read push store %s: %w", path, err)
	}
	var subs []webpush.Subscription
	if err := json.Unmarshal(data, &subs); err != nil {
		return nil, fmt.Errorf("parse push store %s: %w", path, err)
	}
	for _, sub := range subs {
		if sub.Endpoint != "" {
			s.subs[sub.Endpoint] = sub
		}
	}
	return s, nil
}

// Add stores (or replaces) a subscription by endpoint and persists. Idempotent.
func (s *Store) Add(sub webpush.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub.Endpoint] = sub
	return s.persistLocked()
}

// Remove deletes a subscription by endpoint and persists. No-op if absent.
func (s *Store) Remove(endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[endpoint]; !ok {
		return nil
	}
	delete(s.subs, endpoint)
	return s.persistLocked()
}

// All returns a snapshot slice of the current subscriptions.
func (s *Store) All() []webpush.Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]webpush.Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out
}

// Len reports how many subscriptions are stored.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subs)
}

func (s *Store) persistLocked() error {
	subs := make([]webpush.Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	data, err := json.MarshalIndent(subs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, data, 0o600)
}
