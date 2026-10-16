package serve

import (
	"context"
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
	sessionID := own.SessionID
	// As an invitation's answer: the opening and the call live under the
	// device's lease, so revoking the device or withdrawing its VoIP
	// destination aborts the one and ends the other.
	scope, cancel := context.WithCancel(r.ctx)
	lease, err := deviceLeaseForWebSocket(req, func(string) { cancel() })
	if err != nil {
		cancel()
		voiceError(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
		return
	}
	// The owner is reopened only for a call already admitted: a refused one
	// (no plan, rate limited, a call already on) leaves it closed.
	status, out, retry := r.openCallWith(scope, identity.auditID(), "", func() (*ManagedSession, int, map[string]any) {
		if scope.Err() != nil {
			return nil, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"}
		}
		if sess, live := r.mgr.Get(sessionID); live {
			return sess, 0, nil
		}
		_, err := r.mgr.ResumeSession(sessionID)
		switch {
		case errors.Is(err, session.ErrNotFound):
			return nil, http.StatusConflict, map[string]any{"error": "owner_has_no_conversation"}
		case errors.Is(err, ErrBusy):
			// Being reopened or closed by someone else right now: no runtime
			// to call yet, and not missing either.
			if sess, live := r.mgr.Get(sessionID); live {
				return sess, 0, nil
			}
			return nil, http.StatusConflict, map[string]any{"error": "owner_busy", "retryable": true}
		case err != nil:
			return nil, http.StatusInternalServerError, map[string]any{"error": "owner_reopen_failed"}
		}
		if sess, live := r.mgr.Get(sessionID); live {
			return sess, 0, nil
		}
		return nil, http.StatusConflict, map[string]any{"error": "owner_busy", "retryable": true}
	})
	var c *voiceCall
	if id, _ := out["call_id"].(string); status == http.StatusCreated {
		c, _, _ = r.lookup(id)
	}
	if c == nil || lease == nil {
		lease.release()
		cancel()
	} else {
		stop := context.AfterFunc(scope, func() {
			if r.ctx.Err() == nil { // not a server shutdown
				c.requestEnd("device_revoked")
			}
		})
		go func() {
			<-c.done
			stop()
			lease.release()
			cancel()
		}()
	}
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	if status == http.StatusCreated {
		// The phone needs it to hand a call started in the car over to the
		// page that shows that conversation.
		out["session_id"] = sessionID
	}
	voiceError(w, status, out)
}
