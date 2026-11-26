package tool

import (
	"fmt"
	"os"
	"sync"
)

// fileStamp is what a file looked like when the agent last saw it.
type fileStamp struct {
	mtimeNano int64
	size      int64
	exists    bool
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{mtimeNano: info.ModTime().UnixNano(), size: info.Size(), exists: true}
}

// FileTracker records which files the agent has seen, and how they looked.
// Used by the edit tools for stale edit protection: a file the agent never
// read, or one that changed since it last read or wrote it, is refused.
type FileTracker struct {
	mu    sync.Mutex
	reads map[string]fileStamp
}

// NewFileTracker creates a new file tracker.
func NewFileTracker() *FileTracker {
	return &FileTracker{reads: make(map[string]fileStamp)}
}

// MarkRead records that the agent has seen the file as it is now (after a
// read or after its own successful write).
func (ft *FileTracker) MarkRead(resolvedPath string) {
	st := stampOf(resolvedPath)
	ft.mu.Lock()
	ft.reads[resolvedPath] = st
	ft.mu.Unlock()
}

// WasRead returns true if the file has been read at least once in this session.
func (ft *FileTracker) WasRead(resolvedPath string) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	_, ok := ft.reads[resolvedPath]
	return ok
}

// Forget drops a path from the tracker (the agent deleted or moved it away).
func (ft *FileTracker) Forget(resolvedPath string) {
	ft.mu.Lock()
	delete(ft.reads, resolvedPath)
	ft.mu.Unlock()
}

// Changed reports whether a file the agent has seen differs (mtime or size)
// from how it looked then. Unseen files are not "changed" (WasRead covers
// them) and neither are missing ones: there is nothing to clobber.
func (ft *FileTracker) Changed(resolvedPath string) bool {
	ft.mu.Lock()
	st, ok := ft.reads[resolvedPath]
	ft.mu.Unlock()
	if !ok {
		return false
	}
	now := stampOf(resolvedPath)
	return now.exists && st != now
}

// checkFresh guards the tools that replace or remove a whole file blind
// (write, Delete): they refuse when the file changed since the agent saw it.
func checkFresh(cfg ToolConfig, resolved, display string) error {
	if cfg.FileTracker != nil && cfg.FileTracker.Changed(resolved) {
		return fmt.Errorf("%s changed since you last read it; read it again before modifying it", display)
	}
	return nil
}

// staleNote is for the tools that match their old text against the disk
// (edit, multiedit, Update): they still apply, but say that the file moved
// under the agent so it can recheck the result.
func staleNote(cfg ToolConfig, resolved, display string) string {
	if cfg.FileTracker != nil && cfg.FileTracker.Changed(resolved) {
		return fmt.Sprintf("Note: %s changed outside this session since you last read it; the change was applied to its current content. Read it again before rewriting it whole.", display)
	}
	return ""
}
