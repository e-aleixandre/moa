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
}

func newQuestionService(m *Manager) *questionService {
	return &questionService{m: m, repo: m.tasks, locks: map[string]*sync.Mutex{}}
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

// forget drops the lock of a deleted session.
func (q *questionService) forget(sessionID string) {
	q.mu.Lock()
	delete(q.locks, sessionID)
	q.mu.Unlock()
}

// Answer accepts an answer durably (answer and event in one transaction),
// then retires the question's projection and wakes the dispatcher. The same
// answer from the same responder again succeeds without a second event.
func (q *questionService) Answer(ctx context.Context, sessionID, id string, answers []string, responder askuser.Responder) error {
	unlock := q.lock(sessionID)
	defer unlock()
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
// answers already accepted (see tasks.FenceQuestionAnswers). A failure is
// logged and the Stop goes on: it must never be the reason a run cannot be
// stopped.
func (q *questionService) Fence(ctx context.Context, sessionID string, stopRun bool) {
	if q == nil {
		return
	}
	unlock := q.lock(sessionID)
	defer unlock()
	if _, err := q.repo.FenceQuestionAnswers(ctx, sessionID, stopRun); err != nil {
		slog.Warn("questions: fencing the accepted answers failed", "session", sessionID, "error", err)
	}
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
	q.seed(sess)
	sess.runtime.Bus.Subscribe(func(e bus.RunEnded) { q.runEnded(sess.ID, e) })
}

// runEnded settles the continuation an admitted answer owed: the run that read
// it is over, whatever came of it. An answer not yet admitted still owes its
// work, and a Stop already marked its own as stopped.
//
// TODO(phase 2): a restart that finds work still due after the answer was
// admitted should continue the run (ContinueRun); phase 1 only records it.
func (q *questionService) runEnded(sessionID string, e bus.RunEnded) {
	ctx := q.m.baseCtx
	due, err := q.repo.DueQuestionWork(ctx)
	if err != nil {
		slog.Warn("questions: reading owed work failed", "session", sessionID, "error", err)
		return
	}
	for _, question := range due {
		if question.SessionID != sessionID {
			continue
		}
		n, err := q.repo.Notice(ctx, question.EventID)
		if err != nil || n.State != tasks.NoticeDelivered {
			continue
		}
		if _, err := q.repo.SetQuestionWork(ctx, question.EventID, "due", "done"); err != nil {
			slog.Warn("questions: settling owed work failed", "session", sessionID, "error", err)
		}
	}
}

// seed loads the open questions of a session being built or resumed into its
// projection, so a restart keeps them visible without announcing them again.
func (q *questionService) seed(sess *ManagedSession) {
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
		if call.ToolName != "ask_user" && call.ToolName != "request_call" {
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
		"asked_at":          time.UnixMilli(q.AskedAt).UTC().Format(time.RFC3339),
		"answered_at":       time.UnixMilli(q.ClosedAt).UTC().Format(time.RFC3339),
		"autorun":           run,
	}
	if steer {
		custom["steer"] = true
	}
	return custom
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
