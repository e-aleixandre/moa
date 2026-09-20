package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// A call nobody has been heard from for this long is treated as abandoned.
	// A locked screen, a closed tab and a dropped network all look identical
	// from here, and in every one of them the browser's own session.close will
	// never be sent. The client heartbeats every 20s, so this tolerates a few
	// lost pings before acting.
	voiceLiveIdleTTL = 90 * time.Second
	// A hard ceiling on a tracked call, heartbeats or not: a client stuck in a
	// loop that keeps pinging a call nobody is on would otherwise hold an
	// upstream session forever.
	voiceLiveMaxCallAge = 60 * time.Minute
	// How often the sweeper looks. It runs only while calls are tracked.
	voiceLiveSweepEvery = 20 * time.Second
	// How long an upstream close may take before it is abandoned. It is
	// retried by the sweeper while the record survives.
	voiceLiveCloseTimeout = 10 * time.Second
)

// voiceLiveCall is what the server remembers about a Live session it created:
// enough to close it without the browser, and enough to refuse a close
// requested by somebody else.
type voiceLiveCall struct {
	sessionID string
	principal string
	createdAt time.Time
	seenAt    time.Time
	closing   bool
}

// voiceLiveRegistry is the safety net under the browser's graceful close.
// gpt-live-1 caps CONCURRENT sessions, so a session the browser never closed
// is not merely wasted money: leaked often enough, it blocks every future
// call. The browser's session.close stays the normal path — this closes what
// that path cannot reach, and does it idempotently so both closing is not an
// error.
type voiceLiveRegistry struct {
	mu       sync.Mutex
	calls    map[string]*voiceLiveCall
	sweeping bool

	now    func() time.Time
	closer func(context.Context, string) error
	spawn  func(func())

	idleTTL    time.Duration
	maxAge     time.Duration
	sweepEvery time.Duration
}

func newVoiceLiveRegistry(keyFn RealtimeAPIKeyFunc, client *http.Client) *voiceLiveRegistry {
	if client == nil {
		client = &http.Client{Timeout: voiceLiveCloseTimeout}
	}
	return &voiceLiveRegistry{
		calls:      make(map[string]*voiceLiveCall),
		now:        time.Now,
		closer:     voiceLiveUpstreamHangup(keyFn, client),
		spawn:      func(fn func()) { go fn() },
		idleTTL:    voiceLiveIdleTTL,
		maxAge:     voiceLiveMaxCallAge,
		sweepEvery: voiceLiveSweepEvery,
	}
}

// track remembers a session that now exists upstream. It also starts the
// sweeper, which exits again once nothing is tracked: the goroutine lives
// exactly as long as there is something to sweep.
func (reg *voiceLiveRegistry) track(liveID, sessionID, principal string) {
	if reg == nil || liveID == "" {
		return
	}
	reg.mu.Lock()
	now := reg.now().UTC()
	reg.calls[liveID] = &voiceLiveCall{sessionID: sessionID, principal: principal, createdAt: now, seenAt: now}
	start := !reg.sweeping
	reg.sweeping = true
	reg.mu.Unlock()
	if start {
		reg.spawn(reg.sweepLoop)
	}
}

// touch is the client saying it is still on this call.
func (reg *voiceLiveRegistry) touch(liveID, sessionID, principal string) (found, allowed bool) {
	if reg == nil {
		return false, false
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	call, ok := reg.calls[liveID]
	if !ok {
		return false, true
	}
	if call.sessionID != sessionID || call.principal != principal {
		return true, false
	}
	if !call.closing {
		call.seenAt = reg.now().UTC()
	}
	return true, true
}

// hangup closes a call on request. An unknown id is a success, not an error:
// the sweeper or a previous request may already have closed it, and a client
// that hangs up twice has not done anything wrong.
func (reg *voiceLiveRegistry) hangup(liveID, sessionID, principal string) bool {
	if reg == nil {
		return true
	}
	reg.mu.Lock()
	call, ok := reg.calls[liveID]
	if ok && (call.sessionID != sessionID || call.principal != principal) {
		reg.mu.Unlock()
		return false
	}
	if !ok {
		reg.mu.Unlock()
		return true
	}
	if call.closing {
		reg.mu.Unlock()
		return true
	}
	call.closing = true
	reg.mu.Unlock()
	reg.finishClose(liveID, reg.closeUpstream(liveID, "hangup"))
	return true
}

// hangupNow closes a session this server created but never handed over. There
// is no record to match against because nobody else can ever refer to it.
func (reg *voiceLiveRegistry) hangupNow(liveID string) {
	if reg == nil || liveID == "" {
		return
	}
	reg.mu.Lock()
	call, ok := reg.calls[liveID]
	if ok && call.closing {
		reg.mu.Unlock()
		return
	}
	if ok {
		call.closing = true
	}
	reg.mu.Unlock()
	if !ok {
		return
	}
	reg.finishClose(liveID, reg.closeUpstream(liveID, "undelivered"))
}

func (reg *voiceLiveRegistry) sweepLoop() {
	ticker := time.NewTicker(reg.sweepEvery)
	defer ticker.Stop()
	for range ticker.C {
		if done := reg.sweep(); done {
			return
		}
	}
}

// sweep closes what nobody claimed and reports whether the registry is empty,
// which is what ends the sweeper.
func (reg *voiceLiveRegistry) sweep() bool {
	reg.mu.Lock()
	now := reg.now().UTC()
	var idle, aged []string
	for id, call := range reg.calls {
		if call.closing {
			continue
		}
		switch {
		case now.Sub(call.createdAt) >= reg.maxAge:
			aged = append(aged, id)
			call.closing = true
		case now.Sub(call.seenAt) >= reg.idleTTL:
			idle = append(idle, id)
			call.closing = true
		}
	}
	reg.mu.Unlock()
	sort.Strings(idle)
	sort.Strings(aged)
	for _, id := range idle {
		reg.finishClose(id, reg.closeUpstream(id, "abandoned"))
	}
	for _, id := range aged {
		reg.finishClose(id, reg.closeUpstream(id, "max duration"))
	}
	reg.mu.Lock()
	empty := len(reg.calls) == 0
	if empty {
		reg.sweeping = false
	}
	reg.mu.Unlock()
	return empty
}

// finishClose retains a failed close for the sweeper. Losing that record would
// make a transient provider failure a permanent concurrent-session leak.
func (reg *voiceLiveRegistry) finishClose(liveID string, closed bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	call, ok := reg.calls[liveID]
	if !ok || !call.closing {
		return
	}
	if closed {
		delete(reg.calls, liveID)
		return
	}
	call.closing = false
}

func (reg *voiceLiveRegistry) closeUpstream(liveID, reason string) bool {
	if reg.closer == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), voiceLiveCloseTimeout)
	defer cancel()
	if err := reg.closer(ctx, liveID); err != nil {
		slog.Warn("voice live: closing the session upstream failed", "reason", reason, "error", err)
		return false
	}
	slog.Info("voice live: session closed from the server", "reason", reason)
	return true
}

// voiceLiveUpstreamHangup ends a Live session without the browser.
//
// The endpoint is the Live API's call control: POST
// /v1/live/sessions/{id}/hangup with no body, documented in the telephony
// guide ("Transfer or end the call") and reflected in the close reasons, where
// close_requested covers "your application sent session.close or called the
// hangup endpoint" for any transport. The alternative — attaching a sideband
// WebSocket only to send session.close — buys nothing here: this server does
// not need the final usage event, the browser already reports it when it is
// the one closing.
func voiceLiveUpstreamHangup(keyFn RealtimeAPIKeyFunc, client *http.Client) func(context.Context, string) error {
	return func(ctx context.Context, liveID string) error {
		key, ok := "", false
		if keyFn != nil {
			key, ok = keyFn()
		}
		if !ok || strings.TrimSpace(key) == "" {
			return errors.New("no OpenAI API key is configured")
		}
		endpoint := "https://api.openai.com/v1/live/sessions/" + url.PathEscape(liveID) + "/hangup"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close() //nolint:errcheck // response body is read-only
		body, _ := io.ReadAll(io.LimitReader(resp.Body, voiceLiveLogBodyLimit+1))
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode <= 299:
			return nil
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusConflict:
			// The session is already over — usually because the browser's own
			// session.close won the race. Both paths closing is the expected
			// case, not a failure.
			return nil
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, voiceLiveLogSnippet(body, key))
	}
}

// handleVoiceLiveClose is the client hanging up. It runs after the browser's
// own graceful close, so by design it usually finds the session already gone
// upstream and says so silently.
func handleVoiceLiveClose(calls *voiceLiveRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sessionID, liveID, ok := decodeVoiceLiveCall(w, r)
		if !ok {
			return
		}
		if !calls.hangup(liveID, sessionID, voiceLivePrincipal(r)) {
			voiceLiveError(w, http.StatusForbidden, voiceLiveCauseWrongCaller, "that call belongs to another caller")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleVoiceLiveHeartbeat is how the server knows somebody is still on the
// call. A 404 tells the client to stop pinging a call this server no longer
// knows about.
func handleVoiceLiveHeartbeat(calls *voiceLiveRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sessionID, liveID, ok := decodeVoiceLiveCall(w, r)
		if !ok {
			return
		}
		found, allowed := calls.touch(liveID, sessionID, voiceLivePrincipal(r))
		switch {
		case !allowed:
			voiceLiveError(w, http.StatusForbidden, voiceLiveCauseWrongCaller, "that call belongs to another caller")
		case !found:
			voiceLiveError(w, http.StatusNotFound, voiceLiveCauseUnknownCall, "that call is not tracked by this server")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func voiceLivePrincipal(r *http.Request) string {
	identity, authenticated := requestAuthIdentity(r)
	if !authenticated {
		return "anonymous"
	}
	return identity.auditID()
}

func decodeVoiceLiveCall(w http.ResponseWriter, r *http.Request) (sessionID, liveID string, ok bool) {
	var body struct {
		SessionID    string `json:"session_id"`
		LiveSessionD string `json:"live_session_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodySize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		voiceLiveError(w, http.StatusBadRequest, voiceLiveCauseBadRequest, "invalid JSON")
		return "", "", false
	}
	sessionID = strings.TrimSpace(body.SessionID)
	liveID = strings.TrimSpace(body.LiveSessionD)
	if sessionID == "" || liveID == "" || len(liveID) > voiceLiveCallIDLimit {
		voiceLiveError(w, http.StatusBadRequest, voiceLiveCauseBadRequest, "invalid request")
		return "", "", false
	}
	return sessionID, liveID, true
}
