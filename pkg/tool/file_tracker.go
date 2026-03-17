package tool

import (
	"sync"
	"time"
)

// FileTracker records when files were last read by the agent.
// Used by edit/write tools to warn when modifying files that haven't
// been read recently (stale edit protection).
type FileTracker struct {
	mu    sync.Mutex
	reads map[string]time.Time
}

// NewFileTracker creates a new file tracker.
func NewFileTracker() *FileTracker {
	return &FileTracker{reads: make(map[string]time.Time)}
}

// MarkRead records that a file was read at the current time.
func (ft *FileTracker) MarkRead(resolvedPath string) {
	ft.mu.Lock()
	ft.reads[resolvedPath] = time.Now()
	ft.mu.Unlock()
}

// LastRead returns when a file was last read. Returns zero time if never read.
func (ft *FileTracker) LastRead(resolvedPath string) time.Time {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.reads[resolvedPath]
}

// WasRead returns true if the file has been read at least once in this session.
func (ft *FileTracker) WasRead(resolvedPath string) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	_, ok := ft.reads[resolvedPath]
	return ok
}

// Clear resets the tracker (e.g. on session clear).
func (ft *FileTracker) Clear() {
	ft.mu.Lock()
	ft.reads = make(map[string]time.Time)
	ft.mu.Unlock()
}
