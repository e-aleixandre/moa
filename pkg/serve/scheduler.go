package serve

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/schedule"
)

// schedulerService owns the schedule store and its delivery loop. All store
// access goes through it so a delivery's durable status update cannot race a
// command operation.
type schedulerService struct {
	mu      sync.Mutex
	store   *schedule.Store
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
	// accepted maps a schedule ID to the occurrence whose prompt the runtime
	// admitted but whose durable status is not yet saved. The history append is
	// asynchronous, so history alone cannot prove acceptance; guarded by mu.
	accepted map[string]string
}

func newSchedulerService(path string) (*schedulerService, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create schedule directory: %w", err)
	}
	store, err := schedule.Open(path)
	if err != nil {
		return nil, err
	}
	return &schedulerService{store: store, stop: make(chan struct{}), stopped: make(chan struct{}), accepted: make(map[string]string)}, nil
}

func (s *schedulerService) Start(m *Manager) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer close(s.stopped)
		s.purge(time.Now())
		lastPurge := time.Now()
		for {
			select {
			case <-ticker.C:
				now := time.Now()
				s.deliverDue(m, now)
				if now.Sub(lastPurge) >= time.Hour {
					s.purge(now)
					lastPurge = now
				}
			case <-s.stop:
				return
			}
		}
	}()
}

func (s *schedulerService) Close() {
	s.once.Do(func() {
		close(s.stop)
		<-s.stopped
	})
}

func (s *schedulerService) create(record schedule.Schedule) (schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Create(record)
}

func (s *schedulerService) list() []schedule.Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.List()
}

// cancel refuses an occurrence that already reached the session, judged by the
// prompt in its history rather than by the record's status: the status update
// can fail to persist after the prompt was accepted.
func (s *schedulerService) cancel(sess *ManagedSession, id string) (schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.store.Get(id); ok && record.Status == schedule.StatusPending &&
		(s.isAccepted(id, record.OccurrenceID) || scheduleOccurrenceExists(sess.History(), record.OccurrenceID)) {
		if err := s.markDelivered(id, time.Now()); err != nil {
			slog.Error("recover schedule delivery", "schedule", id, "error", err)
		}
		return schedule.Schedule{}, schedule.ErrAlreadyDelivered
	}
	return s.store.Cancel(id)
}

func (s *schedulerService) deleteSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteSession(sessionID); err != nil {
		return err
	}
	s.pruneAcceptedLocked()
	return nil
}

func (s *schedulerService) purge(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.store.PurgeSettled(now.Add(-schedule.RetainSettled)); err != nil {
		slog.Warn("purge settled schedules", "error", err)
		return
	}
	s.pruneAcceptedLocked()
}

func (s *schedulerService) isAccepted(id, occurrenceID string) bool {
	got, ok := s.accepted[id]
	return ok && got == occurrenceID
}

func (s *schedulerService) pruneAcceptedLocked() {
	for id, occurrence := range s.accepted {
		if record, ok := s.store.Get(id); !ok || record.OccurrenceID != occurrence {
			delete(s.accepted, id)
		}
	}
}

// Lock order: session lifecycle, then s.mu. A caller holding a lifecycle lock
// (a slash command) may take s.mu; nothing may wait for a lifecycle lock while
// holding s.mu, or a pending lifecycle writer (session delete) closes a cycle
// with a command already inside the lifecycle read section.
func (s *schedulerService) deliverDue(m *Manager, now time.Time) {
	s.mu.Lock()
	var due []schedule.Schedule
	for _, record := range s.store.List() {
		if record.Status == schedule.StatusPending && !record.DueAt.After(now) {
			due = append(due, record)
		}
	}
	s.mu.Unlock()
	for _, record := range due {
		s.deliver(m, record, now)
	}
}

func (s *schedulerService) deliver(m *Manager, record schedule.Schedule, now time.Time) {
	sess, ok := m.Get(record.SessionID)
	// A session which exists only on disk must never be resumed merely to
	// deliver a schedule. It remains pending until its user opens it.
	if !ok || sess.runtime.State.Current() != bus.StateIdle {
		return
	}
	// Same lifecycle barrier the /send path takes: a schedule must not start
	// a run into a runtime that a concurrent close is tearing down.
	sess.lifecycle.RLock()
	defer sess.lifecycle.RUnlock()
	if sess.closing.Load() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The record may have been canceled or removed since the snapshot.
	if current, ok := s.store.Get(record.ID); !ok || current.Status != schedule.StatusPending {
		return
	}
	// Prompt persistence is the source of truth for exactly-once recovery.
	// If a previous process accepted the prompt but crashed before marking
	// this record delivered, do not ask the agent to perform it twice.
	if current, _ := s.store.Get(record.ID); s.isAccepted(record.ID, current.OccurrenceID) ||
		scheduleOccurrenceExists(sess.History(), record.OccurrenceID) {
		if err := s.markDelivered(record.ID, now); err != nil {
			slog.Error("recover schedule delivery", "schedule", record.ID, "error", err)
		}
		return
	}
	err := sess.runtime.Bus.Execute(bus.SendPrompt{
		Text: record.Text,
		Custom: map[string]any{
			"source":        "schedule",
			"schedule_id":   record.ID,
			"occurrence_id": record.OccurrenceID,
			"scheduled_for": record.DueAt.Format(time.RFC3339),
			"delivered_at":  now.UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return
	}
	s.accepted[record.ID] = record.OccurrenceID
	if err := s.markDelivered(record.ID, now); err != nil {
		// The prompt was accepted, so do not risk silently forgetting this
		// persistence failure; it will be retried after restart.
		slog.Error("mark schedule delivered", "schedule", record.ID, "error", err)
	}
}

func (s *schedulerService) markDelivered(id string, deliveredAt time.Time) error {
	record, err := s.store.MarkDelivered(id, deliveredAt)
	if err != nil {
		return err
	}
	if s.isAccepted(id, record.OccurrenceID) {
		delete(s.accepted, id)
	}
	return nil
}

func scheduleOccurrenceExists(messages []core.AgentMessage, occurrenceID string) bool {
	for _, message := range messages {
		if message.Custom == nil {
			continue
		}
		if message.Custom["source"] == "schedule" && message.Custom["occurrence_id"] == occurrenceID {
			return true
		}
	}
	return false
}
