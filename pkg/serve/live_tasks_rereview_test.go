package serve

// The re-review's residual probes. They first held open the asynchronous
// start writer that no longer exists; a start is now recorded synchronously as
// the work enters (agent.WorkEntry), so each schedule is reproduced through
// that real admission point instead: enterWork is exactly what the agent does
// under its work gate when work proceeds.

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// enterWork is work of sess proceeding, as its agent reports it: under the
// agent's work gate, with the queue as it is at that moment.
func enterWork(t *testing.T, sess *ManagedSession) {
	t.Helper()
	ag, ok := sess.runtime.Context().Agent.(*agent.Agent)
	if !ok {
		t.Fatal("fixture: session without an agent")
	}
	var err error
	sess.workGate(func() {
		var queued []map[string]any
		for _, it := range ag.PendingSteers() {
			queued = append(queued, it.Custom)
		}
		err = sess.work.enter(agent.WorkEntry{Work: true, Queued: queued})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Owner creation's precheck and a session becoming loaded are separate
// operations. The seam pauses only the owner after its precheck, before the
// store write: a filesystem/scheduler delay, not a changed decision.
func TestReReviewConcurrentOwnerCreationCannotLeaveUnobservedExecutor(t *testing.T) {
	shortReportWindow(t, time.Hour)
	m := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	m.testOwnerPrechecked = func() { close(entered); <-release }
	result := make(chan error, 1)
	var ownSess *ManagedSession
	go func() {
		info, err := m.CreateOwner(CreateOwnerOpts{Root: root, Name: "Concurrent owner"})
		if err == nil {
			ownSess, _ = m.Get(info.SessionID)
		}
		result <- err
	}()
	<-entered
	child, err := m.CreateSession(CreateOpts{CWD: root, Title: "created while owner precheck passed"})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	task := liveOwnerTask(t, m, ownSess, "Execute concurrently created child")
	res := runSessionsTool(t, ownSess, map[string]any{"action": "send", "session_id": child.ID, "text": "execute the assigned task", "task": float64(task.ID)})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	pollUntil(t, 5*time.Second, "executor completed instruction", func() bool {
		for _, msg := range child.History() {
			if msg.Role == "assistant" && assistantText(msg) != "" {
				return true
			}
		}
		return false
	})
	child.runtime.Bus.Drain(time.Second)
	got := liveTask(t, m, task.ID)
	if got.Status != tasks.StatusInProgress || got.Execution == nil || got.Execution.StartedAt == 0 {
		t.Fatalf("accepted real sessions send consumed task, but executor has no start: observer=%v status=%s execution=%+v", child.ownerObserver != nil, got.Status, got.Execution)
	}
	child.mu.Lock()
	observed := child.ownerObserver != nil
	child.mu.Unlock()
	if !observed {
		t.Fatal("a linked executor has no way to report its turns to the owner")
	}
}

// A start used to be written by a worker that could be paused past the
// terminal outcome of its own run. The start is now recorded as the work
// enters, before anything of the run is observed: the stop reported after it
// stays, and a resumed executor is warned again.
func TestReReviewDelayedStartCannotEraseItsOwnStop(t *testing.T) {
	for _, previouslyStopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_run", true: "resume_after_prior_notice"}[previouslyStopped], func(t *testing.T) {
			m, own, child, task := reviewLinkedChild(t)
			if previouslyStopped {
				if err := m.tasks.MarkExecutionStarted(context.Background(), child.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := m.tasks.MarkExecutionStopped(context.Background(), child.ID); err != nil {
					t.Fatal(err)
				}
			}
			publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
			enterWork(t, child)
			publishRunEnd(child, 1, "quickly finished")
			child.ownerObserver.sync()
			child.ownerObserver.waitWorkers()
			before := liveTask(t, m, task.ID).Execution
			reports := outbox(t, m, own.CodebaseKey)
			child.runtime.Bus.Drain(time.Second)
			after := liveTask(t, m, task.ID).Execution
			candidates, err := m.tasks.InterruptedExecutions(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if before.StoppedAt == 0 || len(reports) != 1 {
				t.Fatalf("fixture: before=%+v reports=%+v", before, reports)
			}
			if after.StoppedAt == 0 || len(candidates) != 0 || len(reports[0].LinkedTasks) != 1 {
				t.Fatalf("delayed start overtook its own durably reported stop: prior_stop=%v before=%+v after=%+v boot_candidates=%d warned_tasks=%d", previouslyStopped, before, after, len(candidates), len(reports[0].LinkedTasks))
			}
		})
	}
}

// Work of a busy executor proceeding while the owner's linked instruction is
// still queued does not start the task: its instruction has not reached it.
func TestReReviewDelayedUnlinkedStartDoesNotStartQueuedTask(t *testing.T) {
	shortReportWindow(t, time.Hour)
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	entered, releaseProvider := make(chan struct{}, 1), make(chan struct{})
	provider := reviewProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if requestMentions(req, "unrelated current instruction") && !requestMentions(req, "queued linked instruction") {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-releaseProvider:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return simpleResponse("done"), nil
	})
	m := newTestManagerWithConfig(t, context.Background(), provider, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	root := t.TempDir()
	_, ownSess := ownerWithSession(t, m, root, "Review owner")
	child := ownerChild(t, m, root, "busy unrelated executor")
	if _, _, _, err := m.Send(child.ID, "unrelated current instruction", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	<-entered
	task := liveOwnerTask(t, m, ownSess, "Not yet consumed linked instruction")
	res := runSessionsTool(t, ownSess, map[string]any{"action": "send", "session_id": child.ID, "text": "queued linked instruction", "task": float64(task.ID)})
	if res.IsError {
		close(releaseProvider)
		t.Fatal(toolText(res))
	}
	before := liveTask(t, m, task.ID)
	// The unrelated run's work goes on while the instruction waits.
	enterWork(t, child)
	after := liveTask(t, m, task.ID)
	queueLen, _ := bus.QueryTyped[bus.GetQueueLen, int](child.runtime.Bus, bus.GetQueueLen{})
	close(releaseProvider)
	if before.Status != tasks.StatusPending || before.Execution == nil || before.Execution.StartedAt != 0 || queueLen != 1 {
		t.Fatalf("fixture: before=%+v queue=%d", before, queueLen)
	}
	if after.Status != tasks.StatusPending || after.Execution.StartedAt != 0 {
		t.Fatalf("old unrelated run start marked the newly linked task while its instruction is still queued: status=%s execution=%+v queue=%d", after.Status, after.Execution, queueLen)
	}
	pollUntil(t, 5*time.Second, "the queued instruction consumed and the task started", func() bool {
		rec := liveTask(t, m, task.ID)
		return rec.Status == tasks.StatusInProgress && rec.Execution.StartedAt != 0
	})
}

func TestReReviewTwoUserStopsNotRelabeledWhenObserverLagsShutdown(t *testing.T) {
	shortReportWindow(t, time.Hour)
	shortOwnerQuiescence(t, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 4)
	provider := reviewProvider(func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	m := newTestManagerWithConfig(t, ctx, provider, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	root := t.TempDir()
	info, _ := ownerWithSession(t, m, root, "Review owner")
	child := ownerChild(t, m, root, "two user stops")
	terminal := make(chan bus.RunEnded, 4)
	unsub := child.runtime.Bus.Subscribe(func(e bus.RunEnded) { terminal <- e })
	defer unsub()
	for run := 1; run <= 2; run++ {
		if _, _, _, err := m.Send(child.ID, "hold until user Stop", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("provider not entered")
		}
		if run == 1 {
			child.ownerObserver.sync()
			child.ownerObserver.mu.Lock()
		}
		if err := m.Cancel(child.ID); err != nil {
			child.ownerObserver.mu.Unlock()
			t.Fatal(err)
		}
		select {
		case e := <-terminal:
			if !e.Cancelled {
				child.ownerObserver.mu.Unlock()
				t.Fatal("fixture: not cancelled")
			}
		case <-time.After(5 * time.Second):
			child.ownerObserver.mu.Unlock()
			t.Fatal("Stop did not end run")
		}
	}
	cancel()
	child.ownerObserver.mu.Unlock()
	child.ownerObserver.sync()
	child.ownerObserver.waitWorkers()
	got := outbox(t, m, info.CodebaseKey)
	if len(got) != 2 {
		t.Fatalf("fixture: reports=%+v", got)
	}
	for _, rep := range got {
		if rep.InterruptedByRestart {
			t.Fatalf("earlier completed user Stop lost its cause after a second Stop: %+v", got)
		}
	}
}

// The notice's persistence happens under the child's work gate. It must not
// hold the child's ordered bus subscriber, and work entering meanwhile waits
// for it and then clears the stop it marks.
func TestReReviewPersistenceWaitDoesNotBlockOrderedSubscriber(t *testing.T) {
	m, own, child, task := reviewLinkedChild(t)
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	enterWork(t, child)
	child.ownerObserver.sync()
	entered, release := make(chan struct{}), make(chan struct{})
	m.testTaskNoticeDecided = func() {
		select {
		case <-entered:
		default:
			close(entered)
			<-release
		}
	}
	publishRunEnd(child, 1, "quiet: the notice is being persisted")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture: the report never reached its notice")
	}
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	o := child.ownerObserver
	synced := make(chan struct{})
	go func() { o.sync(); close(synced) }()
	select {
	case <-synced:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("persistence wait blocked the bus subscriber")
	}
	worked := make(chan struct{})
	go func() { enterWork(t, child); close(worked) }()
	select {
	case <-worked:
		close(release)
		t.Fatal("work entered while a notice of the stop was half persisted")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-worked
	o.waitWorkers()
	got := outbox(t, m, own.CodebaseKey)
	if len(got) != 1 || len(got[0].LinkedTasks) != 1 {
		t.Fatalf("persist unexpectedly refused: %+v", got)
	}
	if ex := liveTask(t, m, task.ID).Execution; ex.StoppedAt != 0 {
		t.Fatalf("new run was stopped: %+v", ex)
	}
}

func TestReReviewShutdownConcludesOnlyPromisedFollowUps(t *testing.T) {
	m, own, child, _ := reviewLinkedChild(t)
	publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
	child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "turn-1-long-job"})
	publishRunEnd(child, 1, "turn 1 launches background work")
	first := waitForOutbox(t, m, own.CodebaseKey, 1)
	if first[0].BackgroundCount != 1 {
		t.Fatalf("fixture: %+v", first)
	}
	publishRunStart(child, 2, bus.RunOrigin{Explicit: true})
	publishRunEnd(child, 2, "turn 2 finishes while turn 1 job still runs")
	prior := waitForOutbox(t, m, own.CodebaseKey, 2)
	child.ownerObserver.sync()
	child.ownerObserver.waitWorkers()
	m.Shutdown()
	got := outbox(t, m, own.CodebaseKey)
	if len(got) != 4 {
		t.Fatalf("want 2 turns + their 2 promised follow-ups: %+v", got)
	}
	for _, rep := range got[2:] {
		if !rep.InterruptedByRestart || rep.FollowUpOf == "" || rep.FollowUpOf != prior[0].At && rep.FollowUpOf != prior[1].At {
			t.Fatalf("shutdown invented/misnamed a follow-up: %+v prior=%+v", rep, prior)
		}
	}
}
