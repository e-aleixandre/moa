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

// Per-field byte limits. The body as a whole is already capped, but a single
// oversized field is still harmful on its own: a 1 MiB title pollutes every
// session list, and the other fields are identifiers, not payloads.
const (
	maxAutomationTitleBytes          = 200
	maxAutomationOriginBytes         = 64
	maxAutomationIdempotencyKeyBytes = 256
	maxAutomationCallbackURLBytes    = 2048
	maxAutomationCallbackSecretBytes = 256
)

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

// ErrAutomationIndexUnavailable reports that the idempotency index could not be
// rebuilt from disk. Keyed requests are refused while it holds: answering them
// with an empty index would silently execute a redelivered webhook twice.
var ErrAutomationIndexUnavailable = errors.New("idempotency index unavailable")

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

// validateAutomationRun applies the per-field limits and cross-field rules. It
// returns a message suitable for a 400 body, or "" when the request is fine.
func validateAutomationRun(req AutomationRunRequest) string {
	limits := []struct {
		name  string
		value string
		max   int
	}{
		{"title", req.Title, maxAutomationTitleBytes},
		{"origin", req.Origin, maxAutomationOriginBytes},
		{"idempotency_key", req.IdempotencyKey, maxAutomationIdempotencyKeyBytes},
		{"callback_url", req.CallbackURL, maxAutomationCallbackURLBytes},
		{"callback_secret", req.CallbackSecret, maxAutomationCallbackSecretBytes},
	}
	for _, l := range limits {
		if len(l.value) > l.max {
			return fmt.Sprintf("%s too long (max %d bytes)", l.name, l.max)
		}
	}
	if req.CallbackSecret != "" && req.CallbackURL == "" {
		return "callback_secret requires callback_url"
	}
	if err := validateCallbackURL(req.CallbackURL); err != nil {
		return err.Error()
	}
	return ""
}

// sessionWebURL is the path a human can open to continue the session, relative
// to wherever the caller reaches Moa (the web client selects a session with
// ?session=<id>; see sw.js). It is deliberately relative: behind a
// TLS-terminating proxy the request's own scheme/Host describe the proxy hop,
// not the address the caller used.
func sessionWebURL(sessionID string) string {
	return "/?session=" + url.QueryEscape(sessionID)
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
		if msg := validateAutomationRun(req); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}

		sessionID, created, err := mgr.CreateAutomationRun(req)
		if err != nil {
			if errors.Is(err, ErrInvalidCWD) || errors.Is(err, ErrInvalidModel) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if errors.Is(err, ErrAutomationIndexUnavailable) {
				// Retryable: we cannot promise deduplication right now, and
				// running the prompt anyway could duplicate a redelivery.
				w.Header().Set("Retry-After", "5")
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
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
			URL:       sessionWebURL(sessionID),
			Created:   created,
		})
	}
}

// automationIndex maps an idempotency key to the session it created. It is
// built once from persisted session metadata and kept up to date by
// CreateAutomationRun, so a retried webhook resolves to the same session even
// across a restart.
//
// The rebuild is fail-closed: while it has not succeeded, keyed runs are
// refused (ErrAutomationIndexUnavailable) instead of silently losing
// deduplication, and the next keyed request retries it.
type automationIndex struct {
	baseDir string
	mu      sync.Mutex
	keys    map[string]string
	ready   bool
}

func newAutomationIndex(baseDir string) *automationIndex {
	idx := &automationIndex{baseDir: baseDir, keys: make(map[string]string)}
	if err := idx.rebuild(); err != nil {
		slog.Warn("automation idempotency index unavailable", "error", err)
	}
	return idx
}

// rebuild reloads the key→session map from persisted metadata. Partial results
// are kept even when ListAll also reports an error (one unreadable project
// store must not erase the keys of the others), but the index is only marked
// ready on a clean pass.
func (a *automationIndex) rebuild() error {
	summaries, err := session.ListAll(a.baseDir)
	a.load(summaries)
	a.mu.Lock()
	a.ready = err == nil
	a.mu.Unlock()
	return err
}

// ensureReady returns nil once the index reflects disk. A previously failed
// rebuild is retried here, so a transient filesystem problem recovers on the
// next keyed request without a restart.
func (a *automationIndex) ensureReady() error {
	a.mu.Lock()
	ready := a.ready
	a.mu.Unlock()
	if ready {
		return nil
	}
	if err := a.rebuild(); err != nil {
		return fmt.Errorf("%w: %v", ErrAutomationIndexUnavailable, err)
	}
	return nil
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

// sendFirstPrompt delivers the automation run's first prompt. Indirected so
// tests can simulate a delivery failure.
var sendFirstPrompt = func(m *Manager, sessionID, prompt string) error {
	_, _, _, err := m.Send(sessionID, prompt, nil, "")
	return err
}

// CreateAutomationRun creates a session for an external caller and sends it the
// first prompt. It returns the session ID and whether it was created now; a
// repeated idempotency key resolves to the existing session without creating a
// duplicate or re-sending the prompt.
//
// The whole check-create-send-record sequence runs under automationMu so two
// simultaneous retries of the same webhook cannot both pass the check, and so a
// concurrent Delete cannot drop the index entry we are about to write.
//
// The key is committed after success, never rolled back: it is written to the
// session metadata (and indexed) only once the first prompt was accepted. A
// failed send therefore leaves an inert, keyless session — nothing deletes a
// session another client may already be using, and neither a retry in this
// process nor an index rebuilt after a restart can resolve the key to it.
func (m *Manager) CreateAutomationRun(req AutomationRunRequest) (sessionID string, created bool, err error) {
	m.automationMu.Lock()
	defer m.automationMu.Unlock()

	if req.IdempotencyKey != "" {
		// Fail closed: without a trustworthy index a redelivered webhook would
		// run twice. Unkeyed callers never asked for deduplication.
		if err := m.automation.ensureReady(); err != nil {
			return "", false, err
		}
	}
	if id, ok := m.automation.lookup(req.IdempotencyKey); ok {
		return id, false, nil
	}

	origin := req.Origin
	if origin == "" {
		origin = automationOriginDefault
	}
	// The idempotency key is deliberately NOT written here: only a run whose
	// prompt was accepted may answer that key.
	meta := map[string]any{}
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
	if err := sendFirstPrompt(m, sess.ID, req.Prompt); err != nil {
		// Nothing to undo: the session keeps no key, so it is inert bookkeeping
		// the caller can delete, and the retry creates a fresh run. Deleting it
		// here would be worse — it is already visible to other endpoints.
		return "", false, fmt.Errorf("automation run: %w", err)
	}
	if req.IdempotencyKey != "" {
		if sess.persister != nil {
			if err := sess.persister.recordIdempotencyKey(req.IdempotencyKey); err != nil {
				// At-least-once degradation: the run did start, so we still answer
				// 201 and dedupe in this process, but after a restart the rebuilt
				// index won't know this key and a redelivery could duplicate it.
				slog.Error("automation run: persisting the idempotency key failed; deduplication will not survive a restart",
					"session", sess.ID, "error", err)
			}
		} else {
			slog.Error("automation run: session has no persister; idempotency key not durable", "session", sess.ID)
		}
	}
	m.automation.put(req.IdempotencyKey, sess.ID)
	return sess.ID, true, nil
}
