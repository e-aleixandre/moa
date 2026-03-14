// Package checkpoint provides file-level undo for agent turns.
//
// Before each write/edit tool modifies a file, Capture snapshots its current
// content. When the user runs /undo, the most recent checkpoint is popped and
// all files are restored to their pre-modification state.
//
// Checkpoints are in-memory only (lost on process exit) and scoped to a
// session. A circular buffer keeps the most recent N checkpoints.
//
// v1 limitation: only covers write/edit builtins. Changes via bash, MCP,
// extensions, or subagents are not tracked.
package checkpoint

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"
)

// Snapshot holds the pre-modification state of one file.
type Snapshot struct {
	Path    string      // absolute resolved path
	Content []byte      // original content; nil means file didn't exist (created by agent)
	Perm    fs.FileMode // original permissions
}

// Checkpoint groups all snapshots from one agent turn.
type Checkpoint struct {
	ID        int
	CreatedAt time.Time
	Label     string     // e.g. first 60 chars of user message
	Files     []Snapshot // files modified in this turn
}

// Summary is the public info for listing checkpoints.
type Summary struct {
	ID        int       `json:"id"`
	Label     string    `json:"label"`
	FileCount int       `json:"file_count"`
	CreatedAt time.Time `json:"created_at"`
}

type activeCheckpoint struct {
	label    string
	captured map[string]Snapshot // keyed by absolute path
	started  time.Time
}

// Store is an in-memory, thread-safe checkpoint store with a circular buffer.
type Store struct {
	mu     sync.Mutex
	ring   []Checkpoint // circular buffer
	head   int          // next write position
	count  int          // used slots
	cap    int          // buffer capacity
	nextID int
	active *activeCheckpoint // nil when no turn in progress
}

// New creates a Store with the given capacity (number of checkpoints retained).
func New(capacity int) *Store {
	if capacity <= 0 {
		capacity = 20
	}
	return &Store{
		ring: make([]Checkpoint, capacity),
		cap:  capacity,
	}
}

// Begin opens a checkpoint for the current turn. Thread-safe.
// If a previous active checkpoint exists (stale from aborted run), it's discarded.
func (s *Store) Begin(label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = &activeCheckpoint{
		label:    label,
		captured: make(map[string]Snapshot),
		started:  time.Now(),
	}
}

// Capture snapshots a file before modification. Thread-safe.
// No-op if already captured in this turn or if no active checkpoint.
// Returns error on I/O failure reading the file to snapshot.
func (s *Store) Capture(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active == nil {
		return nil // no active checkpoint — tools called outside a turn
	}
	if _, exists := s.active.captured[path]; exists {
		return nil // already captured in this turn
	}

	snap := Snapshot{Path: path}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// File doesn't exist yet — will be created by the write.
			// Undo should delete it.
			snap.Content = nil
			snap.Perm = 0o644
			s.active.captured[path] = snap
			return nil
		}
		return fmt.Errorf("checkpoint: stat %s: %w", path, err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("checkpoint: read %s: %w", path, err)
	}

	snap.Content = content
	snap.Perm = info.Mode().Perm()
	s.active.captured[path] = snap
	return nil
}

// Commit closes the active checkpoint and adds it to the ring.
// No-op if no active checkpoint or if no files were captured.
func (s *Store) Commit() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active == nil || len(s.active.captured) == 0 {
		s.active = nil
		return
	}

	s.nextID++
	cp := Checkpoint{
		ID:        s.nextID,
		CreatedAt: s.active.started,
		Label:     s.active.label,
		Files:     make([]Snapshot, 0, len(s.active.captured)),
	}
	for _, snap := range s.active.captured {
		cp.Files = append(cp.Files, snap)
	}

	s.ring[s.head] = cp
	s.head = (s.head + 1) % s.cap
	if s.count < s.cap {
		s.count++
	}
	s.active = nil
}

// Discard closes the active checkpoint without saving.
func (s *Store) Discard() {
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
}

// Repush puts a checkpoint back on the ring buffer after a failed undo.
// This allows the caller to retry /undo after fixing the restore failure.
func (s *Store) Repush(cp *Checkpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring[s.head] = *cp
	s.head = (s.head + 1) % s.cap
	if s.count < s.cap {
		s.count++
	}
}

// Undo pops the most recent checkpoint and returns it for the caller to restore.
// If restoration fails, call Repush to put it back for retry.
// Returns error if no checkpoints exist or if a turn is in progress.
func (s *Store) Undo() (*Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active != nil {
		return nil, fmt.Errorf("cannot undo while agent is running")
	}
	if s.count == 0 {
		return nil, fmt.Errorf("no checkpoints to undo")
	}

	// Pop from the ring (head-1 is the most recent).
	s.head--
	if s.head < 0 {
		s.head = s.cap - 1
	}
	s.count--

	cp := s.ring[s.head]
	s.ring[s.head] = Checkpoint{} // clear slot
	return &cp, nil
}

// List returns summaries of all checkpoints, newest first.
func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Summary, 0, s.count)
	for i := 0; i < s.count; i++ {
		// Walk backwards from head-1.
		idx := s.head - 1 - i
		if idx < 0 {
			idx += s.cap
		}
		cp := s.ring[idx]
		result = append(result, Summary{
			ID:        cp.ID,
			Label:     cp.Label,
			FileCount: len(cp.Files),
			CreatedAt: cp.CreatedAt,
		})
	}
	return result
}
