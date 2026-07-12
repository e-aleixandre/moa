package serve

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ealeixandre/moa/pkg/bus"
)

func requirePulseOwner(w http.ResponseWriter, r *http.Request) (authIdentity, bool) {
	identity, ok := requestAuthIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return authIdentity{}, false
	}
	return identity, true
}

func handlePulsePrepareInstruction(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseOwner(w, r)
		if !ok {
			return
		}
		var body struct {
			Target    string `json:"target"`
			Text      string `json:"text"`
			RequestID string `json:"request_id"`
		}
		if !decodeInstructionBody(w, r, &body) || !validInstructionBody(w, &body.Text, &body.RequestID) {
			return
		}
		body.Target = strings.TrimSpace(body.Target)
		if body.Target == "" || len(body.Target) > 256 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target is required"})
			return
		}
		candidate, sess, candidates, err := m.resolvePulseInstructionTarget(body.Target)
		if len(candidates) != 0 {
			writeJSON(w, http.StatusConflict, struct {
				Candidates []opsInstructionTarget `json:"candidates"`
			}{Candidates: candidates})
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "target not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operations unavailable"})
			return
		}
		current := string(sess.runtime.State.Current())
		if current != string(bus.StateIdle) && current != string(bus.StateError) && current != string(bus.StateRunning) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "session cannot accept a directed instruction in its current state"})
			return
		}
		operationID, err := newDeviceID()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to prepare operation"})
			return
		}
		target := pulseOperationTarget{ID: candidate.ID, Title: candidate.Title, Project: candidate.CanonicalCWD}
		op := &pulseOperation{durablePulseOperation: durablePulseOperation{
			ID:              operationID,
			Kind:            pulseOperationInstruction,
			RequestID:       body.RequestID,
			Fingerprint:     m.pulseFingerprint(pulseOperationInstruction, identity.auditID(), candidate.ID, body.Text, body.RequestID, current),
			PreparedBy:      identity.auditID(),
			Target:          target,
			CurrentState:    current,
			Scope:           "one directed instruction to this exact session",
			Risk:            "medium",
			Review:          fmt.Sprintf("Deliver this instruction to %q: %s", target.Title, body.Text),
			InstructionText: body.Text,
		}}
		view, err := m.preparePulseOperation(op)
		writePulseOperationResult(w, view, err, http.StatusCreated)
	}
}

func handlePulsePreparePermission(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseOwner(w, r)
		if !ok {
			return
		}
		var body struct {
			SessionID    string `json:"session_id"`
			PermissionID string `json:"permission_id"`
			Decision     string `json:"decision"`
			RequestID    string `json:"request_id"`
		}
		if !decodeInstructionBody(w, r, &body) {
			return
		}
		body.SessionID = strings.TrimSpace(body.SessionID)
		body.PermissionID = strings.TrimSpace(body.PermissionID)
		body.Decision = strings.TrimSpace(body.Decision)
		if body.SessionID == "" || body.PermissionID == "" || !validInstructionID(body.RequestID) || (body.Decision != "approve" && body.Decision != "deny") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid permission operation"})
			return
		}
		sess, ok := m.Get(body.SessionID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		current := string(sess.runtime.State.Current())
		if current != string(bus.StatePermission) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "session is not waiting for this permission"})
			return
		}
		pending, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{})
		if err != nil || pending.Permission == nil || pending.Permission.ID != body.PermissionID {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "permission request is not pending"})
			return
		}
		toolName := safePulseToolName(pending.Permission.ToolName)
		target := pulseOperationTarget{ID: sess.ID, Title: sess.title(), Project: sess.CWD}
		verb := "Approve"
		if body.Decision == "deny" {
			verb = "Deny"
		}
		operationID, err := newDeviceID()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to prepare operation"})
			return
		}
		op := &pulseOperation{durablePulseOperation: durablePulseOperation{
			ID:           operationID,
			Kind:         pulseOperationPermission,
			RequestID:    body.RequestID,
			Fingerprint:  m.pulseFingerprint(pulseOperationPermission, identity.auditID(), sess.ID, body.PermissionID, body.Decision, body.RequestID, current),
			PreparedBy:   identity.auditID(),
			Target:       target,
			CurrentState: current,
			Scope:        "one pending permission request only; no allow pattern or rule will be added",
			Risk:         "high",
			Review:       fmt.Sprintf("%s once permission request %s for the %s tool in %q. This does not add an allow pattern or rule.", verb, body.PermissionID, toolName, target.Title),
			PermissionID: body.PermissionID,
			Decision:     body.Decision,
			ToolName:     toolName,
		}}
		view, err := m.preparePulseOperation(op)
		writePulseOperationResult(w, view, err, http.StatusCreated)
	}
}

func handlePulseConfirmOperation(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requirePulseOwner(w, r); !ok {
			return
		}
		var body struct{}
		if !decodeInstructionBody(w, r, &body) {
			return
		}
		id := r.PathValue("id")
		if !validInstructionID(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid operation id"})
			return
		}
		view, err := m.confirmPulseOperation(id)
		writePulseOperationResult(w, view, err, http.StatusOK)
	}
}

func handlePulseGetOperation(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requirePulseOwner(w, r); !ok {
			return
		}
		id := r.PathValue("id")
		if !validInstructionID(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid operation id"})
			return
		}
		view, err := m.pulseOperation(id)
		writePulseOperationResult(w, view, err, http.StatusOK)
	}
}

func writePulseOperationResult(w http.ResponseWriter, view pulseOperationView, err error, success int) {
	switch {
	case err == nil:
		writeJSON(w, success, view)
	case errors.Is(err, errPulseOperationsUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operations unavailable"})
	case errors.Is(err, errPulseOperationNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "operation not found"})
	case errors.Is(err, errPulseOperationConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "request_id conflicts with an existing operation"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to persist operation"})
	}
}
