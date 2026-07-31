package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	maxAutomationAnswers        = 32
)

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

// automationSession resolves a session the automation token may interact with.
// Anything else — an unknown ID, or a session a human created — is reported as
// ErrNotFound, the same answer for both, so the token cannot probe which
// sessions exist.
//
// A session that is only on disk (after a restart, say) is resumed, but only
// after its persisted metadata proved it is automation-created: the check comes
// first so the token cannot make the server load a session it may not talk to.
func (m *Manager) automationSession(id string) (*ManagedSession, error) {
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
	sess, err := m.ResumeSession(id)
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

// automationSessionOr404 resolves the session or writes the response for the
// caller. It returns nil when it already answered.
func automationSessionOr404(w http.ResponseWriter, mgr *Manager, id string) *ManagedSession {
	sess, err := mgr.automationSession(id)
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrBusy):
		http.Error(w, "session already resuming", http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		return sess
	}
	return nil
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
		action, steerID, _, err := mgr.Send(sess.ID, body.Text, nil, "")
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, bus.ErrSteerQueueFull):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusAccepted, map[string]any{
				"action":   action,
				"steer_id": steerID,
			})
		}
	}
}

// handleAutomationAskResponse answers a pending ask_user prompt, mirroring the
// browser endpoint's body.
func handleAutomationAskResponse(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := automationSessionOr404(w, mgr, r.PathValue("id"))
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
		if err := sess.runtime.Bus.Execute(bus.ResolveAskUser{
			AskID: body.ID, Answers: body.Answers,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleAutomationPermission approves or denies a pending permission request,
// mirroring the browser endpoint's decision body. Editing permanent permission
// rules (the browser's add_rule action) is not mirrored: that is configuration,
// not an answer to this request.
func handleAutomationPermission(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := automationSessionOr404(w, mgr, r.PathValue("id"))
		if sess == nil {
			return
		}
		var body struct {
			ID       string `json:"id"`
			Approved bool   `json:"approved"`
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
			Approved:     body.Approved,
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
