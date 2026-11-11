package serve

// Project owners — what the user says inside a session the owner launched.
//
// The user can type into any child's chat or answer its ask_user question; the
// owner, who answers for the project, used to learn of it only when the child's
// turn ended, if the report happened to mention it. In a long turn that was
// hours later, and the owner kept acting on a question the user had already
// answered. A user-said entry is an ordinary report: it goes through the same
// outbox, batching window and delivery as a finished turn, so a burst is one
// wake-up and a closed owner is woken the way it is for any report.
//
// Only the human is relayed. The two entry points below are the HTTP handlers
// the web UI and paired apps use (SendHuman, and the ask answer with
// ResponderUser); what an agent sends (the owner's `sessions send`, the
// automation API) and what the owner answers (ResponderOwner) never get here.

import (
	"fmt"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
)

const (
	// userSaidHeadChars / userSaidTailChars abridge a long message: the owner
	// needs the decision, which is usually at one end, not a transcript.
	userSaidHeadChars = 1000
	userSaidTailChars = 500
	// userSaidQuestionChars bounds the question an answer is attached to.
	userSaidQuestionChars = 300
)

// userSaid is what the user told a session, before it becomes a report entry.
type userSaid struct {
	kind     string // owner.UserSaidMessage or owner.UserSaidAnswer
	text     string
	ref      string // message id (a direct send) or ask id
	question string
}

// tellOwnerUserSaid relays what the user said in sessionID to the owner of its
// project, when that session is one the owner launched and still follows. It
// never fails the user's action: a relay that cannot happen is logged.
func (m *Manager) tellOwnerUserSaid(sessionID string, said userSaid) {
	// reports.add waits for the actor, which can be confirming an unrelated
	// delivery for seconds; the user's own request must never wait behind it.
	go m.relayUserSaid(sessionID, said)
}

func (m *Manager) relayUserSaid(sessionID string, said userSaid) {
	reports := m.reports
	if reports == nil {
		return
	}
	var (
		origin, kind, cwd, title string
		detached                 bool
		live                     *ManagedSession
	)
	if sess, ok := m.Get(sessionID); ok {
		live = sess
		origin, kind, cwd, title, detached = sess.Origin, sess.Kind, sess.CWD, sess.title(), sess.ownerDetached.Load()
	} else {
		// An asleep child is answered through the question store without being
		// loaded; its identity is on disk.
		found := false
		for _, sum := range m.cachedSavedSessions() {
			if sum.ID != sessionID {
				continue
			}
			origin, _ = sum.Metadata[session.MetaOrigin].(string)
			kind, _ = sum.Metadata[session.MetaKind].(string)
			cwd, _ = sum.Metadata["cwd"].(string)
			title, detached = sum.Title, session.OwnerDetachedIn(sum.Metadata)
			found = true
			break
		}
		if !found {
			return
		}
	}
	if origin != "owner" || kind == session.KindOwner || detached || cwd == "" {
		return
	}
	own, found, err := reports.store.FindByDir(cwd)
	if err != nil || !found {
		return
	}
	rep := owner.Report{
		ID:           newReportID(),
		SessionID:    sessionID,
		Title:        title,
		CWD:          cwd,
		Origin:       origin,
		Status:       reportStatusUserSaid,
		UserKind:     said.kind,
		UserText:     abridge(strings.TrimSpace(said.text), userSaidHeadChars, userSaidTailChars, userSaidReadHint(sessionID, said)),
		UserRef:      said.ref,
		UserQuestion: said.question,
		At:           time.Now().UTC().Format(time.RFC3339),
	}
	if live != nil {
		admitOwnerReport(reports, live, own.CodebaseKey, rep)
		return
	}
	reports.add(own.CodebaseKey, rep)
}

// userSaidReadHint says how the owner reads the whole thing. A message sent
// straight to an idle session keeps its id; a queued one has only a chip id, so
// the owner reads the session instead.
func userSaidReadHint(sessionID string, said userSaid) string {
	if said.kind == owner.UserSaidMessage && said.ref != "" {
		return fmt.Sprintf("read it whole with the sessions tool: action=read, session_id=%s, message_id=%s", sessionID, said.ref)
	}
	return fmt.Sprintf("read it whole with the sessions tool: action=read, session_id=%s", sessionID)
}

// userSaidMessageText is what a message with attachments says when it has no
// words of its own.
func userSaidMessageText(text string, attachments int) string {
	text = strings.TrimSpace(text)
	switch {
	case attachments == 0:
		return text
	case text == "":
		return fmt.Sprintf("(%d attachment(s), no text)", attachments)
	default:
		return text + fmt.Sprintf("\n(+ %d attachment(s))", attachments)
	}
}

// writeUserSaid renders a user-said entry. It deliberately omits what a turn
// report carries (git position, book delta): nothing finished, the user spoke.
func writeUserSaid(b *strings.Builder, rep owner.Report) {
	if rep.CWD != "" {
		fmt.Fprintf(b, "  directory: %s\n", rep.CWD)
	}
	switch rep.UserKind {
	case owner.UserSaidAnswer:
		question := ""
		if q := strings.TrimSpace(rep.UserQuestion); q != "" {
			// Written by the child, not the user: kept on one line so it cannot
			// pass for an entry of its own.
			question = " (" + strings.Join(strings.Fields(q), " ") + ")"
		}
		fmt.Fprintf(b, "  the user answered ask_id %s%s: %s\n", rep.UserRef, question, indentReportText(rep.UserText))
	default:
		fmt.Fprintf(b, "  the user wrote: %s\n", indentReportText(rep.UserText))
	}
}

// askQuestionText is the question text of an ask id, for the entry an answer
// produces: from the durable store when it is there, else from the live
// session's pending prompt.
func askQuestionText(texts []string) string {
	q := strings.Join(texts, " | ")
	if r := []rune(q); len(r) > userSaidQuestionChars {
		q = string(r[:userSaidQuestionChars]) + "…"
	}
	return q
}
