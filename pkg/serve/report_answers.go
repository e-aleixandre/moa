package serve

// Project owners — what a report says about questions answered after it was
// written.
//
// A report is made when a child's turn ends and read by the owner up to a
// batching window later. A child that asks asynchronously ends its turn saying
// it is waiting; when the owner (or the user) answers inside that window, the
// report still says "waiting on q_…" and "idle … unless someone sends it a
// message", while the answer has already reached the child and woken it. Owners
// took that at face value and sent the answer again, cutting the child's work
// to repeat what it already had. So the answer is looked up as the batch is
// painted, like the linked task notices (currentTaskNotices), and the report
// says it was delivered. A failed read paints the report as it was.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// reportAnswerChars bounds an answer quoted in a turn report: the owner needs
// to recognise it, the child already has it whole.
const reportAnswerChars = 300

// withAnsweredQuestions fills, for each report of the batch, the questions of
// its session answered after the report was written and whether the session is
// working now. A report whose questions cannot be read is left untouched.
func (m *Manager) withAnsweredQuestions(pending []owner.Report) []owner.Report {
	if m.tasks == nil {
		return pending
	}
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()
	out := make([]owner.Report, len(pending))
	for i, rep := range pending {
		out[i] = rep
		answered, err := m.answeredSince(ctx, rep)
		if err != nil {
			slog.Warn("owner reports: answered questions not read for a delivery", "session", rep.SessionID, "error", err)
			continue
		}
		if len(answered) == 0 {
			continue
		}
		out[i].Answered = answered
		out[i].WorkingNow = m.sessionWorking(rep.SessionID)
	}
	return out
}

// answeredSince is what a report did not know when it was written:
//   - for a user-said answer, the question it answers (to say whether the
//     session already has it);
//   - for a turn report, the question it was asking and every question of the
//     session that was open when the turn ended and has been answered since.
func (m *Manager) answeredSince(ctx context.Context, rep owner.Report) ([]owner.ReportAnswer, error) {
	if rep.SessionID == "" || len(rep.RestartTasks) > 0 {
		return nil, nil
	}
	if rep.UserKind != "" {
		if rep.UserKind != owner.UserSaidAnswer || rep.UserRef == "" {
			return nil, nil
		}
		q, err := m.tasks.GetQuestion(ctx, rep.UserRef)
		if err != nil {
			if errors.Is(err, tasks.ErrQuestionNotFound) {
				return nil, nil // a blocking ask is not in the store
			}
			return nil, err
		}
		if q.SessionID != rep.SessionID || q.State != tasks.QuestionAnswered {
			return nil, nil
		}
		return []owner.ReportAnswer{m.reportAnswer(q)}, nil
	}
	pendingID := ""
	if rep.Pending != nil && rep.Pending.Kind == pendingKindQuestion {
		pendingID = rep.Pending.ID
	}
	if pendingID == "" && rep.EndedAtMs == 0 {
		return nil, nil // an outbox from before EndedAtMs: paint it as written
	}
	qs, err := m.tasks.SessionQuestions(ctx, rep.SessionID)
	if err != nil {
		return nil, err
	}
	var out []owner.ReportAnswer
	for _, q := range qs {
		if q.State != tasks.QuestionAnswered {
			continue
		}
		// Open when the text was final, answered after it.
		openAtEnd := rep.EndedAtMs != 0 && q.AskedAt <= rep.EndedAtMs && q.ClosedAt > rep.EndedAtMs
		if q.ID != pendingID && !openAtEnd {
			continue
		}
		out = append(out, m.reportAnswer(q))
	}
	return out, nil
}

func (m *Manager) reportAnswer(q tasks.Question) owner.ReportAnswer {
	text := strings.Join(strings.Fields(strings.Join(q.Answers, " | ")), " ")
	if r := []rune(text); len(r) > reportAnswerChars {
		text = string(r[:reportAnswerChars]) + "…"
	}
	return owner.ReportAnswer{
		AskID:     q.ID,
		By:        responderLabel(q.Responder),
		At:        time.UnixMilli(q.ClosedAt).UTC().Format(time.RFC3339),
		Text:      text,
		Delivered: m.answerDelivered(q),
	}
}

// answerDelivered says the answer's event is in the session's conversation,
// or its run already ended (an unloaded session that read it).
func (m *Manager) answerDelivered(q tasks.Question) bool {
	if q.Work == tasks.WorkDone {
		return true
	}
	sess, ok := m.Get(q.SessionID)
	return ok && q.EventID != "" && m.questions != nil && m.questions.read(sess, q.EventID)
}

// answeredLine is a question answered after its report was written.
func answeredLine(a owner.ReportAnswer) string {
	return "answered (ask_id " + a.AskID + ") by " + a.By + " at " + a.At + ": " + a.Text + " — " + deliveryNote(a)
}

func deliveryNote(a owner.ReportAnswer) string {
	if a.Delivered {
		return "already delivered to the session; do not relay it"
	}
	return "queued for the session, which it wakes; do not relay it"
}

// answeredNowLine replaces the "now:" line of a turn report when an answer
// arrived after it: the session is working on it, or will be woken by it.
// Empty means the line computed for the outcome still holds.
func answeredNowLine(rep owner.Report) string {
	if len(rep.Answered) == 0 {
		return ""
	}
	if rep.WorkingNow {
		return "now: working on that answer — it will report when it stops"
	}
	for _, a := range rep.Answered {
		if !a.Delivered {
			return "now: that answer is on its way to it — it will continue on its own"
		}
	}
	return ""
}

// userAnswerDelivery is the line under a user-said answer: the session
// already has the user's words.
func userAnswerDelivery(rep owner.Report) string {
	if len(rep.Answered) == 0 {
		return ""
	}
	a := rep.Answered[0]
	if !a.Delivered {
		return "queued for the session, which it wakes; do not relay it"
	}
	if rep.WorkingNow {
		return "already delivered to the session at " + a.At + "; it is working on it — do not relay it"
	}
	return "already delivered to the session at " + a.At + "; it already finished with it — do not relay it"
}
