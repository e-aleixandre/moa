package serve

// Project owners — the observer that turns a child session's runs into
// reports. It is deliberately NOT subscribeRunOutcomes (outcomes.go): the
// automation callback answers a caller that is waiting for the whole
// autonomous chain and may cancel; an owner reads a status board and needs one
// entry per semantic turn, on time, even when the session leaves work running.
//
// The two policies differ in exactly three places, which is why they are
// separate:
//
//   - Waiting. A callback waits for quiescence indefinitely. An owner waits
//     ownerReportQuiescenceTimeout and then reports anyway, saying how much
//     work is still running. A child that starts a dev server is finished with
//     what it was asked; the server will never exit, and the owner must not be
//     told nothing happened.
//   - Semantic turns. A background job that finishes hours later injects its
//     own notification, which is a new run with a new generation. That is the
//     same turn continuing, not a second thing to report.
//   - Shutdown. A callback is cancelled (its caller will retry). A report is
//     flushed: it is the owner's only record of the turn.

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// ownerReportQuiescenceTimeout bounds how long a completed turn waits for the
// session's background work before it is reported anyway. A var so tests do
// not have to spend it.
var ownerReportQuiescenceTimeout = 15 * time.Second

// ownerReportSettleRecheck is the pause between two quiescence checks while a
// run of the same turn is still open. Short: it only covers the handover
// between a background job ending and the run delivering its result.
var ownerReportSettleRecheck = 25 * time.Millisecond

// ownerReportObserver watches one child session and hands completed semantic
// turns to the report coordinator.
//
// A SEMANTIC TURN is one thing that happened to the project, and it is what
// the owner reads one line about. It may span several runs: the instruction
// itself, the run a finished background job starts to deliver its result, a
// goal's next iteration, a compaction in the middle. Turns are identified by a
// monotonic ID that is never reused and never rewound, and every run
// generation is mapped to the turn it belongs to when it starts. A terminal
// event then resolves through its OWN generation, so it lands on the right
// turn even if the next run was published first.
//
// Everything here is runtime-local and deliberately so: job IDs do not survive
// a restart, and a resumed session legitimately starts a fresh turn. Nothing
// is persisted, so nothing here can be wrong after a restart.
type ownerReportObserver struct {
	sess *ManagedSession
	// emit delivers a finished turn. Injected so tests can observe the
	// sequence without a coordinator, and so the coordinator stays the only
	// thing that knows about the outbox.
	emit func(runOutcome)

	mu sync.Mutex
	// nextTurnID mints turn identities. Monotonic: a turn ID is never reused,
	// so a late continuation can never be mistaken for a newer turn.
	nextTurnID uint64
	// currentTurn is the turn the most recent run was attributed to. It is
	// what a continuation with no job ID, and a background job starting,
	// attach themselves to.
	currentTurn uint64
	// runTurn maps a live run generation to its turn. A terminal event reads
	// this rather than "whatever turn is current now", which is the only way
	// it stays correct when generations overlap.
	runTurn map[uint64]uint64
	// jobTurn maps a background job to the turn that started it, so its
	// eventual completion run folds back into that turn.
	jobTurn map[string]uint64
	// openRuns counts, per turn, the runs that have started and not ended. A
	// turn is not finished while one of its own runs is going, whatever the
	// state machine says at that instant.
	openRuns map[uint64]int
	// latest is the most recent completed outcome of a turn, so a waiter that
	// has been asleep reports what the turn ended up saying rather than the
	// text it captured when it went to sleep.
	latest map[uint64]runOutcome
	// reported is the turns that have already left for the owner. It is what
	// makes a late continuation silent instead of a duplicate.
	reported map[uint64]bool
	// closed stops new work being admitted once the session is being torn
	// down. discard additionally abandons what was pending (an explicit
	// delete: the conversation is gone, and so is the reason to report it).
	closed  bool
	discard bool

	// workers tracks the waiter goroutines this observer launched. Every Add
	// happens under mu, together with the closed/discard decision, so once
	// closed is set no Add can follow and Wait cannot race one.
	workers sync.WaitGroup
	// ctx bounds those waiters, and is deliberately rooted in
	// context.Background() rather than the session's context.
	//
	// A SIGTERM cancels the session context. If the waiters hung off it they
	// would all wake at that instant and race each other to emit, which is
	// precisely the ordering the shutdown flush exists to impose. Their
	// lifetime is instead ended explicitly: flush and discardPending cancel
	// this, in both cases AFTER deciding what is reported and in what order.
	// The ordinary 15s per-turn deadline still applies on top of it.
	ctx    context.Context
	cancel context.CancelFunc

	// syncMu guards the barrier acknowledgements the subscriber closes.
	syncMu      sync.Mutex
	syncPending map[uint64]chan struct{}
	syncSeq     uint64
}

// ownerObserverBarrier is a private event used only to prove that this
// session's observer has consumed everything published before it.
//
// Nothing else subscribes to it and it never leaves the process. It exists
// because a subscriber's queue is FIFO: once the observer's own callback sees
// barrier N, every event published before barrier N has already been handled
// by that same callback. That is a direct proof about the one subscriber that
// matters, rather than an inference from a global drain timeout.
type ownerObserverBarrier struct {
	SessionID string
	ID        uint64
}

// newOwnerReportObserver wires the observer into a child session's bus.
//
// One SubscribeAll subscriber sees run lifecycle, provenance and background
// job events in publication order on a single goroutine. That order is what
// attributes a job to the turn that started it.
func newOwnerReportObserver(sess *ManagedSession, emit func(runOutcome)) *ownerReportObserver {
	ctx, cancel := context.WithCancel(context.Background())
	o := &ownerReportObserver{
		sess:        sess,
		emit:        emit,
		runTurn:     map[uint64]uint64{},
		jobTurn:     map[string]uint64{},
		openRuns:    map[uint64]int{},
		latest:      map[uint64]runOutcome{},
		reported:    map[uint64]bool{},
		syncPending: map[uint64]chan struct{}{},
		ctx:         ctx,
		cancel:      cancel,
	}
	// needsInputSent is per-run, touched only from the subscriber goroutine
	// below, in publication order — the same guard subscribeRunOutcomes uses
	// and for the same reason.
	var needsInputSent bool
	sess.pushUnsubs = append(sess.pushUnsubs, sess.runtime.Bus.SubscribeAll(func(event any) {
		switch e := event.(type) {
		case bus.RunStarted:
			needsInputSent = false
			o.beginRun(e.RunGen, e.Origin)
		case bus.SubagentStarted:
			o.attachJob(e.JobID)
		case bus.BashJobStarted:
			o.attachJob(e.JobID)
		case bus.PermissionRequested:
			if !needsInputSent {
				needsInputSent = true
				o.blocked(e.RunGen, permissionPending(e))
			}
		case bus.AskUserRequested:
			if !needsInputSent {
				needsInputSent = true
				o.blocked(e.RunGen, askPending(e))
			}
		case bus.RunEnded:
			o.runEnded(e)
		case ownerObserverBarrier:
			if e.SessionID == sess.ID {
				o.ackBarrier(e.ID)
			}
		}
	}))
	return o
}

// sync blocks until this observer's subscriber has consumed every event
// published before the call.
//
// It publishes a private barrier and waits for its own callback to reach it.
// Because a subscriber's queue is FIFO, that acknowledgement proves the
// RunEnded events published earlier have already been processed by THIS
// subscriber — a guarantee about the one consumer that matters, which a global
// Bus.Drain timeout cannot give. Used before the shutdown flush so the
// observer's knowledge is complete when it decides what to report. Callers
// must invoke it while this observer's subscription and bus are still live.
func (o *ownerReportObserver) sync() {
	o.syncMu.Lock()
	o.syncSeq++
	id := o.syncSeq
	ack := make(chan struct{})
	o.syncPending[id] = ack
	o.syncMu.Unlock()

	o.sess.runtime.Bus.Publish(ownerObserverBarrier{SessionID: o.sess.ID, ID: id})
	<-ack
}

// ackBarrier releases a sync waiter. Runs on the subscriber goroutine, after
// every event queued before the barrier has been handled.
func (o *ownerReportObserver) ackBarrier(id uint64) {
	o.syncMu.Lock()
	ack, ok := o.syncPending[id]
	delete(o.syncPending, id)
	o.syncMu.Unlock()
	if ok {
		close(ack)
	}
}

// beginRun attributes a starting run to a semantic turn.
//
// Unknown provenance starts a new turn: an extra report costs the owner a
// moment, a missing one costs it the turn.
func (o *ownerReportObserver) beginRun(gen uint64, origin bus.RunOrigin) {
	o.mu.Lock()
	defer o.mu.Unlock()
	turn := o.resolveTurnLocked(origin)
	o.runTurn[gen] = turn
	o.currentTurn = turn
	o.openRuns[turn]++
}

// resolveTurnLocked decides which turn a run belongs to. Callers hold mu.
func (o *ownerReportObserver) resolveTurnLocked(origin bus.RunOrigin) uint64 {
	if !origin.Explicit {
		// A job reporting back belongs to the turn that started it.
		for _, jobID := range origin.ContinuationOf {
			if turn, known := o.jobTurn[jobID]; known {
				return turn
			}
		}
		// Machinery continuing whatever is under way.
		if origin.ContinueCurrent && o.currentTurn != 0 {
			return o.currentTurn
		}
	}
	o.nextTurnID++
	return o.nextTurnID
}

// turnOf resolves a terminal event's turn through its own generation, falling
// back to a fresh turn when the run started before this observer existed (a
// session resumed mid-run). Callers hold mu.
func (o *ownerReportObserver) turnOfLocked(gen uint64) uint64 {
	if turn, known := o.runTurn[gen]; known {
		delete(o.runTurn, gen)
		if o.openRuns[turn] > 0 {
			o.openRuns[turn]--
			if o.openRuns[turn] == 0 {
				delete(o.openRuns, turn)
			}
		}
		return turn
	}
	o.nextTurnID++
	return o.nextTurnID
}

// turnSettled reports that no run of this turn is open. Complements the
// runtime's quiescence rather than duplicating it: quiescence is about
// background work, this is about the foreground run that work is about to
// start.
func (o *ownerReportObserver) turnSettled(turn uint64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.openRuns[turn] <= 0
}

// attachJob records that a background job belongs to the turn running now.
func (o *ownerReportObserver) attachJob(jobID string) {
	if jobID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.jobTurn[jobID] = o.currentTurn
}

// blocked reports a session waiting on a human.
//
// It is NOT deduplicated against an already-reported turn, and that is a
// deliberate exception to the one-entry-per-turn rule: a session stuck on a
// question is the owner's cue to act, and the cost of telling it twice is far
// below the cost of a child waiting for ever because its turn had already been
// reported. The turn is not marked reported either — it still owes its
// eventual done/failed outcome.
func (o *ownerReportObserver) blocked(runGen uint64, pending *CallbackPending) {
	o.mu.Lock()
	if !o.admitLocked() {
		o.mu.Unlock()
		return
	}
	o.mu.Unlock()
	out := runOutcome{Status: callbackStatusNeedsInput, RunGen: runGen, Pending: pending}
	o.launch(func() {
		if o.discarded() {
			return
		}
		slog.Info("owner outcome emitted", "codebase", o.sess.CWD, "session", o.sess.ID,
			"status", callbackStatusNeedsInput, "run_gen", runGen, "background", 0)
		o.emit(out)
	})
}

// runEnded records a terminal run and decides when its turn is reported.
//
// A failed run is reported at once: it is over, and waiting for background
// work would only delay news the owner has to act on. A successful one is the
// turn's result, so it waits for the autonomous chain — bounded.
func (o *ownerReportObserver) runEnded(e bus.RunEnded) {
	if e.Err != nil {
		o.failed(e)
		return
	}
	o.mu.Lock()
	turn := o.turnOfLocked(e.RunGen)
	if o.closed || o.discard {
		o.mu.Unlock()
		return
	}
	if o.reported[turn] {
		o.mu.Unlock()
		slog.Info("owner outcome suppressed", "codebase", o.sess.CWD, "session", o.sess.ID,
			"status", callbackStatusDone, "run_gen", e.RunGen, "turn", turn,
			"reason", "internal continuation of an already reported turn")
		return
	}
	// Record the turn's latest text before the waiter sleeps: whoever emits
	// reads this, so a continuation landing before the deadline updates the
	// single report instead of adding one.
	o.latest[turn] = runOutcome{Status: callbackStatusDone, RunGen: e.RunGen, FinalText: e.FinalText}
	if !o.admitLocked() {
		o.mu.Unlock()
		return
	}
	o.mu.Unlock()
	o.launch(func() { o.awaitAndEmit(turn) })
}

// failed reports a run that ended in an error, immediately.
func (o *ownerReportObserver) failed(e bus.RunEnded) {
	o.mu.Lock()
	turn := o.turnOfLocked(e.RunGen)
	if o.reported[turn] {
		// A late continuation of a turn the owner has already read. Its
		// failure belongs to that same turn, so it is not a second entry.
		o.mu.Unlock()
		slog.Info("owner outcome suppressed", "codebase", o.sess.CWD, "session", o.sess.ID,
			"status", callbackStatusFailed, "run_gen", e.RunGen, "turn", turn,
			"reason", "internal continuation of an already reported turn")
		return
	}
	if !o.admitLocked() {
		o.mu.Unlock()
		return
	}
	o.reported[turn] = true
	delete(o.latest, turn)
	o.mu.Unlock()

	out := runOutcome{
		Status:    callbackStatusFailed,
		RunGen:    e.RunGen,
		FinalText: e.FinalText,
		Err:       e.Err.Error(),
	}
	o.launch(func() {
		// The quiescence counters are maintained by a different subscriber, so
		// a job started moments ago may not be counted yet on this goroutine.
		// Draining the accepted batch first is what makes the number the owner
		// reads the real one. Legal here and not in the subscriber callback:
		// this is a worker goroutine, so the drain is not waiting on itself.
		o.sess.runtime.Bus.Drain(2 * time.Second)
		out.BackgroundCount = o.sess.runtime.BackgroundWork()
		if o.discarded() {
			return
		}
		slog.Info("owner outcome emitted", "codebase", o.sess.CWD, "session", o.sess.ID,
			"status", callbackStatusFailed, "run_gen", e.RunGen, "turn", turn,
			"background", out.BackgroundCount)
		o.emit(out)
	})
}

// discarded reports whether delete has abandoned this observer's work. It
// deliberately ignores closed: shutdown accepts workers already in flight and
// flushes their reports, while delete must suppress them.
func (o *ownerReportObserver) discarded() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.discard
}

// awaitAndEmit waits for the turn to be over, then reports it — or reports it
// anyway when the wait runs out, carrying the count of what is still running.
//
// "Over" is two conditions, not one: the runtime has no background work left
// AND this turn has no run open. A background job delivers its result by
// starting a run of its own, so the instant between the job ending and that
// run being admitted looks quiescent while the turn is plainly still going.
// Reporting there would carry the text of the first pass and suppress the real
// conclusion as a duplicate.
//
// A turn is NOT abandoned because a later turn started. Every completed
// semantic turn is one thing that happened to the project and reports on its
// own deadline; a session that is asked three things in a row owes the owner
// three lines, not one.
func (o *ownerReportObserver) awaitAndEmit(turn uint64) {
	deadline := time.Now().Add(ownerReportQuiescenceTimeout)
	quiescent := false
	for {
		ctx, cancel := context.WithDeadline(o.ctx, deadline)
		quiescent = o.sess.runtime.WaitQuiescent(ctx)
		cancel()
		if !quiescent || o.turnSettled(turn) {
			break
		}
		// Quiescent but a run of this turn is open: the chain is between two
		// of its own steps. Give it the moment it needs, within the deadline.
		select {
		case <-time.After(ownerReportSettleRecheck):
		case <-o.ctx.Done():
			quiescent = false
		}
		if time.Now().After(deadline) {
			quiescent = quiescent && o.turnSettled(turn)
			break
		}
	}

	background := o.sess.runtime.BackgroundWork()
	o.mu.Lock()
	out, have := o.latest[turn]
	if o.discard || !have || o.reported[turn] {
		// Already emitted by another waiter for this same turn, or abandoned.
		o.mu.Unlock()
		return
	}
	o.reported[turn] = true
	delete(o.latest, turn)
	o.mu.Unlock()

	if !quiescent {
		out.BackgroundCount = background
		slog.Info("owner outcome emitted after quiescence timeout", "codebase", o.sess.CWD,
			"session", o.sess.ID, "status", out.Status, "run_gen", out.RunGen, "turn", turn,
			"background", background, "waited", ownerReportQuiescenceTimeout)
	} else {
		slog.Info("owner outcome emitted", "codebase", o.sess.CWD, "session", o.sess.ID,
			"status", out.Status, "run_gen", out.RunGen, "turn", turn, "background", 0)
	}
	o.emit(out)
}

// flush reports EVERY completed turn that has not been reported yet, in the
// order the turns happened, immediately and without waiting for anything.
//
// It is what shutdown calls. Each of those turns is one thing that happened to
// the project whose text is already known; the only thing left that could lose
// them is the process exiting. Reporting just the newest would silently drop
// the others.
func (o *ownerReportObserver) flush() {
	o.mu.Lock()
	if o.discard {
		o.closed = true
		o.mu.Unlock()
		o.cancel()
		return
	}
	turns := make([]uint64, 0, len(o.latest))
	for turn := range o.latest {
		if !o.reported[turn] {
			turns = append(turns, turn)
		}
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i] < turns[j] })
	pending := make([]runOutcome, 0, len(turns))
	for _, turn := range turns {
		o.reported[turn] = true
		pending = append(pending, o.latest[turn])
		delete(o.latest, turn)
	}
	// Stop admitting new work only after choosing: a waiter that wakes now
	// finds its turn reported and returns without duplicating it. The cancel
	// is what wakes them, so waitWorkers does not sit through a quiescence
	// budget for a turn already in hand.
	o.closed = true
	o.mu.Unlock()
	o.cancel()

	background := o.sess.runtime.BackgroundWork()
	for i, out := range pending {
		out.BackgroundCount = background
		slog.Info("owner outcome flushed at shutdown", "codebase", o.sess.CWD,
			"session", o.sess.ID, "status", out.Status, "run_gen", out.RunGen,
			"turn", turns[i], "background", background)
		o.emit(out)
	}
}

// discardPending abandons everything unreported. Used when the session is
// deleted: there is no conversation left for the owner to look into.
func (o *ownerReportObserver) discardPending() {
	o.mu.Lock()
	o.discard = true
	o.closed = true
	o.mu.Unlock()
	o.cancel()
}

// waitWorkers blocks until every waiter this observer launched has finished.
// Safe against a concurrent Add because admission and Add happen together
// under mu, and flush/discardPending set closed before releasing it.
func (o *ownerReportObserver) waitWorkers() { o.workers.Wait() }

// admitLocked reserves a worker slot if the observer is still accepting work.
// Callers hold mu, and must launch() only if it returned true: doing the Add
// inside the same critical section as the closed check is what makes "no Add
// after close" an invariant rather than a race.
func (o *ownerReportObserver) admitLocked() bool {
	if o.closed || o.discard {
		return false
	}
	o.workers.Add(1)
	return true
}

// launch runs an admitted worker. Never called without a matching admitLocked.
func (o *ownerReportObserver) launch(fn func()) {
	go func() {
		defer o.workers.Done()
		fn()
	}()
}
