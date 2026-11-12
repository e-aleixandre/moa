package serve

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/e-aleixandre/moa/pkg/secretaction"
)

// maxSecretBody bounds a secret answer: MaxFields values of MaxValueBytes,
// each possibly six times larger once JSON-escaped, plus the envelope.
const maxSecretBody = secretaction.MaxFields*secretaction.MaxValueBytes*6 + 4<<10

// handleSecretRequestResponse answers a run_with_secret/browser_fill_secret
// card: POST /api/sessions/{id}/secret-requests/{reqId} with
// {"action":"allow"|"deny","values":[{"index":0,"value":"…"}]}.
//
// The values never take the path a permission or ask answer takes (a bus
// command, observable by subscribers): they are decoded into []byte buffers
// this handler owns and handed to the session's secretaction.Broker, which
// passes copies to the waiting tool through an in-memory channel. Nothing here
// logs, persists or publishes them, and every buffer is zeroed before return
// (best effort: net/http's own read buffers are out of reach). It sits on the
// same human-client auth as /permission; no tool can reach it.
func handleSecretRequestResponse(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		broker := sess.runtime.Context().SecretBroker
		if broker == nil {
			http.Error(w, "unknown secret request", http.StatusNotFound)
			return
		}

		// One fixed buffer: io.ReadAll would grow by copying, leaving stale
		// copies of the values behind for the collector.
		buf := make([]byte, maxSecretBody+1)
		defer secretaction.Wipe(buf)
		n, err := io.ReadFull(r.Body, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			http.Error(w, "could not read body", http.StatusBadRequest)
			return
		}
		if n > maxSecretBody {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}

		var body struct {
			Action string `json:"action"`
			// HoldHours > 0 remembers the values for that many hours
			// (secretaction.Memory).
			HoldHours *int `json:"holdHours"`
			Values    []struct {
				Index *int            `json:"index"`
				Value json.RawMessage `json:"value"`
			} `json:"values"`
		}
		decodeErr := json.Unmarshal(buf[:n], &body)
		defer func() {
			for _, v := range body.Values {
				secretaction.Wipe(v.Value)
			}
		}()
		if decodeErr != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		reqID := r.PathValue("reqId")
		hold := 0
		if body.HoldHours != nil {
			hold = *body.HoldHours
		}
		// Checked before anything is resolved: a bad hold must not turn into
		// a deny or a single use the owner did not choose.
		if hold != 0 {
			if body.Action != "allow" || hold < 0 || hold > secretaction.MaxHoldHours {
				http.Error(w, "holdHours must be 1 to 10 hours, on allow", http.StatusBadRequest)
				return
			}
			if mgr.secretMemory == nil {
				http.Error(w, "holdHours is not available here", http.StatusBadRequest)
				return
			}
		}
		switch body.Action {
		case "deny":
			writeSecretResolveError(w, broker.Resolve(reqID, false, nil))
			return
		case "allow":
		default:
			http.Error(w, `action must be "allow" or "deny"`, http.StatusBadRequest)
			return
		}
		var kind string
		var fields []secretaction.Field
		eligible := false
		for _, p := range broker.Pending() {
			if p.ID == reqID {
				kind, fields = p.Kind, p.Fields
				eligible = secretaction.Rememberable(p) && p.HoldMaxHours > 0
			}
		}
		if !broker.IsPending(reqID) {
			http.Error(w, "unknown secret request", http.StatusNotFound)
			return
		}

		values := make([][]byte, len(body.Values))
		defer func() {
			for _, v := range values {
				secretaction.Wipe(v)
			}
		}()
		for _, v := range body.Values {
			if v.Index == nil || *v.Index < 0 || *v.Index >= len(values) || values[*v.Index] != nil {
				http.Error(w, "each value needs a distinct index of a field", http.StatusBadRequest)
				return
			}
			decoded, err := secretaction.DecodeJSONString(v.Value)
			if err != nil {
				http.Error(w, "each value must be a JSON string", http.StatusBadRequest)
				return
			}
			values[*v.Index] = decoded
			if kind == secretaction.KindCommand && hasControlByte(decoded) {
				http.Error(w, "values cannot contain line breaks or control characters", http.StatusBadRequest)
				return
			}
		}
		if hold == 0 {
			writeSecretResolveError(w, broker.Resolve(reqID, true, values))
			return
		}
		if !eligible {
			http.Error(w, "this request cannot be remembered", http.StatusBadRequest)
			return
		}
		if err := secretaction.ValidateRemember(secretaction.MemoryKeys(fields), values); err != nil {
			http.Error(w, "these values cannot be remembered: "+err.Error(), http.StatusBadRequest)
			return
		}
		// The epoch is read now, when the owner approves: a Forget pressed
		// before the waiting tool remembers the values wins.
		epoch := mgr.secretMemory.Epoch()
		writeSecretResolveError(w, broker.ResolveHold(reqID, values, time.Duration(hold)*time.Hour, epoch))
	}
}

// handleRememberedSecrets reports what is remembered:
// GET /api/remembered-secrets. Metadata only; no value ever leaves Memory.
func handleRememberedSecrets(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeRememberedSecrets(w, mgr)
	}
}

// handleForgetSecrets forgets: DELETE /api/remembered-secrets forgets
// everything; with ?alias=<alias> only that alias (every kind under it). The
// alias travels as a query value, not a path segment, so "." and ".." stay
// ordinary aliases. Idempotent; answers with the new status.
func handleForgetSecrets(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Has("alias") {
			mgr.secretMemory.Forget(q.Get("alias"))
		} else {
			mgr.secretMemory.ForgetAll()
		}
		writeRememberedSecrets(w, mgr)
	}
}

type rememberedSecretUse struct {
	secretaction.MemoryUse
	SessionTitle string `json:"sessionTitle,omitempty"`
}

func writeRememberedSecrets(w http.ResponseWriter, mgr *Manager) {
	st := mgr.secretMemory.Status()
	uses := make([]rememberedSecretUse, len(st.Uses))
	for i, u := range st.Uses {
		uses[i].MemoryUse = u
		if s, ok := mgr.Get(u.SessionID); ok {
			uses[i].SessionTitle = s.title()
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		secretaction.MemoryStatus
		Uses []rememberedSecretUse `json:"uses"`
	}{st, uses})
}

// hasControlByte reports a C0 control or DEL. A terminal would act on them (a
// newline submits a second line, an escape starts a sequence) and transform
// their echo, which redaction cannot follow; no password needs them. Browser
// fields are filled as text, so only command requests reject them.
func hasControlByte(v []byte) bool {
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

func writeSecretResolveError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, secretaction.ErrUnknownRequest):
		http.Error(w, "unknown secret request", http.StatusNotFound)
	case errors.Is(err, secretaction.ErrMissingValue):
		http.Error(w, "a value is required for every field", http.StatusBadRequest)
	default:
		http.Error(w, "invalid value", http.StatusBadRequest)
	}
}
