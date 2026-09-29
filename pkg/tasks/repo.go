package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DatabaseName is the file inside the config directory.
const DatabaseName = "tasks.sqlite"

// archiveAfter is how long a done task stays in Done before it is archived.
const archiveAfter = 7 * 24 * time.Hour

// migrations are applied in order; PRAGMA user_version is how many ran. Later
// versions must be additive: an older moa opens a newer database read-only
// rather than guessing.
var migrations = []string{
	`CREATE TABLE tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL CHECK(length(trim(title)) > 0),
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK(status IN ('pending','in_progress','done')),
  place TEXT NOT NULL CHECK(place IN ('you','backlog','agent')),
  project_key TEXT, project_cwd TEXT,
  requester_session_id TEXT, assignee_session_id TEXT,
  completion_note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  completed_at INTEGER, archived_at INTEGER,
  revision INTEGER NOT NULL DEFAULT 1,
  CHECK(place <> 'backlog' OR (project_key IS NOT NULL AND requester_session_id IS NULL AND assignee_session_id IS NULL)),
  CHECK(place <> 'agent' OR assignee_session_id IS NOT NULL),
  CHECK(place <> 'you' OR assignee_session_id IS NULL)
);
CREATE TABLE task_subtasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  title TEXT NOT NULL CHECK(length(trim(title)) > 0),
  done INTEGER NOT NULL DEFAULT 0 CHECK(done IN (0,1)),
  position INTEGER NOT NULL
);
CREATE TABLE task_dependencies (
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  waits_for_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  PRIMARY KEY(task_id, waits_for_id),
  CHECK(task_id <> waits_for_id)
);
CREATE TABLE tasks_meta (
  id INTEGER PRIMARY KEY CHECK(id = 1),
  revision INTEGER NOT NULL
);
INSERT INTO tasks_meta(id, revision) VALUES (1, 0);
CREATE INDEX tasks_place_project ON tasks(place, project_key, archived_at, status);
CREATE INDEX tasks_assignee ON tasks(assignee_session_id, archived_at);
CREATE INDEX tasks_requester ON tasks(requester_session_id, place, archived_at);
CREATE INDEX task_subtasks_task ON task_subtasks(task_id, position);
CREATE INDEX task_dependencies_waits ON task_dependencies(waits_for_id);`,
}

// Repo is the shared task database. It opens lazily: a process that never
// touches tasks never creates the file, and reads of a database that does not
// exist yet see no tasks instead of creating one.
type Repo struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	w      *sql.DB // exactly one connection: this process's writer
	r      *sql.DB // readers
	tooNew bool
	closed bool

	changed chan struct{} // nudges Watch after a local commit
}

// New returns a repository on path. Nothing touches the disk until first use.
func New(path string) *Repo {
	return &Repo{path: path, now: time.Now, changed: make(chan struct{}, 1)}
}

var (
	sharedMu sync.Mutex
	shared   = map[string]*Repo{}
)

// Shared returns the process-wide repository for the current config
// directory. Runtime, CLI and serve all go through it, so there is one
// writer connection per process and never a per-session copy.
func Shared() *Repo {
	path := core.ConfigSubdir(DatabaseName)
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if r, ok := shared[path]; ok && !r.isClosed() {
		return r
	}
	r := New(path)
	shared[path] = r
	return r
}

func (r *Repo) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Path is the database file, "" when it cannot be resolved.
func (r *Repo) Path() string { return r.path }

// SetClock replaces the clock; tests use it to age tasks past the archive
// window without sleeping.
func (r *Repo) SetClock(now func() time.Time) { r.now = now }

// Close releases the connections.
func (r *Repo) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	var errs []error
	if r.r != nil {
		errs = append(errs, r.r.Close())
		r.r = nil
	}
	if r.w != nil {
		errs = append(errs, r.w.Close())
		r.w = nil
	}
	return errors.Join(errs...)
}

func dsn(path string, writer bool) string {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if writer {
		// Every write transaction starts with BEGIN IMMEDIATE: read, validate
		// and write happen under one lock, so a check-then-act cannot be
		// interleaved with another process's write.
		q.Set("_txlock", "immediate")
	} else {
		q.Add("_pragma", "query_only(1)")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ensure opens the databases. With create=false and no file it returns
// (nil, nil, false, nil).
func (r *Repo) ensure(create bool) (w, rd *sql.DB, tooNew bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, false, fmt.Errorf("%w: closed", ErrUnavailable)
	}
	if r.w != nil {
		return r.w, r.r, r.tooNew, nil
	}
	if r.path == "" {
		return nil, nil, false, fmt.Errorf("%w: cannot resolve the config directory", ErrUnavailable)
	}
	if _, statErr := os.Stat(r.path); statErr != nil {
		if !os.IsNotExist(statErr) {
			return nil, nil, false, fmt.Errorf("%w: %v", ErrUnavailable, statErr)
		}
		if !create {
			return nil, nil, false, nil
		}
		if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
			return nil, nil, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		// Create the file ourselves so it is 0600 from the start; SQLite gives
		// the -wal and -shm files the same mode.
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, nil, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		_ = f.Close()
	}
	wdb, tooNew, err := openWriter(r.path)
	if err != nil {
		return nil, nil, false, err
	}
	rdb, err := sql.Open("sqlite", dsn(r.path, false))
	if err != nil {
		_ = wdb.Close()
		return nil, nil, false, err
	}
	rdb.SetMaxOpenConns(4)
	r.w, r.r, r.tooNew = wdb, rdb, tooNew
	return r.w, r.r, r.tooNew, nil
}

// openWriter opens the writer connection and migrates. Two processes
// creating the database at the same moment can make the first one's switch to
// WAL answer BUSY without consulting busy_timeout, so the whole open is retried.
func openWriter(path string) (db *sql.DB, tooNew bool, err error) {
	for attempt := 0; attempt < 20; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
		}
		db, err = sql.Open("sqlite", dsn(path, true))
		if err != nil {
			return nil, false, err
		}
		db.SetMaxOpenConns(1)
		tooNew, err = migrate(db)
		if err == nil {
			return db, tooNew, nil
		}
		_ = db.Close()
		if !isBusy(err) {
			return nil, false, err
		}
	}
	return nil, false, err
}

// migrate brings the schema to the latest version. A database from a newer
// moa is left untouched and reported.
func migrate(db *sql.DB) (tooNew bool, err error) {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return false, err
	}
	if version > len(migrations) {
		return true, nil
	}
	if version == len(migrations) {
		return false, nil
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return false, fmt.Errorf("tasks migration %d: %w", i+1, err)
		}
	}
	// PRAGMA does not accept bound parameters.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	}
	return false
}

// write runs fn in one write transaction. fn returns whether it changed
// anything: a change bumps the global revision the watchers compare. A BUSY
// outcome retries the whole transaction, never its last statement, and is
// never turned into success.
func (r *Repo) write(ctx context.Context, fn func(tx *sql.Tx) (changed bool, err error)) error {
	w, _, tooNew, err := r.ensure(true)
	if err != nil {
		return err
	}
	if tooNew {
		return ErrSchemaTooNew
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
			}
		}
		lastErr = r.writeOnce(ctx, w, fn)
		if lastErr == nil || !isBusy(lastErr) {
			break
		}
	}
	if lastErr == nil {
		select {
		case r.changed <- struct{}{}:
		default:
		}
	}
	return lastErr
}

func (r *Repo) writeOnce(ctx context.Context, w *sql.DB, fn func(tx *sql.Tx) (bool, error)) error {
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	changed, err := fn(tx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if changed {
		if _, err := tx.ExecContext(ctx, "UPDATE tasks_meta SET revision = revision + 1 WHERE id = 1"); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// reader returns the read pool, or nil when the database does not exist yet.
func (r *Repo) reader() (*sql.DB, error) {
	w, rd, _, err := r.ensure(false)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, nil
	}
	return rd, nil
}

// Revision is the global change counter: it grows with every committed change
// by any process. 0 when the database does not exist yet.
func (r *Repo) Revision(ctx context.Context) (int64, error) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return 0, err
	}
	var rev int64
	err = rd.QueryRowContext(ctx, "SELECT revision FROM tasks_meta WHERE id = 1").Scan(&rev)
	return rev, err
}

// archiveDue archives done tasks whose seven days since completion have
// passed. It runs on read, in an idempotent transaction, and only writes when
// there is something to archive. Failure to archive never fails a read.
func (r *Repo) archiveDue(ctx context.Context) {
	rd, err := r.reader()
	if err != nil || rd == nil {
		return
	}
	cutoff := r.now().Add(-archiveAfter).UnixMilli()
	var due bool
	if err := rd.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks
		WHERE status = 'done' AND archived_at IS NULL AND completed_at IS NOT NULL AND completed_at <= ?)`, cutoff).Scan(&due); err != nil || !due {
		return
	}
	now := r.now().UnixMilli()
	_ = r.write(ctx, func(tx *sql.Tx) (bool, error) {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET archived_at = ?, updated_at = ?, revision = revision + 1
			WHERE status = 'done' AND archived_at IS NULL AND completed_at IS NOT NULL AND completed_at <= ?`, now, now, cutoff)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	})
}
