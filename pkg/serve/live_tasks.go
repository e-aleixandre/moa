package serve

// Live tasks: the owner's checklist tasks follow the session that executes
// them. The platform writes the facts it knows — the executor started, went
// quiet, was cut by a restart — and the owner decides what they mean in its
// turn. Nothing here completes a task, resumes a session or notifies the user.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// reportStatusRestart is the status of the entry an owner gets after a start
// that followed no orderly shutdown: linked tasks whose session was working.
const reportStatusRestart = "restart"

// executionTimeout bounds one execution write.
const executionTimeout = 10 * time.Second

// liveTaskKey is the Custom key of an owner's instruction that makes its
// session the executor of a task (the sessions tool's task=N).
const liveTaskKey = "live_task"

// liveTaskInput is that instruction's task. Only the sessions tool makes one,
// and it is a Go value no decoded message can hold, so nobody links a task by
// sending a Custom of their own. A transcript keeps just the task id.
type liveTaskInput struct {
	id    int64
	actor tasks.Actor
}

func (t liveTaskInput) MarshalJSON() ([]byte, error) { return json.Marshal(t.id) }

// withLiveTask makes custom the provenance of an instruction for the owner's
// task id: accepting it links the task to the session that accepts it.
func withLiveTask(custom map[string]any, own owner.Owner, id int64) map[string]any {
	custom[liveTaskKey] = liveTaskInput{id: id, actor: ownerActor(own)}
	return custom
}

func liveTaskOf(custom map[string]any) (liveTaskInput, bool) {
	t, ok := custom[liveTaskKey].(liveTaskInput)
	return t, ok
}

// sessionWork records the facts a session's work gives its linked tasks, at
// their source: the agent reports each input it accepts and each time work
// proceeds (agent.WorkEntry), under the lock that admits and consumes its
// input. So a link is made with the instruction it comes with, a task starts
// only once its instruction left the queue, and a start can never land after
// the stop of the work it started.
type sessionWork struct {
	m         *Manager
	sessionID string
	// noticed says a report told the owner this session stopped and no work
	// entered since: a blocked run that goes on must say it works again.
	// Used only under the agent's work gate.
	noticed bool
}

// enter is the session's agent.AgentConfig.OnWork. An error refuses the input
// or ends the run before its work proceeds: work the owner believes recorded
// must not go on unrecorded.
func (w *sessionWork) enter(e agent.WorkEntry) error {
	tasksRepo := w.m.tasks
	if tasksRepo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	if t, ok := liveTaskOf(e.Admitted); ok {
		if _, err := tasksRepo.LinkExecution(ctx, t.actor, t.id, w.sessionID); err != nil {
			return err
		}
	}
	if !e.Work || e.Resumed && !w.noticed {
		return nil
	}
	// Any work of the executor starts its tasks, except the ones whose
	// instruction is still waiting in the queue: being queued is not started.
	var queued []int64
	for _, custom := range e.Queued {
		if t, ok := liveTaskOf(custom); ok {
			queued = append(queued, t.id)
		}
	}
	if err := tasksRepo.MarkExecutionStartedExcept(ctx, w.sessionID, queued); err != nil {
		return fmt.Errorf("live tasks: the start of this session's linked tasks was not recorded: %w", err)
	}
	w.noticed = false
	return nil
}

// workGate runs fn under the session's work gate (agent.Agent.WorkGate).
func (s *ManagedSession) workGate(fn func()) {
	if g, ok := s.runtime.Context().Agent.(interface{ WorkGate(func()) }); ok {
		g.WorkGate(fn)
		return
	}
	fn()
}

// admitTaskNotices persists a child's report through persist and, when the
// session is quiet or blocked right now, names in it the linked tasks the
// owner must decide on, marking them stopped once the report is durable. It
// holds the session's work gate across exactly that: no work enters between
// the check and the mark, and work entering afterwards clears the mark. Only
// local writes happen under it — never a delivery or an acknowledgement.
func (m *Manager) admitTaskNotices(sess *ManagedSession, rep owner.Report, persist func(owner.Report) (added, durable bool)) (added, durable bool) {
	sess.workGate(func() {
		ids := m.taskNotices(sess, &rep)
		if m.testTaskNoticeDecided != nil {
			m.testTaskNoticeDecided()
		}
		added, durable = persist(rep)
		if added && durable && len(ids) > 0 {
			m.markExecutionStopped(sess.ID, ids...)
			if sess.work != nil {
				sess.work.noticed = true
			}
		}
	})
	return added, durable
}

// taskNotices fills the report's linked tasks: the open ones the session
// executes and was not said to stop on, when it is quiet or blocked now.
func (m *Manager) taskNotices(sess *ManagedSession, rep *owner.Report) []int64 {
	rep.LinkedTasks = nil
	if m.tasks == nil || rep.UserKind != "" || rep.SessionID == "" || !sessionStoppedFor(sess, *rep) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	recs, err := m.tasks.UnwarnedExecutions(ctx, rep.SessionID)
	if err != nil {
		slog.Warn("live tasks: linked tasks not read for a report", "session", rep.SessionID, "error", err)
		return nil
	}
	ids := make([]int64, 0, len(recs))
	for _, r := range recs {
		rep.LinkedTasks = append(rep.LinkedTasks, owner.ReportTask{ID: r.ID, Title: r.Title})
		ids = append(ids, r.ID)
	}
	return ids
}

// sessionStoppedFor says the session, as it is now, is what the report says
// of it: waiting on the prompt the report asks about, or quiet — no run, no
// queued input, nothing left running (a restart ends what was). Called under
// the work gate, so a run cannot start in between.
func sessionStoppedFor(sess *ManagedSession, rep owner.Report) bool {
	state := sess.runtime.State.Current()
	if rep.Status == callbackStatusNeedsInput && state == bus.StatePermission {
		return true
	}
	ag := sess.runtime.Context().Agent
	if state == bus.StateRunning || state == bus.StatePermission || ag.IsRunning() || ag.QueueLen() > 0 {
		return false
	}
	return rep.InterruptedByRestart || rep.BackgroundCount == 0 && sess.runtime.BackgroundJobs() == 0
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

// ownerSessionIDs are the owners' conversations, whose checklists Tasks shows
// without the agents' filter.
func (m *Manager) ownerSessionIDs() []string {
	store, err := m.ownerStore()
	if err != nil {
		return nil
	}
	owners, err := store.List()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(owners))
	for _, own := range owners {
		if own.SessionID != "" {
			ids = append(ids, own.SessionID)
		}
	}
	return ids
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
// the outcome itself: the next scheduled run.
func (m *Manager) withRunState(rep *owner.Report) {
	if m.tasks == nil || rep.UserKind != "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	rep.NextRun = m.nextScheduledRun(ctx, rep.SessionID)
}

// markExecutionStopped records, once the report carrying them is durable,
// that the owner was told about these tasks (all of the session's when none
// are named): not again until the session works and stops once more.
func (m *Manager) markExecutionStopped(sessionID string, ids ...int64) {
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	if _, err := m.tasks.MarkExecutionsStopped(ctx, sessionID, ids); err != nil {
		slog.Warn("live tasks: stop not recorded; the owner may hear it again", "session", sessionID, "error", err)
	}
}

// currentTaskNotices drops, as a batch is painted for the owner, the linked
// task notices that no longer hold: the task was closed, moved or relinked to
// another session after the report was admitted, or its executor is working
// again (its next stop is said again). The report keeps its ID, so
// the batch is the same one however it is painted. A failed read keeps the
// notices: hearing one twice beats not hearing it.
func (m *Manager) currentTaskNotices(own owner.Owner, pending []owner.Report) []owner.Report {
	if m.tasks == nil {
		return pending
	}
	linked := false
	for _, rep := range pending {
		linked = linked || len(rep.LinkedTasks) > 0
	}
	if !linked {
		return pending
	}
	current, err := m.executionsBySession(own)
	if err != nil {
		slog.Warn("live tasks: linked tasks not read for a delivery", "owner", own.ID, "error", err)
		return pending
	}
	out := make([]owner.Report, len(pending))
	for i, rep := range pending {
		out[i] = rep
		if len(rep.LinkedTasks) == 0 {
			continue
		}
		if m.sessionWorking(rep.SessionID) {
			out[i].LinkedTasks = nil
			continue
		}
		open := map[int64]bool{}
		for _, r := range current[rep.SessionID] {
			open[r.ID] = true
		}
		kept := make([]owner.ReportTask, 0, len(rep.LinkedTasks))
		for _, t := range rep.LinkedTasks {
			if open[t.ID] {
				kept = append(kept, t)
			}
		}
		out[i].LinkedTasks = kept
	}
	return out
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
		if !m.reports.add(key, owner.Report{
			ID:           newReportID(),
			Status:       reportStatusRestart,
			RestartTasks: list,
			At:           time.Now().UTC().Format(time.RFC3339),
		}) {
			// Not durable: the next start says it again rather than never.
			continue
		}
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
	out, err := m.executionsBySession(own)
	if err != nil {
		slog.Warn("live tasks: linked tasks not read for the roster", "owner", own.ID, "error", err)
		return nil
	}
	return out
}

// executionsBySession maps each executor to the owner's open tasks it does.
func (m *Manager) executionsBySession(own owner.Owner) (map[string][]tasks.Record, error) {
	if m.tasks == nil || own.SessionID == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	recs, err := m.tasks.OwnerExecutions(ctx, own.SessionID)
	if err != nil {
		return nil, err
	}
	out := map[string][]tasks.Record{}
	for _, r := range recs {
		out[r.Execution.SessionID] = append(out[r.Execution.SessionID], r)
	}
	return out, nil
}
