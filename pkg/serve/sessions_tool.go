package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/book"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
)

// SessionsToolName is the owner's handle on the sessions of its codebase.
const SessionsToolName = "sessions"

const (
	// maxSessionsListed bounds the roster an owner reads in one call.
	maxSessionsListed = 40
	// maxStraySessionsListed bounds the sessions of other codebases named at
	// the end of that roster: it is a warning, not a second roster.
	maxStraySessionsListed = 5
	// defaultSessionReadLimit / maxSessionReadLimit bound `read`: an owner
	// works from reports, and pulling a whole transcript into its context is
	// exactly what reports exist to avoid.
	defaultSessionReadLimit = 20
	maxSessionReadLimit     = 60
	// maxReadTextBytes truncates each message of a `read`.
	maxReadTextBytes = 1500
)

// newSessionsTool builds the owner's sessions tool over a live Manager. It
// lives in pkg/serve rather than a tool package because it needs the Manager
// itself (the precedent is send_file, built with the session's stores).
//
// Every action is scoped to the owner's codebase: a target whose cwd resolves
// to another key is refused rather than silently ignored, so an owner can
// never reach into a project that is not its own.
func newSessionsTool(mgr *Manager, codebaseKey string) core.Tool {
	// The owner is re-read on every call rather than captured: its session ID
	// is written just after this session is built, and answer_asks can be
	// turned off while the conversation is open. A captured copy would answer
	// with the state the process started with.
	load := func() (owner.Owner, error) {
		store, err := mgr.ownerStore()
		if err != nil {
			return owner.Owner{}, err
		}
		own, found, err := store.FindByCodebase(codebaseKey)
		if err != nil {
			return owner.Owner{}, err
		}
		if !found {
			return owner.Owner{}, owner.ErrNotFound
		}
		return own, nil
	}
	return core.Tool{
		Name:  SessionsToolName,
		Label: "Sessions",
		Description: "See and direct the moa sessions working on this project. list: who is " +
			"working, on what and what they are waiting for. read: the last messages of one " +
			"session. send: a message or a correction to a session. new: start a session in a " +
			"directory of this project with a prompt. answer: answer a question a session asked " +
			"the user, when the book already answers it. You cannot approve permissions: those " +
			"are the user's.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["list", "read", "send", "new", "answer"],
					"description": "list, read, send, new or answer."
				},
				"session_id": {"type": "string", "description": "Target session (read, send, answer)."},
				"text": {"type": "string", "description": "Message to send (send), or the prompt for a new session (new)."},
				"limit": {"type": "integer", "description": "For read: how many recent messages (default 20, max 60)."},
				"cwd": {"type": "string", "description": "For new: the directory to work in. Must belong to this project."},
				"title": {"type": "string", "description": "For new: an explicit title."},
				"model": {"type": "string", "description": "For new: model spec (default: the usual default)."},
				"thinking": {"type": "string", "description": "For new: thinking level."},
				"ask_id": {"type": "string", "description": "For answer: the id of the question, as reported."},
				"answers": {
					"type": "array",
					"items": {"type": "string"},
					"description": "For answer: one answer per question asked, in order."
				}
			},
			"required": ["action"]
		}`),
		Effect: core.EffectShell,
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			own, err := load()
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("cannot read this project's owner: %v", err)), nil
			}
			switch action, _ := params["action"].(string); action {
			case "list":
				return mgr.ownerListSessions(own), nil
			case "read":
				return mgr.ownerReadSession(own, getStr(params, "session_id"), getNum(params, "limit")), nil
			case "send":
				return mgr.ownerSendToSession(own, getStr(params, "session_id"), getStr(params, "text")), nil
			case "new":
				return mgr.ownerNewSession(own, params), nil
			case "answer":
				return mgr.ownerAnswerAsk(own, getStr(params, "session_id"), getStr(params, "ask_id"), getStrings(params, "answers")), nil
			default:
				return core.ErrorResult(fmt.Sprintf("unknown action %q", getStr(params, "action"))), nil
			}
		},
	}
}

func getStr(params map[string]any, key string) string {
	s, _ := params[key].(string)
	return strings.TrimSpace(s)
}

func getNum(params map[string]any, key string) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func getStrings(params map[string]any, key string) []string {
	raw, ok := params[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// inCodebase reports whether a directory belongs to the owner's codebase. It
// is the single authorization check of this tool: everything an owner can
// reach is reached through a cwd.
func inCodebase(own owner.Owner, cwd string) bool {
	if cwd == "" {
		return false
	}
	canonical, err := core.CanonicalizePath(cwd)
	if err != nil {
		canonical = cwd
	}
	return core.CodebaseKey(canonical) == own.CodebaseKey
}

// ownerSession resolves a target session and verifies it belongs to the
// owner's codebase and is not the owner's own conversation.
func (m *Manager) ownerSession(own owner.Owner, id string) (SessionInfo, error) {
	if id == "" {
		return SessionInfo{}, errors.New("session_id is required")
	}
	if id == own.SessionID {
		return SessionInfo{}, errors.New("that is your own conversation")
	}
	for _, info := range m.ListWith(ListOptions{IncludeOwners: true}) {
		if info.ID != id {
			continue
		}
		if err := ownerTargetError(own, info.ID, info.Kind, info.CWD, info.ownerDetached); err != nil {
			return SessionInfo{}, err
		}
		return info, nil
	}
	return SessionInfo{}, fmt.Errorf("session %s not found", id)
}

func ownerTargetError(own owner.Owner, id, kind, cwd string, detached bool) error {
	if id == own.SessionID {
		return errors.New("that is your own conversation")
	}
	if kind == session.KindOwner || !inCodebase(own, cwd) {
		return fmt.Errorf("session %s does not belong to this project", id)
	}
	if detached {
		return fmt.Errorf("the user detached session %s from you: you cannot read, direct or answer it", id)
	}
	return nil
}

// ownerListSessions is the roster the owner's balance is built on, so it
// prints the times that balance is asked for: how long ago each session moved,
// and — when one is blocked — how long it has been waiting. A truncated list
// says so: "nothing else is open" and "the other 60 are not shown" are
// different states of the project.
func (m *Manager) ownerListSessions(own owner.Owner) core.Result {
	var mine, strays []SessionInfo
	for _, info := range m.List() {
		if inCodebase(own, info.CWD) {
			mine = append(mine, info)
			continue
		}
		// A session with no cwd has no codebase to warn about, and a saved
		// one is not running: the warning is about work happening now that
		// reports to nobody. A detached one has an owner; the user chose to
		// keep it apart, so it is nobody's business to flag.
		if info.CWD != "" && info.State != StateSaved && info.OwnerID == "" && info.DetachedOwnerID == "" {
			strays = append(strays, info)
		}
	}
	now := time.Now()
	if len(mine) == 0 {
		return core.TextResult("No sessions are open on this project." + strayNotice(strays, now))
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Updated.After(mine[j].Updated) })
	total := len(mine)
	if len(mine) > maxSessionsListed {
		mine = mine[:maxSessionsListed]
	}
	var sb strings.Builder
	if total > len(mine) {
		fmt.Fprintf(&sb, "Showing %d of %d sessions, most recently updated first.\n", len(mine), total)
	}
	for _, info := range mine {
		// That it exists is all a detached session tells its owner: no title,
		// state, brief or pending question, which are its content.
		if info.ownerDetached {
			fmt.Fprintf(&sb, "- %s detached by the user\n", info.ID)
			continue
		}
		fmt.Fprintf(&sb, "- %s [%s] %s", info.ID, info.State, info.Title)
		if info.CWD != own.Root {
			fmt.Fprintf(&sb, " (%s)", info.CWD)
		}
		if !info.Updated.IsZero() {
			fmt.Fprintf(&sb, " — updated %s", book.RelativeAge(now.Sub(info.Updated)))
		}
		sb.WriteString("\n")
		if info.BriefAttempting != "" {
			fmt.Fprintf(&sb, "    attempting: %s\n", info.BriefAttempting)
		}
		if info.BriefProgress != "" {
			fmt.Fprintf(&sb, "    progress: %s\n", info.BriefProgress)
		}
		if pending := m.ownerPendingLine(info.ID); pending != "" {
			fmt.Fprintf(&sb, "    %s", pending)
			if !info.PendingSince.IsZero() {
				fmt.Fprintf(&sb, " — waiting since %s", book.RelativeAge(now.Sub(info.PendingSince)))
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString(strayNotice(strays, now))
	return core.TextResult(sb.String())
}

// strayNotice names live sessions of other codebases that no owner is
// watching. A user's project can span several repositories — a backend, a web
// client, an agent service — while an owner is one git repository, so a
// session started in one of the others reports to nobody and disappears in
// silence. There is no notion of sibling repository in the code and none is
// invented here: the roster only reports what it sees, and whether a stray
// belongs to this project is the owner's judgement from its cwd. Sessions
// another owner already watches are not strays.
func strayNotice(strays []SessionInfo, now time.Time) string {
	if len(strays) == 0 {
		return ""
	}
	sort.Slice(strays, func(i, j int) bool { return strays[i].Updated.After(strays[j].Updated) })
	total := len(strays)
	if len(strays) > maxStraySessionsListed {
		strays = strays[:maxStraySessionsListed]
	}
	var sb strings.Builder
	sb.WriteString("\nOpen elsewhere, in codebases with no owner")
	if total > len(strays) {
		fmt.Fprintf(&sb, " (%d of %d)", len(strays), total)
	}
	sb.WriteString(":\n")
	for _, info := range strays {
		// The directory, not the title: the cwd is what the owner judges
		// with, and a title is another project's content.
		fmt.Fprintf(&sb, "- %s [%s] %s", info.ID, info.State, info.CWD)
		if !info.Updated.IsZero() {
			fmt.Fprintf(&sb, " — updated %s", book.RelativeAge(now.Sub(info.Updated)))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("These are not yours: you cannot read them, direct them or answer them. " +
		"If one of them looks like work on this project in another repository, say so to " +
		"the user in your balance.\n")
	return sb.String()
}

// ownerPendingLine describes what a live session is blocked on. A permission
// is reported but never actionable here: the owner has to know a session is
// stuck without being able to unstick it in the user's name.
func (m *Manager) ownerPendingLine(id string) string {
	sess, ok := m.Get(id)
	if !ok {
		return ""
	}
	pending, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{})
	switch {
	case pending.Ask != nil:
		questions := make([]string, 0, len(pending.Ask.Questions))
		for _, q := range pending.Ask.Questions {
			questions = append(questions, q.Text)
		}
		return fmt.Sprintf("asking (ask_id %s): %s", pending.Ask.ID, strings.Join(questions, " | "))
	case pending.Permission != nil:
		return fmt.Sprintf("waiting for the user to approve %s", pending.Permission.ToolName)
	}
	return ""
}

func (m *Manager) ownerReadSession(own owner.Owner, id string, limit int) core.Result {
	if _, err := m.ownerSession(own, id); err != nil {
		return core.ErrorResult(err.Error())
	}
	if limit <= 0 {
		limit = defaultSessionReadLimit
	}
	if limit > maxSessionReadLimit {
		limit = maxSessionReadLimit
	}
	snapshot, err := m.conversationSnapshot(id)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot read session %s: %v", id, err))
	}
	// Only the conversation itself: an owner reads what was said, not every
	// tool call made on the way.
	var talk []ConversationMessage
	for _, msg := range snapshot.messages {
		if msg.Role == "user" || msg.Role == "assistant" {
			talk = append(talk, msg)
		}
	}
	if len(talk) > limit {
		talk = talk[len(talk)-limit:]
	}
	if len(talk) == 0 {
		return core.TextResult("That session has said nothing yet.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s — %s\n\n", snapshot.id, snapshot.title)
	for _, msg := range talk {
		fmt.Fprintf(&sb, "%s: %s\n\n", msg.Role, truncateText(msg.Text, maxReadTextBytes))
	}
	return core.TextResult(sb.String())
}

func (m *Manager) ownerSendToSession(own owner.Owner, id, text string) core.Result {
	if text == "" {
		return core.ErrorResult("text is required")
	}
	if _, err := m.ownerSession(own, id); err != nil {
		return core.ErrorResult(err.Error())
	}
	if _, ok := m.Get(id); !ok {
		if _, err := m.resumeSessionValidated(id, 0, func(saved *session.Session) error {
			_, cwd, _, _ := saved.RuntimeMeta()
			if cwd == "" {
				cwd = m.workspaceRoot
			}
			return ownerTargetError(own, saved.ID, saved.Kind(), cwd, saved.OwnerDetached())
		}); err != nil {
			// A concurrent resume may have completed between Get and
			// resumeSessionValidated. In that case the target is ready despite
			// ErrBusy and sendValidated rechecks that exact runtime below.
			if _, loaded := m.Get(id); !errors.Is(err, ErrBusy) || !loaded {
				return core.ErrorResult(fmt.Sprintf("cannot reopen session %s: %v", id, err))
			}
		}
	}
	action, msgID, _, err := m.sendValidated(id, text, nil, "", "", ownerPromptCustom(own), func(sess *ManagedSession) error {
		return ownerTargetError(own, sess.ID, sess.Kind, sess.CWD, sess.ownerDetached.Load())
	})
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot send to %s: %v", id, err))
	}
	if action == "steer" {
		return core.TextResult(fmt.Sprintf("Queued for %s (it is working; it will read this at its next step).", id))
	}
	return core.TextResult(fmt.Sprintf("Sent to %s (%s).", id, msgID))
}

func (m *Manager) ownerNewSession(own owner.Owner, params map[string]any) core.Result {
	cwd := getStr(params, "cwd")
	if cwd == "" {
		cwd = own.Root
	}
	if !inCodebase(own, cwd) {
		return core.ErrorResult(fmt.Sprintf("%s does not belong to this project", cwd))
	}
	prompt := getStr(params, "text")
	if prompt == "" {
		return core.ErrorResult("text is required: a new session needs its first instruction")
	}
	sess, err := m.CreateSession(CreateOpts{
		CWD:      cwd,
		Title:    getStr(params, "title"),
		Model:    getStr(params, "model"),
		Thinking: getStr(params, "thinking"),
		Origin:   "owner",
	})
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot create the session: %v", err))
	}
	if _, _, _, err := m.send(sess.ID, prompt, nil, "", "", ownerPromptCustom(own)); err != nil {
		return core.ErrorResult(fmt.Sprintf("session %s was created but the prompt was refused: %v", sess.ID, err))
	}
	return core.TextResult(fmt.Sprintf("Started session %s in %s.", sess.ID, cwd))
}

func ownerPromptCustom(own owner.Owner) map[string]any {
	return map[string]any{"source": "owner", "owner_id": own.ID, "owner_name": own.Name}
}

// ownerAnswerAsk answers a child's ask_user. It is gated on the owner's
// answer_asks flag, and never touches permissions: answering a question the
// book already answers is delegation, approving an action is not.
func (m *Manager) ownerAnswerAsk(own owner.Owner, id, askID string, answers []string) core.Result {
	if !own.AnswerAsks {
		return core.ErrorResult("you are not allowed to answer questions for the user on this project")
	}
	if askID == "" {
		return core.ErrorResult("ask_id is required")
	}
	if _, err := m.ownerSession(own, id); err != nil {
		return core.ErrorResult(err.Error())
	}
	sess, ok := m.Get(id)
	if !ok {
		return core.ErrorResult(fmt.Sprintf("session %s is not loaded; it cannot be waiting on a question", id))
	}
	if sess.Origin != "owner" {
		return core.ErrorResult("This session is the user's: do not answer for them. Ask the user with ask_user, proposing the answer you would give as the first option.")
	}
	if err := sess.runtime.Bus.Execute(bus.ResolveAskUser{AskID: askID, Answers: answers}); err != nil {
		// The user may have answered it in the UI first; the approval manager
		// forgets a resolved ask, so this is the same error as an unknown one.
		return core.ErrorResult(fmt.Sprintf("cannot answer %s: %v (it may already have been answered)", askID, err))
	}
	return core.TextResult(fmt.Sprintf("Answered %s in session %s.", askID, id))
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
