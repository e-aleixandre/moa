package serve

import (
	"context"
	"errors"
	"log/slog"
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
	retryAll, reconcileAll bool
	retry, reconcile       map[string]bool
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
	t := noticeTriggers{retryAll: d.retryAll, reconcileAll: d.reconcileAll, retry: d.retry, reconcile: d.reconcile}
	d.retryAll, d.reconcileAll = false, false
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
	t := time.NewTicker(noticeReconcileInterval)
	defer t.Stop()
	for {
		d.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-t.C:
			d.trigMu.Lock()
			d.reconcileAll = true
			d.trigMu.Unlock()
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
			if n.Reason != "" && !trig.retryAll && !trig.retry[to] {
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
			d.pendingFor(ctx, &n, tasks.ReasonSessionLimit)
			return n
		case errors.Is(err, session.ErrNotFound):
			d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeFailed, Reason: tasks.ReasonSessionDeleted})
			return n
		default:
			slog.Warn("task notices: resuming the recipient failed", "notice", n.ID, "session", to, "error", err)
			d.pendingFor(ctx, &n, "resume_failed")
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
			d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeSent, SteerID: n.SteerID})
			d.markInflight(to)
			return n
		}
	}
	run := n.Method == tasks.MethodRun
	steerID := core.NewSteerID()
	// Persist the recovery decision before admission. If this write fails, no
	// message or turn may start; a crash afterwards is reconciled as sent.
	if !d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeSent, SteerID: steerID}) {
		return n
	}
	d.steers.Store(steerID, n.ID)
	d.markInflight(to)
	steered, err := m.injectEvent(to, eventInjection{
		Text:           func() string { return n.Text },
		Custom:         func(steer bool) map[string]any { return noticeCustom(n, run, steer) },
		Autorun:        run,
		SteerID:        steerID,
		RefuseQuestion: true,
	})
	if !steered || err != nil {
		d.steers.Delete(steerID)
	}
	switch {
	case err == nil:
		if !steered {
			d.setState(ctx, &n, tasks.NoticeChange{State: tasks.NoticeSent})
		}
		// The transcript may already have been saved during admission.
		d.trigger(func() { d.reconcile[to] = true })
	case errors.Is(err, errEventSessionQuestion):
		d.markWaitingIdle(to)
		d.pendingFor(ctx, &n, tasks.ReasonQuestionPending)
	case errors.Is(err, errEventSessionBusy):
		// Only an append waits like this: it never starts or joins a turn.
		d.markWaitingIdle(to)
		d.pendingFor(ctx, &n, "")
	case errors.Is(err, ErrNotFound):
		// Closed while we looked: the next pass sees it saved.
		d.pendingFor(ctx, &n, "")
	default:
		slog.Warn("task notices: the session did not accept the notice", "notice", n.ID, "session", to, "error", err)
		d.markWaitingIdle(to)
		d.pendingFor(ctx, &n, "session_busy")
	}
	return n
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
// saved is never injected a second time. Must run under d.mu.
func (d *noticeDispatcher) reconcileSent(ctx context.Context, n tasks.Notice) {
	m := d.m
	to := n.RecipientSessionID
	if sess, live := m.Get(to); live {
		if sess.persister != nil && sess.persister.has(func(s *session.Session) bool { return transcriptHasNotice(s, n.ID) }) {
			d.delivered(ctx, n)
			return
		}
		if hasNotice(sess.History(), n.ID) || noticeQueued(sess, n) {
			return
		}
		if time.Since(time.UnixMilli(n.UpdatedAt)) < noticeLostAfter {
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
