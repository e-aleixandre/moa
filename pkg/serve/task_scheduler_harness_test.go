package serve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// fakeTaskClock is a race-safe manual clock. Timers fire when Set/Advance
// reaches their deadline; armed reports each new timer's duration.
type fakeTaskClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTaskTimer
	armed  chan time.Duration
}

type fakeTaskTimer struct {
	c       chan time.Time
	at      time.Time
	stopped bool
}

func newFakeTaskClock(at time.Time) *fakeTaskClock {
	return &fakeTaskClock{now: at, armed: make(chan time.Duration, 64)}
}

func (c *fakeTaskClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeTaskClock) NewTimer(d time.Duration) taskTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTaskTimer{c: make(chan time.Time, 1), at: c.now.Add(d)}
	if d <= 0 {
		t.c <- c.now
	} else {
		c.timers = append(c.timers, t)
	}
	select {
	case c.armed <- d:
	default:
	}
	return &fakeTimerHandle{c: c, t: t}
}

type fakeTimerHandle struct {
	c *fakeTaskClock
	t *fakeTaskTimer
}

func (h *fakeTimerHandle) C() <-chan time.Time { return h.t.c }
func (h *fakeTimerHandle) Stop() bool {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	was := !h.t.stopped
	h.t.stopped = true
	return was
}

func (c *fakeTaskClock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
	kept := c.timers[:0]
	for _, t := range c.timers {
		if t.stopped {
			continue
		}
		if !t.at.After(at) {
			select {
			case t.c <- at:
			default:
			}
			continue
		}
		kept = append(kept, t)
	}
	c.timers = kept
}

func (c *fakeTaskClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

// drainArmed forgets the timers armed so far.
func (c *fakeTaskClock) drainArmed() {
	for {
		select {
		case <-c.armed:
		default:
			return
		}
	}
}

// waitArmed waits for the planner to arm a timer of exactly d.
func (c *fakeTaskClock) waitArmed(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-c.armed:
			if got == d {
				return
			}
		case <-deadline:
			t.Fatalf("planner never armed a %v timer", d)
		}
	}
}

func mustUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// schedHarness runs Managers on one set of disk paths (sessions, tasks.sqlite,
// schedules.json) with one fake clock, so a test can restart the process.
type schedHarness struct {
	t     *testing.T
	dir   string
	base  string
	clock *fakeTaskClock
	prov  core.Provider
	hooks taskSchedulerHooks
	mgr   *Manager
	root  string
	repos []*tasks.Repo
	// factoryErr makes every provider construction fail (resume, create).
	factoryErr atomic.Bool
	// attempts counts the dispatcher's delivery attempts.
	attempts atomic.Int32
}

func newSchedHarness(t *testing.T, prov core.Provider, at string) *schedHarness {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	return newSchedHarnessIn(t, t.TempDir(), prov, at)
}

// newSchedHarnessIn uses an existing directory: a crash helper process works
// on its parent's files.
func newSchedHarnessIn(t *testing.T, dir string, prov core.Provider, at string) *schedHarness {
	t.Helper()
	h := &schedHarness{t: t, dir: dir, base: filepath.Join(dir, "sessions"), clock: newFakeTaskClock(mustUTC(at)), prov: prov, root: t.TempDir()}
	t.Cleanup(func() {
		if h.mgr != nil {
			h.mgr.Shutdown()
		}
		for _, r := range h.repos {
			_ = r.Close()
		}
	})
	return h
}

func (h *schedHarness) dbPath() string       { return filepath.Join(h.dir, tasks.DatabaseName) }
func (h *schedHarness) schedulePath() string { return filepath.Join(h.dir, "schedules.json") }

// start runs a fresh Manager on the harness paths; the previous one must be
// stopped first.
func (h *schedHarness) start() *Manager {
	h.t.Helper()
	hooks := h.hooks
	attempted := hooks.attempted
	hooks.attempted = func(id string) {
		h.attempts.Add(1)
		if attempted != nil {
			attempted(id)
		}
	}
	h.mgr = NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) {
			if h.factoryErr.Load() {
				return nil, errors.New("provider unavailable")
			}
			return h.prov, nil
		},
		AuxiliaryModelResolver: func(spec string) (core.Model, bool, error) {
			return core.ResolveAuxiliaryModel(spec, func(string) bool { return true })
		},
		DefaultModel:   core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:  h.root,
		MoaCfg:         noticeTestConfig,
		ConfigLoader:   isolatedTestConfigLoader(h.t, noticeTestConfig),
		SessionBaseDir: h.base,
		clock:          h.clock,
		schedulerHooks: hooks,
	})
	return h.mgr
}

// stop shuts the Manager down gracefully (only where the crash window does
// not matter: setup, or after the state under test was already persisted).
func (h *schedHarness) stop() {
	h.t.Helper()
	if h.mgr != nil {
		h.mgr.Shutdown()
		h.mgr = nil
	}
}

// repo is another connection to the task database, as a second process (the
// CLI) would have, on the harness clock.
func (h *schedHarness) repo() *tasks.Repo {
	r := tasks.New(h.dbPath())
	r.SetClock(h.clock.Now)
	h.repos = append(h.repos, r)
	return r
}

func (h *schedHarness) session() *ManagedSession {
	h.t.Helper()
	s, err := h.mgr.CreateSession(CreateOpts{})
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// savedSession creates a session and closes it, leaving it only on disk.
func (h *schedHarness) savedSession() string {
	h.t.Helper()
	s := h.session()
	closeSession(h.t, h.mgr, s.ID)
	return s.ID
}

func (h *schedHarness) pass() { h.mgr.planner.syncPass() }

func onceAt(at time.Time, target tasks.Target, d tasks.Delivery) *tasks.ScheduleDef {
	return &tasks.ScheduleDef{When: tasks.When{Kind: tasks.WhenOnce, At: at.UnixMilli()}, TZ: "UTC", Target: target, Delivery: d}
}

func dailyAt(h, mi int, tz string, target tasks.Target, d tasks.Delivery) *tasks.ScheduleDef {
	rule := tasks.Rule{Freq: "daily", H: h, Mi: mi}
	return &tasks.ScheduleDef{When: tasks.When{Kind: tasks.WhenRepeat, Rule: &rule}, TZ: tz, Target: target, Delivery: d}
}

func toSession(id string) tasks.Target { return tasks.Target{Kind: tasks.TargetSession, ID: id} }

func mkTemplate(t *testing.T, r *tasks.Repo, title string, def *tasks.ScheduleDef) tasks.Record {
	t.Helper()
	rec, err := r.Create(context.Background(), tasks.CreateInput{Title: title, Description: "please " + title, Schedule: def})
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return rec
}

func runsOfT(t *testing.T, r *tasks.Repo, parent int64) []tasks.Occurrence {
	t.Helper()
	os, err := r.Runs(context.Background(), parent, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(os, func(i, j int) bool { return os[i].ID < os[j].ID })
	return os
}

func oneRun(t *testing.T, r *tasks.Repo, parent int64) tasks.Occurrence {
	t.Helper()
	os := runsOfT(t, r, parent)
	if len(os) != 1 {
		t.Fatalf("template #%d has %d runs, want 1: %+v", parent, len(os), os)
	}
	return os[0]
}

func occNow(t *testing.T, r *tasks.Repo, id int64) tasks.Occurrence {
	t.Helper()
	o, err := r.Occurrence(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func waitOcc(t *testing.T, r *tasks.Repo, id int64, desc string, cond func(tasks.Occurrence) bool) tasks.Occurrence {
	t.Helper()
	var o tasks.Occurrence
	pollUntil(t, 10*time.Second, desc, func() bool {
		o = occNow(t, r, id)
		return cond(o)
	})
	return o
}

func noticeByID(t *testing.T, r *tasks.Repo, id string) tasks.Notice {
	t.Helper()
	n, err := r.Notice(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitNoticeByID(t *testing.T, r *tasks.Repo, id, state string) tasks.Notice {
	t.Helper()
	var n tasks.Notice
	pollUntil(t, 10*time.Second, "notice "+id+" "+state, func() bool {
		n = noticeByID(t, r, id)
		return n.State == state
	})
	return n
}

// testDSN opens the task database like the repository does (with a busy
// timeout), so test SQL waits for a concurrent writer instead of failing.
func testDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = q.Encode()
	return u.String()
}

func sqlCount(t *testing.T, path, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", testDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func sqlExec(t *testing.T, path, stmt string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", testDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

// abortTrigger installs an aborting trigger and returns its removal.
func abortTrigger(t *testing.T, path, name, def string) func() {
	t.Helper()
	sqlExec(t, path, "CREATE TRIGGER "+name+" "+def+" BEGIN SELECT RAISE(ABORT, 'refused by test'); END")
	return func() { sqlExec(t, path, "DROP TRIGGER "+name) }
}

// transcriptNoticeCount counts messages carrying notice id in a session's
// saved transcript (flat or tree).
func transcriptNoticeCount(t *testing.T, base, sessionID, id string) int {
	t.Helper()
	s, _, err := session.FindSessionReadOnly(base, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return savedNoticeCount(s, id)
}

func markedSessions(t *testing.T, base string, occID int64) []session.Summary {
	t.Helper()
	got, err := session.FindByMetadata(base, session.MetaScheduledOccurrenceID, fmt.Sprint(occID), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// blockingHandler holds a run until release is closed.
func blockingHandler(started chan<- struct{}, release <-chan struct{}, text string) mockHandler {
	return func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return simpleResponse(text), nil
	}
}

// ensureDB creates the task database (with one private note) so SQL test
// triggers can be installed before the code under test writes.
func ensureDB(t *testing.T, r *tasks.Repo) {
	t.Helper()
	if _, err := r.Create(context.Background(), tasks.CreateInput{Title: "note", Place: tasks.PlaceYou}); err != nil {
		t.Fatal(err)
	}
}

// jsonInt reads a number from message metadata, live (int64) or decoded
// from a saved transcript (float64).
func jsonInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return -1
}
