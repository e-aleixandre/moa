package serve

// Project owners — reports. A child session's run outcome (the same vocabulary
// the automation callback uses, see outcomes.go) becomes a report; reports are
// batched per owner and delivered into the owner's conversation as one message.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// reportSource is the custom envelope every delivered batch carries. The
// frontend renders it as an event block rather than a user waypoint: the owner
// did not type it.
const reportSource = "report"

// reportBatchWindow coalesces the reports of a codebase: a session finishing is
// rarely urgent on its own, and an owner woken once per child would spend its
// context on interruptions. Overridden in tests.
var reportBatchWindow = 60 * time.Second

// reportConfirmTimeout bounds the wait for the delivered message to appear in
// the owner's transcript. A prompt is appended inside the run goroutine, so the
// message is not in history the instant Execute returns.
var reportConfirmTimeout = 10 * time.Second

// reportDeliveryAttempts counts delivery attempts across the process. It exists
// so shutdown can be asserted to be a full stop: after Close nothing may retry.
var reportDeliveryAttempts atomic.Uint64

// reportHeadChars / reportTailChars abridge a child's final message in a
// report: the beginning says what the turn was about, the end carries the
// conclusion and the book delta. What is cut in between is announced, with how
// to read it whole through the `sessions` tool.
const (
	reportHeadChars = 800
	reportTailChars = 1500
)

// maxBookDeltaBytes caps the delta itself. It is a list of paths and one line
// each; anything longer is a session writing its report into the wrong section.
const maxBookDeltaBytes = 4 << 10

// gitPositionTimeout bounds the three plumbing calls a report makes. A report
// must not wait on a slow filesystem to be delivered. It is a var so a test
// can prove what a git that never answers does to a report.
var gitPositionTimeout = 3 * time.Second

// reportCoordinator batches reports per codebase and delivers them to the
// owner. It is a single actor per Manager: all batch state (pending reports,
// timers, delivery attempts) is owned by one goroutine, so the outbox on disk
// and the batches in memory can never disagree under concurrency.
type reportCoordinator struct {
	mgr   *Manager
	store *owner.Store
	mail  chan reportCommand
	ctx   context.Context
	// window is captured at construction rather than read per batch: a test
	// that shortens it must not race the coordinators other tests left running.
	window time.Duration
	// quit stops the loop on Close; closed once, guarded by closeOnce. done is
	// closed by the loop when it has drained the mailbox and persisted what was
	// pending, so Shutdown can wait for that to have happened.
	quit      chan struct{}
	done      chan struct{}
	ready     chan struct{}
	closeOnce sync.Once
	// admission establishes the order between accepting a child outcome and a
	// heartbeat. An add keeps its read admission until the actor has persisted
	// it; a heartbeat keeps exclusive admission until its actor command has
	// either delivered the beat or established a report barrier.
	admission sync.RWMutex
	// postMu / closing / posters are the admission gate between senders and
	// Close. A sender is admitted under the read side and counted; Close takes
	// the write side, sets closing, and waits for the admitted senders. That
	// ordering is what makes "nothing can enqueue after the actor's final
	// drain" a property of the code rather than a matter of timing: once Close
	// signals quit, every possible sender has already put its command in the
	// mailbox, and the drain sees all of them.
	postMu  sync.RWMutex
	closing bool
	posters sync.WaitGroup
	// Advisory owner events must never wait for an actor currently confirming a
	// transcript. The set coalesces them while their one best-effort mailbox
	// command is outstanding.
	advisoryMu sync.Mutex
	advisory   map[string]struct{}
	// testReportAdmitted makes the admission linearization observable without
	// widening the production protocol. It is installed before tests start any
	// worker and never changed concurrently.
	testReportAdmitted func()
}

// reportCommand is the actor's mailbox message: a new report, or a nudge to try
// delivering what is pending (timer expiry, or the owner going idle).
type reportCommand struct {
	key    string
	report *owner.Report
	// beginShutdown puts the actor in accept-only mode: timers disarmed, no
	// deliveries, reports still accepted and still persisted.
	beginShutdown bool
	// heartbeat is deliberately executed by the actor, rather than returning a
	// "clear" result to the ticker. That makes checking the report barrier and
	// starting the owner run one indivisible protocol step.
	heartbeat *heartbeatCommand
	advisory  bool
	// ack is closed once the actor has processed this command, including its
	// attempt to persist the outbox. A sink that reports a turn has crossed
	// the outbox boundary only when this closes — before that the report exists
	// solely in a channel, which a process exit does not preserve.
	ack chan struct{}
}

type heartbeatCommand struct {
	wake  func() error
	reply chan heartbeatResult
}

type heartbeatResult uint8

const (
	heartbeatDelivered heartbeatResult = iota
	heartbeatDeferred
	heartbeatFailed
	heartbeatStopped
)

// reportBatch is the per-codebase state: what is pending and the timer that
// will flush it.
type reportBatch struct {
	pending            []owner.Report
	outbox             owner.ReportsOutbox
	timer              *time.Timer
	deferNextHeartbeat bool
	// outboxFailed remembers that the last write of this batch failed, so the
	// warning is logged once rather than per report while the disk is broken.
	outboxFailed   bool
	preservedPaths map[string]struct{}
}

// newReportCoordinator starts the actor. Reports are disabled (nil) when the
// config directory cannot be resolved, exactly like owners themselves.
func newReportCoordinator(ctx context.Context, m *Manager) *reportCoordinator {
	store, err := m.ownerStore()
	if err != nil {
		slog.Warn("owner reports disabled", "error", err)
		return nil
	}
	c := &reportCoordinator{
		mgr:      m,
		store:    store,
		mail:     make(chan reportCommand, 64),
		ctx:      ctx,
		window:   reportBatchWindow,
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
		ready:    make(chan struct{}),
		advisory: make(map[string]struct{}),
	}
	go c.loop()
	// A heartbeat may not inspect an apparently empty actor while recovery is
	// still loading its outboxes. Starting only after this barrier gives a
	// recovered report the same precedence as a freshly accepted one.
	<-c.ready
	return c
}

// post hands a command to the actor, returning whether it was admitted.
//
// It deliberately does NOT give up on the root context. A SIGTERM cancels that
// context while sessions are still finishing their last turn, and a report
// dropped there is the one thing this whole path exists to prevent. The only
// thing that refuses a post is Close, through the admission gate: while a
// sender holds admission the actor is guaranteed to still be running, so the
// send cannot be left dangling.
func (c *reportCoordinator) post(cmd reportCommand) bool {
	if c == nil {
		return false
	}
	c.postMu.RLock()
	if c.closing {
		c.postMu.RUnlock()
		if cmd.report != nil {
			slog.Warn("owner reports: report arrived after the coordinator closed",
				"codebase", cmd.key, "session", cmd.report.SessionID)
		}
		return false
	}
	c.posters.Add(1)
	c.postMu.RUnlock()
	defer c.posters.Done()
	c.mail <- cmd
	return true
}

// add records one report for the codebase's owner and waits for the actor to
// have processed it (queued, and its persistence attempted).
//
// The wait is what makes a sink's completion meaningful: "this turn was
// reported" has to mean it reached the outbox, not that it reached a channel.
// Nudges stay asynchronous — they carry nothing that can be lost.
func (c *reportCoordinator) add(key string, rep owner.Report) {
	if c == nil {
		return
	}
	c.admission.RLock()
	defer c.admission.RUnlock()
	if c.testReportAdmitted != nil {
		c.testReportAdmitted()
	}
	ack := make(chan struct{})
	if !c.post(reportCommand{key: key, report: &rep, ack: ack}) {
		return
	}
	<-ack
}

// BeginShutdown puts the actor in accept-only mode and waits for it to be in
// that mode before returning.
//
// Every Manager.Shutdown calls it first, whether or not a signal cancelled the
// root context. From here on no timer fires a delivery and no report — not
// even an immediate failed or needs_input one — can start a run in an owner
// session that is about to be torn down. Reports are still accepted and still
// written to the outbox: that is the whole point of stopping deliveries
// instead of stopping the actor.
func (c *reportCoordinator) BeginShutdown() {
	if c == nil {
		return
	}
	ack := make(chan struct{})
	if !c.post(reportCommand{beginShutdown: true, ack: ack}) {
		return
	}
	<-ack
}

// nudge asks the actor to retry a codebase whose owner may now be free.
func (c *reportCoordinator) nudge(key string) { _ = c.post(reportCommand{key: key}) }

// advisoryNudge is for bus observer callbacks. The actor can be waiting for
// the owner's transcript after it started a report or heartbeat; a callback in
// that run must not block behind the actor and thereby delay that transcript.
// Timers and owner creation use nudge instead: their retry is reliable.
func (c *reportCoordinator) advisoryNudge(key string) {
	if c == nil {
		return
	}
	c.advisoryMu.Lock()
	if _, ok := c.advisory[key]; ok {
		c.advisoryMu.Unlock()
		return
	}
	c.advisory[key] = struct{}{}
	c.advisoryMu.Unlock()

	c.postMu.RLock()
	if c.closing {
		c.postMu.RUnlock()
		c.clearAdvisory(key)
		return
	}
	select {
	case c.mail <- reportCommand{key: key, advisory: true}:
	default:
		// An advisory is only an acceleration. The batch timer remains the
		// durable retry path, and a later RunEnded can post another one.
		c.clearAdvisory(key)
	}
	c.postMu.RUnlock()
}

func (c *reportCoordinator) clearAdvisory(key string) {
	c.advisoryMu.Lock()
	delete(c.advisory, key)
	c.advisoryMu.Unlock()
}

// heartbeat requests a delivery under exclusive report admission. A child
// report which acquired admission first has been persisted before this command
// reaches the actor; a heartbeat which acquired it first is already committed
// when a later add is allowed to proceed.
func (c *reportCoordinator) heartbeat(key string, wake func() error) heartbeatResult {
	if c == nil {
		return heartbeatStopped
	}
	c.admission.Lock()
	defer c.admission.Unlock()
	reply := make(chan heartbeatResult, 1)
	if !c.post(reportCommand{key: key, heartbeat: &heartbeatCommand{wake: wake, reply: reply}}) {
		return heartbeatStopped
	}
	return <-reply
}

func (c *reportCoordinator) loop() {
	batches := map[string]*reportBatch{}
	c.recover(batches)
	close(c.ready)
	// ctxDone is dropped once observed, so the loop stops re-selecting a
	// channel that is permanently ready.
	ctxDone := c.ctx.Done()
	// acceptOnly is the state after the root context is cancelled: the process
	// is stopping, so nothing is delivered into sessions that are being torn
	// down — but reports are still accepted and still written to the outbox,
	// which is how the next process delivers them.
	acceptOnly := false
	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			acceptOnly = c.enterAcceptOnly(batches, acceptOnly, "root context cancelled")
		case <-c.quit:
			c.drain(batches)
			return
		case cmd := <-c.mail:
			// select is free to choose a queued command over ctxDone. Check the
			// root context again before every command so cancellation cannot let
			// an immediate report or nudge start an owner run.
			if c.ctx.Err() != nil {
				acceptOnly = c.enterAcceptOnly(batches, acceptOnly, "root context cancelled")
			}
			if cmd.advisory {
				c.clearAdvisory(cmd.key)
			}
			if cmd.beginShutdown {
				acceptOnly = c.enterAcceptOnly(batches, acceptOnly, "shutdown")
				ackCommand(cmd)
				continue
			}
			batch := c.batch(cmd.key, batches)
			if cmd.heartbeat != nil {
				c.handleHeartbeat(cmd.key, batch, cmd.heartbeat, acceptOnly)
				ackCommand(cmd)
				continue
			}
			if cmd.report != nil {
				if acceptOnly {
					c.queue(cmd.key, batch, *cmd.report)
				} else {
					c.accept(cmd.key, batch, *cmd.report)
				}
				ackCommand(cmd)
				continue
			}
			if !acceptOnly {
				c.flush(cmd.key, batch)
			}
			ackCommand(cmd)
		}
	}
}

func (c *reportCoordinator) enterAcceptOnly(batches map[string]*reportBatch, acceptOnly bool, reason string) bool {
	if acceptOnly {
		return true
	}
	for _, batch := range batches {
		c.disarm(batch)
	}
	slog.Info("owner reports: accepting only, deliveries stopped",
		"codebase", "", "session", "", "reason", reason)
	return true
}

// handleHeartbeat makes reports an absolute barrier to a beat. In particular,
// a successful forced report flush still replies deferred: this request has
// observed a project state that was incomplete when it began, and it must not
// wake the owner again in the same turn.
func (c *reportCoordinator) handleHeartbeat(key string, batch *reportBatch, cmd *heartbeatCommand, acceptOnly bool) {
	if acceptOnly || c.ctx.Err() != nil {
		replyHeartbeat(cmd, heartbeatStopped)
		return
	}
	c.reloadHeartbeatBatch(key, batch)
	// A report accepted or recovered during the preceding batch belongs before
	// this beat even when its immediate delivery already cleared pending. The
	// first heartbeat consumes the marker but never falls through to wake in
	// that same request.
	deferForReport := batch.deferNextHeartbeat
	batch.deferNextHeartbeat = false
	if deferForReport || len(batch.pending) > 0 || batch.outbox.Incomplete || len(batch.outbox.Unreadable) > 0 {
		if len(batch.pending) > 0 {
			c.flush(key, batch)
		}
		replyHeartbeat(cmd, heartbeatDeferred)
		return
	}
	if cmd.wake == nil {
		replyHeartbeat(cmd, heartbeatFailed)
		return
	}
	if err := cmd.wake(); err != nil {
		slog.Debug("owner heartbeat: not delivered", "codebase", key, "error", err)
		replyHeartbeat(cmd, heartbeatFailed)
		return
	}
	replyHeartbeat(cmd, heartbeatDelivered)
}

func replyHeartbeat(cmd *heartbeatCommand, result heartbeatResult) {
	if cmd != nil && cmd.reply != nil {
		cmd.reply <- result
	}
}

// ackCommand releases a caller waiting for the actor to have processed its
// command. Safe for commands that carry no ack.
func ackCommand(cmd reportCommand) {
	if cmd.ack != nil {
		close(cmd.ack)
	}
}

// Close stops the coordinator: timers are stopped so nothing retries after the
// manager is gone, the mailbox is drained so a report accepted moments before
// shutdown is not lost, and everything still pending is written to the outbox
// for the next process to deliver.
//
// It is idempotent. It is also the ONLY thing that stops the actor: the root
// context being cancelled merely stops deliveries (see loop), so a report
// produced while the process is shutting down still reaches the outbox.
// Shutdown calls it after the owner observers have been flushed.
func (c *reportCoordinator) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		// Shut the admission gate and wait for the senders already inside it.
		// After this returns nothing can put anything in the mailbox, so the
		// actor's final drain is guaranteed to see every accepted report.
		c.postMu.Lock()
		c.closing = true
		c.postMu.Unlock()
		c.posters.Wait()
		close(c.quit)
	})
	<-c.done
}

// drain is the loop's exit path: it persists what is pending rather than
// attempting a last delivery, because the sessions it would deliver into are
// being flushed and closed at this very moment.
func (c *reportCoordinator) drain(batches map[string]*reportBatch) {
	defer close(c.done)
	for {
		select {
		case cmd := <-c.mail:
			if cmd.beginShutdown {
				ackCommand(cmd)
				continue
			}
			if cmd.heartbeat != nil {
				replyHeartbeat(cmd.heartbeat, heartbeatStopped)
				ackCommand(cmd)
				continue
			}
			if cmd.report == nil {
				ackCommand(cmd)
				continue // a nudge has nowhere to deliver to any more
			}
			batch := c.batch(cmd.key, batches)
			c.queue(cmd.key, batch, *cmd.report)
			ackCommand(cmd)
		default:
			for key, batch := range batches {
				c.disarm(batch)
				if len(batch.pending) == 0 {
					continue
				}
				if outbox, err := c.store.SaveReportsOutbox(key, batch.outbox, batch.pending); err != nil {
					slog.Warn("owner reports: pending batch lost at shutdown",
						"codebase", key, "error", err)
				} else {
					batch.outbox = outbox
				}
			}
			return
		}
	}
}

// recover reloads the outbox of every owner at startup. A report that was
// accepted but never confirmed inside the owner's transcript is delivered
// again: re-reading a report costs context, losing one costs the owner its
// picture of the project.
func (c *reportCoordinator) recover(batches map[string]*reportBatch) {
	owners, err := c.store.List()
	if err != nil {
		slog.Warn("owner reports: cannot list owners at startup", "error", err)
		return
	}
	for _, own := range owners {
		outbox, err := c.store.LoadReportsOutbox(own.CodebaseKey)
		if err != nil {
			c.warnIncomplete(own.CodebaseKey, outbox, err)
		}
		warned := c.warnPreserved(own.CodebaseKey, outbox, nil)
		if len(outbox.Lanes) > 1 && !outbox.Incomplete {
			consolidated, saveErr := c.store.SaveReportsOutbox(own.CodebaseKey, outbox, outbox.Reports)
			if saveErr != nil {
				slog.Warn("owner reports: could not consolidate recovery outboxes", "codebase", own.CodebaseKey, "error", saveErr)
			} else {
				outbox = consolidated
			}
		}
		if len(outbox.Reports) == 0 {
			if len(outbox.Unreadable) > 0 || outbox.Incomplete {
				// Keep the warning and selected recovery lane with this actor so a
				// later first report does not emit an ambiguous second warning.
				batches[own.CodebaseKey] = &reportBatch{outbox: outbox, preservedPaths: warned}
			}
			continue
		}
		batch := &reportBatch{pending: outbox.Reports, outbox: outbox, preservedPaths: warned, deferNextHeartbeat: true}
		batches[own.CodebaseKey] = batch
		slog.Info("owner reports recovered", "codebase", own.CodebaseKey, "session", "", "owner", own.ID, "status", "recovered", "run_gen", 0, "batch", "", "n", len(outbox.Reports))
		// Arm the normal window rather than delivering now: a restart usually
		// resumes several sessions at once, and one batched message is what the
		// owner wants either way.
		c.arm(own.CodebaseKey, batch)
	}
}

// accept persists a new report and decides when it leaves. The outbox is
// written BEFORE the report is queued in memory, so a crash between the two
// leaves a report that will be redelivered rather than one that was promised
// and then forgotten.
func (c *reportCoordinator) accept(key string, batch *reportBatch, rep owner.Report) {
	if !c.queue(key, batch, rep) {
		return
	}
	// The report owns the next heartbeat even if this status flushes it now.
	// Set this before delivery, which can block awaiting the owner transcript.
	batch.deferNextHeartbeat = true
	// A session that failed or is stuck waiting for an answer is not worth
	// batching: it is exactly what the owner has to act on now.
	if rep.Status == callbackStatusDone {
		c.arm(key, batch)
		return
	}
	c.flush(key, batch)
}

// queue adds a report to the batch and tries to persist the outbox, returning
// whether the report is new. A failed write does NOT drop the report: it stays
// in memory (and is retried on the next accept, flush or shutdown), because the
// disk being full is exactly when losing the owner's picture of the project is
// least acceptable. The warning is logged once per batch so a broken disk does
// not flood the log with one line per outcome.
func (c *reportCoordinator) queue(key string, batch *reportBatch, rep owner.Report) bool {
	if len(batch.pending) == 0 {
		c.reloadEmptyBatch(key, batch)
	}
	if rep.ID == "" {
		rep.ID = newReportID()
	}
	for _, existing := range batch.pending {
		if existing.ID == rep.ID {
			return false // the same outcome, re-delivered: already queued
		}
	}
	batch.pending = append(batch.pending, rep)
	previous := batch.outbox
	outbox, err := c.store.SaveReportsOutbox(key, batch.outbox, batch.pending)
	if err != nil {
		if !batch.outboxFailed {
			batch.outboxFailed = true
			slog.Warn("owner reports: could not persist the outbox; keeping the reports in memory",
				"codebase", key, "session", rep.SessionID, "error", err)
		}
		return true
	}
	batch.outbox = outbox
	batch.preservedPaths = c.warnPreserved(key, outbox, batch.preservedPaths)
	if outbox.ActiveRecoveryOwned && (!previous.ActiveRecoveryOwned || previous.ActivePath != outbox.ActivePath) {
		slog.Warn("owner reports: active recovery outbox created", "codebase", key, "active_path", outbox.ActivePath)
	}
	batch.outboxFailed = false
	slog.Info("owner report accepted in outbox", "codebase", key, "session", rep.SessionID, "owner", "", "status", rep.Status, "run_gen", 0, "batch", "", "n", len(batch.pending))
	return true
}

func (c *reportCoordinator) arm(key string, batch *reportBatch) {
	if batch.timer != nil {
		return
	}
	batch.timer = time.AfterFunc(c.window, func() { c.nudge(key) })
}

func (c *reportCoordinator) disarm(batch *reportBatch) {
	if batch.timer != nil {
		batch.timer.Stop()
		batch.timer = nil
	}
}

// flush attempts one delivery of everything pending for a codebase. A busy
// owner keeps its batch: the owner is never steered, so the reports wait for it
// to be idle (its own run outcomes nudge this coordinator) and the timer is
// re-armed as a backstop in case no further run ever happens.
func (c *reportCoordinator) flush(key string, batch *reportBatch) {
	c.disarm(batch)
	if len(batch.pending) == 0 {
		return
	}
	reportDeliveryAttempts.Add(1)
	slog.Info("owner reports delivery attempt", "codebase", key, "session", "", "owner", "", "status", "attempt", "run_gen", 0, "batch", "", "n", len(batch.pending))
	err := c.deliver(key, batch.pending)
	if err != nil {
		status := "error"
		if errors.Is(err, ErrBusy) {
			status = "waiting"
		}
		slog.Info("owner reports delivery result", "codebase", key, "session", "", "owner", "", "status", status, "run_gen", 0, "batch", "", "n", len(batch.pending), "error", err)
		slog.Debug("owner reports: batch waiting", "codebase", key, "error", err)
		c.arm(key, batch)
		return
	}
	slog.Info("owner reports delivery result", "codebase", key, "session", "", "owner", "", "status", "delivered", "run_gen", 0, "batch", "", "n", len(batch.pending))
	batch.pending = nil
	outbox, err := c.store.SaveReportsOutbox(key, batch.outbox, nil)
	if err != nil {
		// The reports reached the owner and the transcript was flushed; a
		// surviving outbox only means they are read twice after a restart.
		slog.Warn("owner reports: delivered batch still on disk", "codebase", key, "error", err)
	} else {
		batch.outbox = outbox
	}
}

// batch loads the lanes before the first report for a codebase. That matters
// for an owner created after startup and for a manually repaired canonical
// outbox: a fresh actor must merge it rather than blindly replacing it.
func (c *reportCoordinator) batch(key string, batches map[string]*reportBatch) *reportBatch {
	if batch := batches[key]; batch != nil {
		return batch
	}
	outbox, err := c.store.LoadReportsOutbox(key)
	if err != nil {
		c.warnIncomplete(key, outbox, err)
	}
	batch := &reportBatch{outbox: outbox, pending: outbox.Reports, deferNextHeartbeat: len(outbox.Reports) > 0}
	batch.preservedPaths = c.warnPreserved(key, outbox, nil)
	batches[key] = batch
	return batch
}

// reloadEmptyBatch makes a cached empty batch observe manual repairs or lanes
// created after startup before its first new report can replace the outbox.
func (c *reportCoordinator) reloadEmptyBatch(key string, batch *reportBatch) {
	fresh, err := c.store.LoadReportsOutbox(key)
	if err != nil {
		c.warnIncomplete(key, fresh, err)
	}
	if err == nil && !fresh.Incomplete {
		batch.outbox = fresh
		batch.pending = appendUniqueOwnerReports(nil, fresh.Reports)
		batch.preservedPaths = c.warnPreserved(key, fresh, batch.preservedPaths)
		return
	}
	// A failed enumeration is not permission to forget reports or an
	// actor-created recovery lane that we already know is durable.
	batch.outbox = mergeKnownOutboxes(batch.outbox, fresh)
	batch.pending = appendUniqueOwnerReports(batch.pending, batch.outbox.Reports)
	batch.preservedPaths = c.warnPreserved(key, batch.outbox, batch.preservedPaths)
}

// reloadHeartbeatBatch observes lanes which may have changed since recovery
// or a prior empty nudge. It never discards in-memory reports after a failed
// outbox write: a heartbeat is not permission to forget an accepted report.
func (c *reportCoordinator) reloadHeartbeatBatch(key string, batch *reportBatch) {
	fresh, err := c.store.LoadReportsOutbox(key)
	if err != nil {
		c.warnIncomplete(key, fresh, err)
		batch.outbox = mergeKnownOutboxes(batch.outbox, fresh)
		batch.pending = appendUniqueOwnerReports(batch.pending, fresh.Reports)
		batch.preservedPaths = c.warnPreserved(key, batch.outbox, batch.preservedPaths)
		return
	}
	// A complete enumeration is authoritative for lane health. Only pending
	// reports are unioned: they may be accepted in memory after a failed write,
	// but stale incomplete/unreadable metadata must disappear after repair.
	batch.outbox = fresh
	batch.pending = appendUniqueOwnerReports(batch.pending, fresh.Reports)
	batch.preservedPaths = c.warnPreserved(key, fresh, batch.preservedPaths)
}

func mergeKnownOutboxes(known, partial owner.ReportsOutbox) owner.ReportsOutbox {
	out := partial
	out.Reports = appendUniqueOwnerReports(appendUniqueOwnerReports(nil, known.Reports), partial.Reports)
	out.Lanes = appendUniquePaths(known.Lanes, partial.Lanes)
	out.Unreadable = appendUniqueUnreadable(known.Unreadable, partial.Unreadable)
	if known.ActiveRecoveryOwned {
		out.ActivePath = known.ActivePath
		out.ActiveRecoveryOwned = true
	}
	out.Incomplete = known.Incomplete || partial.Incomplete
	if partial.IncompleteErr != nil {
		out.IncompleteErr = partial.IncompleteErr
	} else {
		out.IncompleteErr = known.IncompleteErr
	}
	if out.CanonicalPath == "" {
		out.CanonicalPath = known.CanonicalPath
	}
	out.CanonicalUnreadable = known.CanonicalUnreadable || partial.CanonicalUnreadable
	return out
}

func appendUniqueOwnerReports(dst, src []owner.Report) []owner.Report {
	seen := make(map[string]struct{}, len(dst)+len(src))
	for _, report := range dst {
		seen[report.ID] = struct{}{}
	}
	for _, report := range src {
		if _, ok := seen[report.ID]; !ok {
			seen[report.ID] = struct{}{}
			dst = append(dst, report)
		}
	}
	return dst
}

func appendUniquePaths(dst, src []string) []string {
	seen := make(map[string]struct{}, len(dst)+len(src))
	for _, path := range dst {
		seen[path] = struct{}{}
	}
	for _, path := range src {
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			dst = append(dst, path)
		}
	}
	return dst
}

func appendUniqueUnreadable(dst, src []owner.ReportsUnreadableLane) []owner.ReportsUnreadableLane {
	seen := make(map[string]struct{}, len(dst)+len(src))
	for _, lane := range dst {
		seen[lane.Path] = struct{}{}
	}
	for _, lane := range src {
		if _, ok := seen[lane.Path]; !ok {
			seen[lane.Path] = struct{}{}
			dst = append(dst, lane)
		}
	}
	return dst
}

func (c *reportCoordinator) warnPreserved(key string, outbox owner.ReportsOutbox, warned map[string]struct{}) map[string]struct{} {
	if warned == nil {
		warned = make(map[string]struct{})
	}
	for _, lane := range outbox.Unreadable {
		if _, ok := warned[lane.Path]; ok {
			continue
		}
		slog.Warn("owner reports: outbox lane preserved; using recovery outbox",
			"codebase", key, "preserved_path", lane.Path, "active_path", outbox.ActivePath, "error", lane.Err)
		warned[lane.Path] = struct{}{}
	}
	return warned
}

func (c *reportCoordinator) warnIncomplete(key string, outbox owner.ReportsOutbox, err error) {
	slog.Warn("owner reports: incomplete outbox", "codebase", key, "active_path", outbox.ActivePath, "error", err)
}

// deliver resolves the owner and puts the batch into its conversation.
func (c *reportCoordinator) deliver(key string, pending []owner.Report) error {
	own, found, err := c.store.FindByCodebase(key)
	if err != nil {
		return err
	}
	if !found || own.SessionID == "" {
		return fmt.Errorf("codebase %s has no owner conversation", key)
	}
	return c.mgr.deliverReportsIfIdle(own, pending)
}

// deliverReportsIfIdle injects a batch as one message in the owner's
// conversation, and only when the owner is free.
//
// It is deliberately NOT Manager.Send: that one turns into a steer whenever the
// session is busy or has a queue, and steering an owner would splice a batch of
// reports into the middle of whatever it was reasoning about. An owner that is
// working keeps its batch until it is quiescent.
//
// bus.SendPrompt{IdleOnly} is what makes that exact: the bus decides idleness
// under the same lock in which it would otherwise convert the prompt into a
// steer, so there is no window for a concurrent send to turn this batch into
// one. bus.ErrNotIdle means the owner is working; the batch is retained and
// tried again when the owner's own run ends.
func (m *Manager) deliverReportsIfIdle(own owner.Owner, pending []owner.Report) error {
	sess, ok := m.Get(own.SessionID)
	if !ok {
		// An owner asleep on disk is resumed like any other conversation: the
		// automation resident cap guards machine-created runs, and an owner is
		// neither transient nor throwaway.
		resumed, err := m.ResumeSession(own.SessionID)
		if err != nil {
			return fmt.Errorf("resume owner session: %w", err)
		}
		sess = resumed
	}

	text := reportsMessage(own, pending)
	batchID := reportBatchID(pending)
	sessions := make([]map[string]string, 0, len(pending))
	for _, rep := range pending {
		sessions = append(sessions, map[string]string{"id": rep.SessionID, "title": rep.Title, "status": rep.Status, "origin": rep.Origin})
	}
	custom := map[string]any{"source": reportSource, "batch": batchID, "count": len(pending), "sessions": sessions}

	if err := func() error {
		sess.lifecycle.RLock()
		defer sess.lifecycle.RUnlock()
		if sess.closing.Load() {
			return ErrNotFound
		}
		if err := sess.runtime.Bus.Execute(bus.SendPrompt{
			SessionID: sess.ID,
			Text:      text,
			Custom:    custom,
			IdleOnly:  true,
		}); err != nil {
			if errors.Is(err, bus.ErrNotIdle) {
				return fmt.Errorf("%w: %v", ErrBusy, err)
			}
			return err
		}
		sess.sendGeneration.Add(1)
		return nil
	}(); err != nil {
		return err
	}

	// Only a report that is in the transcript AND on disk may leave the outbox:
	// anything else could be lost by a crash between the two.
	if !m.awaitCustomInTranscript(sess, "batch", batchID) {
		return errors.New("reports were sent but did not reach the transcript")
	}
	if err := sess.runtime.Flush(); err != nil {
		return fmt.Errorf("flush owner transcript: %w", err)
	}
	return nil
}

// awaitCustomInTranscript waits for a machine-sent message to land in history,
// identified by one of its custom fields (a report batch, a heartbeat beat).
// The prompt is appended by the run goroutine, so it is not there when Execute
// returns. It is announced live for the transcript, but the outbox and the
// heartbeat's memory need the message on disk, hence the bounded poll.
func (m *Manager) awaitCustomInTranscript(sess *ManagedSession, field, value string) bool {
	deadline := time.Now().Add(reportConfirmTimeout)
	for {
		msgs := sess.History()
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Custom == nil {
				continue
			}
			if msgs[i].Custom[field] == value {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-sess.infra.sessionCtx.Done():
			return false
		}
	}
}

// subscribeOwnerReports wires a session into the reports loop, according to
// what it is inside the project:
//
//   - a child (an ordinary session whose codebase has an owner) feeds its
//     completed semantic turns to the coordinator, through the owner observer
//     (owner_observer.go) rather than the automation callback's policy;
//   - the owner itself feeds nothing, but nudges the coordinator on every
//     outcome of its own: that is the moment a retained batch can finally be
//     delivered without steering it.
//
// The owner is looked up once, when the session is built, like the project book
// in its prompt: a session that predates its project's owner starts reporting
// when it is next resumed.
func (m *Manager) subscribeOwnerReports(sess *ManagedSession, ownerSession bool) {
	// A subscription can outlive a test replacing the coordinator to model a
	// restart. Keep it bound to the actor it was created for instead of reading
	// m.reports from an asynchronous bus callback.
	reports := m.reports
	if reports == nil {
		return
	}
	own, found, err := reports.store.FindByDir(sess.CWD)
	if err != nil {
		slog.Warn("owner reports: cannot resolve the owner of a session", "session", sess.ID, "error", err)
		return
	}
	if !found {
		slog.Debug("owner reports: session has no resolved owner", "cwd", sess.CWD)
		return
	}
	key := own.CodebaseKey
	if ownerSession {
		// The owner's own conversation needs no turn semantics: every time it
		// stops working is a chance to hand it a waiting batch. Reacting to
		// the terminal run event directly keeps it off the child policy (and
		// off its 15s wait) entirely — a nudge is cheap and idempotent.
		sess.pushUnsubs = append(sess.pushUnsubs, sess.runtime.Bus.Subscribe(func(e bus.RunEnded) {
			slog.Info("owner reports nudge received", "codebase", key, "session", sess.ID,
				"owner", own.ID, "status", "run_ended", "run_gen", e.RunGen, "batch", "", "n", 0)
			reports.advisoryNudge(key)
		}))
		// Background work settling is the other moment the owner becomes
		// deliverable: IdleOnly refuses a batch while the owner has any.
		sess.pushUnsubs = append(sess.pushUnsubs, sess.runtime.Bus.Subscribe(func(e bus.BashJobSettled) {
			reports.advisoryNudge(key)
		}))
		sess.pushUnsubs = append(sess.pushUnsubs, sess.runtime.Bus.Subscribe(func(e bus.SubagentEnded) {
			reports.advisoryNudge(key)
		}))
		return
	}
	observer := newOwnerReportObserver(sess, func(out runOutcome) {
		reports.add(key, reportFrom(sess, out))
	})
	sess.mu.Lock()
	sess.ownerObserver = observer
	sess.mu.Unlock()
}

// reportFrom turns a run outcome into the report the owner will read. The
// pending interaction is carried literally: an owner deciding whether to answer
// a question needs the question, not a label saying one exists.
func reportFrom(sess *ManagedSession, out runOutcome) owner.Report {
	rep := owner.Report{
		ID:        newReportID(),
		SessionID: sess.ID,
		Title:     sess.title(),
		CWD:       sess.CWD,
		Origin:    sess.Origin,
		Status:    out.Status,
		// The delta comes out of the WHOLE message; an abridged copy is carried
		// for reading. A session that closes with a long summary would otherwise
		// push its own book delta out of the report that exists to apply it.
		BookDelta: extractBookDelta(out.FinalText),
		FinalText: reportAbridge(sess.ID, out.FinalText),
		// What the session left running when its turn ended. A report is about
		// a completed turn, not a quiet session; this is what keeps the two
		// from reading the same.
		BackgroundCount: out.BackgroundCount,
		At:              time.Now().UTC().Format(time.RFC3339),
	}
	pos := gitPosition(sess.CWD)
	rep.GitAvailable, rep.Branch, rep.Head, rep.Dirty = pos.Available, pos.Branch, pos.Head, pos.Dirty
	if out.Status == callbackStatusFailed && out.Err != "" {
		rep.FinalText = strings.TrimSpace(out.Err + "\n\n" + rep.FinalText)
	}
	if out.Pending != nil {
		rep.Pending = &owner.ReportPending{Kind: out.Pending.Kind, ID: out.Pending.ID}
		switch out.Pending.Kind {
		case pendingKindQuestion:
			texts := make([]string, 0, len(out.Pending.Questions))
			for _, q := range out.Pending.Questions {
				line := q.Text
				if len(q.Options) > 0 {
					line += " (" + strings.Join(q.Options, " / ") + ")"
				}
				texts = append(texts, line)
			}
			rep.Pending.Text = strings.Join(texts, " | ")
		case pendingKindPermission:
			rep.Pending.Text = strings.TrimSpace(out.Pending.Tool + " " + out.Pending.Summary)
		}
	}
	return rep
}

// extractBookDelta lifts the "## Book delta" section out of a final message.
//
// The section runs to the next heading of the SAME level or higher (H1/H2) or
// to the end of the text: a delta whose bullets carry an "### detail" heading
// is still one delta, and stopping at any "#" cut it in half. Headings inside
// a fenced block are text, not structure — a session pasting a diff of a
// markdown file would otherwise truncate its own delta.
//
// An empty section is treated as absent, because a session that printed the
// heading and nothing under it did not answer.
func extractBookDelta(text string) string {
	lines := strings.Split(text, "\n")
	start := -1
	fenced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isFence(trimmed) {
			fenced = !fenced
			continue
		}
		if fenced || !strings.HasPrefix(trimmed, "#") {
			continue
		}
		heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "# ")))
		heading = strings.TrimSuffix(heading, ":")
		if heading == "book delta" {
			start = i + 1
			// Keep looking: a session that writes the section twice (a draft and
			// a final one) means the last.
		}
	}
	if start < 0 {
		return ""
	}
	var body []string
	fenced = false
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if isFence(trimmed) {
			fenced = !fenced
		}
		if !fenced && headingLevel(trimmed) > 0 && headingLevel(trimmed) <= 2 {
			break
		}
		body = append(body, line)
	}
	delta := strings.TrimSpace(strings.Join(body, "\n"))
	if delta == "" {
		return ""
	}
	// "- none", "(none)", "none." all mean the same thing, and the owner should
	// not have to parse three spellings of it.
	flat := strings.ToLower(strings.Trim(delta, "-*() ."))
	if flat == owner.BookDeltaNone {
		return owner.BookDeltaNone
	}
	if len(delta) > maxBookDeltaBytes {
		// Cut on a rune boundary and say so: a delta that ends mid-path reads
		// like a path, and the owner would apply it to a file that does not
		// exist.
		cut := cutAtRuneBoundary(delta, maxBookDeltaBytes)
		delta = cut + fmt.Sprintf("\n[... %d more characters truncated — read the session's final message for the rest ...]",
			len([]rune(delta))-len([]rune(cut)))
	}
	return delta
}

// isFence reports a markdown code fence (``` or ~~~).
func isFence(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// headingLevel is the number of leading '#' of an ATX heading, 0 when the line
// is not one.
func headingLevel(trimmed string) int {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level >= len(trimmed) {
		return level
	}
	if trimmed[level] != ' ' && trimmed[level] != '\t' {
		return 0 // "#hashtag" is not a heading
	}
	return level
}

// cutAtRuneBoundary caps s at limit bytes without splitting a rune.
func cutAtRuneBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

// gitPosition answers where the work happened: branch, short head, and whether
// the tree was dirty. Three cheap plumbing calls with a short deadline.
//
// The result is all-or-none. A partial answer — a branch, an empty head and
// "clean" because `status` timed out or the worktree disappeared between two
// calls — reads exactly like verified work with nothing uncommitted, and that
// is the one thing it must never be mistaken for. A directory that is not a
// repository, a git that does not answer, or any call that fails: unavailable,
// and the report says so.
func gitPosition(cwd string) gitInfo {
	if cwd == "" {
		return gitInfo{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitPositionTimeout)
	defer cancel()
	run := func(args ...string) (string, bool) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = cwd
		// The deadline only kills git itself; a grandchild holding the output
		// pipe would keep Output blocked past it. WaitDelay is what makes the
		// timeout a real bound on how long a report can be delayed.
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}
	branch, ok := run("rev-parse", "--abbrev-ref", "HEAD")
	if !ok {
		return gitInfo{}
	}
	head, ok := run("rev-parse", "--short", "HEAD")
	if !ok {
		return gitInfo{}
	}
	status, ok := run("status", "--porcelain")
	if !ok {
		return gitInfo{}
	}
	return gitInfo{Available: true, Branch: branch, Head: head, Dirty: status != ""}
}

// gitInfo is where a report's work happened, or nothing at all.
type gitInfo struct {
	Available bool
	Branch    string
	Head      string
	Dirty     bool
}

// newReportID mints the identity of one outcome, at the moment the outcome is
// observed. It is random rather than derived from the session and the run
// generation: RunGen restarts at 0 with every runtime, so a session that is
// closed and resumed would produce the same derived ID for a genuinely new
// outcome and the second report would be silently dropped as a duplicate.
//
// Dedup by this ID therefore protects against the same report object being
// handed to the coordinator twice (a retry, a recovered outbox), which is the
// only duplication that exists once the ID travels with the report.
func newReportID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("rep_%d", time.Now().UnixNano())
	}
	return "rep_" + hex.EncodeToString(b)
}

// reportBatchID identifies the delivered message, so the transcript can be
// checked for the exact batch that was just sent.
func reportBatchID(pending []owner.Report) string {
	sum := sha256.New()
	for _, rep := range pending {
		sum.Write([]byte(rep.ID))
		sum.Write([]byte{0})
	}
	return "rep_" + hex.EncodeToString(sum.Sum(nil))[:16]
}

// reportAbridge keeps the beginning and the end of the child's final message
// and says what is missing in between and how to read it.
func reportAbridge(sessionID, text string) string {
	return abridge(strings.TrimSpace(text), reportHeadChars, reportTailChars,
		fmt.Sprintf("read the whole message with the sessions tool: action=read, session_id=%s, message_id=%s", sessionID, lastMessageID))
}

// reportsMessage renders a batch. The format is fixed and plain: the owner is
// reading a status board, and a stable shape is what lets it compare one cycle
// with the next.
//
// Every position is stated against the project's canonical ref, because that is
// what decides where the delta goes: the canonical branch updates areas/, any
// other branch updates work/. "git: unavailable" is said out loud rather than
// rendered as an absence — an owner cannot tell a clean tree from an unasked
// question unless the report distinguishes them.
func reportsMessage(own owner.Owner, pending []owner.Report) string {
	var b strings.Builder
	if len(pending) == 1 {
		b.WriteString("Report from a session of your project:\n\n")
	} else {
		fmt.Fprintf(&b, "Reports from %d sessions of your project:\n\n", len(pending))
	}
	for _, rep := range pending {
		fmt.Fprintf(&b, "- %s — %s\n", rep.SessionID, strings.TrimSpace(rep.Title))
		fmt.Fprintf(&b, "  origin: %s\n", reportOrigin(rep.Origin))
		fmt.Fprintf(&b, "  status: %s\n", rep.Status)
		if line := backgroundWorkLine(rep.BackgroundCount); line != "" {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		if rep.CWD != "" {
			fmt.Fprintf(&b, "  directory: %s\n", rep.CWD)
		}
		if rep.Pending != nil {
			switch rep.Pending.Kind {
			case pendingKindQuestion:
				fmt.Fprintf(&b, "  asking (ask_id %s): %s\n", rep.Pending.ID, rep.Pending.Text)
			case pendingKindPermission:
				fmt.Fprintf(&b, "  waiting for the user to approve: %s\n", rep.Pending.Text)
			}
		}
		if rep.FinalText != "" {
			fmt.Fprintf(&b, "  said: %s\n", indentReportText(rep.FinalText))
		} else if rep.Status == callbackStatusDone {
			// A run that stops on a tool call or an empty reply ends "done"
			// with nothing to quote. Saying nothing would read as finished work.
			fmt.Fprintf(&b, "  said: nothing — the turn ended without a final message, so it may have "+
				"stopped mid-work. See where with the sessions tool: action=read, session_id=%s, tools=true\n", rep.SessionID)
		}
		switch {
		case !rep.GitAvailable:
			b.WriteString("  git: unavailable (position unknown — ask this session where its work is)\n")
		default:
			fmt.Fprintf(&b, "  canonical: %s · branch: %s", canonicalRefLabel(own), rep.Branch)
			if rep.Head != "" {
				fmt.Fprintf(&b, " @ %s", rep.Head)
			}
			if rep.Dirty {
				b.WriteString(" (uncommitted changes)")
			}
			b.WriteString("\n")
		}
		switch rep.BookDelta {
		case "":
			b.WriteString("  book delta: missing — ask this session what its work changes in the book\n")
		case owner.BookDeltaNone:
			b.WriteString("  book delta: none\n")
		default:
			fmt.Fprintf(&b, "  book delta: %s\n", indentReportText(rep.BookDelta))
		}
	}
	b.WriteString("\nUse the sessions tool to read or answer any of them, and update the book " +
		"with what this changes about the project.")
	return b.String()
}

func reportOrigin(origin string) string {
	if origin == "owner" {
		return "owner"
	}
	return "user"
}

// backgroundWorkLine says what the session left running. It is stated in the
// report rather than inferred from silence: a child that finished its turn and
// a child that finished its turn while a dev server keeps running are two
// different things for an owner deciding what to do next.
func backgroundWorkLine(count int) string {
	switch {
	case count <= 0:
		return ""
	case count == 1:
		return "1 background job still running"
	default:
		return fmt.Sprintf("%d background jobs still running", count)
	}
}

// canonicalRefLabel names the branch areas/ describes, or says it is unknown.
func canonicalRefLabel(own owner.Owner) string {
	if ref := strings.TrimSpace(own.CanonicalRef); ref != "" {
		return ref
	}
	return "unknown"
}

// indentReportText keeps a multi-line tail inside its bullet.
func indentReportText(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n    ")
}
