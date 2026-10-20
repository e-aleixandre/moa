package tasks

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	// 2: the notice outbox. A row is written in the same transaction as the
	// owner gesture that causes it and carries its rendered text, so it has no
	// foreign key: it outlives a deleted task.
	`CREATE TABLE task_notifications (
  id TEXT PRIMARY KEY,
  task_id INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('assigned','updated','request_done','agent_done','agent_deleted')),
  recipient_session_id TEXT NOT NULL,
  deliver TEXT NOT NULL CHECK(deliver IN ('wake','hold')),
  method TEXT NOT NULL CHECK(method IN ('run','append')),
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('pending','held','sent','delivered','failed')),
  reason TEXT NOT NULL DEFAULT '',
  steer_id TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, delivered_at INTEGER
);
CREATE INDEX task_notifications_task ON task_notifications(task_id, created_at);
CREATE INDEX task_notifications_open ON task_notifications(state, created_at);`,
	// 3: scheduled templates and their runs. A template is an ordinary private
	// task with one task_schedules row; each consumed slot is an occurrence.
	// Occurrences have no foreign keys on purpose: an authorized run outlives
	// its template, and its history outlives its child task. Live links are
	// kept by unique columns and by the transactions that delete or complete.
	`ALTER TABLE tasks_meta ADD COLUMN legacy_schedules_imported INTEGER NOT NULL DEFAULT 0
  CHECK(legacy_schedules_imported IN (0,1));
CREATE TABLE task_schedules (
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  when_json TEXT NOT NULL CHECK(json_valid(when_json)),
  tz TEXT NOT NULL CHECK(length(tz) > 0),
  target_json TEXT NOT NULL CHECK(json_valid(target_json)),
  busy TEXT NOT NULL DEFAULT 'steer' CHECK(busy IN ('steer','wait')),
  saved TEXT NOT NULL DEFAULT 'wake' CHECK(saved IN ('wake','hold')),
  late TEXT NOT NULL DEFAULT 'ask' CHECK(late IN ('ask','run','skip')),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  next_due_at INTEGER CHECK(next_due_at IS NULL OR next_due_at > 0),
  created_by_session_id TEXT,
  CHECK(json_extract(when_json,'$.kind') IS 'once' OR json_extract(when_json,'$.kind') IS 'repeat'),
  CHECK(json_extract(target_json,'$.kind') IS 'session' OR json_extract(target_json,'$.kind') IS 'owner'
     OR json_extract(target_json,'$.kind') IS 'new')
);
CREATE INDEX task_schedules_due ON task_schedules(next_due_at, task_id) WHERE enabled = 1 AND next_due_at IS NOT NULL;
CREATE INDEX task_schedules_creator ON task_schedules(created_by_session_id);
CREATE TABLE task_occurrences (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  schedule_task_id INTEGER NOT NULL,
  due_at INTEGER NOT NULL CHECK(due_at > 0),
  definition_revision INTEGER NOT NULL CHECK(definition_revision > 0),
  spec_json TEXT NOT NULL CHECK(json_valid(spec_json)),
  trigger TEXT NOT NULL CHECK(trigger IN ('timer','run_now','skip_next','legacy')),
  observed_at INTEGER NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('late','ready','assigned','done','failed','skipped')),
  reason TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  confirmed_at INTEGER,
  resolved_session_id TEXT,
  child_task_id INTEGER UNIQUE,
  notice_id TEXT UNIQUE,
  admitted_at INTEGER,
  completed_at INTEGER,
  missed_count INTEGER NOT NULL DEFAULT 0 CHECK(missed_count >= 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
  UNIQUE(schedule_task_id, due_at),
  CHECK(notice_id IS NULL OR (child_task_id IS NOT NULL AND resolved_session_id IS NOT NULL)),
  CHECK(state <> 'assigned' OR notice_id IS NOT NULL),
  CHECK(state <> 'late' OR (child_task_id IS NULL AND notice_id IS NULL AND confirmed_at IS NULL))
);
CREATE INDEX task_occurrences_work ON task_occurrences(state, due_at, id);
CREATE INDEX task_occurrences_session ON task_occurrences(resolved_session_id, state);`,
	// 4: a saved session whose delete is between its file's unlink and the
	// settlement of its runs. The file cannot come back with a rollback, so
	// the mark is committed first: nothing recreates or delivers to it, and
	// a start after a crash finishes or undoes the delete.
	`CREATE TABLE session_discards (
  session_id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL
);
ALTER TABLE task_occurrences ADD COLUMN reserved_session_id TEXT;
CREATE UNIQUE INDEX task_occurrences_reserved ON task_occurrences(reserved_session_id)
  WHERE reserved_session_id IS NOT NULL;`,
}

// Repo is the shared task database. It opens lazily: a process that never
// touches tasks never creates the file, and reads of a database that does not
// exist yet see no tasks instead of creating one.
type Repo struct {
	path  string
	clock atomic.Pointer[func() time.Time]

	mu     sync.Mutex
	w      *sql.DB // exactly one connection: this process's writer
	r      *sql.DB // readers
	tooNew bool
	// noCompat is why the database is open in its pre-compat state: the
	// backup before upgrading failed. Reads work, writes are refused.
	noCompat   error
	noCompatAt time.Time
	closed     bool

	changed chan struct{} // nudges Watch after a local commit

	// hookAfterPlan, when set by a test, runs between a slot consumption's
	// calendar read and its write transaction.
	hookAfterPlan func()
	// hookBeforeQuestionEvent, when set by a test, fails an answer inside its
	// transaction, after the question was read and before the notice is written.
	hookBeforeQuestionEvent func() error
	// hookBeforeQuestionClose, when set by a test, holds a close inside its
	// transaction, after the question was read as open and before it is closed.
	hookBeforeQuestionClose func()
	// hookBeforeCallClaim, when set by a test, runs before a call claim's
	// transaction: the scan has read the question as due, the claim has not
	// written yet.
	hookBeforeCallClaim func()
	// questionsEmpty caches that the questions table held no row; questionsSeen
	// that this process wrote one. See HasQuestions.
	questionsEmpty atomic.Bool
	questionsSeen  atomic.Bool
}

// New returns a repository on path. Nothing touches the disk until first use.
func New(path string) *Repo {
	r := &Repo{path: path, changed: make(chan struct{}, 1)}
	r.SetClock(time.Now)
	return r
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
// window and to cross schedule boundaries without sleeping. It is safe to call
// while other goroutines use the repository.
func (r *Repo) SetClock(now func() time.Time) { r.clock.Store(&now) }

func (r *Repo) now() time.Time { return (*r.clock.Load())() }

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
		if r.noCompat == nil || r.now().Before(r.noCompatAt.Add(compatRetryAfter)) {
			return r.w, r.r, r.tooNew, nil
		}
		// The backup failed a while ago: try again. The old pools may still be
		// in use by a caller that got them before; close them a bit later.
		oldW, oldR := r.w, r.r
		time.AfterFunc(compatRetryAfter, func() { _ = oldR.Close(); _ = oldW.Close() })
		r.w, r.r, r.noCompat = nil, nil, nil
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
	var noCompat error
	if bf := (*backupFailure)(nil); errors.As(err, &bf) {
		noCompat, err = err, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	rdb, err := openReader(r.path, noCompat != nil)
	if err != nil {
		_ = wdb.Close()
		return nil, nil, false, err
	}
	rdb.SetMaxOpenConns(4)
	r.w, r.r, r.tooNew, r.noCompat = wdb, rdb, tooNew, noCompat
	if noCompat != nil {
		r.noCompatAt = r.now()
	}
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
		tooNew, err = migrateWith(db, path, migrations, compatMigrations)
		if err == nil {
			return db, tooNew, nil
		}
		if bf := (*backupFailure)(nil); errors.As(err, &bf) {
			// Nothing was migrated: the database stays as the older binary
			// left it, open for reading only.
			return db, false, err
		}
		_ = db.Close()
		if !isBusy(err) {
			return nil, false, err
		}
	}
	return nil, false, err
}

// compatMigrations are migrations an older moa can live with: tables it never
// reads, columns with defaults, CHECKs widened. They are counted in
// tasks_compat.version, not in user_version, so a binary that does not know
// them still opens the database for writing and simply ignores them. A change
// that an older binary would read or write wrongly — a rename, a narrower
// CHECK, a column whose meaning changes — does not belong here: it goes to
// migrations and raises user_version.
var compatMigrations = []string{
	// 1: tasks between owners. A handoff row turns a task into one owner's
	// task for another; comments are its thread (and that of an agent's
	// request). Both cascade with the task, so an older binary deleting a
	// task leaves nothing behind. The outbox is rebuilt only to widen the
	// kinds it accepts: same columns, same order, same rowids.
	`CREATE TABLE task_handoffs (
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  from_owner_id TEXT NOT NULL, from_owner_name TEXT NOT NULL, from_session_id TEXT NOT NULL,
  to_owner_id TEXT NOT NULL, to_owner_name TEXT NOT NULL, to_session_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('awaiting_permission','sent','declined')),
  outcome TEXT CHECK(outcome IS NULL OR outcome IN ('done','cant_do')),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX task_handoffs_from ON task_handoffs(from_session_id);
CREATE INDEX task_handoffs_to ON task_handoffs(to_session_id);
CREATE TABLE task_comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  author_kind TEXT NOT NULL CHECK(author_kind IN ('human','session')),
  author_session_id TEXT,
  author_name TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL CHECK(length(trim(body)) > 0),
  created_at INTEGER NOT NULL
);
CREATE INDEX task_comments_task ON task_comments(task_id, id);
CREATE TABLE task_notifications_new (
  id TEXT PRIMARY KEY,
  task_id INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('assigned','updated','request_done','agent_done','agent_deleted',
    'handoff','comment','handoff_closed','handoff_declined')),
  recipient_session_id TEXT NOT NULL,
  deliver TEXT NOT NULL CHECK(deliver IN ('wake','hold')),
  method TEXT NOT NULL CHECK(method IN ('run','append')),
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('pending','held','sent','delivered','failed')),
  reason TEXT NOT NULL DEFAULT '',
  steer_id TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, delivered_at INTEGER
);
INSERT INTO task_notifications_new(rowid, id, task_id, kind, recipient_session_id, deliver, method, title, body,
  state, reason, steer_id, created_at, updated_at, delivered_at)
  SELECT rowid, id, task_id, kind, recipient_session_id, deliver, method, title, body,
  state, reason, steer_id, created_at, updated_at, delivered_at FROM task_notifications ORDER BY rowid;
DROP TABLE task_notifications;
ALTER TABLE task_notifications_new RENAME TO task_notifications;
CREATE INDEX task_notifications_task ON task_notifications(task_id, created_at);
CREATE INDEX task_notifications_open ON task_notifications(state, created_at);`,
	// 2: questions an agent posted and left open (asynchronous ask_user). A
	// question is not a task: it has its own table, and its answer travels as
	// a task_notifications row of kind question_answer with task_id 0 (no task
	// has that id, so an older binary scanning the outbox sees an unknown
	// notice for nobody's task and nothing else). The outbox is rebuilt only to
	// widen the kinds, as in 1: same columns, same rowids.
	//
	// Rollback limit: a binary from before this migration opens the database
	// and ignores the questions table, but it would deliver an open
	// question_answer notice again as a Tasks notice (it does not know the
	// receipt in the transcript), and it cannot present an open question or
	// run a continuation that is due. Going back is therefore safe only once
	// the pilot never ran, or with activity quiescent (no runs, no
	// publications) and all three counts at zero: questions in state 'open',
	// questions with work 'due', and question_answer notices in state
	// pending, held or sent. The notice count alone does not prove it.
	`CREATE TABLE questions (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  origin_msg_id TEXT NOT NULL DEFAULT '',
  origin_tool_call_id TEXT NOT NULL,
  origin_tool TEXT NOT NULL CHECK(origin_tool IN ('ask_user','request_call')),
  questions_json TEXT NOT NULL,
  requires_tap INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL CHECK(state IN ('open','answered','withdrawn','dismissed','invalidated')),
  reason TEXT NOT NULL DEFAULT '',
  ack_json TEXT NOT NULL DEFAULT '',
  answers_json TEXT NOT NULL DEFAULT '',
  responder_json TEXT NOT NULL DEFAULT '',
  event_id TEXT NOT NULL DEFAULT '',
  work TEXT NOT NULL DEFAULT '' CHECK(work IN ('','due','done','stopped')),
  asked_at INTEGER NOT NULL, closed_at INTEGER, updated_at INTEGER NOT NULL,
  UNIQUE(session_id, origin_msg_id, origin_tool_call_id)
);
CREATE UNIQUE INDEX questions_one_open ON questions(session_id) WHERE state = 'open';
CREATE INDEX questions_event ON questions(event_id) WHERE event_id <> '';
CREATE TABLE task_notifications_new (
  id TEXT PRIMARY KEY,
  task_id INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('assigned','updated','request_done','agent_done','agent_deleted',
    'handoff','comment','handoff_closed','handoff_declined','question_answer')),
  recipient_session_id TEXT NOT NULL,
  deliver TEXT NOT NULL CHECK(deliver IN ('wake','hold')),
  method TEXT NOT NULL CHECK(method IN ('run','append')),
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('pending','held','sent','delivered','failed')),
  reason TEXT NOT NULL DEFAULT '',
  steer_id TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, delivered_at INTEGER
);
INSERT INTO task_notifications_new(rowid, id, task_id, kind, recipient_session_id, deliver, method, title, body,
  state, reason, steer_id, created_at, updated_at, delivered_at)
  SELECT rowid, id, task_id, kind, recipient_session_id, deliver, method, title, body,
  state, reason, steer_id, created_at, updated_at, delivered_at FROM task_notifications ORDER BY rowid;
DROP TABLE task_notifications;
ALTER TABLE task_notifications_new RENAME TO task_notifications;
CREATE INDEX task_notifications_task ON task_notifications(task_id, created_at);
CREATE INDEX task_notifications_open ON task_notifications(state, created_at);`,
	// 3: News, what a session deliberately told the user (news.go). Its own
	// table and indexes only: an older binary never reads it, and deleting a
	// task or a session there leaves nothing that points at it.
	`CREATE TABLE news (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL CHECK(session_id <> ''),
  key TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK(kind IN ('done','deployed','problem','info')),
  title TEXT NOT NULL CHECK(length(trim(title)) > 0),
  body TEXT NOT NULL DEFAULT '',
  revision INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, read_at INTEGER
);
CREATE UNIQUE INDEX news_key ON news(session_id, key) WHERE key <> '';
CREATE INDEX news_origin ON news(session_id, origin) WHERE origin <> '';
CREATE INDEX news_read ON news(read_at);`,
	// 4: a question can escalate to a call when it stays unanswered
	// (question_calls.go). Columns with defaults only: an older binary never
	// names them, so it reads and writes questions as before; a question it
	// answers or closes simply leaves its escalation pending, and the claim
	// only ever takes an open question.
	`ALTER TABLE questions ADD COLUMN call_due_at INTEGER;
ALTER TABLE questions ADD COLUMN call_state TEXT NOT NULL DEFAULT ''
  CHECK(call_state IN ('','pending','calling','missed','interrupted','cancelled','done'));
ALTER TABLE questions ADD COLUMN call_attempt TEXT NOT NULL DEFAULT '';
CREATE INDEX questions_call_due ON questions(call_due_at) WHERE call_state = 'pending';`,
	// 5: every invocation that published or updated a News item, so a retry
	// of an older one is told duplicate even after a keyed update replaced
	// the item's origin (news.go). Its own table only, as in 3.
	`CREATE TABLE news_origins (
  session_id TEXT NOT NULL,
  origin TEXT NOT NULL,
  news_id TEXT NOT NULL,
  PRIMARY KEY(session_id, origin)
);
CREATE INDEX news_origins_news ON news_origins(news_id);
INSERT OR IGNORE INTO news_origins(session_id, origin, news_id)
  SELECT session_id, origin, id FROM news WHERE origin <> '';`,
	// 6: the user said Keep to a recurring task that runs on its own (For
	// you's "Running on its own"). Its own table, cascading with the
	// template: an older binary never reads it, and deleting the template
	// there leaves nothing behind.
	`CREATE TABLE task_schedule_reviews (
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  kept_at INTEGER NOT NULL
);`,
}

// newsCompatLevel is the compat migration that created the news table.
const newsCompatLevel = 3

// migrateWith brings the schema to the latest version: base migrations count
// in user_version, compat ones in tasks_compat. A database from a newer moa
// (user_version beyond base) is left untouched and reported; a newer compat
// level is simply ignored. Everything runs in one immediate transaction, so
// two processes never apply a migration twice. path, when set, is where an
// existing database is backed up before its first pending compat migration.
func migrateWith(db *sql.DB, path string, migrations, compat []string) (tooNew bool, err error) {
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
	level := 0
	var hasCompat bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'tasks_compat')").Scan(&hasCompat); err != nil {
		return false, err
	}
	if hasCompat {
		if err := tx.QueryRow("SELECT version FROM tasks_compat WHERE id = 1").Scan(&level); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	if version == len(migrations) && level >= len(compat) {
		return false, nil
	}
	if level < len(compat) && version >= 1 && path != "" {
		// Under this transaction's lock, before it writes: the copy is the
		// database exactly as the older binary left it.
		if err := backupBeforeCompat(ctx, path, level+1); err != nil {
			err = fmt.Errorf("tasks backup before compat migration %d: %w", level+1, err)
			if version == len(migrations) {
				// Only the compat level is pending: the data is readable as it is.
				return false, &backupFailure{err}
			}
			return false, err
		}
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return false, fmt.Errorf("tasks migration %d: %w", i+1, err)
		}
	}
	// PRAGMA does not accept bound parameters.
	if version < len(migrations) {
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
			return false, err
		}
	}
	if level < len(compat) {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS tasks_compat (id INTEGER PRIMARY KEY CHECK(id = 1), version INTEGER NOT NULL)`); err != nil {
			return false, err
		}
		for i := level; i < len(compat); i++ {
			if _, err := tx.Exec(compat[i]); err != nil {
				return false, fmt.Errorf("tasks compat migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO tasks_compat(id, version) VALUES (1, ?)
			ON CONFLICT(id) DO UPDATE SET version = excluded.version`, len(compat)); err != nil {
			return false, err
		}
	}
	return false, tx.Commit()
}

// backupBeforeCompat copies the database to <path>.pre-compat<n>.bak, once.
// The copy is made under a temporary name, validated and only then renamed,
// so a crash can never leave a partial file where a backup is expected. An
// existing backup is accepted only if it is a regular file that passes the
// same validation; anything else is an error, never deleted or trusted. The
// copy reads through its own connection; the caller's transaction holds the
// write lock but has not written, so it is the last committed state.
func backupBeforeCompat(ctx context.Context, path string, n int) error {
	bak := fmt.Sprintf("%s.pre-compat%d.bak", path, n)
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = url.Values{"_pragma": {"busy_timeout(5000)"}}.Encode()
	src, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	var version int
	if err := src.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch st, err := os.Lstat(bak); {
	case err == nil && !st.Mode().IsRegular():
		return fmt.Errorf("%s exists and is not a regular file", bak)
	case err == nil && st.Size() > 0:
		if err := validBackup(ctx, bak, version); err != nil {
			return fmt.Errorf("existing backup %s is not usable: %w", bak, err)
		}
		return nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	// An empty file is a backup that never finished: it is replaced below.
	if stale, _ := filepath.Glob(bak + ".tmp-*"); len(stale) > 0 {
		for _, f := range stale {
			_ = os.Remove(f)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(bak), filepath.Base(bak)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := src.ExecContext(ctx, "VACUUM INTO ?", tmpName); err != nil {
		return err
	}
	if err := validBackup(ctx, tmpName, version); err != nil {
		return err
	}
	if err := syncPath(tmpName); err != nil {
		return err
	}
	if err := os.Rename(tmpName, bak); err != nil {
		return err
	}
	committed = true
	return syncPath(filepath.Dir(bak))
}

// validBackup opens a backup on its own and checks it is a complete database
// of the same base version.
func validBackup(ctx context.Context, file string, version int) error {
	u := url.URL{Scheme: "file", Path: file}
	u.RawQuery = url.Values{"mode": {"ro"}}.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("integrity check: %s", check)
	}
	var got int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&got); err != nil {
		return err
	}
	if got != version {
		return fmt.Errorf("user_version %d, want %d", got, version)
	}
	return nil
}

// syncPath fsyncs a file or a directory.
func syncPath(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// compatRetryAfter is how long a failed pre-compat backup is trusted to keep
// failing: until then reads and writes reuse the read-only pools; after it,
// the next use reopens and tries the backup and the migration again. Every
// try copies the whole database, so it is not done on every write.
const compatRetryAfter = 30 * time.Second

// backupFailure marks a migration refused only because the backup before it
// could not be made.
type backupFailure struct{ error }

func (b *backupFailure) Unwrap() error { return b.error }

// compatTempTables stand in, on a connection to a database without the compat
// tables, for the ones the queries mention: they are empty, so a read sees a
// task as one nobody sent and with no thread, exactly as the older binary does.
const compatTempTables = `PRAGMA query_only = 0;
CREATE TEMP TABLE task_handoffs (task_id INTEGER PRIMARY KEY, from_owner_id TEXT, from_owner_name TEXT, from_session_id TEXT,
  to_owner_id TEXT, to_owner_name TEXT, to_session_id TEXT, state TEXT, outcome TEXT, created_at INTEGER, updated_at INTEGER);
CREATE TEMP TABLE task_comments (id INTEGER PRIMARY KEY, task_id INTEGER, author_kind TEXT, author_session_id TEXT,
  author_name TEXT, body TEXT, created_at INTEGER);
CREATE TEMP TABLE task_schedule_reviews (task_id INTEGER PRIMARY KEY, kept_at INTEGER);
PRAGMA query_only = 1;`

// openReader opens the read pool. With noCompat each connection first gets
// the empty stand-in tables, so every read query works unchanged.
func openReader(path string, noCompat bool) (*sql.DB, error) {
	if !noCompat {
		return sql.Open("sqlite", dsn(path, false))
	}
	probe, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	drv := probe.Driver()
	_ = probe.Close()
	return sql.OpenDB(tempTablesConnector{drv: drv, dsn: dsn(path, false)}), nil
}

type tempTablesConnector struct {
	drv driver.Driver
	dsn string
}

func (c tempTablesConnector) Driver() driver.Driver { return c.drv }

func (c tempTablesConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	ex, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("sqlite connection cannot exec")
	}
	if _, err := ex.ExecContext(ctx, compatTempTables, nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
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
	r.mu.Lock()
	noCompat := r.noCompat
	r.mu.Unlock()
	if noCompat != nil {
		return fmt.Errorf("%w: tasks are read-only: %s", ErrUnavailable, strings.TrimPrefix(noCompat.Error(), "tasks "))
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
