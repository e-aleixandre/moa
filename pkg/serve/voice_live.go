package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	voiceLiveSDPLimit      = 256 << 10
	voiceLiveResponseLimit = 512 << 10
	voiceLiveMaxInFlight   = 2
	voiceLiveGlobalRate    = 8
	voiceLivePrincipalRate = 2
)

const (
	voiceLiveModel        = "gpt-live-1"
	voiceLiveBackendModel = "gpt-5.6-terra"
	// Published rate for gpt-live-1 voice duration, billed per second. It is
	// absent from core's model table because that table prices tokens, and a
	// Live voice session is not billed in tokens at all.
	voiceLiveUSDPerMinute = 0.05
	// Creating a WebRTC session bills 15 seconds of duration up front, credited
	// against the running session rather than added to it. A call therefore
	// costs at least this much, and showing less would understate every short
	// call.
	voiceLiveMinBilledSeconds = 15
)

// voiceLivePricing is what the client needs to turn metered voice duration into
// money. It deliberately carries no backend token rate: pricing a Responses
// call correctly needs cache and long-context tiers (core.Pricing.Cost), and
// the forwarded usage is not reliable enough to feed it. The backend is named
// so the UI can say what it is NOT counting.
func voiceLivePricing() map[string]any {
	pricing := map[string]any{
		"voice_usd_per_minute":     voiceLiveUSDPerMinute,
		"voice_min_billed_seconds": voiceLiveMinBilledSeconds,
		"backend_model":            voiceLiveBackendModel,
	}
	return pricing
}

func handleVoiceLiveSession(mgr *Manager, keyFn RealtimeAPIKeyFunc, client *http.Client) http.HandlerFunc {
	return handleVoiceLiveSessionWithAdmission(mgr, keyFn, client, newVoiceLiveAdmission())
}

// voiceLiveAdmission limits starts rather than Live connections: the upstream
// session continues billing after this HTTP exchange has returned, so releasing
// the request slot cannot be treated as releasing the cost of a call.
type voiceLiveAdmission struct {
	mu        sync.Mutex
	now       func() time.Time
	active    int
	global    []time.Time
	principal map[string][]time.Time
}

func newVoiceLiveAdmission() *voiceLiveAdmission {
	return &voiceLiveAdmission{now: time.Now, principal: make(map[string][]time.Time)}
}

func (a *voiceLiveAdmission) acquire(principal string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now().UTC()
	cutoff := now.Add(-time.Minute)
	a.global = pruneTimes(a.global, cutoff)
	a.principal[principal] = pruneTimes(a.principal[principal], cutoff)
	if a.active >= voiceLiveMaxInFlight || len(a.global) >= voiceLiveGlobalRate || len(a.principal[principal]) >= voiceLivePrincipalRate {
		return retryAfter(now, append(a.global, a.principal[principal]...)), false
	}
	a.active++
	a.global = append(a.global, now)
	a.principal[principal] = append(a.principal[principal], now)
	return 0, true
}

func (a *voiceLiveAdmission) release() { a.mu.Lock(); a.active--; a.mu.Unlock() }

func handleVoiceLiveSessionWithAdmission(mgr *Manager, keyFn RealtimeAPIKeyFunc, client *http.Client, admission *voiceLiveAdmission) http.HandlerFunc {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		identity, authenticated := requestAuthIdentity(r)
		principal := "anonymous"
		if authenticated {
			principal = identity.auditID()
		}
		if retry, ok := admission.acquire(principal); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "live session rate limit exceeded"})
			return
		}
		defer admission.release()
		var body struct {
			SessionID string `json:"session_id"`
			SDP       string `json:"sdp"`
			Note      string `json:"note"`
		}
		if !decodeVoiceLiveJSON(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.SDP) == "" || len(body.SDP) > voiceLiveSDPLimit {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid SDP"})
			return
		}
		if len(body.Note) > voiceLiveNoteLimit {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "note too large"})
			return
		}
		sess, ok := mgr.Get(body.SessionID)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown session"})
			return
		}
		key, keyOK := "", false
		if keyFn != nil {
			key, keyOK = keyFn()
		}
		if !keyOK || strings.TrimSpace(key) == "" {
			realtimeUnavailable(w)
			return
		}
		brief, err := buildVoiceLiveBrief(mgr, sess, body.Note)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversation unavailable"})
			return
		}
		payload := map[string]any{"session": map[string]any{
			"model": voiceLiveModel, "instructions": brief.VoiceInstructions,
			"audio": map[string]any{"output": map[string]any{"voice": "marin"}}, "input": brief.Input,
			"delegation": map[string]any{"type": "responses", "responses": map[string]any{
				"model": voiceLiveBackendModel, "instructions": brief.DelegationInstructions,
				"reasoning": map[string]any{"effort": "low"}, "tools": voiceLiveTools(), "tool_choice": "auto",
			}},
		}, "transport": map[string]any{"type": "webrtc", "sdp": body.SDP}}
		encoded, _ := json.Marshal(payload)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/live/sessions", bytes.NewReader(encoded))
		if err != nil {
			http.Error(w, "could not create live session", http.StatusServiceUnavailable)
			return
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		if identity.Kind == "device" {
			store, ok := requestDeviceStore(r)
			if !ok || !activeRealtimeDevice(store, identity.DeviceID) {
				http.Error(w, "device credential is no longer active", http.StatusForbidden)
				return
			}
		}
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
			if resp != nil {
				resp.Body.Close()
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "live session unavailable"})
			return
		}
		defer resp.Body.Close() //nolint:errcheck // response body is read-only
		answer, err := io.ReadAll(io.LimitReader(resp.Body, voiceLiveResponseLimit+1))
		if err != nil || len(answer) > voiceLiveResponseLimit {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "live session unavailable"})
			return
		}
		var result struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
			Transport struct {
				SDP string `json:"sdp"`
			} `json:"transport"`
		}
		if json.Unmarshal(answer, &result) != nil || result.Session.ID == "" || result.Transport.SDP == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "live session unavailable"})
			return
		}
		deliver := func() error {
			writeJSON(w, http.StatusOK, map[string]any{"live_session_id": result.Session.ID, "sdp": result.Transport.SDP, "owner_id": brief.OwnerID, "brief_messages": len(brief.Input), "pricing": voiceLivePricing()})
			return nil
		}
		if identity.Kind == "device" {
			store, ok := requestDeviceStore(r)
			if !ok || store.withActiveDevice(identity.DeviceID, deliver) != nil {
				http.Error(w, "device credential is no longer active", http.StatusForbidden)
			}
			return
		}
		_ = deliver()
	}
}

func decodeVoiceLiveJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	limitBody(w, r, voiceLiveSDPLimit+maxJSONBodySize)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return false
	}
	return true
}

func voiceLiveTools() []map[string]any {
	return []map[string]any{
		{"type": "function", "name": "book_list", "description": "List the owner's book files.", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"type": "function", "name": "book_read", "description": "Read a file from the owner's book.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}},
		{"type": "function", "name": "ask_session", "description": "Ask the opened session a question.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"question": map[string]any{"type": "string"}}, "required": []string{"question"}}},
		// One field, not two: the minutes carry their own sections, written in
		// the language of the call. A separate "pending" argument was rendered
		// as a heading of its own and contradicted the section already inside
		// the minutes.
		{"type": "function", "name": "end_call", "description": "Finish the call and hand over its minutes. Write them in the language of the call, with three sections: what was Decided, what is Still open, and what is Pending confirmation with the owner.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"minutes": map[string]any{"type": "string"}}, "required": []string{"minutes"}}},
	}
}
