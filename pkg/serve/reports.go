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

// maxReportFinalTextBytes caps the tail of a child's final message carried in a
// report. The owner reads the full session through the `sessions` tool when the
// tail is not enough.
const maxReportFinalTextBytes = 2 << 10

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
	closeOnce sync.Once
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
	// outboxFailed remembers that the last write of this batch failed, so the
	// warning is logged once rather than per report while the disk is broken.
	outboxFailed bool
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
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
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
	case <-c.quit:
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
			c.drain(batches)
			return
		case <-c.quit:
			c.drain(batches)
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

// Close stops the coordinator: timers are stopped so nothing retries after the
// manager is gone, the mailbox is drained so a report accepted moments before
// shutdown is not lost, and everything still pending is written to the outbox
// for the next process to deliver.
//
// It is idempotent and safe to call after the root context was cancelled: the
// loop closes done exactly once, whichever of the two paths wins.
func (c *reportCoordinator) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() { close(c.quit) })
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
			if cmd.report == nil {
				continue // a nudge has nowhere to deliver to any more
			}
			batch := batches[cmd.key]
			if batch == nil {
				batch = &reportBatch{}
				batches[cmd.key] = batch
			}
			c.queue(cmd.key, batch, *cmd.report)
		default:
			for key, batch := range batches {
				c.disarm(batch)
				if len(batch.pending) == 0 {
					continue
				}
				if err := c.store.SaveReports(key, batch.pending); err != nil {
					slog.Warn("owner reports: pending batch lost at shutdown",
						"codebase", key, "error", err)
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
	if !c.queue(key, batch, rep) {
		return
	}
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
	for _, existing := range batch.pending {
		if existing.ID == rep.ID {
			return false // the same outcome, re-delivered: already queued
		}
	}
	batch.pending = append(batch.pending, rep)
	if err := c.store.SaveReports(key, batch.pending); err != nil {
		if !batch.outboxFailed {
			batch.outboxFailed = true
			slog.Warn("owner reports: could not persist the outbox; keeping the reports in memory",
				"codebase", key, "session", rep.SessionID, "error", err)
		}
		return true
	}
	batch.outboxFailed = false
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
		ID:        newReportID(),
		SessionID: sess.ID,
		Title:     sess.title(),
		CWD:       sess.CWD,
		Origin:    sess.Origin,
		Status:    out.Status,
		// The delta comes out of the WHOLE message; the tail is what is carried
		// for reading. A session that closes with a long summary would otherwise
		// push its own book delta out of the report that exists to apply it.
		BookDelta: extractBookDelta(out.FinalText),
		FinalText: reportTail(out.FinalText),
		At:        time.Now().UTC().Format(time.RFC3339),
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
		delta = cutAtRuneBoundary(delta, maxBookDeltaBytes) + "\n[truncated]"
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
