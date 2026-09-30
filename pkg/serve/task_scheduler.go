package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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
	reasonDestinationAmbiguous = "destination_ambiguous"
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
	beforePass      func(ctx context.Context)
	afterMarkedSave func(sessionID string, occurrenceID int64)
	beforeAssign    func(occurrenceID int64)
	afterAssign     func(occurrenceID int64)
	attempted       func(noticeID string)
	beforeAdmit     func(noticeID string)
	legacyRename    func(from, to string) error
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
	for ctx.Err() == nil {
		os, err := repo.MaterializeDue(ctx, materializeBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("task scheduler: materializing due runs failed", "error", err)
			}
			break
		}
		if len(os) < materializeBatch {
			break
		}
	}
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
// attempt created: the run's ID is written in that session's first save, and
// a full scan of the saved sessions finds it after a crash. Held under
// automationMu, like session Delete and Close, so a delete cannot slip between
// the scan, the creation and T1.
func (s *taskScheduler) provisionNew(ctx context.Context, o tasks.Occurrence) bool {
	m := s.m
	m.automationMu.Lock()
	defer m.automationMu.Unlock()
	cur, err := m.tasks.Occurrence(ctx, o.ID)
	if err != nil || cur.State != tasks.OccReady {
		return false
	}
	occKey, parentKey := strconv.FormatInt(o.ID, 10), strconv.FormatInt(o.ScheduleTaskID, 10)
	found, err := session.FindByMetadata(m.sessionBaseDir, session.MetaScheduledOccurrenceID, occKey)
	if err != nil {
		s.fail(ctx, o.ID, reasonDestinationUncertain, err.Error())
		return false
	}
	var dest tasks.Destination
	switch len(found) {
	case 0:
		t := o.Spec.Target
		sess, err := m.CreateSession(CreateOpts{
			Title: o.Spec.Title, CWD: t.CWD, Model: t.Model, Thinking: t.Thinking,
			Origin: scheduledOrigin, TZ: o.Spec.TZ,
			extraMeta: map[string]any{session.MetaScheduledOccurrenceID: occKey, session.MetaScheduledTaskID: parentKey},
		})
		if err != nil {
			reason := reasonCreateFailed
			switch {
			case errors.Is(err, ErrInvalidModel), errors.Is(err, ErrInvalidThinking):
				reason = reasonModelUnavailable
			case errors.Is(err, ErrInvalidCWD):
				reason = reasonProjectMissing
			}
			s.fail(ctx, o.ID, reason, err.Error())
			return false
		}
		if s.hooks.afterMarkedSave != nil {
			s.hooks.afterMarkedSave(sess.ID, o.ID)
		}
		dest = tasks.Destination{SessionID: sess.ID, ProjectKey: m.projectKey(sess.CWD), ProjectCWD: sess.CWD}
	case 1:
		sum := found[0]
		if p, _ := sum.Metadata[session.MetaScheduledTaskID].(string); p != parentKey {
			s.fail(ctx, o.ID, reasonDestinationAmbiguous, fmt.Sprintf("session %s carries run #%d for another task", sum.ID, o.ID))
			return false
		}
		cwd, _ := sum.Metadata[session.MetaCWD].(string)
		if sess, ok := m.Get(sum.ID); ok {
			cwd = sess.CWD
		}
		dest = tasks.Destination{SessionID: sum.ID, ProjectKey: m.projectKey(cwd), ProjectCWD: cwd}
	default:
		s.fail(ctx, o.ID, reasonDestinationAmbiguous, fmt.Sprintf("%d sessions claim run #%d", len(found), o.ID))
		return false
	}
	return s.assign(ctx, o.ID, dest)
}

// recoverScheduledTasks runs once at startup, before the dispatcher and the
// planner start: the legacy /schedule import, then the restart re-gate of
// runs that never reached their session.
//
// A reserved (sent) assignment is not proof of delivery: the process may
// have died before admitting it. Sent notices are first settled against the
// saved transcripts, so the re-gate sees as undelivered (back to pending)
// every assignment that is not there.
func (m *Manager) recoverScheduledTasks(ctx context.Context) {
	m.importLegacySchedules(ctx)
	if _, err := os.Stat(m.tasks.Path()); err != nil {
		// No database yet: nothing to recover, and a read must not create it.
		return
	}
	m.notices.reconcileSentAtStartup(ctx)
	if n, err := m.tasks.RegateOnRestart(ctx); err != nil {
		slog.Warn("task scheduler: re-gating runs at startup failed", "error", err)
	} else if n > 0 {
		slog.Info("task scheduler: runs found late at startup wait for the owner", "count", n)
	}
}
