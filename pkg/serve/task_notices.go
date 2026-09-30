package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

const (
	// noticeReconcileInterval is how often sent notices are looked for in the
	// saved transcript even when no save announced them.
	noticeReconcileInterval = 30 * time.Second
	// noticeLostAfter is how long a notice may be sent without being in the
	// session's history, queue or saved transcript before it is treated as
	// lost and delivered again.
	noticeLostAfter = 2 * time.Minute
	// noticeDetailLimit is how many notices a task detail shows.
	noticeDetailLimit = 5
)

// maxNoticeLoadedSessions caps how many sessions may be resident when a notice
// wakes a saved one; the same bound automation resumes use. A var so tests can
// reach it without loading dozens of sessions.
var maxNoticeLoadedSessions = maxAutomationLoadedSessions

// errNoticeSettled answers a manual deliver for a notice already delivered or
// on its way.
var errNoticeSettled = errors.New("notice already delivered")

// noticeDispatcher delivers task notices. There is one per serve process: the
// outbox row is the only state that must survive, and every delivery attempt
// runs under mu, so a notice is never injected twice by concurrent triggers.
//
// It never retries on its own schedule. A notice that could not be delivered
// is tried again only when something makes success plausible: its session is
// opened or goes idle, the process starts, or the owner asks.
type noticeDispatcher struct {
	m    *Manager
	mu   sync.Mutex
	wake chan struct{}
	done chan struct{}

	trigMu       sync.Mutex
	retryAll     bool
	reconcileAll bool
	retryWaiting bool            // the periodic tick: retry notices waiting on purpose
	retry        map[string]bool // sessions whose held or undeliverable notices get another attempt
	reconcile    map[string]bool // sessions whose sent notices are looked for in the saved transcript
	waitingIdle  map[string]bool // sessions with a notice waiting for them to become idle
	inflight     map[string]bool // sessions that were sent a notice: their saves are worth a look

	steers sync.Map // steer ID → notice ID, to recognise a discarded notice steer
}

func newNoticeDispatcher(m *Manager) *noticeDispatcher {
	return &noticeDispatcher{
		m:           m,
		wake:        make(chan struct{}, 1),
		done:        make(chan struct{}),
		retry:       map[string]bool{},
		reconcile:   map[string]bool{},
		waitingIdle: map[string]bool{},
		inflight:    map[string]bool{},
	}
}

type noticeTriggers struct {
	retryAll, reconcileAll, retryWaiting bool
	retry, reconcile                     map[string]bool
}

func (d *noticeDispatcher) nudge() {
	if d == nil {
		return
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *noticeDispatcher) trigger(fn func()) {
	if d == nil {
		return
	}
	d.trigMu.Lock()
	fn()
	d.trigMu.Unlock()
	d.nudge()
}

func (d *noticeDispatcher) takeTriggers() noticeTriggers {
	d.trigMu.Lock()
	defer d.trigMu.Unlock()
	t := noticeTriggers{retryAll: d.retryAll, reconcileAll: d.reconcileAll, retryWaiting: d.retryWaiting, retry: d.retry, reconcile: d.reconcile}
	d.retryAll, d.reconcileAll, d.retryWaiting = false, false, false
	d.retry, d.reconcile = map[string]bool{}, map[string]bool{}
	return t
}

// sessionOpened is called whenever a session is loaded from disk, by any path.
func (d *noticeDispatcher) sessionOpened(id string) {
	d.trigger(func() { d.retry[id] = true })
}

// sessionIdle retries a notice that was waiting for the session to stop
// working or to leave a question.
func (d *noticeDispatcher) sessionIdle(id string) {
	if d == nil {
		return
	}
	d.trigMu.Lock()
	waiting := d.waitingIdle[id]
	if waiting {
		delete(d.waitingIdle, id)
		d.retry[id] = true
	}
	d.trigMu.Unlock()
	if waiting {
		d.nudge()
	}
}

// transcriptSaved looks for sent notices after the session's transcript
// reached disk: that is when a notice counts as delivered.
func (d *noticeDispatcher) transcriptSaved(id string) {
	if d == nil {
		return
	}
	d.trigMu.Lock()
	inflight := d.inflight[id]
	if inflight {
		d.reconcile[id] = true
	}
	d.trigMu.Unlock()
	if inflight {
		d.nudge()
	}
}

func (d *noticeDispatcher) markWaitingIdle(id string) {
	d.trigMu.Lock()
	d.waitingIdle[id] = true
	d.trigMu.Unlock()
}

func (d *noticeDispatcher) markInflight(id string) {
	d.trigMu.Lock()
	d.inflight[id] = true
	d.trigMu.Unlock()
}

func (d *noticeDispatcher) run(ctx context.Context) {
	defer close(d.done)
	// Startup is a plausible moment for everything: sessions are back on disk
	// in a known state and the resident set is empty.
	d.trigMu.Lock()
	d.retryAll, d.reconcileAll = true, true
	d.trigMu.Unlock()
	t := d.m.clock.NewTimer(noticeReconcileInterval)
	defer func() { t.Stop() }()
	for {
		d.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-t.C():
			// A wait for idle may end without an idle event (background
			// work finishing quietly): the tick retries those too.
			d.trigMu.Lock()
			d.reconcileAll, d.retryWaiting = true, true
			d.trigMu.Unlock()
			t = d.m.clock.NewTimer(noticeReconcileInterval)
		}
	}
}

func (d *noticeDispatcher) pass(ctx context.Context) {
	trig := d.takeTriggers()
	d.mu.Lock()
	defer d.mu.Unlock()
	notices, err := d.m.tasks.OpenNotices(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("task notices: reading the outbox failed", "error", err)
		}
		return
	}
	for _, n := range notices {
		if ctx.Err() != nil {
			return
		}
		to := n.RecipientSessionID
		switch n.State {
		case tasks.NoticePending:
			waiting := n.Reason == noticeReasonBusyWait || n.Reason == tasks.ReasonQuestionPending
			retry := trig.retryAll || trig.retry[to] || (waiting && trig.retryWaiting)
			if n.Reason != "" && !retry {
				continue
			}
			d.attempt(ctx, n, n.Deliver == tasks.DeliverWake && n.Method == tasks.MethodRun)
		case tasks.NoticeHeld:
			if _, live := d.m.Get(to); live || trig.retry[to] {
				d.attempt(ctx, n, false)
			}
		case tasks.NoticeSent:
			if trig.reconcileAll || trig.reconcile[to] {
				d.reconcileSent(ctx, n)
			}
		}
	}
}

// setState moves n to state/reason unless it is already there, so a pass that
// changes nothing writes nothing.
func (d *noticeDispatcher) setState(ctx context.Context, n *tasks.Notice, c tasks.NoticeChange) bool {
	if n.State == c.State && n.Reason == c.Reason && (c.Method == "" || c.Method == n.Method) && n.SteerID == c.SteerID {
		return true
	}
	if len(c.From) == 0 {
		c.From = []string{n.State}
	}
	ok, err := d.m.tasks.SetNoticeState(ctx, n.ID, c)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("task notices: recording a state failed", "notice", n.ID, "state", c.State, "error", err)
		}
		return false
	}
	if ok {
		n.State, n.Reason, n.SteerID = c.State, c.Reason, c.SteerID
		if c.Method != "" {
			n.Method = c.Method
		}
	}
	return ok
}

func (d *noticeDispatcher) pendingFor(ctx context.Context, n *tasks.Notice, reason string) {
	d.setState(ctx, n, tasks.NoticeChange{State: tasks.NoticePending, Reason: reason})
}

// attempt tries to put n in its session. wake allows resuming a saved one.
// Must run under d.mu.
func (d *noticeDispatcher) attempt(ctx context.Context, n tasks.Notice, wake bool) tasks.Notice {
	m := d.m
	if m.planner != nil && m.planner.hooks.attempted != nil {
		defer m.planner.hooks.attempted(n.ID)
	}
	// A scheduled run's assignment follows the run's frozen delivery policy,
	// and a failure to deliver it is final (Not sent) instead of retried.
	var occ *tasks.Occurrence
	if n.Kind == tasks.NoticeAssigned {
		o, ok, err := m.tasks.OccurrenceForNotice(ctx, n.ID)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("task notices: reading the notice's run failed", "notice", n.ID, "error", err)
			}
			return n
		}
		if ok {
			occ = &o
		}
	}
	to := n.RecipientSessionID
	if _, live := m.Get(to); !live {
		if _, known := m.sessionCWD(to); !known {
			d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeFailed, Reason: tasks.ReasonSessionDeleted})
			return n
		}
		if !wake {
			d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeHeld})
			return n
		}
		_, err := m.resumeSession(to, maxNoticeLoadedSessions)
		switch {
		case err == nil, errors.Is(err, ErrBusy):
			// ErrBusy: someone else is loading it; their resume retries us.
		case errors.Is(err, ErrAutomationTooManySessions):
			d.undeliverable(ctx, &n, occ, tasks.ReasonSessionLimit)
			return n
		case errors.Is(err, session.ErrNotFound):
			d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeFailed, Reason: tasks.ReasonSessionDeleted})
			return n
		default:
			slog.Warn("task notices: resuming the recipient failed", "notice", n.ID, "session", to, "error", err)
			d.undeliverable(ctx, &n, occ, reasonResumeFailed)
			return n
		}
		if _, live := m.Get(to); !live {
			d.pendingFor(ctx, &n, "")
			return n
		}
	}

	// Reconcile even a pending row: an earlier admission may have reached the
	// transcript before recording its state failed (including older binaries).
	if sess, live := m.Get(to); live {
		if sess.persister != nil && sess.persister.has(func(s *session.Session) bool { return transcriptHasNotice(s, n.ID) }) {
			n = d.delivered(ctx, n)
			return n
		}
		if hasNotice(sess.History(), n.ID) || noticeQueued(sess, n) {
			// Seen in the session: for a scheduled run, that is its admission.
			d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeSent, SteerID: n.SteerID, Admitted: occ != nil})
			d.markInflight(to)
			return n
		}
	}
	run := n.Method == tasks.MethodRun
	// busy=wait: start a fresh turn when the session is free, never steer.
	// A cheap look first avoids a reservation write per pass while it is
	// clearly working; the IdleOnly gate below is the authority.
	idleOnly := run && occ != nil && occ.Spec.Delivery.Busy == tasks.BusyWait
	if idleOnly {
		if sess, live := m.Get(to); live && sessionBusy(sess) {
			d.markWaitingIdle(to)
			d.pendingFor(ctx, &n, noticeReasonBusyWait)
			return n
		}
	}
	steerID := core.NewSteerID()
	// Persist the recovery decision before admission. If this write fails, no
	// message or turn may start; a crash afterwards is reconciled as sent.
	if !d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeSent, SteerID: steerID}) {
		return n
	}
	d.steers.Store(steerID, n.ID)
	d.markInflight(to)
	if m.planner != nil && m.planner.hooks.beforeAdmit != nil {
		m.planner.hooks.beforeAdmit(n.ID)
	}
	steered, err := m.injectEvent(to, eventInjection{
		Text: func() string { return n.Text },
		Custom: func(steer bool) map[string]any {
			c := noticeCustom(n, run, steer)
			if occ != nil {
				scheduledCustom(c, *occ, m.clock.Now())
			}
			return c
		},
		Autorun:        run,
		SteerID:        steerID,
		RefuseQuestion: true,
		IdleOnly:       idleOnly,
	})
	if !steered || err != nil {
		d.steers.Delete(steerID)
	}
	switch {
	case err == nil:
		if !steered || occ != nil {
			// Accepted: a scheduled run records its admission with it.
			change := tasks.NoticeChange{From: []string{tasks.NoticeSent}, State: tasks.NoticeSent, SteerID: n.SteerID, Admitted: occ != nil}
			if !steered {
				change.SteerID = ""
			}
			if ok, err := m.tasks.SetNoticeState(ctx, n.ID, change); err != nil {
				slog.Warn("task notices: recording an admission failed", "notice", n.ID, "error", err)
			} else if ok {
				n.SteerID = change.SteerID
			}
		}
		// The transcript may already have been saved during admission.
		d.trigger(func() { d.reconcile[to] = true })
	case errors.Is(err, errEventSessionQuestion):
		d.markWaitingIdle(to)
		d.pendingFor(ctx, &n, tasks.ReasonQuestionPending)
	case errors.Is(err, errEventSessionNotIdle):
		d.markWaitingIdle(to)
		d.pendingFor(ctx, &n, noticeReasonBusyWait)
	case errors.Is(err, errEventSessionBusy):
		// Only an append waits like this: it never starts or joins a turn.
		d.markWaitingIdle(to)
		d.pendingFor(ctx, &n, "")
	case errors.Is(err, ErrNotFound):
		// Closed while we looked: the next pass sees it saved.
		d.pendingFor(ctx, &n, "")
	case occ != nil:
		// The session refused the input (a full steer queue, say): proven
		// not admitted, so back to pending first, then Not sent.
		slog.Warn("task notices: the session refused a scheduled assignment", "notice", n.ID, "session", to, "error", err)
		reason := reasonAdmissionFailed
		if errors.Is(err, bus.ErrSteerQueueFull) {
			reason = reasonSteerQueueFull
		}
		d.pendingFor(ctx, &n, reason)
		d.undeliverable(ctx, &n, occ, reason)
	default:
		slog.Warn("task notices: the session did not accept the notice", "notice", n.ID, "session", to, "error", err)
		d.markWaitingIdle(to)
		d.pendingFor(ctx, &n, "session_busy")
	}
	return n
}

// undeliverable handles a delivery that cannot succeed now. An ordinary
// notice waits for a later trigger; a scheduled run's assignment fails, and
// its run with it (Not sent), in the same transaction.
func (d *noticeDispatcher) undeliverable(ctx context.Context, n *tasks.Notice, occ *tasks.Occurrence, reason string) {
	if occ == nil {
		d.pendingFor(ctx, n, reason)
		return
	}
	d.setState(ctx, n, tasks.NoticeChange{From: []string{tasks.NoticePending, tasks.NoticeHeld}, State: tasks.NoticeFailed, Reason: reason})
}

// sessionBusy is a cheap look at whether a live session is working or has
// a queue; the IdleOnly gate decides for real.
func sessionBusy(sess *ManagedSession) bool {
	state := sess.runtime.State.Current()
	if state == bus.StateRunning || state == bus.StatePermission || sess.runtime.Context().Agent.IsRunning() {
		return true
	}
	ql, _ := bus.QueryTyped[bus.GetQueueLen, int](sess.runtime.Bus, bus.GetQueueLen{})
	return ql > 0
}

// scheduledCustom adds a scheduled run's identity and times to its
// assignment's message metadata, and the title the transcript shows:
// "Scheduled task · <title> · due 03:00, sent 09:14 after your OK", in the
// run's zone. sent is the admission time.
func scheduledCustom(c map[string]any, o tasks.Occurrence, sent time.Time) {
	c["parent_task_id"] = o.ScheduleTaskID
	c["occurrence_id"] = o.ID
	c["due_at"] = o.DueAt
	c["tz"] = o.Spec.TZ
	c["sent_at"] = sent.UnixMilli()
	if o.ConfirmedAt != 0 {
		c["confirmed_at"] = o.ConfirmedAt
	}
	loc, err := tasks.LoadZone(o.Spec.TZ)
	if err != nil {
		loc = time.UTC
	}
	due, at := time.UnixMilli(o.DueAt).In(loc), sent.In(loc)
	dueLabel := due.Format("15:04")
	if due.Format("2006-01-02") != at.Format("2006-01-02") {
		dueLabel = due.Format("Jan 2 15:04")
	}
	title := fmt.Sprintf("Scheduled task · %s · due %s, sent %s", oneLineTitle(o.Spec.Title), dueLabel, at.Format("15:04"))
	if o.ConfirmedAt != 0 {
		title += " after your OK"
	}
	c["title"] = title
}

func oneLineTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func noticeCustom(n tasks.Notice, autorun, steer bool) map[string]any {
	custom := map[string]any{
		"source":      "event",
		"source_name": tasks.NoticeSourceName,
		"id":          n.ID,
		"title":       n.Title,
		"kind":        string(n.Kind),
		"task_id":     n.TaskID,
		"autorun":     autorun,
	}
	if steer {
		custom["steer"] = true
	}
	return custom
}

// isTaskNotice reports whether a message or steer carries a task notice.
func isTaskNotice(custom map[string]any) bool {
	return custom["source"] == "event" && custom["source_name"] == tasks.NoticeSourceName
}

func hasNotice(msgs []core.AgentMessage, id string) bool {
	for _, msg := range msgs {
		if isTaskNotice(msg.Custom) && msg.Custom["id"] == id {
			return true
		}
	}
	return false
}

// transcriptHasNotice looks for the notice in a saved transcript, tree or flat.
func transcriptHasNotice(s *session.Session, id string) bool {
	if s == nil {
		return false
	}
	for _, e := range s.Entries {
		if isTaskNotice(e.Message.Custom) && e.Message.Custom["id"] == id {
			return true
		}
	}
	return hasNotice(s.Messages, id)
}

// reconcileSent settles a sent notice against the saved transcript: there, it
// is delivered; nowhere (not saved, not in history, not queued), it goes back
// to pending and is delivered again. A notice is never lost, and one already
// saved is never injected a second time. A scheduled run's assignment is
// settled by its recorded admission instead (reconcileScheduled). Must run
// under d.mu.
func (d *noticeDispatcher) reconcileSent(ctx context.Context, n tasks.Notice) {
	m := d.m
	to := n.RecipientSessionID
	if n.Kind == tasks.NoticeAssigned {
		o, ok, err := m.tasks.OccurrenceForNotice(ctx, n.ID)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("task notices: reading the notice's run failed", "notice", n.ID, "error", err)
			}
			return
		}
		if ok {
			d.reconcileScheduled(ctx, n, o)
			return
		}
	}
	if sess, live := m.Get(to); live {
		if sess.persister != nil && sess.persister.has(func(s *session.Session) bool { return transcriptHasNotice(s, n.ID) }) {
			d.delivered(ctx, n)
			return
		}
		if hasNotice(sess.History(), n.ID) || noticeQueued(sess, n) {
			return
		}
		if d.m.clock.Now().Sub(time.UnixMilli(n.UpdatedAt)) < noticeLostAfter {
			return
		}
		slog.Warn("task notices: a sent notice is missing from its session; delivering it again", "notice", n.ID, "session", to)
		d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticePending})
		d.nudge()
		return
	}
	saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, to)
	switch {
	case errors.Is(err, session.ErrNotFound):
		d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeFailed, Reason: tasks.ReasonSessionDeleted})
		return
	case err != nil:
		slog.Warn("task notices: reading the recipient transcript failed", "notice", n.ID, "session", to, "error", err)
		return
	}
	if transcriptHasNotice(saved, n.ID) {
		d.delivered(ctx, n)
		return
	}
	// Admitted but never saved: the process stopped, or the session closed
	// with the notice still queued. Deliver it again by its original choice.
	d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticePending})
	d.nudge()
}

// reconcileScheduled settles a scheduled run's sent assignment by its
// persisted admission, never by the transcript. Every attempt holds d.mu from
// its reservation to its acknowledgment or refusal, so here none is in
// flight. Admitted, it is never sent again: a live session's is delivered
// once saved, or once it has been missing for noticeLostAfter (a steer the
// owner discarded is meanwhile kept as an append by steersCanceled). Not
// admitted, it may or may not have arrived: the owner decides.
func (d *noticeDispatcher) reconcileScheduled(ctx context.Context, n tasks.Notice, o tasks.Occurrence) {
	if o.AdmittedAt == 0 {
		ok, err := d.m.tasks.MarkDeliveryUncertain(ctx, n.ID)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				slog.Warn("task notices: recording an uncertain delivery failed", "notice", n.ID, "error", err)
			}
		case ok:
			slog.Warn("task notices: a scheduled assignment's admission was never recorded; waiting for the owner", "notice", n.ID, "run", o.ID)
		}
		return
	}
	if sess, live := d.m.Get(n.RecipientSessionID); live {
		saved := sess.persister != nil && sess.persister.has(func(s *session.Session) bool { return transcriptHasNotice(s, n.ID) })
		if !saved && (hasNotice(sess.History(), n.ID) || noticeQueued(sess, n) ||
			d.m.clock.Now().Sub(time.UnixMilli(n.UpdatedAt)) < noticeLostAfter) {
			return
		}
	}
	d.delivered(ctx, n)
}

func (d *noticeDispatcher) delivered(ctx context.Context, n tasks.Notice) tasks.Notice {
	if n.SteerID != "" {
		d.steers.Delete(n.SteerID)
	}
	d.setState(ctx, &n, tasks.NoticeChange{From: []string{tasks.NoticePending, tasks.NoticeHeld, tasks.NoticeSent}, State: tasks.NoticeDelivered})
	return n
}

func noticeQueued(sess *ManagedSession, n tasks.Notice) bool {
	steers, _ := bus.QueryTyped[bus.GetPendingSteers, []core.SteerItem](sess.runtime.Bus, bus.GetPendingSteers{})
	for _, s := range steers {
		if (n.SteerID != "" && s.ID == n.SteerID) || (isTaskNotice(s.Custom) && s.Custom["id"] == n.ID) {
			return true
		}
	}
	return false
}

// steersCanceled keeps a notice whose steer the owner discarded (Stop, recall,
// cancel): it is appended to the transcript without starting a turn once the
// session is idle, so the agent reads it on its next turn.
func (d *noticeDispatcher) steersCanceled(sessionID string, steerIDs []string) {
	if d == nil {
		return
	}
	var ids []string
	for _, sid := range steerIDs {
		if nid, ok := d.steers.LoadAndDelete(sid); ok {
			ids = append(ids, nid.(string))
		}
	}
	if len(ids) == 0 {
		return
	}
	ctx := d.m.baseCtx
	// Wait for the attempt that queued the steer to record it as sent.
	d.mu.Lock()
	for _, id := range ids {
		if _, err := d.m.tasks.SetNoticeState(ctx, id, tasks.NoticeChange{
			From:   []string{tasks.NoticePending, tasks.NoticeSent},
			State:  tasks.NoticePending,
			Method: tasks.MethodAppend,
		}); err != nil {
			slog.Warn("task notices: keeping a discarded notice failed", "notice", id, "error", err)
		}
	}
	d.mu.Unlock()
	d.markWaitingIdle(sessionID)
	d.nudge()
}

// deliverNow is the owner's "Wake now" / "Retry": it may resume a saved
// session and tries at once.
func (d *noticeDispatcher) deliverNow(ctx context.Context, id string) (tasks.Notice, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n, err := d.m.tasks.Notice(ctx, id)
	if err != nil {
		return tasks.Notice{}, err
	}
	switch n.State {
	case tasks.NoticeDelivered, tasks.NoticeSent:
		return n, errNoticeSettled
	case tasks.NoticeFailed:
		return n, nil
	}
	return d.attempt(ctx, n, true), nil
}

// subscribeTaskNotices wires a session's runtime into the dispatcher.
func (m *Manager) subscribeTaskNotices(sess *ManagedSession) {
	if m.notices == nil {
		return
	}
	d := m.notices
	sess.runtime.Bus.Subscribe(func(e bus.SteersCanceled) {
		d.steersCanceled(e.SessionID, e.SteerIDs)
	})
	sess.runtime.Bus.Subscribe(func(e bus.StateChanged) {
		if e.State == string(bus.StateIdle) {
			d.sessionIdle(e.SessionID)
		}
	})
}

// noticeRecipient is who a gesture on the task would notify now.
type noticeRecipient struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"` // live | saved | missing
}

func (m *Manager) noticeRecipientOf(rec tasks.Record) *noticeRecipient {
	id := ""
	switch {
	case rec.Place == tasks.PlaceAgent:
		id = rec.AssigneeSessionID
	case rec.Place == tasks.PlaceYou && rec.RequesterSessionID != "":
		id = rec.RequesterSessionID
	}
	if id == "" {
		return nil
	}
	state := "missing"
	if _, live := m.Get(id); live {
		state = "live"
	} else if _, known := m.sessionCWD(id); known {
		state = "saved"
	}
	return &noticeRecipient{SessionID: id, State: state}
}
