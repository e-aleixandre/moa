package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The fence journal makes a Stop's fence outlive a crash. When the database
// refuses the fence a Stop commits, the session stays barred in memory (see
// questionService.unfenced), which a crash would forget: the answer accepted
// before the Stop would run again in the next process. So that Stop, and only
// Stop (a recall keeps its in-memory barrier), is also written to a file beside
// the task database. The next process reapplies it before anything is admitted.
//
// One file per session, never inside the session's own directory:
// <dir of tasks.sqlite>/question-fences/<sessionID>. Nothing here exists until
// a Stop's fence has failed, so with the pilot off, or never used, the
// directory is never created.

const questionFenceDir = "question-fences"

// fenceJournalEntry is the journal's content. Only the file name names the
// session; the rest is for whoever inspects the file.
type fenceJournalEntry struct {
	SessionID string    `json:"session_id"`
	StoppedAt time.Time `json:"stopped_at"`
}

func (q *questionService) journalDir() string {
	if q == nil || q.repo == nil || q.repo.Path() == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(q.repo.Path()), questionFenceDir)
}

// journalPath is the session's file, or "" when the ID cannot be one.
func (q *questionService) journalPath(sessionID string) string {
	dir := q.journalDir()
	if dir == "" || sessionID == "" || sessionID == "." || sessionID == ".." ||
		strings.HasPrefix(sessionID, ".") || strings.ContainsAny(sessionID, `/\`) {
		return ""
	}
	return filepath.Join(dir, sessionID)
}

// journalWrite durably records that the session's Stop fence is pending: a
// temporary file is written and synced, renamed into place, and the directory
// (and its parent, when it had to be created) synced.
func (q *questionService) journalWrite(sessionID string) error {
	path := q.journalPath(sessionID)
	if path == "" {
		return fmt.Errorf("no fence journal for session %q", sessionID)
	}
	dir := filepath.Dir(path)
	_, statErr := os.Stat(dir)
	created := errors.Is(statErr, os.ErrNotExist)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(fenceJournalEntry{SessionID: sessionID, StoppedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".fence-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := syncJournalDir(dir); err != nil {
		return err
	}
	if created {
		return syncJournalDir(filepath.Dir(dir))
	}
	return nil
}

// journalRemove drops the session's file once its fence is saved, or the
// session is gone. A file that is not there is the common case.
func (q *questionService) journalRemove(sessionID string) {
	path := q.journalPath(sessionID)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("questions: removing a fence journal failed", "session", sessionID, "error", err)
		}
		return
	}
	if err := syncJournalDir(filepath.Dir(path)); err != nil {
		slog.Warn("questions: syncing the fence journal directory failed", "session", sessionID, "error", err)
	}
}

// replayJournal runs when the Manager starts, before the dispatcher, a
// continuation or a seed can touch an answer: every Stop fence a previous
// process could not save is applied now. A fence that still cannot be saved
// leaves its file and bars the session, as the failed Stop did. Synchronous: it
// owns no goroutine that could write after Shutdown.
func (q *questionService) replayJournal() {
	dir := q.journalDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("questions: reading the fence journal failed", "error", err)
		}
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			if strings.HasPrefix(name, ".fence-") {
				_ = os.Remove(filepath.Join(dir, name)) // a write that never reached its rename
			}
			continue
		}
		func() {
			unlock := q.lock(name)
			defer unlock()
			if err := q.fenceLocked(q.m.baseCtx, name, true); err != nil {
				slog.Warn("questions: reapplying a Stop fence failed; the session stays barred", "session", name, "error", err)
				q.unfenced.Store(name, true)
				return
			}
			q.journalRemove(name)
		}()
	}
}

// syncJournalDir flushes a directory's entries; file systems that cannot are
// not an error.
func syncJournalDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close() //nolint:errcheck // cannot affect the completed sync
	if err := dir.Sync(); err != nil && !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.EOPNOTSUPP) && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
