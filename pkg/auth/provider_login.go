package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Attempt states reported by Progress.
const (
	attemptWaiting    = "waiting"
	attemptExchanging = "exchanging"
	attemptSaved      = "saved"
	attemptSaveFailed = "save_failed"
	attemptDenied     = "denied"
	attemptExpired    = "expired"
	attemptCanceled   = "canceled"
	attemptSuperseded = "superseded"
	attemptFailed     = "failed"
)

// maxAPIKeyLen bounds a pasted API key; minAPIKeyLen rejects fragments.
// Real provider keys (sk-..., sk-ant-..., xai-...) are far longer.
const (
	minAPIKeyLen = 20
	maxAPIKeyLen = 8 << 10
)

// LoginAttemptView is what Begin returns to the browser that started the
// sign-in. It never holds the verifier, device code or any token.
type LoginAttemptView struct {
	Provider                string    `json:"provider"`
	Flow                    string    `json:"flow"` // paste_code_state, paste_url or device
	AttemptID               string    `json:"attempt_id"`
	AuthorizeURL            string    `json:"authorize_url,omitempty"`
	UserCode                string    `json:"user_code,omitempty"`
	VerificationURI         string    `json:"verification_uri,omitempty"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	ExpiresAt               time.Time `json:"expires_at"`
	State                   string    `json:"state,omitempty"`
}

// LoginProgress is the state of one attempt. ErrorClass is a LoginError or
// core credential class, never provider text.
type LoginProgress struct {
	Provider   string    `json:"provider"`
	State      string    `json:"state"`
	ExpiresAt  time.Time `json:"expires_at"`
	ErrorClass string    `json:"error_class,omitempty"`
}

// ProviderLoginManager runs web sign-ins against the shared Store. Attempts
// live only in memory (a restart means starting again, with the previous
// credential intact), there is at most one per provider, and each is bound to
// the browser that started it and usable once.
//
// Lock order: mu → Store locks. mu is never held across network calls;
// it is held across the final CommitLogin so a cancel, replacement or
// expiry cannot interleave with the save.
type ProviderLoginManager struct {
	store  *Store
	ctx    context.Context // lifetime: owns xAI polling, ended by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// endpoints is zero in production (pinned provider endpoints);
	// same-package tests point it at httptest servers.
	endpoints tokenEndpoints
	now       func() time.Time
	// onSettled, when set by tests, observes every terminal transition.
	onSettled func(provider, state string)

	mu       sync.Mutex
	closed   bool
	attempts map[string]*loginAttempt
}

type loginAttempt struct {
	provider string
	flow     string
	id       [sha256.Size]byte // hashes: the raw values are the caller's secrets
	binding  [sha256.Size]byte
	baseline string // stored generation at Begin; the commit requires it
	expires  time.Time
	state    string
	errClass string
	code     *CodeAttempt // until claimed or settled
	// cancel stops the in-flight exchange or device polling.
	cancel context.CancelFunc
}

// NewProviderLoginManager returns a manager whose background work (xAI
// polling) lives until ctx ends or Close is called.
func NewProviderLoginManager(ctx context.Context, store *Store) *ProviderLoginManager {
	ctx, cancel := context.WithCancel(ctx)
	return &ProviderLoginManager{store: store, ctx: ctx, cancel: cancel, now: time.Now, attempts: map[string]*loginAttempt{}}
}

// webLoginProvider lists the providers administered from the web.
func webLoginProvider(provider string) bool {
	return provider == "anthropic" || provider == "openai" || provider == "xai"
}

func envManaged(provider string) bool { return os.Getenv(envKeyForProvider(provider)) != "" }

// Begin starts a sign-in for provider bound to browserBinding (an opaque
// per-browser secret), replacing any attempt in progress for that provider.
// expectedGeneration is the stored generation the caller's page showed; a
// different one means the page is stale. For xAI the device code is requested
// with ctx, and polling then continues on the manager's lifetime.
func (m *ProviderLoginManager) Begin(ctx context.Context, provider, browserBinding, expectedGeneration string) (LoginAttemptView, error) {
	if !webLoginProvider(provider) {
		return LoginAttemptView{}, loginError(provider, LoginUnsupported)
	}
	if browserBinding == "" {
		return LoginAttemptView{}, loginError(provider, LoginUnavailable)
	}
	if envManaged(provider) {
		return LoginAttemptView{}, loginError(provider, LoginEnvManaged)
	}
	gen, err := m.store.StoredGeneration(provider)
	if err != nil {
		return LoginAttemptView{}, err
	}
	if gen != expectedGeneration {
		return LoginAttemptView{}, credentialsChanged(provider, "login", gen)
	}
	id, err := randomToken(32)
	if err != nil {
		return LoginAttemptView{}, loginError(provider, LoginUnavailable)
	}
	now := m.now()
	att := &loginAttempt{
		provider: provider, id: sha256.Sum256([]byte(id)), binding: sha256.Sum256([]byte(browserBinding)),
		baseline: gen, expires: now.Add(loginAttemptTTL), state: attemptWaiting,
	}
	view := LoginAttemptView{Provider: provider, AttemptID: id}

	if provider != "xai" {
		code, err := beginCodeAttempt(provider, now)
		if err != nil {
			return LoginAttemptView{}, err
		}
		att.code, att.flow = code, "paste_url"
		if provider == "anthropic" {
			att.flow = "paste_code_state"
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed {
			return LoginAttemptView{}, loginError(provider, LoginUnavailable)
		}
		m.replaceLocked(att)
		view.Flow, view.AuthorizeURL, view.ExpiresAt = att.flow, code.AuthorizeURL(), att.expires
		return view, nil
	}

	// Reserve before the network so a concurrent Begin, Cancel or Close
	// decides whether this attempt may start polling.
	pollCtx, cancel := context.WithCancel(m.ctx)
	att.flow, att.cancel = "device", cancel
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return LoginAttemptView{}, loginError(provider, LoginUnavailable)
	}
	m.replaceLocked(att)
	m.mu.Unlock()

	device, err := StartXAIDeviceFlow(ctx, m.endpoints.client, m.endpoints.xai)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempts[provider] != att || att.state != attemptWaiting {
		cancel()
		return LoginAttemptView{}, loginError(provider, LoginSuperseded)
	}
	if err != nil {
		delete(m.attempts, provider)
		cancel()
		return LoginAttemptView{}, classifyLoginError(provider, err)
	}
	// The attempt ends at the provider's deadline (counted from issuance) or
	// 15 minutes after Begin, whichever comes first.
	if deadline := device.issuedAt.Add(device.ExpiresIn); deadline.Before(att.expires) {
		att.expires = deadline
	} else {
		device.ExpiresIn = att.expires.Sub(device.issuedAt)
	}
	m.wg.Add(1)
	go m.pollXAI(pollCtx, att, device)
	view.Flow, view.ExpiresAt, view.State = "device", att.expires, attemptWaiting
	view.UserCode, view.VerificationURI, view.VerificationURIComplete = device.UserCode, device.VerificationURI, device.VerificationURIComplete
	return view, nil
}

// Complete finishes an Anthropic or OpenAI sign-in with what the user pasted.
// A paste that is malformed or belongs to another attempt, provider or
// browser consumes nothing and makes no request. A matching one is claimed
// once before the exchange; a matching denial consumes the attempt without
// any exchange.
func (m *ProviderLoginManager) Complete(ctx context.Context, provider, attemptID, browserBinding, input string) error {
	if provider != "anthropic" && provider != "openai" {
		return loginError(provider, LoginUnsupported)
	}
	cb, parseErr := parseCallback(provider, input)

	m.mu.Lock()
	att := m.lookupLocked(provider, attemptID, browserBinding)
	if att == nil {
		m.mu.Unlock()
		return loginError(provider, LoginNotFound)
	}
	m.expireLocked(att)
	if att.state == attemptExpired {
		m.mu.Unlock()
		return loginError(provider, LoginExpired)
	}
	if att.state != attemptWaiting || att.code == nil {
		m.mu.Unlock()
		return loginError(provider, LoginNotFound)
	}
	if parseErr != nil {
		m.mu.Unlock()
		return parseErr
	}
	if err := att.code.match(cb); err != nil {
		m.mu.Unlock()
		return err
	}
	if envManaged(provider) {
		m.mu.Unlock()
		return loginError(provider, LoginEnvManaged)
	}
	if cb.denied {
		m.settleLocked(att, attemptDenied, "")
		m.mu.Unlock()
		return loginError(provider, LoginDenied)
	}
	code := att.code
	exCtx, cancel := context.WithTimeout(ctx, att.expires.Sub(m.now()))
	defer cancel()
	att.state, att.code, att.cancel = attemptExchanging, nil, cancel
	m.mu.Unlock()

	creds, err := exchangeCode(exCtx, m.endpoints, code, cb.code)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempts[provider] != att || att.state != attemptExchanging {
		return loginError(provider, endedClass(att.state))
	}
	if err != nil {
		err = classifyLoginError(provider, err)
		m.settleLocked(att, attemptFailed, errorClass(err))
		return err
	}
	return m.commitLocked(att, Credential{Type: "oauth", Access: creds.Access, Refresh: creds.Refresh, Expires: creds.Expires, AccountID: creds.AccountID})
}

// Progress reports an attempt's state. It never exchanges or starts polling.
func (m *ProviderLoginManager) Progress(provider, attemptID, browserBinding string) (LoginProgress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	att := m.lookupLocked(provider, attemptID, browserBinding)
	if att == nil {
		return LoginProgress{}, loginError(provider, LoginNotFound)
	}
	m.expireLocked(att)
	return LoginProgress{Provider: provider, State: att.state, ExpiresAt: att.expires, ErrorClass: att.errClass}, nil
}

// Cancel stops an attempt; nothing it obtains afterwards is saved.
func (m *ProviderLoginManager) Cancel(provider, attemptID, browserBinding string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	att := m.lookupLocked(provider, attemptID, browserBinding)
	if att == nil {
		return loginError(provider, LoginNotFound)
	}
	m.settleLocked(att, attemptCanceled, "")
	return nil
}

// Close cancels every attempt and waits for background polling to stop.
func (m *ProviderLoginManager) Close() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		for _, att := range m.attempts {
			m.settleLocked(att, attemptCanceled, "")
		}
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// SaveAPIKey stores a write-only API key as a new generation, provided the
// stored generation is still expectedGeneration. The key is never echoed.
func (m *ProviderLoginManager) SaveAPIKey(provider, expectedGeneration, key string) (string, error) {
	if !webLoginProvider(provider) {
		return "", loginError(provider, LoginUnsupported)
	}
	if envManaged(provider) {
		return "", loginError(provider, LoginEnvManaged)
	}
	key = strings.TrimSpace(key)
	if len(key) < minAPIKeyLen || len(key) > maxAPIKeyLen || !printableASCII(key) {
		return "", loginError(provider, LoginInvalidKey)
	}
	// Stored as api_key, an Anthropic OAuth token would select the wrong
	// transport and could not be renewed.
	if provider == "anthropic" && strings.HasPrefix(key, "sk-ant-oat") {
		return "", loginError(provider, LoginNotAPIKey)
	}
	return m.store.CommitLogin(provider, expectedGeneration, Credential{Type: "api_key", Key: key})
}

func (m *ProviderLoginManager) pollXAI(ctx context.Context, att *loginAttempt, device *XAIDeviceCode) {
	defer m.wg.Done()
	creds, err := CompleteXAIDeviceFlow(ctx, m.endpoints.client, m.endpoints.xai, device)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempts["xai"] != att || att.state != attemptWaiting {
		return // canceled, replaced or closed: a late result is dropped
	}
	switch {
	case err == nil:
		_ = m.commitLocked(att, Credential{Type: "oauth", Access: creds.Access, Refresh: creds.Refresh, Expires: creds.Expires})
	case errors.Is(err, errXAIDenied):
		m.settleLocked(att, attemptDenied, "")
	case errors.Is(err, errXAIExpired):
		m.settleLocked(att, attemptExpired, "")
	default:
		m.settleLocked(att, attemptFailed, errorClass(classifyLoginError("xai", err)))
	}
}

// commitLocked saves a sign-in result unless the attempt expired or the
// environment took over, guarded by the generation seen at Begin.
func (m *ProviderLoginManager) commitLocked(att *loginAttempt, cred Credential) error {
	if !m.now().Before(att.expires) {
		m.settleLocked(att, attemptExpired, "")
		return loginError(att.provider, LoginExpired)
	}
	if envManaged(att.provider) {
		m.settleLocked(att, attemptFailed, LoginEnvManaged)
		return loginError(att.provider, LoginEnvManaged)
	}
	if _, err := m.store.CommitLogin(att.provider, att.baseline, cred); err != nil {
		class := errorClass(err)
		switch class {
		case core.CredentialChanged:
			m.settleLocked(att, attemptSuperseded, class)
		case core.CredentialPersistenceFailed, core.CredentialStoreUnavailable:
			m.settleLocked(att, attemptSaveFailed, class)
		default:
			m.settleLocked(att, attemptFailed, class)
		}
		return err
	}
	m.settleLocked(att, attemptSaved, "")
	return nil
}

// lookupLocked returns provider's attempt if both the attempt id and the
// browser binding match, compared as hashes in constant time.
func (m *ProviderLoginManager) lookupLocked(provider, attemptID, browserBinding string) *loginAttempt {
	att := m.attempts[provider]
	if att == nil {
		return nil
	}
	id, binding := sha256.Sum256([]byte(attemptID)), sha256.Sum256([]byte(browserBinding))
	if subtle.ConstantTimeCompare(id[:], att.id[:])&subtle.ConstantTimeCompare(binding[:], att.binding[:]) != 1 {
		return nil
	}
	return att
}

func (m *ProviderLoginManager) replaceLocked(att *loginAttempt) {
	if old := m.attempts[att.provider]; old != nil {
		m.settleLocked(old, attemptSuperseded, "")
	}
	m.attempts[att.provider] = att
}

func (m *ProviderLoginManager) expireLocked(att *loginAttempt) {
	if att.state == attemptWaiting && !m.now().Before(att.expires) {
		m.settleLocked(att, attemptExpired, "")
	}
}

// settleLocked moves an attempt to a terminal state once, dropping its
// secrets and stopping its exchange or polling.
func (m *ProviderLoginManager) settleLocked(att *loginAttempt, state, class string) {
	if att.state != attemptWaiting && att.state != attemptExchanging {
		return
	}
	att.state, att.errClass, att.code = state, class, nil
	if att.cancel != nil {
		att.cancel()
	}
	if m.onSettled != nil {
		m.onSettled(att.provider, state)
	}
}

// endedClass reports why an exchange's attempt can no longer be committed.
func endedClass(state string) string {
	switch state {
	case attemptCanceled, attemptSuperseded, attemptExpired:
		return state
	}
	return LoginNotFound
}

func errorClass(err error) string {
	var le *LoginError
	if errors.As(err, &le) {
		return le.Class
	}
	if pe, ok := core.AsProviderCredentialError(err); ok {
		return pe.Class
	}
	return core.CredentialProviderUnavailable
}
