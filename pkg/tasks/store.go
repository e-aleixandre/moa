// Package tasks provides a standalone task tracking system.
// It can be used independently or as part of plan mode.
package tasks

import (
	"encoding/json"
	"sync"
	"time"
)

// WidgetMode controls how the task widget is displayed.
type WidgetMode string

const (
	WidgetAll     WidgetMode = "all"
	WidgetCurrent WidgetMode = "current"
	WidgetHidden  WidgetMode = "hidden"
)

// Task tracks a unit of work.
type Task struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"` // "pending", "in_progress", "done"
	DependsOn   []int  `json:"depends_on,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"`   // unix millis
	CompletedAt int64  `json:"completed_at,omitempty"` // unix millis
}

// State is the serializable snapshot of the task store.
type State struct {
	Tasks      []Task     `json:"tasks"`
	NextTaskID int        `json:"next_task_id"`
	WidgetMode WidgetMode `json:"widget_mode,omitempty"`
}

// Store manages tasks with thread-safe access.
type Store struct {
	mu    sync.Mutex
	state State
}

// NewStore creates an empty task store.
func NewStore() *Store {
	return &Store{
		state: State{WidgetMode: WidgetAll},
	}
}

// Tasks returns a copy of the current task list.
func (s *Store) Tasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, len(s.state.Tasks))
	copy(out, s.state.Tasks)
	return out
}

// Progress returns (done, total) task counts.
func (s *Store) Progress() (done, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress()
}

func (s *Store) progress() (done, total int) {
	total = len(s.state.Tasks)
	for _, t := range s.state.Tasks {
		if t.Status == "done" {
			done++
		}
	}
	return
}

// AllDone returns true if all tasks are complete and there's at least one task.
func (s *Store) AllDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	done, total := s.progress()
	return total > 0 && done == total
}

// Create adds a new task. Returns the created task.
func (s *Store) Create(title, description string, dependsOn []int) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.NextTaskID++
	t := Task{
		ID:        s.state.NextTaskID,
		Title:     title,
		Description: description,
		Status:    "pending",
		DependsOn: dependsOn,
		CreatedAt: time.Now().UnixMilli(),
	}
	s.state.Tasks = append(s.state.Tasks, t)
	return t
}

// Update modifies a task's fields. Returns false if not found.
func (s *Store) Update(id int, title, description *string, dependsOn *[]int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.find(id)
	if t == nil {
		return false
	}
	if title != nil && *title != "" {
		t.Title = *title
	}
	if description != nil {
		t.Description = *description
	}
	if dependsOn != nil {
		t.DependsOn = *dependsOn
	}
	return true
}

// MarkDone marks a task as done. Returns false if not found.
func (s *Store) MarkDone(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.find(id)
	if t == nil {
		return false
	}
	t.Status = "done"
	t.CompletedAt = time.Now().UnixMilli()
	return true
}

// Get returns a copy of a task by ID. Returns (Task{}, false) if not found.
func (s *Store) Get(id int) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.find(id)
	if t == nil {
		return Task{}, false
	}
	return *t, true
}

// Reset clears all tasks.
func (s *Store) Reset() {
	s.mu.Lock()
	s.state.Tasks = nil
	s.state.NextTaskID = 0
	s.mu.Unlock()
}

// WidgetMode returns the current widget display mode.
func (s *Store) GetWidgetMode() WidgetMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.WidgetMode
}

// SetWidgetMode changes the widget display mode.
func (s *Store) SetWidgetMode(mode WidgetMode) {
	s.mu.Lock()
	s.state.WidgetMode = mode
	s.mu.Unlock()
}

// SaveState returns the serializable state for session persistence.
func (s *Store) SaveState() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.Tasks = make([]Task, len(s.state.Tasks))
	copy(st.Tasks, s.state.Tasks)
	return st
}

// RestoreState loads state from a saved snapshot.
func (s *Store) RestoreState(st State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = st
	switch s.state.WidgetMode {
	case WidgetAll, WidgetCurrent, WidgetHidden:
		// valid
	default:
		s.state.WidgetMode = WidgetAll
	}
}

func (s *Store) find(id int) *Task {
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == id {
			return &s.state.Tasks[i]
		}
	}
	return nil
}

// --- Session metadata helpers ---

const metadataKey = "tasks"

// SaveToMetadata serializes task state for session.Metadata.
func (s *Store) SaveToMetadata() map[string]any {
	st := s.SaveState()
	data, _ := json.Marshal(st)
	var m map[string]any
	json.Unmarshal(data, &m)
	return map[string]any{metadataKey: m}
}

// RestoreFromMetadata loads task state from session.Metadata.
func (s *Store) RestoreFromMetadata(meta map[string]any) {
	raw, ok := meta[metadataKey]
	if !ok {
		return
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	s.RestoreState(st)
}
