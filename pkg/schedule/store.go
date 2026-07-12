// Package schedule provides durable one-shot schedule records.
package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	StatusPending   = "pending"
	StatusCanceled  = "canceled"
	StatusDelivered = "delivered"
)

var ErrNotFound = errors.New("schedule not found")

// Schedule is a single, durable delivery occurrence. All stored timestamps are
// normalized to UTC.
type Schedule struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"session_id"`
	Text         string    `json:"text"`
	DueAt        time.Time `json:"due_at"`
	TimeZone     string    `json:"time_zone"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	DeliveredAt  time.Time `json:"delivered_at,omitempty"`
	OccurrenceID string    `json:"occurrence_id"`
}

// Store keeps schedules in a caller-selected JSON file. Successful mutating
// operations are persisted before they return.
type Store struct {
	mu        sync.RWMutex
	path      string
	schedules map[string]Schedule
}

// NewStore creates an empty store for path. Call Load or Open to restore an
// existing file.
func NewStore(path string) *Store {
	return &Store{path: path, schedules: make(map[string]Schedule)}
}

// Open loads the store at path. A missing file is an empty store.
func Open(path string) (*Store, error) {
	s := NewStore(path)
	if err := s.Load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load is an alias for Open.
func Load(path string) (*Store, error) {
	return Open(path)
}

// Path returns the file used by the store.
func (s *Store) Path() string { return s.path }

// Load replaces the in-memory records with those in the store file. A missing
// file is treated as an empty store.
func (s *Store) Load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.mu.Lock()
		s.schedules = make(map[string]Schedule)
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}

	var records []Schedule
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("decode schedules: %w", err)
	}
	recordsByID := make(map[string]Schedule, len(records))
	for _, record := range records {
		if record.ID == "" {
			return errors.New("decode schedules: record has empty id")
		}
		if _, exists := recordsByID[record.ID]; exists {
			return fmt.Errorf("decode schedules: duplicate id %q", record.ID)
		}
		record.DueAt = record.DueAt.UTC()
		record.CreatedAt = record.CreatedAt.UTC()
		record.DeliveredAt = record.DeliveredAt.UTC()
		recordsByID[record.ID] = record
	}

	s.mu.Lock()
	s.schedules = recordsByID
	s.mu.Unlock()
	return nil
}

// Save writes the complete store atomically.
func (s *Store) Save() error {
	s.mu.RLock()
	records := s.recordsLocked()
	s.mu.RUnlock()
	return save(s.path, records)
}

// Create persists record and returns its fully populated form. ID,
// OccurrenceID, CreatedAt, and pending Status are assigned when omitted.
func (s *Store) Create(record Schedule) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(record.SessionID) == "" {
		return Schedule{}, errors.New("schedule session id is required")
	}
	if strings.TrimSpace(record.Text) == "" {
		return Schedule{}, errors.New("schedule text is required")
	}
	if record.DueAt.IsZero() {
		return Schedule{}, errors.New("schedule due time is required")
	}
	if strings.TrimSpace(record.TimeZone) == "" {
		return Schedule{}, errors.New("schedule time zone is required")
	}
	if record.ID == "" {
		var err error
		record.ID, err = newID()
		if err != nil {
			return Schedule{}, err
		}
	}
	if _, exists := s.schedules[record.ID]; exists {
		return Schedule{}, fmt.Errorf("schedule %q already exists", record.ID)
	}
	if record.OccurrenceID == "" {
		var err error
		record.OccurrenceID, err = newID()
		if err != nil {
			return Schedule{}, err
		}
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.Status == "" {
		record.Status = StatusPending
	}
	record.DueAt = record.DueAt.UTC()
	record.CreatedAt = record.CreatedAt.UTC()
	record.DeliveredAt = record.DeliveredAt.UTC()

	s.schedules[record.ID] = record
	if err := s.saveLocked(); err != nil {
		delete(s.schedules, record.ID)
		return Schedule{}, err
	}
	return record, nil
}

// List returns all records ordered by due time and then ID.
func (s *Store) List() []Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.recordsLocked()
}

// Get returns a record by ID.
func (s *Store) Get(id string) (Schedule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.schedules[id]
	return record, ok
}

// Cancel marks a record canceled and persists it. Canceling an already
// canceled record succeeds without changing it.
func (s *Store) Cancel(id string) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.schedules[id]
	if !ok {
		return Schedule{}, ErrNotFound
	}
	if record.Status == StatusCanceled {
		return record, nil
	}
	previousStatus := record.Status
	record.Status = StatusCanceled
	s.schedules[id] = record
	if err := s.saveLocked(); err != nil {
		record.Status = previousStatus
		s.schedules[id] = record
		return Schedule{}, err
	}
	return record, nil
}

// MarkDelivered records that a schedule's single occurrence was accepted by
// its delivery target. It is idempotent so recovery code can safely repeat it
// after a crash between prompt persistence and this store update.
func (s *Store) MarkDelivered(id string, deliveredAt time.Time) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.schedules[id]
	if !ok {
		return Schedule{}, ErrNotFound
	}
	if record.Status == StatusDelivered {
		return record, nil
	}
	if record.Status != StatusPending {
		return Schedule{}, fmt.Errorf("schedule %q cannot be delivered from status %s", id, record.Status)
	}
	record.Status = StatusDelivered
	record.DeliveredAt = deliveredAt.UTC()
	s.schedules[id] = record
	if err := s.saveLocked(); err != nil {
		record.Status = StatusPending
		record.DeliveredAt = time.Time{}
		s.schedules[id] = record
		return Schedule{}, err
	}
	return record, nil
}

func (s *Store) recordsLocked() []Schedule {
	records := make([]Schedule, 0, len(s.schedules))
	for _, record := range s.schedules {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].DueAt.Equal(records[j].DueAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].DueAt.Before(records[j].DueAt)
	})
	return records
}

func (s *Store) saveLocked() error {
	return save(s.path, s.recordsLocked())
}

func save(path string, records []Schedule) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("encode schedules: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".schedules-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	err = tmp.Chmod(0o600)
	if err == nil {
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
	if directory, derr := os.Open(dir); derr == nil {
		err = directory.Sync()
		if closeErr := directory.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate schedule id: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}
