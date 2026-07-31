package serve

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/e-aleixandre/moa/pkg/session"
)

// automationPathPrefix scopes the Automation API. Only routes under it accept
// (and require) the automation bearer token; the rest of Serve stays on the
// browser cookie/token or device credential.
const automationPathPrefix = "/api/automation/"

// automationOriginDefault labels sessions created through the Automation API
// when the caller did not name its integration.
const automationOriginDefault = "automation"

// maxAutomationPromptBytes bounds a single automation prompt. Automation
// callers are machines; an accidental dump of a whole log file should be
// rejected rather than turned into a session.
const maxAutomationPromptBytes = 256 << 10

// WithAutomationToken enables the Automation API with its own shared secret,
// separate from the browser token. An empty token leaves the automation routes
// disabled entirely (they answer 404), so the API is fail-closed even on
// localhost.
func WithAutomationToken(token string) ServerOption {
	return func(o *serverOptions) { o.automationToken = token }
}

// automationMiddleware terminates every /api/automation/ request: either the
// bearer token authenticates it and it is dispatched straight to the routes
// (skipping the browser auth and the X-Moa-Request CSRF check — a bearer header
// cannot be attached by a cross-site form), or it is rejected. It sits INSIDE
// the Host check, so DNS-rebinding protection still applies, and it is the only
// way into these routes: an owner cookie or a paired device does not grant
// access to the Automation API.
func automationMiddleware(token string, routes http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, automationPathPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		if token == "" {
			// Fail closed: without a configured token the surface does not exist.
			http.NotFound(w, r)
			return
		}
		if !bearerTokenEqual(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		routes.ServeHTTP(w, withAuthIdentity(r, authIdentity{Kind: "automation"}))
	})
}

// bearerTokenEqual reports whether an Authorization header carries exactly the
// configured automation token. The comparison is constant time, and the token
// is never read from the URL.
func bearerTokenEqual(header, token string) bool {
	scheme, value, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	value = strings.TrimSpace(value)
	if value == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(value), []byte(token)) == 1
}

// AutomationRunRequest is the body of POST /api/automation/runs: create a
// session and send it a first prompt in one call.
type AutomationRunRequest struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
	CWD    string `json:"cwd"`
	Title  string `json:"title"`
	Origin string `json:"origin"`
	// IdempotencyKey deduplicates retries: webhooks redeliver, and a redelivery
	// must not spawn a second session.
	IdempotencyKey string `json:"idempotency_key"`
	// CallbackURL/CallbackSecret are validated and stored now; delivering the
	// callback when the run goes quiescent lands in a future release.
	CallbackURL    string `json:"callback_url"`
	CallbackSecret string `json:"callback_secret"`
}

// AutomationRunResponse identifies the session the run landed in.
type AutomationRunResponse struct {
	SessionID string `json:"session_id"`
	URL       string `json:"url"`
	// Created is false when an idempotency key matched an existing session.
	Created bool `json:"created"`
}

var errAutomationInvalidCallback = errors.New("callback_url must be an absolute http or https URL")

// validateCallbackURL keeps the stored target to what the future callback
// sender is willing to POST to: an absolute http(s) URL with a host.
func validateCallbackURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errAutomationInvalidCallback
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errAutomationInvalidCallback
	}
	return nil
}

// sessionWebURL builds the address a human can open to continue the session,
// derived from the request's own Host so it is reachable by whoever called us
// (the web client selects a session with ?session=<id>; see sw.js).
func sessionWebURL(r *http.Request, sessionID string) string {
	path := "/?session=" + url.QueryEscape(sessionID)
	if r.Host == "" {
		return path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

func handleAutomationRun(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxJSONBodySize)
		var req AutomationRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		req.Prompt = strings.TrimSpace(req.Prompt)
		if req.Prompt == "" {
			http.Error(w, "prompt required", http.StatusBadRequest)
			return
		}
		if len(req.Prompt) > maxAutomationPromptBytes {
			http.Error(w, "prompt too large", http.StatusBadRequest)
			return
		}
		if err := validateCallbackURL(req.CallbackURL); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		sessionID, created, err := mgr.CreateAutomationRun(req)
		if err != nil {
			if errors.Is(err, ErrInvalidCWD) || errors.Is(err, ErrInvalidModel) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, AutomationRunResponse{
			SessionID: sessionID,
			URL:       sessionWebURL(r, sessionID),
			Created:   created,
		})
	}
}

// automationIndex maps an idempotency key to the session it created. It is
// built once from persisted session metadata and kept up to date by
// CreateAutomationRun, so a retried webhook resolves to the same session even
// across a restart.
type automationIndex struct {
	mu   sync.Mutex
	keys map[string]string
}

func newAutomationIndex(baseDir string) *automationIndex {
	idx := &automationIndex{keys: make(map[string]string)}
	summaries, err := session.ListAll(baseDir)
	if err != nil {
		// A missing index only costs deduplication; automation still works.
		slog.Warn("automation idempotency index unavailable", "error", err)
		return idx
	}
	idx.load(summaries)
	return idx
}

func (a *automationIndex) load(summaries []session.Summary) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, sum := range summaries {
		if key, _ := sum.Metadata[session.MetaIdempotencyKey].(string); key != "" {
			a.keys[key] = sum.ID
		}
	}
}

func (a *automationIndex) lookup(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.keys[key]
	return id, ok
}

func (a *automationIndex) put(key, sessionID string) {
	if key == "" {
		return
	}
	a.mu.Lock()
	a.keys[key] = sessionID
	a.mu.Unlock()
}

func (a *automationIndex) forget(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, id := range a.keys {
		if id == sessionID {
			delete(a.keys, key)
		}
	}
}

// CreateAutomationRun creates a session for an external caller and sends it the
// first prompt. It returns the session ID and whether it was created now; a
// repeated idempotency key resolves to the existing session without creating a
// duplicate or re-sending the prompt.
//
// The whole check-create-record sequence runs under automationMu so two
// simultaneous retries of the same webhook cannot both pass the check.
func (m *Manager) CreateAutomationRun(req AutomationRunRequest) (sessionID string, created bool, err error) {
	m.automationMu.Lock()
	defer m.automationMu.Unlock()

	if id, ok := m.automation.lookup(req.IdempotencyKey); ok {
		return id, false, nil
	}

	origin := req.Origin
	if origin == "" {
		origin = automationOriginDefault
	}
	meta := map[string]any{}
	if req.IdempotencyKey != "" {
		meta[session.MetaIdempotencyKey] = req.IdempotencyKey
	}
	if req.CallbackURL != "" {
		meta[session.MetaCallbackURL] = req.CallbackURL
	}
	if req.CallbackSecret != "" {
		meta[session.MetaCallbackSecret] = req.CallbackSecret
	}

	sess, err := m.CreateSession(CreateOpts{
		Model:     req.Model,
		Title:     req.Title,
		CWD:       req.CWD,
		Origin:    origin,
		extraMeta: meta,
	})
	if err != nil {
		return "", false, err
	}
	if _, _, _, err := m.Send(sess.ID, req.Prompt, nil, ""); err != nil {
		return "", false, fmt.Errorf("automation run: %w", err)
	}
	m.automation.put(req.IdempotencyKey, sess.ID)
	return sess.ID, true, nil
}
