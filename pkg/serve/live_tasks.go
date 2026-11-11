package serve

// Live tasks: the owner's checklist tasks follow the session that executes
// them. The platform writes the facts it knows — the executor started, went
// quiet, was cut by a restart — and the owner decides what they mean in its
// turn. Nothing here completes a task, resumes a session or notifies the user.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// reportStatusRestart is the status of the entry an owner gets after a start
// that followed no orderly shutdown: linked tasks whose session was working.
const reportStatusRestart = "restart"

// executionTimeout bounds one execution write; it never holds a bus callback.
const executionTimeout = 10 * time.Second

// executionWorkers admits the goroutines that record run starts. Admission
// and Add happen under one lock with the closed check, so close can wait for
// them without racing a late Add from a bus callback.
type executionWorkers struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func (w *executionWorkers) admit() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	w.wg.Add(1)
	return true
}

func (w *executionWorkers) close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.wg.Wait()
}

// stopping says moa itself is stopping: a run cancelled now was cut by the
// restart, not by a user's Stop (which leaves the root context alive).
func (m *Manager) stopping() bool {
	return m.shuttingDown.Load() || m.baseCtx.Err() != nil
}

// subscribeExecutionStarts marks the session's linked tasks started on each
// of its runs, whatever started it. The write happens off the bus callback;
// a session with no links costs one indexed read.
func (m *Manager) subscribeExecutionStarts(sess *ManagedSession) {
	if m.tasks == nil {
		return
	}
	id := sess.ID
	sess.pushUnsubs = append(sess.pushUnsubs, sess.runtime.Bus.Subscribe(func(bus.RunStarted) {
		if !m.executions.admit() {
			return
		}
		go func() {
			defer m.executions.wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
			defer cancel()
			if err := m.tasks.MarkExecutionStarted(ctx, id); err != nil {
				slog.Warn("live tasks: run start not recorded", "session", id, "error", err)
			}
		}()
	}))
}

// sessionTitle names a session, loaded or saved, for the owner's tasks list.
func (m *Manager) sessionTitle(id string) string {
	if sess, ok := m.Get(id); ok {
		return sess.title()
	}
	for _, sum := range m.cachedSavedSessions() {
		if sum.ID == id {
			return sum.Title
		}
	}
	return ""
}

// sessionWorking says a loaded session is running or has work in flight.
func (m *Manager) sessionWorking(id string) bool {
	sess, ok := m.Get(id)
	if !ok {
		return false
	}
	state, _ := bus.QueryTyped[bus.GetSessionState, string](sess.runtime.Bus, bus.GetSessionState{})
	return SessionState(state) == StateRunning || sess.runtime.BackgroundWork() > 0
}

// withRunState adds to a child's report what decides its "now:" line beyond
// the outcome itself — the next scheduled run — and the linked tasks the
// owner must decide on because the session went quiet with them open.
// running says another run of the session is already under way: then it is
// not quiet, and that run's own report carries the notice.
func (m *Manager) withRunState(rep *owner.Report, running bool) {
	if m.tasks == nil || rep.UserKind != "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	rep.NextRun = m.nextScheduledRun(ctx, rep.SessionID)
	quiet := rep.InterruptedByRestart || rep.BackgroundCount == 0
	if !quiet || running && rep.Status != callbackStatusNeedsInput {
		return
	}
	recs, err := m.tasks.UnwarnedExecutions(ctx, rep.SessionID)
	if err != nil {
		slog.Warn("live tasks: linked tasks not read for a report", "session", rep.SessionID, "error", err)
		return
	}
	for _, r := range recs {
		rep.LinkedTasks = append(rep.LinkedTasks, owner.ReportTask{ID: r.ID, Title: r.Title})
	}
}

// markExecutionStopped records, once the report carrying them is in the
// outbox, that the owner was told about these tasks: not again until the
// session runs and stops once more.
func (m *Manager) markExecutionStopped(sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	if _, err := m.tasks.MarkExecutionStopped(ctx, sessionID); err != nil {
		slog.Warn("live tasks: stop not recorded; the owner may hear it again", "session", sessionID, "error", err)
	}
}

// nextScheduledRun is the soonest enabled schedule aimed at the session.
func (m *Manager) nextScheduledRun(ctx context.Context, sessionID string) *owner.ReportNextRun {
	recs, err := m.tasks.ScheduledFor(ctx, sessionID, nil)
	if err != nil {
		return nil
	}
	var next *tasks.Record
	for i := range recs {
		r := &recs[i]
		if r.Next <= 0 || r.ScheduleState == "paused" || r.Target == nil || r.Target.Kind != tasks.TargetSession || r.Target.ID != sessionID {
			continue
		}
		if next == nil || r.Next < next.Next {
			next = r
		}
	}
	if next == nil {
		return nil
	}
	return &owner.ReportNextRun{TaskID: next.ID, At: time.UnixMilli(next.Next).UTC().Format(time.RFC3339)}
}

// reportInterruptedExecutions runs once at startup. A link whose executor
// started and was never seen stopping means the last process died without an
// orderly shutdown (which reports and marks them): each owner gets one entry
// naming them, through the persistent coordinator, and only then are they
// marked, so a crash in between repeats the entry rather than losing it.
func (m *Manager) reportInterruptedExecutions() {
	if m.tasks == nil || m.reports == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	recs, err := m.tasks.InterruptedExecutions(ctx)
	if err != nil {
		slog.Warn("live tasks: interrupted executions not read at startup", "error", err)
		return
	}
	if len(recs) == 0 {
		return
	}
	owners, err := m.reports.store.List()
	if err != nil {
		slog.Warn("live tasks: owners not read at startup", "error", err)
		return
	}
	bySession := make(map[string]owner.Owner, len(owners))
	for _, own := range owners {
		if own.SessionID != "" {
			bySession[own.SessionID] = own
		}
	}
	groups := map[string][]owner.ReportTask{}
	var keys []string
	for _, r := range recs {
		own, ok := bySession[r.AssigneeSessionID]
		if !ok || r.Execution == nil || m.sessionWorking(r.Execution.SessionID) {
			continue
		}
		if _, seen := groups[own.CodebaseKey]; !seen {
			keys = append(keys, own.CodebaseKey)
		}
		groups[own.CodebaseKey] = append(groups[own.CodebaseKey], owner.ReportTask{ID: r.ID, Title: r.Title, SessionID: r.Execution.SessionID})
	}
	for _, key := range keys {
		list := groups[key]
		m.reports.add(key, owner.Report{
			ID:           newReportID(),
			Status:       reportStatusRestart,
			RestartTasks: list,
			At:           time.Now().UTC().Format(time.RFC3339),
		})
		marked := map[string]bool{}
		for _, t := range list {
			if !marked[t.SessionID] {
				marked[t.SessionID] = true
				m.markExecutionStopped(t.SessionID)
			}
		}
	}
}

// executionTaskParam reads the sessions tool's task: an id of the owner's
// checklist, or "none" for what is not a commitment. ok is false when it was
// not given at all.
func executionTaskParam(params map[string]any) (id int64, none, ok bool, err error) {
	switch v := params["task"].(type) {
	case nil:
		return 0, false, false, nil
	case float64:
		if v != float64(int64(v)) || v <= 0 {
			return 0, false, true, fmt.Errorf("task must be a task id of your checklist or \"none\"")
		}
		return int64(v), false, true, nil
	case string:
		s := strings.TrimPrefix(strings.TrimSpace(v), "#")
		switch {
		case s == "":
			return 0, false, false, nil
		case strings.EqualFold(s, "none"):
			return 0, true, true, nil
		}
		n, perr := strconv.ParseInt(s, 10, 64)
		if perr != nil || n <= 0 {
			return 0, false, true, fmt.Errorf("task must be a task id of your checklist or \"none\", not %q", v)
		}
		return n, false, true, nil
	}
	return 0, false, true, fmt.Errorf("task must be a task id of your checklist or \"none\"")
}

// ownerActor is the owner conversation acting on its own checklist.
func ownerActor(own owner.Owner) tasks.Actor {
	return tasks.Actor{SessionID: own.SessionID, ProjectKey: own.CodebaseKey, Owner: true}
}

// checkExecutionLink refuses, before anything is created or sent, a task the
// owner cannot hand to a session, saying how to fix it.
func (m *Manager) checkExecutionLink(ctx context.Context, own owner.Owner, id int64) error {
	if core.AgentIDFromContext(ctx) != "" {
		return fmt.Errorf("a subagent cannot link tasks to sessions: pass task=\"none\" or tell the conversation that started you")
	}
	if m.tasks == nil {
		return fmt.Errorf("tasks are unavailable: pass task=\"none\"")
	}
	return m.tasks.CheckExecutionLink(ctx, ownerActor(own), id)
}

// linkExecution makes sessionID the executor of the owner's task id.
func (m *Manager) linkExecution(ctx context.Context, own owner.Owner, id int64, sessionID string) error {
	_, err := m.tasks.LinkExecution(ctx, ownerActor(own), id, sessionID)
	return err
}

// linkedTasksBySession maps each executor to the owner's open tasks it does,
// for the sessions roster.
func (m *Manager) linkedTasksBySession(own owner.Owner) map[string][]tasks.Record {
	if m.tasks == nil || own.SessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	recs, err := m.tasks.OwnerExecutions(ctx, own.SessionID)
	if err != nil {
		slog.Warn("live tasks: linked tasks not read for the roster", "owner", own.ID, "error", err)
		return nil
	}
	out := map[string][]tasks.Record{}
	for _, r := range recs {
		out[r.Execution.SessionID] = append(out[r.Execution.SessionID], r)
	}
	return out
}
