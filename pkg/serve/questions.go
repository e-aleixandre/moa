package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// askUserAsyncDefault is what ask_user_async means when the config leaves it
// unset. The asynchronous contract is a pilot, so the build's default is off;
// the package tests turn it on (testmain_test.go) and pin the legacy tests that
// exercise the blocking path to false.
var askUserAsyncDefault atomic.Bool

// questionContract names the version of the asynchronous contract in the
// metadata a transcript carries, so an acknowledgement is never mistaken for a
// legacy answer.
const questionContract = "async-v1"

// questionService owns the life of a session's asynchronous questions: posting,
// answering, withdrawing, dismissing and fencing. The rows live in the shared
// task database (pkg/tasks); this type adds the order between them and the
// live session.
//
// Locking: one mutex per session serializes the operations that must see each
// other in a single durable order: a post, an acceptance, a close, a Stop's
// fence and the dispatcher's admission of an answer. It is taken after the
// dispatcher's mutex, never before, and the dispatcher's mutex is never taken
// while holding it.
type questionService struct {
	m     *Manager
	repo  *tasks.Repo
	mu    sync.Mutex
	locks map[string]*sync.Mutex
	hooks questionHooks
	// unfenced holds, per session, a Stop or recall whose fence could not be
	// written (true when it also stopped the run). Until a retry commits it, no
	// accepted answer of that session is admitted or continued in this
	// process. Guarded by the session's lock.
	unfenced sync.Map
	// gone marks the sessions Delete has settled for the last time; a post
	// for one of them refuses to commit. Written under the session's lock.
	gone sync.Map
}

// questionHooks are seams for tests to stop the service at the boundaries the
// contract names. All nil in production.
type questionHooks struct {
	// afterCheckpoint runs once the origin of a post is durable, before the
	// question is committed.
	afterCheckpoint func()
	// afterCommit runs once the question is committed, before it is announced
	// and acknowledged.
	afterCommit func()
	// afterBranchSnapshot runs once a rewound branch is on disk, before the
	// questions it left behind are invalidated.
	afterBranchSnapshot func()
}

func newQuestionService(m *Manager) *questionService {
	return &questionService{m: m, repo: m.tasks, locks: map[string]*sync.Mutex{}}
}

// engaged reports whether the pilot has ever left a question in the task
// database. Until it has, nothing here has anything to settle, fence or
// recover, so the callers that sit on the paths every deployment runs (Stop,
// rewind, resume, delete, answering a card) skip their work: with the pilot off
// they behave as they did before it existed.
func (q *questionService) engaged() bool {
	return q != nil && q.repo.HasQuestions(q.m.baseCtx)
}

// lock takes the session's lock and returns its release.
func (q *questionService) lock(sessionID string) func() {
	q.mu.Lock()
	l, ok := q.locks[sessionID]
	if !ok {
		l = &sync.Mutex{}
		q.locks[sessionID] = l
	}
	q.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Answer accepts an answer durably (answer and event in one transaction),
// then retires the question's projection and wakes the dispatcher. The same
// answer from the same responder again succeeds without a second event.
func (q *questionService) Answer(ctx context.Context, sessionID, id string, answers []string, responder askuser.Responder) error {
	unlock := q.lock(sessionID)
	defer unlock()
	if err := q.checkOrigin(ctx, sessionID, id); err != nil {
		return err
	}
	res, err := q.repo.AnswerQuestion(ctx, sessionID, id, answers, responder, renderQuestionAnswer)
	if err != nil {
		return err
	}
	if res.Duplicate {
		return nil
	}
	q.retire(sessionID, id)
	q.m.notices.nudge()
	return nil
}

// Withdraw is the model's own retirement of a question it no longer needs.
func (q *questionService) Withdraw(ctx context.Context, sessionID, id, reason string) (tasks.Question, error) {
	return q.close(ctx, sessionID, id, tasks.QuestionWithdrawn, reason)
}

// Dismiss is the human's administrative close: no answer, no event, no model
// call.
func (q *questionService) Dismiss(ctx context.Context, sessionID, id string) error {
	_, err := q.close(ctx, sessionID, id, tasks.QuestionDismissed, "")
	return err
}

func (q *questionService) close(ctx context.Context, sessionID, id string, to tasks.QuestionState, reason string) (tasks.Question, error) {
	unlock := q.lock(sessionID)
	defer unlock()
	question, changed, err := q.repo.CloseQuestion(ctx, sessionID, id, to, reason)
	if err != nil {
		return tasks.Question{}, err
	}
	if changed {
		q.retire(sessionID, id)
	}
	return question, nil
}

// Bounds of the open-questions block: it is read on every request, so it stays
// small whatever the session has asked.
//
// The bounds are on what is serialized: at most 8 questions listed, each one's
// text (all its items together) at most 400 bytes once escaped, and the whole
// block at most inventoryMaxBytes whatever those add up to.
const (
	inventoryMaxQuestions = 8
	inventoryMaxText      = 400
	inventoryMaxBytes     = 4608
)

// escapedPrefix is s, whitespace-normalized and XML-escaped, cut so the
// escaped text is at most max bytes (an ellipsis included when it was cut).
// It cuts the raw text rune by rune, so no entity or character is split.
func escapedPrefix(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if escaped := xmlText(s); len(escaped) <= max {
		return escaped
	}
	const ellipsis = "..."
	var b strings.Builder
	for _, r := range s {
		piece := xmlText(string(r))
		if b.Len()+len(piece) > max-len(ellipsis) {
			break
		}
		b.WriteString(piece)
	}
	return b.String() + ellipsis
}

// inventory returns the per-request block that tells the model which of its
// questions are still open. It is derived from the live projection, so it
// follows answers and withdrawals made during the run, and it is never stored:
// the transcript keeps only the acknowledgement and, later, the answer.
func (q *questionService) inventory(enabled bool, session func() *ManagedSession) func() *core.Message {
	if q == nil || !enabled {
		return nil
	}
	return func() *core.Message {
		sess := session()
		if sess == nil {
			return nil
		}
		open := sess.runtime.Context().Approvals.AsyncAsks()
		if len(open) == 0 {
			return nil
		}
		var b strings.Builder
		b.WriteString("<open_questions>\nQuestions you asked that nobody has answered yet. Each answer arrives later as a question_answer event; do not ask them again, and apply the pending-answer rule to anything you do meanwhile. Use ask_user with action \"list\" for detail.\n")
		const closing = "</open_questions>"
		const more = "(and 99999 more)\n"
		for i, ask := range open {
			tap := ""
			if ask.RequiresTap {
				tap = " requires the user's own tap"
			}
			texts := make([]string, 0, len(ask.Questions))
			for _, item := range ask.Questions {
				texts = append(texts, item.Text)
			}
			line := fmt.Sprintf("- %s%s, asked %s: %s\n", ask.ID, tap, ask.RequestedAt.UTC().Format(time.RFC3339), escapedPrefix(strings.Join(texts, " "), inventoryMaxText))
			if i == inventoryMaxQuestions || b.Len()+len(line)+len(more)+len(closing) > inventoryMaxBytes {
				fmt.Fprintf(&b, "(and %d more)\n", len(open)-i)
				break
			}
			b.WriteString(line)
		}
		b.WriteString(closing)
		msg := core.NewUserMessage(b.String())
		return &msg
	}
}

// originOnPath reports whether the call that posted q is on the branch: the
// assistant message, with that tool call in it. The three parts of the
// invocation's identity have to agree, so a message that merely shares an ID
// does not stand in for the origin.
func originOnPath(path []session.Entry, q tasks.Question) bool {
	if q.OriginMsgID == "" {
		return true
	}
	for _, entry := range path {
		if entry.Type != session.EntryMessage || entry.Message.MsgID != q.OriginMsgID {
			continue
		}
		for _, c := range entry.Message.Content {
			if c.Type == "tool_call" && c.ToolCallID == q.OriginToolCallID {
				return true
			}
		}
	}
	return false
}

func entriesHaveNotice(entries []session.Entry, id string) bool {
	for _, entry := range entries {
		if isAdmissionReceipt(entry.Message.Custom) && entry.Message.Custom["id"] == id {
			return true
		}
	}
	return false
}

// reconcileLocked settles the session's questions against the branch its tree
// is on. It must run when the tree is quiescent and be called under the
// session's lock: after a move of the leaf (once the new branch is on disk),
// and when a session is loaded, where the tree is the saved one.
//
//   - an open question whose origin left the branch is invalidated: the
//     conversation that asked is gone, and a late answer would wake one that
//     never did;
//   - an answer consumed on a branch that was left stays where it is (no
//     re-injection: the dedup looks at every branch) and owes no run;
//   - an answer not yet admitted whose origin left the branch fails.
//
// Terminal questions are never reopened by coming back to an older branch.
func (q *questionService) reconcileLocked(ctx context.Context, sessionID string, tree *session.Tree) {
	questions, err := q.repo.SessionQuestions(ctx, sessionID)
	if err != nil {
		slog.Warn("questions: reading a session's questions to reconcile them failed", "session", sessionID, "error", err)
		return
	}
	if len(questions) == 0 {
		return
	}
	path, all := tree.Path(), tree.Entries()
	for _, question := range questions {
		switch question.State {
		case tasks.QuestionOpen:
			if originOnPath(path, question) {
				continue
			}
			if _, changed, err := q.repo.CloseQuestion(ctx, sessionID, question.ID, tasks.QuestionInvalidated, tasks.ReasonRewound); err != nil {
				slog.Warn("questions: invalidating a rewound question failed", "session", sessionID, "question", question.ID, "error", err)
			} else if changed {
				q.retire(sessionID, question.ID)
			}
		case tasks.QuestionAnswered:
			// Whatever a Stop decided about its run, an answer that was never
			// admitted is only deliverable to the branch that asked: Stop removes
			// the autorun, it does not make the answer valid anywhere.
			var err error
			switch {
			case entriesHaveNotice(path, question.EventID):
				// Read on this branch: its run, if still owed, stays owed.
			case entriesHaveNotice(all, question.EventID):
				if question.Work == tasks.WorkDue {
					_, err = q.repo.SetQuestionWork(ctx, question.EventID, tasks.WorkDue, tasks.WorkStopped)
				}
			case !originOnPath(path, question):
				_, err = q.repo.AbandonQuestionAnswer(ctx, question.EventID, tasks.ReasonRewound)
			}
			if err != nil {
				slog.Warn("questions: settling a rewound answer failed", "session", sessionID, "question", question.ID, "error", err)
			}
		}
	}
}

// branch moves the session's leaf and settles its questions in the order the
// durable record needs: under the session's lock (no post, answer or
// admission interleaves), the new branch is on disk before anything is
// invalidated, so a crash in between reloads a session that settles itself
// from the snapshot. If the snapshot cannot be written nothing is
// invalidated: disk and database keep agreeing, and an answer's own check
// still refuses a question whose origin the live tree has left.
func (q *questionService) branch(sess *ManagedSession, cmd bus.BranchTo) error {
	if !q.engaged() {
		return sess.runtime.Bus.Execute(cmd)
	}
	unlock := q.lock(sess.ID)
	defer unlock()
	if err := sess.runtime.Bus.Execute(cmd); err != nil {
		return err
	}
	q.settleLeafLocked(sess)
	return nil
}

// settleLeafLocked is the second half of every move of the leaf: save the new
// branch, then settle the questions against it.
func (q *questionService) settleLeafLocked(sess *ManagedSession) {
	if err := sess.runtime.CheckpointAcknowledged(q.m.baseCtx); err != nil {
		slog.Warn("questions: saving the rewound branch failed; its questions are settled later", "session", sess.ID, "error", err)
		return
	}
	if q.hooks.afterBranchSnapshot != nil {
		q.hooks.afterBranchSnapshot()
	}
	q.reconcileLocked(q.m.baseCtx, sess.ID, sess.runtime.Context().Tree)
}

// deleted settles a deleted session's questions a last time, under its lock
// and once its runtime's users are drained: the first settlement (with the
// rest of its scheduled work) cannot see a post that was between its
// checkpoint and its commit. Later posts see gone and refuse.
func (q *questionService) deleted(sess *ManagedSession) {
	// A session that cannot post and a database with no question has nothing
	// to settle (a session that can post marks itself gone either way: its
	// post may be racing this delete).
	if q == nil || (!sess.askAsync && !q.engaged()) {
		return
	}
	sessionID := sess.ID
	unlock := q.lock(sessionID)
	defer unlock()
	q.gone.Store(sessionID, struct{}{})
	if err := q.repo.SettleDeletedSessionQuestions(q.m.baseCtx, sessionID); err != nil {
		slog.Warn("questions: settling a deleted session's questions failed", "session", sessionID, "error", err)
	}
}

// cleared settles the questions of a session whose conversation was emptied
// (/clear run from the queue): nothing is left for an origin to be on.
func (q *questionService) cleared(sess *ManagedSession) {
	unlock := q.lock(sess.ID)
	defer unlock()
	q.settleLeafLocked(sess)
}

// checkOrigin refuses to answer a question whose origin is not on the branch
// the session is on (the live tree, or the saved snapshot of a session that is
// not loaded), closing it as invalidated. It is what keeps a stale card from
// waking a conversation after any rewind, whichever path moved the leaf.
func (q *questionService) checkOrigin(ctx context.Context, sessionID, id string) error {
	question, err := q.repo.GetQuestion(ctx, id)
	if err != nil || question.SessionID != sessionID || question.State != tasks.QuestionOpen {
		return nil // AnswerQuestion reports it
	}
	tree, err := q.currentTree(sessionID)
	switch {
	case errors.Is(err, session.ErrNotFound):
		// The conversation is gone: nothing could ever read the answer.
		if _, _, err := q.repo.CloseQuestion(ctx, sessionID, id, tasks.QuestionInvalidated, tasks.ReasonSessionDeleted); err != nil {
			return err
		}
		return &tasks.ErrQuestionConflict{State: tasks.QuestionInvalidated}
	case err != nil:
		return err
	}
	if tree == nil || originOnPath(tree.Path(), question) {
		return nil
	}
	if _, changed, err := q.repo.CloseQuestion(ctx, sessionID, id, tasks.QuestionInvalidated, tasks.ReasonRewound); err != nil {
		return err
	} else if changed {
		q.retire(sessionID, id)
	}
	return &tasks.ErrQuestionConflict{State: tasks.QuestionInvalidated}
}

// currentTree is the session's branch as it stands: the live tree, or the
// saved snapshot read without loading the session; nil for a saved session
// with no entries, session.ErrNotFound when the session exists nowhere.
func (q *questionService) currentTree(sessionID string) (*session.Tree, error) {
	if sess, ok := q.m.Get(sessionID); ok {
		return sess.runtime.Context().Tree, nil
	}
	saved, _, err := session.FindSessionReadOnly(q.m.sessionBaseDir, sessionID)
	switch {
	case errors.Is(err, session.ErrNotFound):
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("reading the saved session: %w", err)
	case len(saved.Entries) == 0:
		return nil, nil
	}
	return session.NewTreeFromEntries(saved.Entries, saved.LeafID)
}

// retire removes the projection of a question that is no longer open and tells
// the clients. The durable state is already committed.
func (q *questionService) retire(sessionID, id string) {
	sess, ok := q.m.Get(sessionID)
	if !ok {
		return
	}
	if sess.runtime.Context().Approvals.DropAsync(id) {
		sess.runtime.Bus.Publish(bus.AskUserResolved{SessionID: sessionID, ID: id})
	}
}

// Open lists the session's open questions.
func (q *questionService) Open(ctx context.Context, sessionID string) ([]tasks.Question, error) {
	return q.repo.OpenQuestions(ctx, sessionID)
}

// Fence commits, before a Stop or a recall is confirmed, what becomes of the
// answers already accepted (see tasks.FenceQuestionAnswers). A failure never
// keeps the run from being stopped: the caller stops it and reports the
// error, and the session stays barred (see unfenced) so no answer accepted
// before the Stop starts a run while the decision is not on disk.
//
// A recall (stopRun false) never touches an answer that already has a receipt
// in the conversation: it is being read, and the outbox state ("sent") only
// says what the transport did, not whether the model has it.
func (q *questionService) Fence(ctx context.Context, sessionID string, stopRun bool) error {
	if !q.engaged() {
		return nil
	}
	unlock := q.lock(sessionID)
	defer unlock()
	if prev, ok := q.unfenced.Load(sessionID); ok && prev.(bool) {
		stopRun = true
	}
	if err := q.fenceLocked(ctx, sessionID, stopRun); err != nil {
		q.unfenced.Store(sessionID, stopRun)
		slog.Warn("questions: fencing the accepted answers failed", "session", sessionID, "error", err)
		return fmt.Errorf("%w: %w", errFenceNotSaved, err)
	}
	q.unfenced.Delete(sessionID)
	return nil
}

func (q *questionService) fenceLocked(ctx context.Context, sessionID string, stopRun bool) error {
	var admitted []string
	if !stopRun {
		admitted = q.admittedEvents(ctx, sessionID)
	}
	_, err := q.repo.FenceQuestionAnswers(ctx, sessionID, stopRun, admitted)
	return err
}

// errFenceNotSaved marks a Stop or recall that did happen but whose decision
// about accepted answers is not on disk.
var errFenceNotSaved = errors.New("the answers accepted before this stop could not be saved as stopped")

// barredLocked reports whether the session still has a fence that never
// reached the database, retrying it first. Callers hold the session's lock.
func (q *questionService) barredLocked(ctx context.Context, sessionID string) bool {
	stopRun, ok := q.unfenced.Load(sessionID)
	if !ok {
		return false
	}
	if err := q.fenceLocked(ctx, sessionID, stopRun.(bool)); err != nil {
		return true
	}
	q.unfenced.Delete(sessionID)
	return false
}

// admittedEvents returns the session's owed answers whose message is already
// in its conversation, in the history the model reads or anywhere in the tree.
func (q *questionService) admittedEvents(ctx context.Context, sessionID string) []string {
	sess, ok := q.m.Get(sessionID)
	if !ok {
		return nil
	}
	due, err := q.repo.DueQuestionWork(ctx)
	if err != nil {
		slog.Warn("questions: reading owed work to fence it failed", "session", sessionID, "error", err)
		return nil
	}
	history := sess.History()
	tree := sess.runtime.Context().Tree
	var ids []string
	for _, question := range due {
		if question.SessionID != sessionID {
			continue
		}
		if hasNotice(history, question.EventID) || (tree != nil && entriesHaveNotice(tree.Entries(), question.EventID)) {
			ids = append(ids, question.EventID)
		}
	}
	return ids
}

// project adds a question to the live session's pending set.
func (q *questionService) project(sess *ManagedSession, question tasks.Question, announce bool) {
	info := bus.PendingAskInfo{
		ID: question.ID, Questions: busQuestions(question.Questions), RequiresTap: question.RequiresTap,
		RequestedAt: time.UnixMilli(question.AskedAt), Async: true,
	}
	sess.runtime.Context().Approvals.AddAsync(info)
	if announce {
		sess.runtime.Bus.Publish(bus.AskUserRequested{
			SessionID: sess.ID, ID: question.ID, Questions: info.Questions, RequiresTap: question.RequiresTap, Async: true,
		})
	}
}

// attach wires a freshly built session to the service: answers that reach the
// session through the bus (voice, owner, tests) go through the durable
// acceptance, and the questions it already has open are projected.
func (q *questionService) attach(sess *ManagedSession) {
	sess.runtime.Context().Approvals.SetAsyncAskResolver(func(id string, answers []string, responder askuser.Responder) error {
		return q.Answer(q.m.baseCtx, sess.ID, id, answers, responder)
	})
	sess.runtime.Context().AnswerWorkOwed = func() bool { return q.owed(sess) }
	q.seed(sess)
	sess.runtime.Bus.Subscribe(func(e bus.RunEnded) { q.runEnded(sess, e) })
	sess.runtime.Bus.Subscribe(func(e bus.CommandExecuted) {
		if e.Command == "clear" {
			q.cleared(sess)
		}
	})
}

// runEnded settles the continuation an admitted answer owed: the run that
// read it is over. An answer not yet admitted still owes its work, a Stop
// already marked its own as stopped, and a run that never got to read it (the
// receipt could not be saved) or that a shutdown cut short leaves it due, for
// the next time the session is loaded.
//
// The work is only done once the end of that run is on disk: the snapshot
// below is acknowledged after the run, so a conversation that survives a crash
// either has the model's final reply or still owes the run. If the save
// fails the work stays due and the next run end tries again.
func (q *questionService) runEnded(sess *ManagedSession, e bus.RunEnded) {
	ctx := q.m.baseCtx
	if e.Cancelled || errors.Is(e.Err, agent.ErrAdmissionNotSaved) || ctx.Err() != nil {
		return
	}
	due, err := q.repo.DueQuestionWork(ctx)
	if err != nil {
		slog.Warn("questions: reading owed work failed", "session", sess.ID, "error", err)
		return
	}
	var settled []tasks.Question
	for _, question := range due {
		if question.SessionID == sess.ID && q.read(sess, question.EventID) {
			settled = append(settled, question)
		}
	}
	if len(settled) == 0 {
		return
	}
	if err := sess.runtime.CheckpointAcknowledged(ctx); err != nil {
		slog.Warn("questions: the end of the run could not be saved; its work stays due", "session", sess.ID, "error", err)
		return
	}
	for _, question := range settled {
		if _, err := q.repo.SetQuestionWork(ctx, question.EventID, tasks.WorkDue, tasks.WorkDone); err != nil {
			slog.Warn("questions: settling owed work failed", "session", sess.ID, "error", err)
		}
	}
}

// read reports whether the answer's event is already in the conversation the
// session holds. History is the projection the model reads, which a
// compaction may have cut the answer out of; the tree keeps every entry.
func (q *questionService) read(sess *ManagedSession, eventID string) bool {
	if hasNotice(sess.History(), eventID) {
		return true
	}
	tree := sess.runtime.Context().Tree
	return tree != nil && entriesHaveNotice(tree.Path(), eventID)
}

// owed reports whether an accepted answer still has to be read: its work is
// due and its event has not entered the conversation. The run that read it
// has nothing owed even while its work is still marked due (runEnded settles
// it), so a goal verifying that run's end is not held back by it.
func (q *questionService) owed(sess *ManagedSession) bool {
	due, err := q.repo.DueQuestionWork(q.m.baseCtx)
	if err != nil {
		// Fail closed: yielding spends nothing, a wrong verdict does.
		slog.Warn("questions: reading owed work failed", "session", sess.ID, "error", err)
		return true
	}
	for _, question := range due {
		if question.SessionID == sess.ID && !q.read(sess, question.EventID) {
			return true
		}
	}
	return false
}

// continueDue is for a session just loaded from disk: an answer that reached
// its saved transcript and still owes a run (the process died before the run
// ended) gets that run now, over the conversation as saved, with no new
// message. Stop's fence decides under the same lock, so a stopped answer
// owes nothing, and the work stays due until its own run ends.
func (q *questionService) continueDue(sess *ManagedSession) {
	if !q.engaged() {
		return
	}
	tree := sess.runtime.Context().Tree
	if tree == nil {
		return
	}
	ctx := q.m.baseCtx
	unlock := q.lock(sess.ID)
	defer unlock()
	if q.barredLocked(ctx, sess.ID) {
		return
	}
	due, err := q.repo.DueQuestionWork(ctx)
	if err != nil {
		slog.Warn("questions: reading owed work failed", "session", sess.ID, "error", err)
		return
	}
	path := tree.Path()
	var owed []tasks.Question
	for _, question := range due {
		if question.SessionID != sess.ID || !entriesHaveNotice(path, question.EventID) {
			continue
		}
		n, err := q.repo.Notice(ctx, question.EventID)
		switch {
		case err != nil:
			slog.Warn("questions: reading an answer's notice failed", "session", sess.ID, "error", err)
		case n.State == tasks.NoticeFailed:
			// The answer was refused its delivery (rewound, deleted): no run is owed.
			_, _ = q.repo.SetQuestionWork(ctx, question.EventID, tasks.WorkDue, tasks.WorkStopped)
		case n.Method == tasks.MethodRun:
			owed = append(owed, question)
		}
	}
	if len(owed) == 0 {
		return
	}
	switch err := sess.runtime.Bus.Execute(bus.ContinueRun{SessionID: sess.ID, Source: "question_answer"}); {
	case err == nil:
	case errors.Is(err, bus.ErrNothingToContinue):
		// The saved conversation ends in the model's own final reply: the run did
		// finish, and that reply is what reached the disk.
		for _, question := range owed {
			_, _ = q.repo.SetQuestionWork(ctx, question.EventID, tasks.WorkDue, tasks.WorkDone)
		}
	default:
		slog.Warn("questions: continuing a session's owed run failed", "session", sess.ID, "error", err)
	}
}

// seed loads the open questions of a session being built or resumed into its
// projection, so a restart keeps them visible without announcing them again.
// The tree it is built from is the saved snapshot, so what a rewind left
// undone (a crash between the snapshot and the invalidation) is settled first.
func (q *questionService) seed(sess *ManagedSession) {
	unlock := q.lock(sess.ID)
	defer unlock()
	if tree := sess.runtime.Context().Tree; tree != nil {
		q.reconcileLocked(q.m.baseCtx, sess.ID, tree)
	}
	open, err := q.repo.OpenQuestions(q.m.baseCtx, sess.ID)
	if err != nil {
		slog.Warn("questions: loading a session's open questions failed", "session", sess.ID, "error", err)
		return
	}
	for _, question := range open {
		q.project(sess, question, false)
	}
}

func busQuestions(qs []askuser.Question) []bus.AskQuestion {
	out := make([]bus.AskQuestion, len(qs))
	for i, q := range qs {
		out[i] = bus.AskQuestion{Text: q.Text, Options: q.Options, Multiple: q.Multiple}
	}
	return out
}

// repairer closes a tool call the transcript left without a result with the
// acknowledgement its question recorded, so a crash between the question's
// commit and the result's save leaves the same single result the call would
// have returned. A call that never posted is left to the generic repair.
func (q *questionService) repairer(sessionID string) session.ToolCallRepair {
	if q == nil {
		return nil
	}
	return func(originMsgID string, call core.Content) (core.AgentMessage, bool) {
		if (call.ToolName != "ask_user" && call.ToolName != "request_call") || !q.engaged() {
			return core.AgentMessage{}, false
		}
		question, ok, err := q.repo.QuestionByInvocation(q.m.baseCtx, sessionID, originMsgID, call.ToolCallID)
		if err != nil || !ok {
			if err != nil {
				slog.Warn("questions: reading a dangling call's question failed", "session", sessionID, "call", call.ToolCallID, "error", err)
			}
			return core.AgentMessage{}, false
		}
		return ackMessage(call, question.Ack), true
	}
}

func ackMessage(call core.Content, ack tasks.QuestionAck) core.AgentMessage {
	msg := core.WrapMessage(core.NewToolResultMessage(call.ToolCallID, call.ToolName, []core.Content{core.TextContent(ack.Text)}, false))
	if len(ack.Custom) > 0 {
		msg.Custom = make(map[string]any, len(ack.Custom))
		for k, v := range ack.Custom {
			msg.Custom[k] = v
		}
	}
	return msg
}

// openAckText is what a post tells the model. Prose, not contract: the
// contract is the Custom metadata.
func openAckText(id string) string {
	return fmt.Sprintf("Question %s is open. Your answer will arrive later as a question_answer event.\n"+
		"Continue only with work the pending-answer policy allows, or end the turn.\n"+
		"Do not treat this acknowledgement as an answer; do not ask the same question again.", id)
}

func openAck(id string) tasks.QuestionAck {
	return tasks.QuestionAck{
		Text: openAckText(id),
		Custom: map[string]any{
			"question_contract": questionContract,
			"question_id":       id,
			"question_state":    "open",
		},
	}
}

// --- the answer event ---

// xmlText escapes what a block interpolates so no answer, question or name
// can close it or open another one.
func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func xmlAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", " ", "\r", " ").Replace(s)
}

// responderLabel is the by= attribute: who answered, as the server recorded.
func responderLabel(r askuser.Responder) string {
	switch r.Kind {
	case askuser.ResponderUser:
		return "user"
	case askuser.ResponderOwner:
		if r.Name != "" {
			return "owner:" + r.Name
		}
		return "owner"
	case askuser.ResponderAutomation:
		return "automation"
	case askuser.ResponderVoice:
		return "voice"
	}
	return "unknown"
}

// renderQuestionAnswer is the title and the text of an answer's event. Only
// this text reaches the provider: the metadata does not, so authorship has to
// be in it.
func renderQuestionAnswer(q tasks.Question) (title, text string) {
	var b strings.Builder
	fmt.Fprintf(&b, "<question_answer id=\"%s\" asked_at=\"%s\" answered_at=\"%s\" by=\"%s\">\n",
		xmlAttr(q.ID), time.UnixMilli(q.AskedAt).UTC().Format(time.RFC3339),
		time.UnixMilli(q.ClosedAt).UTC().Format(time.RFC3339), xmlAttr(responderLabel(q.Responder)))
	if line := q.Responder.Attribution(); line != "" {
		b.WriteString(xmlText(line) + "\n")
	} else if q.Responder.Kind != askuser.ResponderUser {
		b.WriteString("[The responder is not identified; this is not the user's authorization]\n")
	}
	for i, question := range q.Questions {
		answer := ""
		if i < len(q.Answers) {
			answer = q.Answers[i]
		}
		fmt.Fprintf(&b, "Q: %s\nA: %s\n", xmlText(question.Text), xmlText(answer))
	}
	b.WriteString("</question_answer>")
	return "Answer to your question", b.String()
}

// questionAnswerCustom is the metadata of the answer's message. The server
// fixes it all; the id is the dispatcher's dedup key.
func questionAnswerCustom(n tasks.Notice, q tasks.Question, run, steer bool) map[string]any {
	custom := map[string]any{
		"source":            "question_answer",
		"question_contract": questionContract,
		"question_id":       q.ID,
		"event_id":          n.ID,
		"id":                n.ID,
		"title":             n.Title,
		askuser.CustomKey:   q.Responder,
		"answers":           append([]string(nil), q.Answers...),
		// The questions as asked, so the answer's row can show what it answers
		// without finding the call that posted them (it may be on another page
		// of the history, or compacted away).
		"questions":   questionTexts(q),
		"asked_at":    time.UnixMilli(q.AskedAt).UTC().Format(time.RFC3339),
		"answered_at": time.UnixMilli(q.ClosedAt).UTC().Format(time.RFC3339),
		"autorun":     run,
		// The loop saves the session before the model reads this message: the
		// saved transcript is the admission's receipt (see core.CustomDurableAdmission).
		core.CustomDurableAdmission: true,
	}
	if steer {
		custom["steer"] = true
	}
	return custom
}

// questionTexts are the literal texts of q's questions, in order.
func questionTexts(q tasks.Question) []string {
	out := make([]string, len(q.Questions))
	for i, item := range q.Questions {
		out[i] = item.Text
	}
	return out
}

// isAdmissionReceipt reports whether a message or steer is an event whose
// presence in a transcript proves the dispatcher already admitted it: a task
// notice or an answer to a question.
func isAdmissionReceipt(custom map[string]any) bool {
	return isTaskNotice(custom) || custom["source"] == "question_answer"
}

// newInvocationID names a post that arrived with no tool call identity (a
// caller outside the agent loop): it is never matched by a retry.
func newInvocationID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "inv_" + hex.EncodeToString(b[:])
}

// errQuestionsUnavailable: the task database cannot be reached.
var errQuestionsUnavailable = errors.New("questions are unavailable")
