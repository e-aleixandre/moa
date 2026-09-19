package serve

// Project owners — heartbeat. The owner's own clock: a deterministic evaluator
// that looks at the project every few minutes and wakes the owner ONLY when
// something new is true. No model runs to decide whether a model should run, so
// a quiet project costs nothing.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
)

// heartbeatSource is the custom envelope a beat carries, rendered as an event
// block: the owner did not type it, and it is not a report either.
const heartbeatSource = "heartbeat"

// heartbeatInterval is how often the evaluator looks. Overridden in tests.
var heartbeatInterval = owner.HeartbeatInterval

// heartbeatFact is one thing that is true about the project and was not true
// (or was not announced) at the last beat. Key is its stable identity: the same
// session waiting on the same question is the same fact an hour later, and the
// owner is told about it once.
type heartbeatFact struct {
	Key  string
	Text string
}

// heartbeatSession is the part of a session the evaluator reads. It exists so
// the rule can be tested without a live Manager: what wakes an owner is worth
// asserting exactly.
type heartbeatSession struct {
	ID      string
	Title   string
	Waiting string // what it is blocked on, "" when it is not
	// PendingID identifies the question or the permission, and Since is when it
	// was asked — not when the session was last touched.
	PendingID string
	Since     time.Time
}

// heartbeatWork is one work/<feature>.md file and when it last moved.
type heartbeatWork struct {
	Path     string
	Modified time.Time
}

// heartbeatFacts is the whole rule, and it is deliberately small: a session
// blocked on the user for longer than the threshold, and a work file nobody has
// touched in days. Anything else is the owner's judgement, not a timer's.
//
// Reports and failures are NOT here on purpose. The report coordinator owns
// every outcome end to end — it persists them, retries them while the owner is
// busy, and resumes on restart — so a heartbeat reading the same outbox either
// duplicates the turn or, once the outbox is emptied, invents a failure that
// was already delivered.
func heartbeatFacts(now time.Time, settings owner.HeartbeatSettings, sessions []heartbeatSession, work []heartbeatWork) []heartbeatFact {
	var facts []heartbeatFact
	idle := time.Duration(settings.IdleMinutes) * time.Minute
	for _, sess := range sessions {
		if sess.Waiting == "" || sess.Since.IsZero() || now.Sub(sess.Since) < idle {
			continue
		}
		facts = append(facts, heartbeatFact{
			// The key carries WHAT it waits for: a session that gets unblocked
			// and then asks something else is a new fact, and the same question
			// an hour later is not.
			Key: "waiting:" + sess.ID + ":" + heartbeatPendingKey(sess),
			Text: fmt.Sprintf("%s — %s has been waiting %s: %s",
				sess.ID, sessionLabel(sess.Title), roundDuration(now.Sub(sess.Since)), sess.Waiting),
		})
	}
	stale := time.Duration(settings.StaleDays) * 24 * time.Hour
	for _, file := range work {
		if file.Modified.IsZero() || now.Sub(file.Modified) < stale {
			continue
		}
		facts = append(facts, heartbeatFact{
			// The modification time is part of the identity: touching the file
			// and letting it go stale again is news again.
			Key:  fmt.Sprintf("stale:%s:%d", file.Path, file.Modified.Unix()),
			Text: fmt.Sprintf("%s has not moved in %s", file.Path, roundDuration(now.Sub(file.Modified))),
		})
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Key < facts[j].Key })
	return facts
}

func sessionLabel(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "untitled session"
	}
	return title
}

// heartbeatPendingKey identifies what a session waits for. The id of the ask or
// the permission is the exact identity; the rendered line is the fallback for a
// pending the bus could not name.
func heartbeatPendingKey(sess heartbeatSession) string {
	if sess.PendingID != "" {
		return sess.PendingID
	}
	return sess.Waiting
}

// roundDuration says "3h" or "8 days", which is the resolution the fact is
// worth: the owner acts on "for a while", not on minutes.
func roundDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// heartbeatMessage is what the owner reads when it is woken. It states the
// facts and asks for the balance the role prompt already requires: the beat is
// the occasion, not a second set of instructions.
func heartbeatMessage(facts []heartbeatFact) string {
	var b strings.Builder
	b.WriteString("Heartbeat. Nobody is talking to you; these are new since the last beat:\n\n")
	for _, fact := range facts {
		b.WriteString("- " + fact.Text + "\n")
	}
	b.WriteString("\nCheck the state of the project (sessions list, work/), deal with what you " +
		"can, and end with your balance: what moved, what is stopped and waiting on whom, " +
		"what you propose. Ask the user only about what is theirs to decide.")
	return b.String()
}

// heartbeatBeatID is the identity of one delivered beat, so the transcript can
// be checked for the exact message that was just sent (see wake). It is derived
// from the facts, which makes it stable across a retry of the same beat.
func heartbeatBeatID(facts []heartbeatFact) string {
	sum := sha256.New()
	for _, fact := range facts {
		sum.Write([]byte(fact.Key))
		sum.Write([]byte{0})
	}
	return "beat_" + hex.EncodeToString(sum.Sum(nil))[:16]
}

// heartbeatService runs the evaluator for every owner on one ticker.
type heartbeatService struct {
	mgr     *Manager
	store   *owner.Store
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newHeartbeatService(m *Manager) *heartbeatService {
	store, err := m.ownerStore()
	if err != nil {
		slog.Warn("owner heartbeat disabled", "error", err)
		return nil
	}
	return &heartbeatService{mgr: m, store: store, stop: make(chan struct{}), stopped: make(chan struct{})}
}

func (h *heartbeatService) Start() {
	if h == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		defer close(h.stopped)
		for {
			select {
			case <-ticker.C:
				h.beat(time.Now())
			case <-h.stop:
				return
			}
		}
	}()
}

func (h *heartbeatService) Close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		close(h.stop)
		<-h.stopped
	})
}

// beat evaluates every owner once.
func (h *heartbeatService) beat(now time.Time) {
	if h == nil {
		return
	}
	owners, err := h.store.List()
	if err != nil {
		slog.Warn("owner heartbeat: cannot list owners", "error", err)
		return
	}
	for _, own := range owners {
		h.beatOwner(own, now)
	}
}

func (h *heartbeatService) beatOwner(own owner.Owner, now time.Time) {
	settings := own.HeartbeatSettings()
	if !settings.Enabled || own.SessionID == "" {
		return
	}
	facts := heartbeatFacts(now, settings, h.sessionsOf(own), workFiles(h.store.BookDir(own.CodebaseKey)))

	state := h.store.LoadHeartbeatState(own.CodebaseKey)
	fresh := make([]heartbeatFact, 0, len(facts))
	kept := make(map[string]time.Time, len(facts))
	for _, fact := range facts {
		if at, seen := state.Announced[fact.Key]; seen {
			// Still true, already said: keep the memory of it, say nothing.
			kept[fact.Key] = at
			continue
		}
		fresh = append(fresh, fact)
	}
	if len(fresh) == 0 {
		// Nothing new: no prompt, no model, no cost. Only the bookkeeping is
		// written, and only when a fact stopped being true.
		h.persist(own, HeartbeatRecord{Announced: kept, LastBeat: state.LastBeat}, state)
		return
	}
	if err := h.wake(own, fresh); err != nil {
		// A busy owner, or a beat that never reached the transcript, is not a
		// missed beat: the facts stay unannounced and the next tick tries
		// again. Marking them announced here is how a crash between the send
		// and the transcript loses the only notice the owner would ever get.
		slog.Debug("owner heartbeat: not delivered", "codebase", own.CodebaseKey, "error", err)
		h.persist(own, HeartbeatRecord{Announced: kept, LastBeat: state.LastBeat}, state)
		return
	}
	slog.Info("owner heartbeat", "codebase", own.CodebaseKey, "facts", len(fresh))
	for _, fact := range fresh {
		kept[fact.Key] = now
	}
	h.persist(own, HeartbeatRecord{Announced: kept, LastBeat: now}, state)
}

// HeartbeatRecord mirrors owner.HeartbeatState; it is a local alias so the
// persistence call reads as one statement.
type HeartbeatRecord = owner.HeartbeatState

func (h *heartbeatService) persist(own owner.Owner, next, prev HeartbeatRecord) {
	if sameHeartbeatState(next, prev) {
		return
	}
	if err := h.store.SaveHeartbeatState(own.CodebaseKey, next); err != nil {
		slog.Warn("owner heartbeat: cannot persist state", "codebase", own.CodebaseKey, "error", err)
	}
}

func sameHeartbeatState(a, b HeartbeatRecord) bool {
	if !a.LastBeat.Equal(b.LastBeat) || len(a.Announced) != len(b.Announced) {
		return false
	}
	for key, at := range a.Announced {
		other, ok := b.Announced[key]
		if !ok || !other.Equal(at) {
			return false
		}
	}
	return true
}

// sessionsOf snapshots the owner's children: what they are blocked on and
// since when. Only live sessions are read — a session on disk is not waiting
// for anybody.
func (h *heartbeatService) sessionsOf(own owner.Owner) []heartbeatSession {
	var out []heartbeatSession
	for _, info := range h.mgr.ListWith(ListOptions{IncludeOwners: true}) {
		if info.Kind == session.KindOwner || !inCodebase(own, info.CWD) {
			continue
		}
		out = append(out, heartbeatSession{
			ID:    info.ID,
			Title: info.Title,
			// Since is when the user was actually asked, not when the session
			// was last written to: a long-lived conversation that asks a
			// question now must not wake the owner as if it had been blocked
			// since this morning.
			PendingID: info.PendingID,
			Since:     info.PendingSince,
			Waiting:   h.mgr.ownerPendingLine(info.ID),
		})
	}
	return out
}

// workFiles lists book/work/*.md with their modification times. A branch file
// nobody edits is the cheapest signal that a piece of work stopped.
func workFiles(bookDir string) []heartbeatWork {
	entries, err := os.ReadDir(filepath.Join(bookDir, "work"))
	if err != nil {
		return nil
	}
	var out []heartbeatWork
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		if strings.EqualFold(name, "README.md") {
			continue // the template's explanation, not a piece of work
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, heartbeatWork{Path: "work/" + name, Modified: info.ModTime()})
	}
	return out
}

// wake delivers the beat into the owner's conversation, idle-only for the same
// reason a batch of reports is: an owner mid-thought is never steered by the
// machinery around it.
//
// A beat counts as delivered only when it is in the transcript AND the
// transcript is on disk. Execute returning means the run goroutine accepted the
// prompt, not that the message exists: persisting "announced" at that point is
// how a crash in that window leaves heartbeat.json claiming the owner was told
// something it will never read. Anything short of both is an error, and the
// next tick retries with the same stable beat id.
func (h *heartbeatService) wake(own owner.Owner, facts []heartbeatFact) error {
	sess, ok := h.mgr.Get(own.SessionID)
	if !ok {
		// A sleeping owner is not woken from disk by a timer: the facts are
		// still true when it is next opened, and resuming a conversation to
		// tell it that nothing is happening is exactly the cost this design
		// exists to avoid.
		return errors.New("the owner's conversation is not loaded")
	}
	beatID := heartbeatBeatID(facts)
	if err := func() error {
		sess.lifecycle.RLock()
		defer sess.lifecycle.RUnlock()
		if sess.closing.Load() {
			return ErrNotFound
		}
		if err := sess.runtime.Bus.Execute(bus.SendPrompt{
			SessionID: sess.ID,
			Text:      heartbeatMessage(facts),
			Custom: map[string]any{
				"source": heartbeatSource,
				"beat":   beatID,
				"facts":  len(facts),
			},
			IdleOnly: true,
		}); err != nil {
			return err
		}
		sess.sendGeneration.Add(1)
		return nil
	}(); err != nil {
		return err
	}
	if !h.mgr.awaitCustomInTranscript(sess, "beat", beatID) {
		return errors.New("the beat was sent but did not reach the transcript")
	}
	if err := sess.runtime.Flush(); err != nil {
		return fmt.Errorf("flush owner transcript: %w", err)
	}
	return nil
}
