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

// errFenceBarred is what answering a session whose Stop fence is not saved
// returns: the answer would otherwise be accepted after the Stop, and the fence
// that is eventually saved would cover it.
var errFenceBarred = errors.New("this session's last Stop could not be saved yet")

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
// session is gone, and reports whether that removal is durable. Until it is,
// the file still claims a Stop over the session's answers, so callers keep the
// session barred: an answer accepted now would be stopped by the stale file at
// the next start. A file already gone is still synced away, in case its unlink
// was not.
func (q *questionService) journalRemove(sessionID string) error {
	path := q.journalPath(sessionID)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := syncJournalDir(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil // no journal directory: there never was a file
	}
	return err
}

// readJournal lists the sessions with a pending Stop fence. A directory that
// does not exist means none; one that cannot be read is an error, never "none".
func (q *questionService) readJournal() ([]string, error) {
	dir := q.journalDir()
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			if strings.HasPrefix(name, ".fence-") {
				_ = os.Remove(filepath.Join(dir, name)) // a write that never reached its rename
			}
			continue
		}
		ids = append(ids, name)
	}
	return ids, nil
}

// replayJournal runs when the Manager starts, before the dispatcher, a
// continuation or a seed can touch an answer: every session with a pending
// Stop fence is barred, then its fence is applied and its file removed where
// the stores allow it (barredLocked). A journal that cannot be read bars every
// session's answers (journalUnread) until it can. Synchronous: it owns no
// goroutine that could write after Shutdown.
func (q *questionService) replayJournal() {
	ids, err := q.readJournal()
	if err != nil {
		q.journalUnread.Store(true)
		slog.Error("questions: the fence journal cannot be read; answers stay barred until it can", "error", err)
		return
	}
	for _, id := range ids {
		func() {
			unlock := q.lock(id)
			defer unlock()
			q.unfenced.Store(id, true)
			q.barredLocked(q.m.baseCtx, id)
		}()
	}
}

// loadJournalLocked turns a journal that could not be read into per-session
// barriers once it can be. Callers hold some session's lock; other sessions
// are only marked, and settle on their own next barredLocked.
func (q *questionService) loadJournalLocked() bool {
	ids, err := q.readJournal()
	if err != nil {
		return false
	}
	for _, id := range ids {
		q.unfenced.Store(id, true)
	}
	q.journalUnread.Store(false)
	return true
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
