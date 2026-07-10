package serve

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/ops"
)

// subscribeOps attaches the safe operational projection before a session is
// exposed. Like attention, it subscribes before taking its initial snapshots
// and gates events until those snapshots are installed, preventing an attach
// snapshot/event gap.
func (m *Manager) subscribeOps(sess *ManagedSession) {
	if m.ops == nil {
		return
	}

	sess.mu.Lock()
	title := sess.Title
	sess.mu.Unlock()
	_ = m.ops.UpsertSession(ops.SessionInput{
		ID:           sess.ID,
		Title:        title,
		CanonicalCWD: sess.CWD,
		Presence:     ops.PresenceActive,
	})

	var gateMu sync.Mutex
	ready := false
	var buffered []any
	var jobsMu sync.Mutex
	bashJobs := make(map[string]struct{})
	subagentCount := 0

	updateJobs := func() {
		jobsMu.Lock()
		jobs := ops.JobCounts{Subagents: subagentCount, Bash: len(bashJobs)}
		jobsMu.Unlock()
		_ = m.ops.UpdateJobs(sess.ID, jobs)
	}
	apply := func(event any) {
		now := time.Now()
		switch e := event.(type) {
		case bus.StateChanged:
			state, activity := opsLifecycle(e.State)
			_ = m.ops.UpdateLifecycle(sess.ID, ops.LifecycleUpdate{State: state, Activity: activity, At: now})
			if e.State == string(bus.StateError) {
				_ = m.ops.RecordMilestone(sess.ID, ops.Milestone{Type: ops.MilestoneError, At: now, RefID: fmt.Sprintf("state_error_%d", now.UnixNano())})
			}
		case bus.RunStarted:
			_ = m.ops.UpdateLifecycle(sess.ID, ops.LifecycleUpdate{State: ops.LifecycleRunning, Activity: ops.ActivityRunning, At: now})
			_ = m.ops.RecordMilestone(sess.ID, ops.Milestone{Type: ops.MilestoneRunStarted, At: now, RefID: opsRunRef(e.RunGen)})
		case bus.RunEnded:
			_ = m.ops.RecordMilestone(sess.ID, ops.Milestone{Type: ops.MilestoneRunEnded, At: now, RefID: opsRunRef(e.RunGen)})
			if e.Err != nil {
				_ = m.ops.RecordMilestone(sess.ID, ops.Milestone{Type: ops.MilestoneError, At: now, RefID: opsRunRef(e.RunGen)})
			}
		case bus.PermissionRequested:
			_ = m.ops.UpdateLifecycle(sess.ID, ops.LifecycleUpdate{State: ops.LifecycleRunning, Activity: ops.ActivityPermission, At: now})
			_ = m.ops.RecordMilestone(sess.ID, ops.Milestone{Type: ops.MilestonePermission, At: now, RefID: e.ID})
		case bus.AskUserRequested:
			_ = m.ops.RecordMilestone(sess.ID, ops.Milestone{Type: ops.MilestoneAskUser, At: now, RefID: e.ID})
		case bus.SubagentCountChanged:
			jobsMu.Lock()
			subagentCount = max(e.Count, 0)
			jobsMu.Unlock()
			updateJobs()
		case bus.BashJobStarted:
			jobsMu.Lock()
			bashJobs[e.JobID] = struct{}{}
			jobsMu.Unlock()
			updateJobs()
		case bus.BashJobEnded:
			jobsMu.Lock()
			delete(bashJobs, e.JobID)
			jobsMu.Unlock()
			updateJobs()
		case bus.AutoVerifyStarted:
			_ = m.ops.UpdateVerification(sess.ID, ops.Verification{State: ops.VerificationPending, At: now})
		case bus.AutoVerifyEnded:
			verification := ops.VerificationFailed
			if e.AllPass && e.Err == nil {
				verification = ops.VerificationPassed
			}
			_ = m.ops.UpdateVerification(sess.ID, ops.Verification{State: verification, At: now})
			_ = m.ops.RecordMilestone(sess.ID, ops.Milestone{Type: ops.MilestoneVerification, At: now, RefID: fmt.Sprintf("verification_%d", now.UnixNano())})
		}
	}
	unsub := sess.runtime.Bus.SubscribeAll(func(event any) {
		switch event.(type) {
		case bus.StateChanged, bus.RunStarted, bus.RunEnded, bus.PermissionRequested,
			bus.AskUserRequested, bus.SubagentCountChanged, bus.BashJobStarted,
			bus.BashJobEnded, bus.AutoVerifyStarted, bus.AutoVerifyEnded:
		default:
			return
		}
		gateMu.Lock()
		if !ready {
			buffered = append(buffered, event)
			gateMu.Unlock()
			return
		}
		gateMu.Unlock()
		apply(event)
	})

	// Query after subscribing. Buffered events are replayed before a final
	// reconciliation, so the durable snapshot wins over a delayed callback.
	reconcile := func() {
		if state, err := bus.QueryTyped[bus.GetSessionState, string](sess.runtime.Bus, bus.GetSessionState{}); err == nil {
			lifecycle, activity := opsLifecycle(state)
			_ = m.ops.UpdateLifecycle(sess.ID, ops.LifecycleUpdate{State: lifecycle, Activity: activity, At: time.Now()})
		}
		if subagents, err := bus.QueryTyped[bus.GetSubagents, []bus.SubagentSnapshot](sess.runtime.Bus, bus.GetSubagents{}); err == nil {
			jobsMu.Lock()
			subagentCount = 0
			for _, job := range subagents {
				if opsJobActive(job.Status) {
					subagentCount++
				}
			}
			jobsMu.Unlock()
		}
		if bash, err := bus.QueryTyped[bus.GetBashJobs, []bus.BashJobSnapshot](sess.runtime.Bus, bus.GetBashJobs{}); err == nil {
			jobsMu.Lock()
			bashJobs = make(map[string]struct{})
			for _, job := range bash {
				if opsJobActive(job.Status) {
					bashJobs[job.JobID] = struct{}{}
				}
			}
			jobsMu.Unlock()
		}
		updateJobs()
	}
	reconcile()
	gateMu.Lock()
	for _, event := range buffered {
		apply(event)
	}
	buffered = nil
	reconcile()
	ready = true
	gateMu.Unlock()

	var once sync.Once
	sess.pushUnsubs = append(sess.pushUnsubs, func() {
		once.Do(func() {
			unsub()
			m.ops.RemoveSession(sess.ID)
		})
	})
}

func (m *Manager) updateOpsTitle(sess *ManagedSession) {
	if m.ops == nil {
		return
	}
	sess.mu.Lock()
	title := sess.Title
	sess.mu.Unlock()
	_ = m.ops.UpsertSession(ops.SessionInput{ID: sess.ID, Title: title, CanonicalCWD: sess.CWD, Presence: ops.PresenceActive})
}

func opsLifecycle(state string) (ops.LifecycleState, ops.Activity) {
	switch state {
	case string(bus.StateRunning):
		return ops.LifecycleRunning, ops.ActivityRunning
	case string(bus.StatePermission):
		return ops.LifecycleRunning, ops.ActivityPermission
	case string(bus.StateError):
		return ops.LifecycleError, ops.ActivityError
	default:
		return ops.LifecycleIdle, ops.ActivityIdle
	}
}

func opsRunRef(runGen uint64) string { return fmt.Sprintf("run_%d", runGen) }

func opsJobActive(status string) bool { return status == "running" || status == "cancelling" }

func handleOpsOverview(m *Manager) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		if m.ops == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ops unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, m.ops.Snapshot())
	}
}

// handleOpsQuery exposes deterministic, read-only operational briefings. Its
// intentionally small query shape avoids accepting a natural-language command:
// view is sitrep, blockers, or status; status additionally requires target.
func handleOpsQuery(m *Manager) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.ops == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ops unavailable"})
			return
		}
		query := r.URL.Query()
		for key, values := range query {
			if (key != "view" && key != "target") || len(values) != 1 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ops query"})
				return
			}
		}
		view := query.Get("view")
		switch view {
		case "sitrep":
			if query.Has("target") {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target is only valid for status"})
				return
			}
			writeJSON(w, http.StatusOK, m.ops.Sitrep())
		case "blockers":
			if query.Has("target") {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target is only valid for status"})
				return
			}
			writeJSON(w, http.StatusOK, m.ops.Blockers())
		case "status":
			target, ok := query["target"]
			if !ok || target[0] == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status requires target"})
				return
			}
			writeJSON(w, http.StatusOK, m.ops.Status(target[0]))
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ops view"})
		}
	}
}
