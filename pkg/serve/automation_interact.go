package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Per-field byte limits for the interaction endpoints, in the same spirit as
// the per-field limits on runs: the body cap alone would still let a single
// field be absurd.
const (
	maxAutomationRequestIDBytes = 128
	maxAutomationFeedbackBytes  = 4 << 10
	maxAutomationAllowBytes     = 512
	maxAutomationAnswerBytes    = 8 << 10
	// maxAutomationAnswers mirrors the cap ask_user applies when it creates a
	// prompt, so every answerable prompt fits in one request.
	maxAutomationAnswers = askuser.MaxQuestions
)

// maxAutomationLoadedSessions bounds how many sessions may be resident when a
// reply resumes one from disk. Resuming builds a whole runtime (provider, MCP
// servers, watchers), so an automation caller looping over saved session IDs
// would otherwise be an amplification lever. The cap is generous for real use:
// it only refuses to grow the resident set further, never touches sessions
// that are already loaded.
const maxAutomationLoadedSessions = 32

// automationCreatedMeta reports whether session metadata proves the session was
// created by CreateAutomationRun, which is what authorizes the automation token
// to interact with it.
//
// MetaAutomationCreated is the marker: it is written on exactly that one path.
// The origin label is NOT sufficient on its own — CreateOpts.Origin is part of
// the public POST /api/sessions body, so any browser client could claim
// "automation". Sessions created by the Automation API before the marker
// existed are still recognized through the callback URL or the idempotency
// key, both of which are only ever written by CreateAutomationRun (they live in
// the unexported extraMeta, unreachable from the public create body). An older
// automation session that carried neither is indistinguishable from a
// user-created one and is therefore treated as user-created.
func automationCreatedMeta(meta map[string]any) bool {
	if meta == nil {
		return false
	}
	if created, _ := meta[session.MetaAutomationCreated].(bool); created {
		return true
	}
	if url, _ := meta[session.MetaCallbackURL].(string); url != "" {
		return true
	}
	key, _ := meta[session.MetaIdempotencyKey].(string)
	return key != ""
}

// ErrAutomationNotLive reports that the session exists and belongs to the
// automation token, but is not currently loaded — so it cannot have a pending
// interaction to answer.
var ErrAutomationNotLive = errors.New("session is not loaded")

// ErrAutomationTooManySessions reports that resuming a saved session would push
// the resident set past maxAutomationLoadedSessions.
var ErrAutomationTooManySessions = errors.New("too many loaded sessions")

// automationLiveSession resolves a session the automation token may interact
// with WITHOUT ever loading it from disk. An unknown ID, or a session a human
// created, is reported as ErrNotFound — the same answer for both, so the token
// cannot probe which sessions exist. A session that exists on disk and is
// automation-created but is not resident is ErrAutomationNotLive.
//
// The ask/permission endpoints use this: a pending ask_user prompt or
// permission request only lives in the in-memory runtime of a live session (the
// tool call is blocked on a channel), so a saved-only session has nothing to
// answer. Resuming for it would build a full runtime for a request that can
// only fail.
func (m *Manager) automationLiveSession(id string) (*ManagedSession, error) {
	if sess, ok := m.Get(id); ok {
		if !sess.automationCreated {
			return nil, ErrNotFound
		}
		return sess, nil
	}
	saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, id)
	if err != nil || !automationCreatedMeta(saved.Metadata) {
		return nil, ErrNotFound
	}
	return nil, ErrAutomationNotLive
}

// automationSession resolves a session for /reply, which may legitimately talk
// to a saved session (sending a message starts a new run). A session that is
// only on disk is resumed, but only after its persisted metadata proved it is
// automation-created — the check comes first so the token cannot make the
// server load a session it may not talk to — and only while the resident set is
// below maxAutomationLoadedSessions (enforced atomically with the resume
// reservation, so concurrent replies cannot race past the cap).
func (m *Manager) automationSession(id string) (*ManagedSession, error) {
	sess, err := m.automationLiveSession(id)
	if !errors.Is(err, ErrAutomationNotLive) {
		return sess, err
	}
	sess, err = m.resumeSession(id, maxAutomationLoadedSessions)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !sess.automationCreated {
		return nil, ErrNotFound
	}
	return sess, nil
}

// writeAutomationSessionError writes the response for a session-resolution
// failure.
func writeAutomationSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrAutomationNotLive):
		// Same class as answering a request ID that is no longer pending, which
		// already answers 400: there is nothing to answer, and retrying will not
		// change that.
		http.Error(w, "no pending interaction: the session is not running", http.StatusBadRequest)
	case errors.Is(err, ErrAutomationTooManySessions):
		http.Error(w, "too many loaded sessions; retry later", http.StatusServiceUnavailable)
	case errors.Is(err, ErrBusy):
		http.Error(w, "session already resuming", http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// automationSessionOr404 resolves the session for /reply (resuming a saved one
// when needed) or writes the response for the caller. It returns nil when it
// already answered.
func automationSessionOr404(w http.ResponseWriter, mgr *Manager, id string) *ManagedSession {
	sess, err := mgr.automationSession(id)
	if err != nil {
		writeAutomationSessionError(w, err)
		return nil
	}
	return sess
}

// automationLiveSessionOrError resolves a live session for the ask/permission
// endpoints, never resuming, or writes the response itself.
func automationLiveSessionOrError(w http.ResponseWriter, mgr *Manager, id string) *ManagedSession {
	sess, err := mgr.automationLiveSession(id)
	if err != nil {
		writeAutomationSessionError(w, err)
		return nil
	}
	return sess
}

// decodeAutomationBody decodes a capped JSON body, answering 400 itself.
func decodeAutomationBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	limitBody(w, r, maxJSONBodySize)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

// handleAutomationReply sends a user message into an automation session, the
// same way the web client's send does. Attachments are deliberately not
// supported: a machine caller sends text.
func handleAutomationReply(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := automationSessionOr404(w, mgr, r.PathValue("id"))
		if sess == nil {
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if !decodeAutomationBody(w, r, &body) {
			return
		}
		body.Text = strings.TrimSpace(body.Text)
		if body.Text == "" {
			http.Error(w, "text required", http.StatusBadRequest)
			return
		}
		if len(body.Text) > maxAutomationPromptBytes {
			http.Error(w, "text too large", http.StatusBadRequest)
			return
		}
		action, acceptedID, _, err := mgr.Send(sess.ID, body.Text, nil, "", "")
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, bus.ErrSteerQueueFull):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			resp := map[string]any{"action": action}
			// A steer is accepted under a chip ID, a direct send under a message
			// ID — never conflate them (see handleSend).
			if action == "steer" {
				resp["steer_id"] = acceptedID
			} else {
				resp["msg_id"] = acceptedID
			}
			writeJSON(w, http.StatusAccepted, resp)
		}
	}
}

// handleAutomationAskResponse answers a pending ask_user prompt, mirroring the
// browser endpoint's body. A pending prompt only exists on a live session, so
// this never loads one from disk.
func handleAutomationAskResponse(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := automationLiveSessionOrError(w, mgr, r.PathValue("id"))
		if sess == nil {
			return
		}
		var body struct {
			ID      string   `json:"id"`
			Answers []string `json:"answers"`
		}
		if !decodeAutomationBody(w, r, &body) {
			return
		}
		if body.ID == "" {
			http.Error(w, "ask request ID is required", http.StatusBadRequest)
			return
		}
		if msg := validateAutomationInteraction(body.ID, body.Answers); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := sess.runtime.Bus.Execute(bus.ResolveAskUser{AskID: body.ID, Answers: body.Answers}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleAutomationPermission approves or denies a pending permission request,
// mirroring the browser endpoint's decision body. Editing permanent permission
// rules (the browser's add_rule action) is not mirrored: that is configuration,
// not an answer to this request. Like ask-response, it only talks to a live
// session: a pending request cannot exist on one that is merely saved.
func handleAutomationPermission(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := automationLiveSessionOrError(w, mgr, r.PathValue("id"))
		if sess == nil {
			return
		}
		var body struct {
			ID string `json:"id"`
			// Approved is a pointer so an omitted field is an explicit error
			// rather than a silent denial.
			Approved *bool  `json:"approved"`
			Feedback string `json:"feedback"`
			Allow    string `json:"allow"`
		}
		if !decodeAutomationBody(w, r, &body) {
			return
		}
		if body.ID == "" {
			http.Error(w, "permission request ID is required", http.StatusBadRequest)
			return
		}
		if body.Approved == nil {
			http.Error(w, "approved is required (true or false)", http.StatusBadRequest)
			return
		}
		if msg := validateAutomationInteraction(body.ID, nil); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if len(body.Feedback) > maxAutomationFeedbackBytes {
			http.Error(w, fmt.Sprintf("feedback too long (max %d bytes)", maxAutomationFeedbackBytes), http.StatusBadRequest)
			return
		}
		if len(body.Allow) > maxAutomationAllowBytes {
			http.Error(w, fmt.Sprintf("allow too long (max %d bytes)", maxAutomationAllowBytes), http.StatusBadRequest)
			return
		}
		if err := sess.runtime.Bus.Execute(bus.ResolvePermission{
			PermissionID: body.ID,
			Approved:     *body.Approved,
			Feedback:     body.Feedback,
			AllowPattern: body.Allow,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// validateAutomationInteraction applies the per-field limits shared by the
// interaction endpoints. It returns a 400 message, or "" when the request is
// fine.
func validateAutomationInteraction(id string, answers []string) string {
	if len(id) > maxAutomationRequestIDBytes {
		return fmt.Sprintf("id too long (max %d bytes)", maxAutomationRequestIDBytes)
	}
	if len(answers) > maxAutomationAnswers {
		return fmt.Sprintf("too many answers (max %d)", maxAutomationAnswers)
	}
	for _, a := range answers {
		if len(a) > maxAutomationAnswerBytes {
			return fmt.Sprintf("answer too long (max %d bytes)", maxAutomationAnswerBytes)
		}
	}
	return ""
}
