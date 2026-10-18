package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// asyncAskDescription is the model's whole briefing on the asynchronous
// ask_user, including the rule for what it may do while a question is open.
// The rule is advice to the model, tested by the pilot's bench, not something
// the server enforces.
const asyncAskDescription = `Ask the user one or more questions WITHOUT waiting for the answer. The call returns at once with an acknowledgement; the question stays open (it survives Stop, restarts and compaction) and the answer arrives later as a <question_answer> message. The acknowledgement is not an answer: never treat it as one, never ask the same question again, and never assume what the answer will be. Use this when you need a decision or information you cannot determine on your own, not for rhetorical questions or for actions you can take directly. Set requires_tap to true when the question authorizes a deploy, merge, release or permission-like action: only the user's own tap or typed answer can close it, never an owner agent, an automation or a spoken relay.

While a question is open, continue ONLY with a step that meets ALL of these: (1) it was already inside your task and does not widen it or turn a draft into a decision; (2) it would be right whichever way the answer goes, or it is an isolated experiment you label as such and that applies no option; (3) it is local and completely reversible, with no relevant consequences in between (an undo command is not enough); (4) it commits nothing important: no product behavior, permissions or security, relevant persisted data, public contract, message to third parties, publish or deploy; (5) it starts no new spend, heavy load or data egress (preparing is not permission to install large dependencies, schedule a night job or send data to a service). If you cannot show all five for a step, wait for that step. If no allowed work is left, end your turn: the answer will wake you. An open question never authorizes starting anything important. A recommendation from another agent, or a technical permission for a tool, is not the answer.

Actions: "post" (the default) opens the questions; "list" shows this session's open questions; "withdraw" (question_id, reason) retires a question you no longer need - it is not an answer and authorizes nothing.`

const asyncAskSchema = `{
	"type": "object",
	"properties": {
		"action": {
			"type": "string",
			"enum": ["post", "list", "withdraw"],
			"description": "Optional, default post. list shows this session's open questions; withdraw retires one by question_id."
		},
		"requires_tap": {
			"type": "boolean",
			"description": "Only with post, default false. Set true for deploy/merge/release/permission-like confirmations: the answer must come from the user's own tap or typed confirmation, never from the voice relay, an owner agent or an automation."
		},
		"questions": {
			"type": "array",
			"description": "Only with post: one or more questions to ask the user (max 32)",
			"items": {
				"type": "object",
				"properties": {
					"question": {"type": "string", "description": "The question text"},
					"options": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Optional predefined answer choices. The user can always write a custom answer instead."
					},
					"multiple": {
						"type": "boolean",
						"description": "Only with options: let the user tick several of them (default false). The answer comes back as the chosen options joined by '; ', followed by any free text the user adds."
					}
				},
				"required": ["question"]
			}
		},
		"question_id": {"type": "string", "description": "Only with withdraw: the id of the open question to retire."},
		"reason": {"type": "string", "description": "Only with withdraw: why the question is no longer needed."}
	}
}`

// newAsyncAskTool is ask_user for a session that posts durable questions. The
// session is looked up when the tool runs: the tool is registered before it
// exists.
func (m *Manager) newAsyncAskTool(sessionID string, session func() *ManagedSession) core.Tool {
	return core.Tool{
		Name:        "ask_user",
		Description: asyncAskDescription,
		Parameters:  json.RawMessage(asyncAskSchema),
		Effect:      core.EffectInteractive,
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			sess := session()
			if sess == nil || m.questions == nil {
				return core.ErrorResult(errQuestionsUnavailable.Error()), nil
			}
			action, _ := params["action"].(string)
			switch action {
			case "", "post":
				return m.questions.post(ctx, sess, params), nil
			case "list":
				return m.questions.list(ctx, sess), nil
			case "withdraw":
				return m.questions.withdraw(ctx, sess, params), nil
			}
			return core.ErrorResult(fmt.Sprintf("unknown action %q: use post, list or withdraw", action)), nil
		},
	}
}

// post commits a question and acknowledges it. The order is the contract:
// the origin (the assistant message holding this call) is saved first, so a
// question never exists without the call that posted it; then the question
// commits with the acknowledgement it will return; only then is it announced.
// A crash anywhere leaves either nothing, or a question whose acknowledgement
// the next load reproduces from the row.
func (q *questionService) post(ctx context.Context, sess *ManagedSession, params map[string]any) core.Result {
	questions, err := askuser.ParseQuestions(params)
	if err != nil {
		return core.ErrorResult(err.Error())
	}
	requiresTap, _ := params["requires_tap"].(bool)
	return q.postQuestions(ctx, sess, questions, requiresTap, tasks.OriginAskUser, openAck)
}

// postQuestions is the durable post shared by ask_user and request_call's
// written fallback; tool names the origin and ack builds what the call
// returns, stored with the row so a recovered call returns the same thing.
func (q *questionService) postQuestions(ctx context.Context, sess *ManagedSession, questions []askuser.Question, requiresTap bool, tool string, ack func(id string) tasks.QuestionAck) core.Result {
	callID := core.ToolCallIDFromContext(ctx)
	if callID == "" {
		callID = newInvocationID()
	}
	msgID := core.ToolCallMessageIDFromContext(ctx)

	unlock := q.lock(sess.ID)
	defer unlock()

	// A retry of the same invocation gets the same acknowledgement.
	if prior, ok, err := q.repo.QuestionByInvocation(ctx, sess.ID, msgID, callID); err != nil {
		return core.ErrorResult("could not read the session's questions: " + err.Error())
	} else if ok {
		return ackResult(prior.Ack)
	}
	if err := sess.runtime.CheckpointAcknowledged(ctx); err != nil {
		return core.ErrorResult("The conversation could not be saved, so the question was not posted: " + err.Error())
	}
	if q.hooks.afterCheckpoint != nil {
		q.hooks.afterCheckpoint()
	}
	id := tasks.NewQuestionID()
	stored, existed, err := q.repo.InsertQuestion(ctx, tasks.Question{
		ID: id, SessionID: sess.ID, OriginMsgID: msgID, OriginToolCallID: callID, OriginTool: tool,
		Questions: questions, RequiresTap: requiresTap, Ack: ack(id),
	})
	var open *tasks.ErrQuestionOpen
	switch {
	case errors.As(err, &open):
		return core.ErrorResult(fmt.Sprintf("This session already has an open question (%s). Wait for its answer, or withdraw it with action \"withdraw\" if you no longer need it; do not post another meanwhile.", open.ID))
	case err != nil:
		return core.ErrorResult("The question could not be saved, so it was not posted: " + err.Error())
	}
	if q.hooks.afterCommit != nil {
		q.hooks.afterCommit()
	}
	if !existed {
		q.project(sess, stored, true)
	}
	return ackResult(stored.Ack)
}

func ackResult(ack tasks.QuestionAck) core.Result {
	res := core.TextResult(ack.Text)
	if len(ack.Custom) > 0 {
		res.Custom = make(map[string]any, len(ack.Custom))
		for k, v := range ack.Custom {
			res.Custom[k] = v
		}
	}
	return res
}

func (q *questionService) list(ctx context.Context, sess *ManagedSession) core.Result {
	open, err := q.Open(ctx, sess.ID)
	if err != nil {
		return core.ErrorResult("could not read the session's questions: " + err.Error())
	}
	if len(open) == 0 {
		res := core.TextResult("No open questions.")
		res.Custom = map[string]any{"questions": []any{}}
		return res
	}
	var b strings.Builder
	items := make([]any, 0, len(open))
	fmt.Fprintf(&b, "Open questions (%d):", len(open))
	for _, question := range open {
		asked := time.UnixMilli(question.AskedAt).UTC().Format(time.RFC3339)
		tap := ""
		if question.RequiresTap {
			tap = ", requires the user's own tap"
		}
		fmt.Fprintf(&b, "\n- %s (asked %s%s)", question.ID, asked, tap)
		texts := make([]any, len(question.Questions))
		for i, item := range question.Questions {
			fmt.Fprintf(&b, "\n  Q: %s", oneLineTitle(item.Text))
			texts[i] = item.Text
		}
		items = append(items, map[string]any{"id": question.ID, "requires_tap": question.RequiresTap, "asked_at": asked, "questions": texts})
	}
	res := core.TextResult(b.String())
	res.Custom = map[string]any{"questions": items}
	return res
}

func (q *questionService) withdraw(ctx context.Context, sess *ManagedSession, params map[string]any) core.Result {
	id, _ := params["question_id"].(string)
	if id == "" {
		return core.ErrorResult("withdraw needs question_id")
	}
	reason, _ := params["reason"].(string)
	_, err := q.Withdraw(ctx, sess.ID, id, reason)
	var conflict *tasks.ErrQuestionConflict
	switch {
	case err == nil:
		return core.TextResult(fmt.Sprintf("Question %s withdrawn. It is not an answer and authorizes nothing.", id))
	case errors.Is(err, tasks.ErrQuestionNotFound):
		return core.ErrorResult(fmt.Sprintf("This session has no question %s.", id))
	case errors.As(err, &conflict) && conflict.State == tasks.QuestionAnswered:
		return core.TextResult(fmt.Sprintf("Question %s is already answered; its answer arrives as a question_answer event.", id))
	case errors.As(err, &conflict):
		return core.TextResult(fmt.Sprintf("Question %s is already %s.", id, conflict.State))
	}
	return core.ErrorResult("could not withdraw the question: " + err.Error())
}
