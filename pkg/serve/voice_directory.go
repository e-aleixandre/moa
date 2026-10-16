package serve

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
)

// voiceDirectoryEntry is what a car screen shows of an owner: enough to pick
// whom to call and nothing of its configuration or conversation, since the
// locked phone's voice credential may read it.
type voiceDirectoryEntry struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Avatar owner.Avatar `json:"avatar"`
	// State is "open", "closed" (calling reopens it) or "none" (no
	// conversation to call).
	State string `json:"state"`
}

func (r *voiceCallRegistry) handleDirectory(w http.ResponseWriter, _ *http.Request) {
	owners, err := r.mgr.ListOwners()
	if err != nil {
		voiceError(w, http.StatusInternalServerError, map[string]any{"error": "owners_unavailable"})
		return
	}
	out := make([]voiceDirectoryEntry, 0, len(owners))
	for _, own := range owners {
		state := "open"
		switch {
		case own.SessionID == "":
			state = "none"
		case own.SessionState == StateSaved:
			state = "closed"
		}
		out = append(out, voiceDirectoryEntry{ID: own.ID, Name: own.Name, Avatar: own.Avatar, State: state})
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": out})
}

// handleOwnerCall calls an owner by its id, reopening its conversation when
// it is closed: the car's one tap. It never takes a session id, so the voice
// credential can reach owners' conversations and no other.
func (r *voiceCallRegistry) handleOwnerCall(w http.ResponseWriter, req *http.Request) {
	identity, ok := requestAuthIdentity(req)
	if !ok {
		voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
		return
	}
	own, err := r.mgr.GetOwner(req.PathValue("id"))
	if errors.Is(err, owner.ErrNotFound) {
		voiceError(w, http.StatusNotFound, map[string]any{"error": "owner_not_found"})
		return
	}
	if err != nil {
		voiceError(w, http.StatusInternalServerError, map[string]any{"error": "owners_unavailable"})
		return
	}
	if own.SessionID == "" {
		voiceError(w, http.StatusConflict, map[string]any{"error": "owner_has_no_conversation"})
		return
	}
	if _, live := r.mgr.Get(own.SessionID); !live {
		// ErrBusy: another request is reopening it right now.
		if _, err := r.mgr.ResumeSession(own.SessionID); err != nil && !errors.Is(err, ErrBusy) {
			if errors.Is(err, session.ErrNotFound) {
				voiceError(w, http.StatusConflict, map[string]any{"error": "owner_has_no_conversation"})
				return
			}
			voiceError(w, http.StatusInternalServerError, map[string]any{"error": "owner_reopen_failed"})
			return
		}
	}
	status, out, retry := r.openCall(identity.auditID(), own.SessionID, "")
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	voiceError(w, status, out)
}
