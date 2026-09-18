package serve

// Project owners — reports. A child session's run outcome (the same vocabulary
// the automation callback uses, see outcomes.go) becomes a report; reports are
// batched per owner and delivered into the owner's conversation as one message.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// maxReportFinalTextBytes caps the tail of a child's final message carried in a
// report. The owner reads the full session through the `sessions` tool when the
// tail is not enough.
const maxReportFinalTextBytes = 2 << 10

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
}

// reportCommand is the actor's mailbox message: a new report, or a nudge to try
// delivering what is pending (timer expiry, or the owner going idle).
type reportCommand struct {
	key    string
	report *owner.Report
}

// reportBatch is the per-codebase state: what is pending and the timer that
// will flush it.
type reportBatch struct {
	pending []owner.Report
	timer   *time.Timer
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
		mgr:    m,
		store:  store,
		mail:   make(chan reportCommand, 64),
		ctx:    ctx,
		window: reportBatchWindow,
	}
	go c.loop()
	return c
}

// post hands a command to the actor without ever blocking the caller past
// shutdown: senders are bus subscribers and timers, which must not be held.
func (c *reportCoordinator) post(cmd reportCommand) {
	if c == nil {
		return
	}
	select {
	case c.mail <- cmd:
	case <-c.ctx.Done():
	}
}

// add records one report for the codebase's owner.
func (c *reportCoordinator) add(key string, rep owner.Report) {
	c.post(reportCommand{key: key, report: &rep})
}

// nudge asks the actor to retry a codebase whose owner may now be free.
func (c *reportCoordinator) nudge(key string) { c.post(reportCommand{key: key}) }

func (c *reportCoordinator) loop() {
	batches := map[string]*reportBatch{}
	c.recover(batches)
	for {
		select {
		case <-c.ctx.Done():
			return
		case cmd := <-c.mail:
			batch := batches[cmd.key]
			if batch == nil {
				batch = &reportBatch{}
				batches[cmd.key] = batch
			}
			if cmd.report != nil {
				c.accept(cmd.key, batch, *cmd.report)
				continue
			}
			c.flush(cmd.key, batch)
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
		pending, err := c.store.LoadReports(own.CodebaseKey)
		if err != nil {
			slog.Warn("owner reports: unreadable outbox", "codebase", own.CodebaseKey, "error", err)
			continue
		}
		if len(pending) == 0 {
			continue
		}
		batch := &reportBatch{pending: pending}
		batches[own.CodebaseKey] = batch
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
	for _, existing := range batch.pending {
		if existing.ID == rep.ID {
			return // same run, same outcome: already queued
		}
	}
	next := append(append([]owner.Report(nil), batch.pending...), rep)
	if err := c.store.SaveReports(key, next); err != nil {
		slog.Warn("owner reports: could not persist the outbox; dropping the report",
			"codebase", key, "session", rep.SessionID, "error", err)
		return
	}
	batch.pending = next
	// A session that failed or is stuck waiting for an answer is not worth
	// batching: it is exactly what the owner has to act on now.
	if rep.Status == callbackStatusDone {
		c.arm(key, batch)
		return
	}
	c.flush(key, batch)
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
	err := c.deliver(key, batch.pending)
	if err != nil {
		slog.Debug("owner reports: batch waiting", "codebase", key, "error", err)
		c.arm(key, batch)
		return
	}
	batch.pending = nil
	if err := c.store.SaveReports(key, nil); err != nil {
		// The reports reached the owner and the transcript was flushed; a
		// surviving outbox only means they are read twice after a restart.
		slog.Warn("owner reports: delivered batch still on disk", "codebase", key, "error", err)
	}
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
// The bus offers no atomic "start a run only if idle" primitive: SendPrompt
// itself converts into a steer when the queue rail is not empty, and
// DoIfQuiescent cannot execute a command (its callback must not re-enter the
// state machine). So the check is: quiescent (state idle + no background work,
// taken under the state lock) and an empty queue, then Execute. If a concurrent
// send wins the remaining window the prompt is queued as a steer; that is
// reported as an error and the batch is retained, which is the conservative
// side of the trade — the owner may read those reports twice, but never loses
// them and is never steered on purpose.
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

	text := reportsMessage(pending)
	batchID := reportBatchID(pending)
	custom := map[string]any{"source": reportSource, "batch": batchID, "count": len(pending)}

	if err := func() error {
		sess.lifecycle.RLock()
		defer sess.lifecycle.RUnlock()
		if sess.closing.Load() {
			return ErrNotFound
		}
		ql, _ := bus.QueryTyped[bus.GetQueueLen, int](sess.runtime.Bus, bus.GetQueueLen{})
		if ql > 0 {
			return ErrBusy
		}
		if !sess.runtime.DoIfQuiescent(func() {}) {
			return ErrBusy
		}
		steerID := ""
		if err := sess.runtime.Bus.Execute(bus.SendPrompt{
			SessionID:       sess.ID,
			Text:            text,
			Custom:          custom,
			AcceptedSteerID: &steerID,
		}); err != nil {
			return err
		}
		if steerID != "" {
			return fmt.Errorf("%w: the batch was queued behind a concurrent send", ErrBusy)
		}
		sess.sendGeneration.Add(1)
		return nil
	}(); err != nil {
		return err
	}

	// Only a report that is in the transcript AND on disk may leave the outbox:
	// anything else could be lost by a crash between the two.
	if !m.awaitReportInTranscript(sess, batchID) {
		return errors.New("reports were sent but did not reach the transcript")
	}
	if err := sess.runtime.Flush(); err != nil {
		return fmt.Errorf("flush owner transcript: %w", err)
	}
	return nil
}

// awaitReportInTranscript waits for the batch message to land in history. The
// prompt is appended by the run goroutine, so it is not there when Execute
// returns; the message carries no live announcement to subscribe to, hence the
// bounded poll.
func (m *Manager) awaitReportInTranscript(sess *ManagedSession, batchID string) bool {
	deadline := time.Now().Add(reportConfirmTimeout)
	for {
		msgs := sess.History()
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Custom == nil {
				continue
			}
			if msgs[i].Custom["batch"] == batchID {
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
//   - a child (an ordinary session whose codebase has an owner) feeds its run
//     outcomes to the coordinator;
//   - the owner itself feeds nothing, but nudges the coordinator on every
//     outcome of its own: that is the moment a retained batch can finally be
//     delivered without steering it.
//
// The owner is looked up once, when the session is built, like the project book
// in its prompt: a session that predates its project's owner starts reporting
// when it is next resumed.
func (m *Manager) subscribeOwnerReports(sess *ManagedSession, ownerSession bool) {
	if m.reports == nil {
		return
	}
	own, found, err := m.reports.store.FindByDir(sess.CWD)
	if err != nil {
		slog.Warn("owner reports: cannot resolve the owner of a session", "session", sess.ID, "error", err)
		return
	}
	if !found {
		return
	}
	key := own.CodebaseKey
	if ownerSession {
		subscribeRunOutcomes(sess, func(runOutcome) { m.reports.nudge(key) })
		return
	}
	subscribeRunOutcomes(sess, func(out runOutcome) {
		m.reports.add(key, reportFrom(sess, out))
	})
}

// reportFrom turns a run outcome into the report the owner will read. The
// pending interaction is carried literally: an owner deciding whether to answer
// a question needs the question, not a label saying one exists.
func reportFrom(sess *ManagedSession, out runOutcome) owner.Report {
	rep := owner.Report{
		ID:        reportID(sess.ID, out),
		SessionID: sess.ID,
		Title:     sess.title(),
		CWD:       sess.CWD,
		Status:    out.Status,
		FinalText: reportTail(out.FinalText),
		At:        time.Now().UTC().Format(time.RFC3339),
	}
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

// reportID identifies one outcome of one run, so the same event queued twice
// (a retried delivery, a recovered outbox) is recognized as one report.
func reportID(sessionID string, out runOutcome) string {
	return fmt.Sprintf("%s:%d:%s", sessionID, out.RunGen, out.Status)
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

// reportTail keeps the end of the child's final message: what it concluded is
// nearer the end than the beginning.
func reportTail(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxReportFinalTextBytes {
		return text
	}
	cut := len(text) - maxReportFinalTextBytes
	for cut < len(text) && !utf8Start(text[cut]) {
		cut++
	}
	return "…" + text[cut:]
}

// reportsMessage renders a batch. The format is fixed and plain: the owner is
// reading a status board, and a stable shape is what lets it compare one cycle
// with the next.
func reportsMessage(pending []owner.Report) string {
	var b strings.Builder
	if len(pending) == 1 {
		b.WriteString("Report from a session of your project:\n\n")
	} else {
		fmt.Fprintf(&b, "Reports from %d sessions of your project:\n\n", len(pending))
	}
	for _, rep := range pending {
		fmt.Fprintf(&b, "- %s — %s\n", rep.SessionID, strings.TrimSpace(rep.Title))
		fmt.Fprintf(&b, "  status: %s\n", rep.Status)
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
		}
	}
	b.WriteString("\nUse the sessions tool to read or answer any of them, and update the book " +
		"with what this changes about the project.")
	return b.String()
}

// indentReportText keeps a multi-line tail inside its bullet.
func indentReportText(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n    ")
}
