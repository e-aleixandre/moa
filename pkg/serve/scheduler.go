package serve

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/schedule"
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
}

func newSchedulerService(path string) (*schedulerService, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create schedule directory: %w", err)
	}
	store, err := schedule.Open(path)
	if err != nil {
		return nil, err
	}
	return &schedulerService{store: store, stop: make(chan struct{}), stopped: make(chan struct{})}, nil
}

func (s *schedulerService) Start(m *Manager) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer close(s.stopped)
		for {
			select {
			case <-ticker.C:
				s.deliverDue(m, time.Now())
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

func (s *schedulerService) cancel(id string) (schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Cancel(id)
}

func (s *schedulerService) deliverDue(m *Manager, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.store.List() {
		if record.Status != schedule.StatusPending || record.DueAt.After(now) {
			continue
		}
		sess, ok := m.Get(record.SessionID)
		// A session which exists only on disk must never be resumed merely to
		// deliver a schedule. It remains pending until its user opens it.
		if !ok || sess.runtime.State.Current() != bus.StateIdle {
			continue
		}
		if err := sess.runtime.Bus.Execute(bus.SendPrompt{
			Text: record.Text,
			Custom: map[string]any{
				"source":        "schedule",
				"schedule_id":   record.ID,
				"occurrence_id": record.OccurrenceID,
			},
		}); err != nil {
			continue
		}
		if err := s.markDelivered(record.ID, now); err != nil {
			// The prompt was accepted, so do not risk silently forgetting this
			// persistence failure; it will be retried after restart.
			slog.Error("mark schedule delivered", "schedule", record.ID, "error", err)
		}
	}
}

// markDelivered is intentionally kept in the serve-owned service: pkg/schedule
// currently exposes creation, listing, and cancellation only. Reopen the core
// store after atomically replacing its JSON representation so its in-memory
// view remains authoritative for subsequent commands.
func (s *schedulerService) markDelivered(id string, deliveredAt time.Time) error {
	records := s.store.List()
	found := false
	for i := range records {
		if records[i].ID == id {
			records[i].Status = schedule.StatusDelivered
			records[i].DeliveredAt = deliveredAt.UTC()
			found = true
			break
		}
	}
	if !found {
		return schedule.ErrNotFound
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.store.Path()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".schedules-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		err = dir.Sync()
		closeErr := dir.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	store, err := schedule.Open(path)
	if err != nil {
		return err
	}
	s.store = store
	return nil
}
