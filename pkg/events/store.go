package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Retention: the inbox is a working surface, not an archive. Settled events
// (routed/dismissed) are pruned after retentionAge, and the whole file is
// capped at maxEvents so a chatty emitter cannot grow it without bound.
// Nothing prunes a `new` event: it is still waiting for a decision.
const (
	maxEvents    = 200
	retentionAge = 7 * 24 * time.Hour
)

// ErrNotFound reports an unknown event id.
var ErrNotFound = errors.New("event not found")

// ErrSettled reports an event that has already been routed or dismissed, so it
// cannot be acted on a second time (a double tap, or a retried request).
var ErrSettled = errors.New("event already settled")

// Store is a thread-safe, disk-backed inbox of events. Single-user personal
// use: one file, loaded at startup, rewritten atomically on every change.
type Store struct {
	path  string
	mu    sync.RWMutex
	items []Event
}

// NewStore loads events from path (empty if the file does not exist yet).
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read event store %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s.items); err != nil {
		return nil, fmt.Errorf("parse event store %s: %w", path, err)
	}
	return s, nil
}

// Add stores an event and reports whether it was created now. An event whose
// (Source, Key) matches a stored one is NOT stored again: the existing event comes back
// with created=false, so a redelivered webhook neither injects a second
// message nor sends a second push.
func (s *Store) Add(ev Event) (Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.Key != "" {
		for _, existing := range s.items {
			if existing.Source == ev.Source && existing.Key == ev.Key {
				return existing, false, nil
			}
		}
	}
	if ev.ID == "" {
		ev.ID = NewID()
	}
	if ev.Created.IsZero() {
		ev.Created = time.Now()
	}
	if ev.State == "" {
		ev.State = StateNew
	}
	previous := append([]Event(nil), s.items...)
	s.items = append(s.items, ev)
	dropped := s.pruneLocked()
	if err := s.persistLocked(); err != nil {
		s.items = previous
		return Event{}, false, err
	}
	s.removeBodies(dropped)
	return ev, true, nil
}

// Get returns a stored event by id.
func (s *Store) Get(id string) (Event, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ev := range s.items {
		if ev.ID == id {
			return ev, true
		}
	}
	return Event{}, false
}

// List returns the stored events in the given state, newest first. An empty
// state lists everything.
func (s *Store) List(state string) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Event, 0, len(s.items))
	for _, ev := range s.items {
		if state == "" || ev.State == state {
			out = append(out, ev)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// MarkRouting CAS-claims a `new` event for delivery. A second claim (two
// concurrent route requests) reports ErrSettled so the message is injected
// at most once. Persist this transition before sending.
func (s *Store) MarkRouting(id string) (Event, error) {
	return s.settleFrom(id, StateNew, func(ev *Event) {
		ev.State = StateRouting
	})
}

// MarkRouted settles a claimed event onto a session. Only `routing` can
// become `routed`; a `new` event must be claimed first.
func (s *Store) MarkRouted(id, sessionID string) (Event, error) {
	return s.settleFrom(id, StateRouting, func(ev *Event) {
		ev.State = StateRouted
		ev.RoutedTo = sessionID
		ev.RoutedAt = time.Now()
	})
}

// ReleaseRouting returns a claimed event to `new` after a failed delivery.
func (s *Store) ReleaseRouting(id string) (Event, error) {
	return s.settleFrom(id, StateRouting, func(ev *Event) {
		ev.State = StateNew
	})
}

// MarkDismissed settles an event without sending it anywhere.
func (s *Store) MarkDismissed(id string) (Event, error) {
	return s.settleFrom(id, StateNew, func(ev *Event) { ev.State = StateDismissed })
}

// DismissSource settles every pending event from source. Already routed or
// dismissed rows are left alone — history is not rewritten.
func (s *Store) DismissSource(source string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := append([]Event(nil), s.items...)
	n := 0
	for i := range s.items {
		if s.items[i].Source != source || s.items[i].State != StateNew {
			continue
		}
		s.items[i].State = StateDismissed
		n++
	}
	if n == 0 {
		return 0, nil
	}
	if err := s.persistLocked(); err != nil {
		s.items = previous
		return 0, err
	}
	return n, nil
}

// SetSuggested updates the session an inbox card offers to send to. Used when
// the suggestion is recomputed after ingress; a settled event is left alone.
func (s *Store) SetSuggested(id, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID != id || s.items[i].State != StateNew {
			continue
		}
		if s.items[i].Suggested == sessionID {
			return nil
		}
		previous := s.items[i]
		s.items[i].Suggested = sessionID
		if err := s.persistLocked(); err != nil {
			s.items[i] = previous
			return err
		}
		return nil
	}
	return nil
}

func (s *Store) settleFrom(id, from string, apply func(*Event)) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID != id {
			continue
		}
		if s.items[i].State != from {
			return s.items[i], ErrSettled
		}
		previous := s.items[i]
		apply(&s.items[i])
		if err := s.persistLocked(); err != nil {
			s.items[i] = previous
			return Event{}, err
		}
		return s.items[i], nil
	}
	return Event{}, ErrNotFound
}

// pruneLocked drops settled events older than retentionAge, then the oldest
// entries past maxEvents. A `new` event is never dropped by age — it is still
// waiting for the owner — but the hard cap applies to it too, since an
// unbounded file is worse than losing the oldest of 200 pending events.
func (s *Store) pruneLocked() []Event {
	dropped := make([]Event, 0)
	cutoff := time.Now().Add(-retentionAge)
	kept := s.items[:0]
	for _, ev := range s.items {
		if ev.State != StateNew && ev.State != StateRouting && ev.Created.Before(cutoff) {
			dropped = append(dropped, ev)
			continue
		}
		kept = append(kept, ev)
	}
	s.items = kept
	if len(s.items) > maxEvents {
		dropped = append(dropped, s.items[:len(s.items)-maxEvents]...)
		s.items = s.items[len(s.items)-maxEvents:]
	}
	return dropped
}

// removeBodies clears full-text spill files only after their corresponding
// events have been durably removed from the index.
func (s *Store) removeBodies(dropped []Event) {
	for _, ev := range dropped {
		_ = os.Remove(filepath.Join(filepath.Dir(s.path), "event-bodies", ev.ID+".txt"))
	}
}

func (s *Store) persistLocked() error {
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, data, 0o600)
}
