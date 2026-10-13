package serve

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

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
			Values []struct {
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
		switch body.Action {
		case "deny":
			writeSecretResolveError(w, broker.Resolve(reqID, false, nil))
			return
		case "allow":
		default:
			http.Error(w, `action must be "allow" or "deny"`, http.StatusBadRequest)
			return
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
		}
		writeSecretResolveError(w, broker.Resolve(reqID, true, values))
	}
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
