package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

var requestCallDescription = "Ask to talk to the user by voice.\n\n" + owner.CallCriterion + "\n\n" +
	"It rings once. If the user answers, you are told they are on a call with you: end your turn; what you agree arrives as call minutes and relayed tasks. " +
	"If no call happens (not answered, they prefer writing, calls are off, paused or outside hours, or the plan fails), written_fallback is asked in writing instead and you get the answer. Do not call again about the same thing."

// newRequestCallTool is the owner's request_call. bridge is the owner
// session's ask_user bridge: the written fallback is an ordinary question.
func (m *Manager) newRequestCallTool(sessionID string, bridge *askuser.Bridge) core.Tool {
	return core.Tool{
		Name:        "request_call",
		Description: requestCallDescription,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"reason": {"type": "string", "description": "Why you need to talk, in one line the user sees before answering (e.g. \"OK to deploy 0.47; it interrupts 6 sessions\")."},
				"urgency": {"type": "string", "enum": ["normal", "urgent"], "description": "urgent only for a real time window."},
				"written_fallback": {
					"type": "object",
					"description": "The question asked in writing when no call happens.",
					"properties": {
						"question": {"type": "string"},
						"options": {"type": "array", "items": {"type": "string"}},
						"requires_tap": {"type": "boolean", "description": "true when the answer approves a deployment, a release, a merge or a permission: it then needs the user's tap or typed answer, never a voice relay. Same rule as ask_user."}
					},
					"required": ["question"]
				}
			},
			"required": ["reason", "written_fallback"]
		}`),
		Effect: core.EffectInteractive,
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			reason, _ := params["reason"].(string)
			reason = strings.Join(strings.Fields(reason), " ") // one line, as announced
			if reason == "" || len(reason) > 1000 || !utf8.ValidString(reason) {
				return core.ErrorResult("reason is required (one line, at most 1000 bytes)"), nil
			}
			urgency, _ := params["urgency"].(string)
			if urgency != "urgent" {
				urgency = "normal"
			}
			fb, _ := params["written_fallback"].(map[string]any)
			if fb == nil {
				return core.ErrorResult("written_fallback is required"), nil
			}
			question, err := askuser.ParseQuestion(fb)
			if err != nil {
				return core.ErrorResult("written_fallback: " + err.Error()), nil
			}
			// An approval asked in writing keeps its tap requirement whatever
			// path led to writing; accepted at either level, never dropped.
			fbTap, _ := fb["requires_tap"].(bool)
			topTap, _ := params["requires_tap"].(bool)
			requiresTap := fbTap || topTap
			own, err := m.callingOwner(sessionID)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			reg := m.callRequests()
			req, why := reg.open(sessionID, own, reason, urgency, question.Text)
			id := ""
			if req != nil {
				id = req.ID
				// Stopping the run withdraws the request at any point from
				// here on, delivery and opening included: a withdrawn
				// request never becomes a call (see committer).
				stop := context.AfterFunc(ctx, func() {
					reg.resolve(id, []string{crRinging, crHeld, crAccepted, crConnecting}, crCancelled, "")
				})
				reg.announce(ctx, req)
				<-req.done
				stop()
				state, w := reg.outcome(req)
				if state == crCancelled {
					return core.ErrorResult("Call request withdrawn: this run was stopped."), nil
				}
				switch state {
				case crAnswered:
					res := core.TextResult("The user answered: they are on a voice call with you now, through the call relay. End your turn. What you agree arrives as call minutes and relayed tasks.")
					res.Custom = map[string]any{"call_request": map[string]any{"id": id, "outcome": crAnswered, "reason": reason}}
					return res, nil
				}
				why = w
			}
			if bridge == nil {
				return core.ErrorResult("No call (" + whyText[why] + ") and this session cannot ask in writing."), nil
			}
			res := askuser.Ask(ctx, bridge, []askuser.Question{question}, requiresTap)
			var b strings.Builder
			b.WriteString("No call: " + whyText[why] + ". Your written_fallback was asked in writing instead.")
			if !res.IsError {
				b.WriteString(" The user's answer:")
			}
			b.WriteString("\n")
			for _, c := range res.Content {
				b.WriteString(c.Text)
			}
			out := core.TextResult(b.String())
			out.IsError = res.IsError
			out.Custom = map[string]any{"call_request": map[string]any{"id": id, "outcome": crWritten, "why": why, "reason": reason}}
			for k, v := range res.Custom {
				out.Custom[k] = v
			}
			return out, nil
		},
	}
}

// handleCallRequests is GET /api/voice/requests.
func handleCallRequests(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"requests": m.callRequests().snapshot()})
	}
}

// handleCallRequestWrite is POST /api/voice/requests/{id}/write: Write it
// instead on a ringing request, Cancel on a held one.
func handleCallRequestWrite(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := m.callRequests().resolve(r.PathValue("id"), []string{crRinging, crHeld, crAccepted}, crWritten, whyDeclined); !ok {
			voiceError(w, http.StatusConflict, map[string]any{"error": "request_not_live"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleCallRequestAccept is POST /api/voice/requests/{id}/accept: Call on a
// held request, before the current call is hung up.
func handleCallRequestAccept(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !m.callRequests().accept(r.PathValue("id")) {
			voiceError(w, http.StatusConflict, map[string]any{"error": "request_not_held"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleCallRequestsWS is GET /api/voice/requests/ws: the live requests, sent
// whole on every change, so every open client can ring.
func handleCallRequestsWS(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, wsAcceptOptions())
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
		lease, err := deviceLeaseForWebSocket(r, func(string) { _ = conn.CloseNow() })
		if err != nil {
			_ = conn.CloseNow()
			return
		}
		var leaseDone <-chan struct{}
		if lease != nil {
			defer lease.release()
			leaseDone = lease.Done()
		}
		ctx := conn.CloseRead(r.Context())
		reg := m.callRequests()
		changes, cancel := reg.subscribe()
		defer cancel()
		send := func() bool {
			return wsWriteJSON(ctx, conn, map[string]any{"type": "call_requests", "requests": reg.snapshot()}) == nil
		}
		if !send() {
			return
		}
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-leaseDone:
				return
			case <-changes:
				if !send() {
					return
				}
			case <-ping.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					return
				}
			}
		}
	}
}
