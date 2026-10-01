package serve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

const (
	// plannerMaxSleep caps how long the planner sleeps between passes, so a
	// wall-clock jump or a failed read is looked at again within a bound
	// instead of waiting for a distant cursor.
	plannerMaxSleep = 30 * time.Second
	// materializeBatch bounds one T0 query; a pass repeats it while it fills.
	materializeBatch = 100
	// scheduledOrigin labels the sessions the planner creates.
	scheduledOrigin = "scheduled"
)

// Reasons a run could not be sent, decided here. They are stored as given
// and shown to the owner, who can send the run to another session.
const (
	reasonOwnerMissing         = "owner_missing"
	reasonOwnerHasNoSession    = "owner_has_no_session"
	reasonProjectMissing       = "project_missing"
	reasonModelUnavailable     = "model_unavailable"
	reasonCreateFailed         = "create_failed"
	reasonDestinationUncertain = "destination_unverifiable"
	reasonResumeFailed         = "resume_failed"
	reasonSteerQueueFull       = "steer_queue_full"
	reasonAdmissionFailed      = "admission_failed"
	noticeReasonBusyWait       = "busy_wait"
)

// taskClock is the scheduler's clock. Production uses the system clock; tests
// inject a fake one per Manager so schedule boundaries are crossed without
// sleeping.
type taskClock interface {
	Now() time.Time
	NewTimer(d time.Duration) taskTimer
}

type taskTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) NewTimer(d time.Duration) taskTimer { return systemTimer{time.NewTimer(d)} }

type systemTimer struct{ t *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.t.C }
func (t systemTimer) Stop() bool          { return t.t.Stop() }

// taskSchedulerHooks are test seams at crash and race boundaries. All nil in
// production.
type taskSchedulerHooks struct {
	beforePass       func(ctx context.Context)
	afterReserve     func(sessionID string, occurrenceID int64)
	afterSessionSave func(sessionID string, occurrenceID int64)
	beforeAssign     func(occurrenceID int64)
	afterAssign      func(occurrenceID int64)
	attempted        func(noticeID string)
	beforeAdmit      func(noticeID string)
	legacyRename     func(from, to string) error
}

// taskScheduler is the Manager's single planner for scheduled tasks.
type taskScheduler struct {
	m      *Manager
	clock  taskClock
	hooks  taskSchedulerHooks
	wake   chan struct{}
	syncCh chan chan struct{}
	done   chan struct{}
}

func newTaskScheduler(m *Manager, clock taskClock, hooks taskSchedulerHooks) *taskScheduler {
	return &taskScheduler{m: m, clock: clock, hooks: hooks, wake: make(chan struct{}, 1),
		syncCh: make(chan chan struct{}), done: make(chan struct{})}
}

func (s *taskScheduler) nudge() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// syncPass runs one full pass that starts after the call and waits for it.
func (s *taskScheduler) syncPass() {
	ack := make(chan struct{})
	select {
	case s.syncCh <- ack:
		<-ack
	case <-s.done:
	}
}

func (s *taskScheduler) run(ctx context.Context) {
	defer close(s.done)
	var acks []chan struct{}
	defer func() {
		for _, a := range acks {
			close(a)
		}
	}()
	for {
		s.pass(ctx)
		for _, a := range acks {
			close(a)
		}
		acks = nil
		if ctx.Err() != nil {
			return
		}
		t := s.clock.NewTimer(s.sleepFor(ctx))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
		case a := <-s.syncCh:
			acks = append(acks, a)
		case <-t.C():
		}
		t.Stop()
	}
}

// sleepFor is the time to the earliest cursor, capped at plannerMaxSleep.
func (s *taskScheduler) sleepFor(ctx context.Context) time.Duration {
	next, err := s.m.tasks.NextDueAt(ctx)
	if err != nil || next == 0 {
		return plannerMaxSleep
	}
	d := time.UnixMilli(next).Sub(s.clock.Now())
	if d < 0 {
		d = 0
	}
	if d > plannerMaxSleep {
		d = plannerMaxSleep
	}
	return d
}

// pass consumes due slots (T0) and gives every authorized run its child and
// assignment (T1). Delivery is the dispatcher's: the planner only nudges it.
func (s *taskScheduler) pass(ctx context.Context) {
	if s.hooks.beforePass != nil {
		s.hooks.beforePass(ctx)
	}
	repo := s.m.tasks
	s.materialize(ctx)
	ready, err := repo.ReadyOccurrences(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("task scheduler: reading ready runs failed", "error", err)
		}
		return
	}
	assigned := false
	for _, o := range ready {
		if ctx.Err() != nil {
			return
		}
		if s.provision(ctx, o) {
			assigned = true
		}
	}
	if assigned {
		s.m.notices.nudge()
	}
}

// materialize consumes every due slot (T0), serialized with scheduled
// delivery attempts (noticeDispatcher.admitMu).
func (s *taskScheduler) materialize(ctx context.Context) {
	for ctx.Err() == nil {
		s.m.notices.admitMu.Lock()
		os, err := s.m.tasks.MaterializeDue(ctx, materializeBatch)
		s.m.notices.admitMu.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("task scheduler: materializing due runs failed", "error", err)
			}
			return
		}
		if len(os) < materializeBatch {
			return
		}
	}
}

// provision resolves a ready run's target and assigns it. A target that
// cannot receive it fails the run (Not sent); an error that may pass (an
// unreadable file, SQL) leaves it ready for the next pass.
func (s *taskScheduler) provision(ctx context.Context, o tasks.Occurrence) bool {
	var dest tasks.Destination
	var reason, note string
	var err error
	switch o.Spec.Target.Kind {
	case tasks.TargetSession:
		dest, reason, err = s.m.sessionDestination(o.Spec.Target.ID)
	case tasks.TargetOwner:
		dest, reason, err = s.m.ownerDestination(o.Spec.Target.ID)
	case tasks.TargetNew:
		return s.provisionNew(ctx, o)
	default:
		reason, note = tasks.ReasonTemplateInvalid, "unknown target "+o.Spec.Target.Kind
	}
	if err != nil {
		slog.Warn("task scheduler: resolving a run's target failed; will retry", "run", o.ID, "error", err)
		return false
	}
	if reason != "" {
		s.fail(ctx, o.ID, reason, note)
		return false
	}
	return s.assign(ctx, o.ID, dest)
}

func (s *taskScheduler) fail(ctx context.Context, id int64, reason, note string) {
	if _, err := s.m.tasks.FailOccurrence(ctx, id, reason, note); err != nil && ctx.Err() == nil {
		var conflict *tasks.OccurrenceConflictError
		if !errors.As(err, &conflict) {
			slog.Warn("task scheduler: recording a failed run failed", "run", id, "reason", reason, "error", err)
		}
	}
}

// assign is T1: child, notice and links in one transaction.
func (s *taskScheduler) assign(ctx context.Context, id int64, dest tasks.Destination) bool {
	if s.hooks.beforeAssign != nil {
		s.hooks.beforeAssign(id)
	}
	o, err := s.m.tasks.AssignOccurrence(ctx, id, dest)
	if err != nil {
		var conflict *tasks.OccurrenceConflictError
		if !errors.As(err, &conflict) && ctx.Err() == nil {
			slog.Warn("task scheduler: assigning a run failed; will retry", "run", id, "error", err)
		}
		return false
	}
	if s.hooks.afterAssign != nil {
		s.hooks.afterAssign(id)
	}
	return o.State == tasks.OccAssigned
}

// sessionDestination resolves an exact session, live or saved. Absence is
// read from disk, not from the saved-list cache: a missing session fails the
// run, an unreadable one is retried.
func (m *Manager) sessionDestination(id string) (tasks.Destination, string, error) {
	if sess, ok := m.Get(id); ok {
		return tasks.Destination{SessionID: id, ProjectKey: m.projectKey(sess.CWD), ProjectCWD: sess.CWD}, "", nil
	}
	saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, id)
	if errors.Is(err, session.ErrNotFound) {
		return tasks.Destination{}, tasks.ReasonSessionDeleted, nil
	}
	if err != nil {
		return tasks.Destination{}, "", err
	}
	_, cwd, _, _ := saved.RuntimeMeta()
	if cwd == "" {
		cwd = m.workspaceRoot
	}
	return tasks.Destination{SessionID: id, ProjectKey: m.projectKey(cwd), ProjectCWD: cwd}, "", nil
}

// ownerDestination resolves an owner entity to its conversation now; the
// run then keeps that session. No owner is ever created for a run.
func (m *Manager) ownerDestination(ownerID string) (tasks.Destination, string, error) {
	store, err := m.ownerStore()
	if err != nil {
		return tasks.Destination{}, reasonOwnerMissing, nil
	}
	own, found, err := store.FindByID(ownerID)
	if err != nil {
		return tasks.Destination{}, "", err
	}
	if !found {
		return tasks.Destination{}, reasonOwnerMissing, nil
	}
	if own.SessionID == "" {
		return tasks.Destination{}, reasonOwnerHasNoSession, nil
	}
	return m.sessionDestination(own.SessionID)
}

// provisionNew creates the run's session, or finds the one a previous
// attempt created. The session's ID is reserved in SQLite before it is
// created, so the run's session is exactly one path, derived from the run's
// fixed target directory: absent, it is created with that ID (never
// replacing anything); present, it is the run's, and if it cannot be read
// as that session the run stops there, fail-closed. No other file counts.
//
// It runs under automationMu, which session Delete also takes: a delete of
// the reserved session settles the run (no longer ready) or leaves a
// discard intent that keeps it from being created again.
func (s *taskScheduler) provisionNew(ctx context.Context, o tasks.Occurrence) bool {
	m := s.m
	m.automationMu.Lock()
	defer m.automationMu.Unlock()
	cur, err := m.tasks.Occurrence(ctx, o.ID)
	if err != nil || cur.State != tasks.OccReady {
		return false
	}
	id := cur.ReservedSessionID
	if id == "" {
		candidate, err := session.NewID()
		if err != nil {
			s.fail(ctx, o.ID, reasonCreateFailed, err.Error())
			return false
		}
		if id, err = m.tasks.ReserveSession(ctx, o.ID, candidate); err != nil {
			if ctx.Err() == nil {
				slog.Warn("task scheduler: reserving a run's session failed; will retry", "run", o.ID, "error", err)
			}
			return false
		}
		if s.hooks.afterReserve != nil {
			s.hooks.afterReserve(id, o.ID)
		}
	}
	// A delete of the run's session that lost its settlement (a crash, SQL)
	// leaves the run ready until a start finishes it: never a new session.
	if discarding, err := m.tasks.SessionDiscarding(ctx, id); err != nil || discarding {
		return false
	}
	t := cur.Spec.Target
	var dest tasks.Destination
	if sess, ok := m.Get(id); ok {
		dest = tasks.Destination{SessionID: id, ProjectKey: m.projectKey(sess.CWD), ProjectCWD: sess.CWD}
		return s.assign(ctx, o.ID, dest)
	}
	m.mu.RLock()
	_, resuming := m.resuming[id]
	m.mu.RUnlock()
	if resuming {
		return false // the next pass finds it loaded
	}
	store, err := session.OpenFileStoreReadOnly(m.sessionBaseDir, t.CWD)
	if err != nil {
		s.fail(ctx, o.ID, reasonDestinationUncertain, err.Error())
		return false
	}
	present, err := store.Exists(id)
	if err != nil {
		s.fail(ctx, o.ID, reasonDestinationUncertain, err.Error())
		return false
	}
	if present {
		saved, err := store.LoadReadOnly(id)
		if err != nil {
			s.fail(ctx, o.ID, reasonDestinationUncertain, fmt.Sprintf("session %s: %v", id, err))
			return false
		}
		_, cwd, _, _ := saved.RuntimeMeta()
		if cwd == "" {
			cwd = t.CWD
		}
		return s.assign(ctx, o.ID, tasks.Destination{SessionID: id, ProjectKey: m.projectKey(cwd), ProjectCWD: cwd})
	}
	sess, err := m.CreateSession(CreateOpts{
		Title: cur.Spec.Title, CWD: t.CWD, Model: t.Model, Thinking: t.Thinking,
		Origin: scheduledOrigin, TZ: cur.Spec.TZ, sessionID: id,
	})
	if err != nil {
		reason := reasonCreateFailed
		switch {
		case errors.Is(err, ErrBusy):
			return false
		case errors.Is(err, fs.ErrExist):
			reason = reasonDestinationUncertain
		case errors.Is(err, ErrInvalidModel), errors.Is(err, ErrInvalidThinking):
			reason = reasonModelUnavailable
		case errors.Is(err, ErrInvalidCWD):
			reason = reasonProjectMissing
		}
		s.fail(ctx, o.ID, reason, err.Error())
		return false
	}
	if s.hooks.afterSessionSave != nil {
		s.hooks.afterSessionSave(sess.ID, o.ID)
	}
	dest = tasks.Destination{SessionID: sess.ID, ProjectKey: m.projectKey(sess.CWD), ProjectCWD: sess.CWD}
	return s.assign(ctx, o.ID, dest)
}

// finishSessionDiscards completes the saved-session deletes a crash or a
// SQL failure left between their discard intent and their settlement: a
// session whose file is gone is settled as deleted; one whose file is still
// there, readable or not, was never unlinked and keeps its session and its
// work. A file that cannot be checked leaves the intent for the next start.
func (m *Manager) finishSessionDiscards(ctx context.Context) {
	ids, err := m.tasks.SessionDiscards(ctx)
	if err != nil {
		slog.Warn("task scheduler: reading interrupted session deletes failed", "error", err)
		return
	}
	for _, id := range ids {
		present, err := session.ExistsByID(m.sessionBaseDir, id)
		switch {
		case err != nil:
		case present:
			err = m.tasks.ClearSessionDiscard(ctx, id)
		default:
			_, err = m.tasks.SettleSessionDeleted(ctx, id)
		}
		if err != nil {
			slog.Warn("task scheduler: finishing an interrupted session delete failed", "session", id, "error", err)
		}
	}
}

// recoverScheduledTasks runs once at startup, before the dispatcher and the
// planner start: the legacy /schedule import, then the restart re-gate of
// runs that were never attempted. A reserved (sent) assignment was
// attempted: the dispatcher's first pass settles it by its recorded
// admission (reconcileScheduled), never re-gating or re-sending it.
func (m *Manager) recoverScheduledTasks(ctx context.Context) {
	m.importLegacySchedules(ctx)
	if _, err := os.Stat(m.tasks.Path()); err != nil {
		// No database yet: nothing to recover, and a read must not create it.
		return
	}
	m.finishSessionDiscards(ctx)
	// Runs of a delete left unsettled stay out of the re-gate, so the next
	// start still finds them.
	if n, err := m.tasks.RegateOnRestart(ctx); err != nil {
		slog.Warn("task scheduler: re-gating runs at startup failed", "error", err)
	} else if n > 0 {
		slog.Info("task scheduler: runs found late at startup wait for the owner", "count", n)
	}
}
