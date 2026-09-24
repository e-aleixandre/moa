package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	// How much of an upstream error body reaches the log. Enough to carry
	// OpenAI's `error.message` and `code`, short enough that a verbose refusal
	// cannot flood the journal.
	voiceLiveLogBodyLimit = 512
)

// The causes a failed call can have. They travel in the JSON body next to the
// human line so the client can say something actionable instead of printing
// the body: a server without a key and a server that is rate limiting are
// different problems with different next actions, and both used to arrive as
// one opaque 503.
const (
	voiceLiveCauseRateLimited   = "rate_limited"
	voiceLiveCauseNoAPIKey      = "no_api_key"
	voiceLiveCauseUnreachable   = "upstream_unreachable"
	voiceLiveCauseRefused       = "upstream_refused"
	voiceLiveCauseUpstreamBusy  = "upstream_busy"
	voiceLiveCauseUnreadable    = "upstream_unreadable"
	voiceLiveCauseBadRequest    = "bad_request"
	voiceLiveCauseUnknownSess   = "unknown_session"
	voiceLiveCauseConversation  = "conversation_unavailable"
	voiceLiveCauseDeviceRevoked = "device_revoked"
	voiceLiveCauseWrongCaller   = "wrong_caller"
	voiceLiveCauseUnknownCall   = "unknown_call"
)

// voiceLiveError is the one shape every failure of this feature takes. The
// cause is the contract with the client; the message exists for anything that
// reads the body raw (curl, logs), never as UI copy.
func voiceLiveError(w http.ResponseWriter, status int, cause, message string) {
	writeJSON(w, status, map[string]string{"error": message, "cause": cause})
}

// TEMPORARY INSTRUMENTATION — voice 503 diagnosis. Remove by flipping this to
// false and deleting it along with voiceLiveLogSnippet and its call sites.
//
// The owner authorised logging the provider's response body only as a means of
// identifying one specific failure: users were shown "live session unavailable"
// for three different causes and the real error was discarded, so two rounds
// were spent guessing. A provider body can echo request context, which for this
// feature can include what was said on a call, so this is a privacy cost the
// owner accepted for a purpose, not a permanent behaviour.
//
// It is a named constant, not a comment or a memory, because the commitment to
// withdraw it must be visible in the code that does it. Once a 503 has been
// diagnosed in production, this goes.
const voiceLiveLogUpstreamBodies = true

// voiceLiveLogSnippet keeps an upstream body loggable: bounded, on a single
// line. Only the provider's response is ever passed here — never the request,
// which carries the API key in its Authorization header.
func voiceLiveLogSnippet(body []byte, secrets ...string) string {
	if !voiceLiveLogUpstreamBodies {
		return "(body logging disabled)"
	}
	text := string(body)
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return strings.Join(strings.Fields(truncateUTF8(text, voiceLiveLogBodyLimit)), " ")
}

const (
	voiceLiveModel        = "gpt-live-1"
	voiceLiveBackendModel = "gpt-6-sol"
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

func handleVoiceLiveSession(mgr *Manager, keyFn RealtimeAPIKeyFunc, client *http.Client, calls *voiceLiveRegistry) http.HandlerFunc {
	return handleVoiceLiveSessionWithAdmission(mgr, keyFn, client, calls, newVoiceLiveAdmission())
}

// voiceLiveAdmission limits starts rather than Live connections: the upstream
// session continues billing after this HTTP exchange has returned, so releasing
// the request slot cannot be treated as releasing the cost of a call.
//
// Only calls that ESTABLISH spend a principal's allowance. A failed attempt
// costs nothing upstream, and charging for it punishes exactly the behaviour a
// human has when something does not start — retrying — by locking him out of
// the feature. The in-flight cap and the global rate still count attempts:
// those two exist against a runaway client loop, which fails in a tight cycle
// and would otherwise be unbounded.
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
	a.prune(cutoff)
	if a.active >= voiceLiveMaxInFlight || len(a.global) >= voiceLiveGlobalRate || len(a.principal[principal]) >= voiceLivePrincipalRate {
		return retryAfter(now, append(a.global, a.principal[principal]...)), false
	}
	a.active++
	a.global = append(a.global, now)
	return 0, true
}

// established records the only thing the per-principal allowance counts: a
// session that exists upstream and is billing.
func (a *voiceLiveAdmission) established(principal string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now().UTC()
	a.prune(now.Add(-time.Minute))
	a.principal[principal] = append(a.principal[principal], now)
}

// prune drops expired timestamps and the principals left with none, so a
// long-lived server does not keep a map entry per identity ever seen.
func (a *voiceLiveAdmission) prune(cutoff time.Time) {
	for id, times := range a.principal {
		times = pruneTimes(times, cutoff)
		if len(times) == 0 {
			delete(a.principal, id)
			continue
		}
		a.principal[id] = times
	}
}

func (a *voiceLiveAdmission) release() { a.mu.Lock(); a.active--; a.mu.Unlock() }

func handleVoiceLiveSessionWithAdmission(mgr *Manager, keyFn RealtimeAPIKeyFunc, client *http.Client, calls *voiceLiveRegistry, admission *voiceLiveAdmission) http.HandlerFunc {
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
			voiceLiveError(w, http.StatusTooManyRequests, voiceLiveCauseRateLimited, "too many voice sessions started")
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
			voiceLiveError(w, http.StatusBadRequest, voiceLiveCauseBadRequest, "invalid SDP")
			return
		}
		if len(body.Note) > voiceLiveNoteLimit {
			voiceLiveError(w, http.StatusBadRequest, voiceLiveCauseBadRequest, "note too large")
			return
		}
		sess, ok := mgr.Get(body.SessionID)
		if !ok {
			voiceLiveError(w, http.StatusBadRequest, voiceLiveCauseUnknownSess, "unknown session")
			return
		}
		key, keyOK := "", false
		if keyFn != nil {
			key, keyOK = keyFn()
		}
		if !keyOK || strings.TrimSpace(key) == "" {
			// Distinct from every upstream failure: nothing is wrong with the
			// provider, the server was simply never given a key.
			slog.Warn("voice live: no OpenAI API key configured; the call cannot be started")
			voiceLiveError(w, http.StatusServiceUnavailable, voiceLiveCauseNoAPIKey, "no OpenAI API key is configured")
			return
		}
		brief, err := buildVoiceLiveBrief(mgr, sess, body.Note)
		if err != nil {
			slog.Warn("voice live: the conversation brief could not be built", "session", body.SessionID, "error", err)
			voiceLiveError(w, http.StatusServiceUnavailable, voiceLiveCauseConversation, "conversation unavailable")
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
			slog.Warn("voice live: the upstream request could not be built", "error", err)
			voiceLiveError(w, http.StatusServiceUnavailable, voiceLiveCauseUnreachable, "could not create live session")
			return
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		if identity.Kind == "device" {
			store, ok := requestDeviceStore(r)
			if !ok || !activeRealtimeDevice(store, identity.DeviceID) {
				voiceLiveError(w, http.StatusForbidden, voiceLiveCauseDeviceRevoked, "device credential is no longer active")
				return
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			// The error carries the URL and the transport failure, never the
			// request headers, so the key cannot travel into the log with it.
			slog.Warn("voice live: the provider could not be reached", "error", err)
			voiceLiveError(w, http.StatusBadGateway, voiceLiveCauseUnreachable, "the voice provider could not be reached")
			return
		}
		defer resp.Body.Close() //nolint:errcheck // response body is read-only
		answer, err := io.ReadAll(io.LimitReader(resp.Body, voiceLiveResponseLimit+1))
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			// The status and the provider's own message are the whole point of
			// this log line: without them a refusal (bad key, quota, concurrent
			// session cap) is indistinguishable from a network failure.
			slog.Warn("voice live: the provider refused to start the session", "status", resp.StatusCode, "body", voiceLiveLogSnippet(answer, key))
			if resp.StatusCode == http.StatusTooManyRequests {
				if retry := resp.Header.Get("Retry-After"); retry != "" {
					w.Header().Set("Retry-After", retry)
				}
				voiceLiveError(w, http.StatusTooManyRequests, voiceLiveCauseUpstreamBusy, "the voice provider rate limited this project")
				return
			}
			voiceLiveError(w, http.StatusBadGateway, voiceLiveCauseRefused, "the voice provider refused to start the call")
			return
		}
		if err != nil || len(answer) > voiceLiveResponseLimit {
			slog.Warn("voice live: the provider response could not be read", "status", resp.StatusCode, "bytes", len(answer), "error", err)
			voiceLiveError(w, http.StatusBadGateway, voiceLiveCauseUnreadable, "the voice provider sent a response this server could not read")
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
			// A 2xx with an id has already created a billable session even if its
			// answer cannot be used. Keep it long enough for hangupNow to retry
			// a failed close instead of losing the only id that can end it.
			if result.Session.ID != "" {
				calls.track(result.Session.ID, body.SessionID, principal)
				calls.hangupNow(result.Session.ID)
			}
			slog.Warn("voice live: the provider response was not a usable session", "status", resp.StatusCode, "body", voiceLiveLogSnippet(answer, key))
			voiceLiveError(w, http.StatusBadGateway, voiceLiveCauseUnreadable, "the voice provider sent a response this server could not read")
			return
		}
		// From here the session EXISTS upstream and bills until somebody closes
		// it, so it is remembered before anything else can fail, and only now
		// does it spend the caller's allowance.
		calls.track(result.Session.ID, body.SessionID, principal)
		admission.established(principal)
		deliver := func() error {
			writeJSON(w, http.StatusOK, map[string]any{"live_session_id": result.Session.ID, "sdp": result.Transport.SDP, "owner_id": brief.OwnerID, "brief_messages": len(brief.Input), "pricing": voiceLivePricing()})
			return nil
		}
		if identity.Kind == "device" {
			store, ok := requestDeviceStore(r)
			if !ok || store.withActiveDevice(identity.DeviceID, deliver) != nil {
				// The session was already created: the browser will never learn
				// its id, so this server is the only one that can end it.
				calls.hangupNow(result.Session.ID)
				voiceLiveError(w, http.StatusForbidden, voiceLiveCauseDeviceRevoked, "device credential is no longer active")
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
		voiceLiveError(w, http.StatusBadRequest, voiceLiveCauseBadRequest, "invalid JSON")
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
