package serve

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

const (
	providersPath = "/api/providers"
	// providersBodyLimit caps every Providers request body.
	providersBodyLimit = 16 << 10
	// providersRequestTimeout bounds the upstream work of one call: the xAI
	// device request (begin) or the code exchange (complete).
	providersRequestTimeout = 45 * time.Second
	// providerBrowserCookie binds sign-in attempts to the browser that began
	// them. It is not an identity; authority comes from owner authentication.
	providerBrowserCookie = "moa_provider_browser"
	providerBrowserMaxAge = 15 * 60
)

// providerIDs are the providers administered from the web, in display order.
var providerIDs = []string{"anthropic", "openai", "xai"}

func supportedProvider(id string) bool {
	for _, p := range providerIDs {
		if p == id {
			return true
		}
	}
	return false
}

// providerCredentials is the Providers API. Both fields are nil when the
// embedder did not wire them: every route then answers unavailable instead of
// opening some other credential file.
type providerCredentials struct {
	store        *auth.Store
	logins       *auth.ProviderLoginManager
	secureCookie bool
}

// WithProviderCredentials enables Settings → Providers on the given live
// credential store, the same instance every provider request resolves from,
// and the login manager built on it. The caller closes the manager.
func WithProviderCredentials(store *auth.Store, logins *auth.ProviderLoginManager) ServerOption {
	return func(o *serverOptions) {
		o.providerStore = store
		o.providerLogins = logins
	}
}

type providerRow struct {
	ID                   string `json:"id"`
	Source               string `json:"source"`
	Kind                 string `json:"kind,omitempty"`
	CredentialGeneration string `json:"credential_generation"`
	State                string `json:"state"`
	Attention            bool   `json:"attention"`
	Action               string `json:"action"`
	ChangedAt            string `json:"changed_at,omitempty"`
	RenewAt              string `json:"renew_at,omitempty"`
	LastUseOKAt          string `json:"last_use_ok_at,omitempty"`
	// Owner-only fields (GET /api/providers and mutation responses).
	OAuthEnabled  *bool    `json:"oauth_enabled,omitempty"`
	APIKeyEnabled *bool    `json:"api_key_enabled,omitempty"`
	Actions       []string `json:"actions,omitempty"`
	PendingSave   *bool    `json:"pending_save,omitempty"`
}

type providersStatus struct {
	Version        int           `json:"version"`
	CanAdmin       bool          `json:"can_admin"`
	AttentionCount int           `json:"attention_count"`
	Providers      []providerRow `json:"providers"`
}

type providerErrorDetail struct {
	Provider string `json:"provider,omitempty"`
	Class    string `json:"class"`
	Action   string `json:"action"`
}

type providerErrorBody struct {
	Error       string              `json:"error"`
	ErrorDetail providerErrorDetail `json:"error_detail"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ownerAction is the next step for a provider row, from the owner's side.
func ownerAction(st auth.ProviderStatus) string {
	env := st.Source == core.CredentialSourceEnv
	switch st.State {
	case auth.StatusMissing, auth.StatusReconnect, auth.StatusKeyRejected:
		if env {
			return "manage_environment"
		}
		switch st.State {
		case auth.StatusMissing:
			return "connect"
		case auth.StatusReconnect:
			return "reconnect"
		}
		return "replace_key"
	case auth.StatusSaveFailed:
		return "retry_save"
	case auth.StatusStoreUnavailable:
		return "repair_store"
	case auth.StatusTemporary:
		return "retry"
	}
	return ""
}

// row projects a status for the caller: a device sees what is wrong but no
// owner action; detailed adds the owner's administration fields.
func (pc *providerCredentials) row(st auth.ProviderStatus, owner, detailed bool) providerRow {
	r := providerRow{
		ID:                   st.Provider,
		Source:               st.Source,
		Kind:                 st.Kind,
		CredentialGeneration: st.Generation,
		State:                st.State,
		Attention:            st.NeedsAttention(),
		Action:               ownerAction(st),
		ChangedAt:            formatTime(st.ChangedAt),
		RenewAt:              formatTime(st.RenewAt),
		LastUseOKAt:          formatTime(st.LastUseOKAt),
	}
	if !owner {
		// A device can only report the problem; it cannot fix it.
		if r.Action != "" && r.Action != "retry" {
			r.Action = "ask_owner"
		}
		return r
	}
	if !detailed {
		return r
	}
	enabled, pending := true, st.PendingSave
	r.OAuthEnabled, r.APIKeyEnabled, r.PendingSave = &enabled, &enabled, &pending
	r.Actions = []string{}
	if st.Source == core.CredentialSourceStore && st.State != auth.StatusStoreUnavailable {
		r.Actions = append(r.Actions, "sign_in", "api_key")
		if st.PendingSave {
			r.Actions = append(r.Actions, "retry_save")
		}
	}
	return r
}

func (pc *providerCredentials) status(owner, detailed bool) providersStatus {
	out := providersStatus{Version: 1, CanAdmin: owner, Providers: make([]providerRow, 0, len(providerIDs))}
	for _, id := range providerIDs {
		st := pc.store.ProviderStatus(id)
		if st.NeedsAttention() {
			out.AttentionCount++
		}
		out.Providers = append(out.Providers, pc.row(st, owner, detailed))
	}
	return out
}

func isOwnerIdentity(r *http.Request) bool {
	identity, ok := requestAuthIdentity(r)
	return ok && (identity.Kind == "token" || identity.Kind == "network")
}

// isProvidersPath reports whether path belongs to the Providers API subtree.
func isProvidersPath(path string) bool {
	return path == providersPath || strings.HasPrefix(path, providersPath+"/")
}

// ServeHTTP routes the whole /api/providers subtree itself, so an unknown
// path or method ends here with 404/405 instead of reaching another handler.
func (pc *providerCredentials) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == providersPath+"/status" || path == providersPath {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeProviderError(w, http.StatusMethodNotAllowed, "", "method_not_allowed")
			return
		}
		if pc.store == nil {
			writeProviderError(w, http.StatusServiceUnavailable, "", "unavailable")
			return
		}
		owner := isOwnerIdentity(r)
		writeJSON(w, http.StatusOK, pc.status(owner, owner && path == providersPath))
		return
	}
	rest, _ := strings.CutPrefix(path, providersPath+"/")
	provider, action, ok := strings.Cut(rest, "/")
	handlers := map[string]func(http.ResponseWriter, *http.Request, string){
		"oauth/begin":    pc.begin,
		"oauth/complete": pc.complete,
		"oauth/progress": pc.progress,
		"oauth/cancel":   pc.cancel,
		"api-key":        pc.apiKey,
		"retry-save":     pc.retrySave,
	}
	handle, known := handlers[action]
	if !ok || !known {
		writeProviderError(w, http.StatusNotFound, "", auth.LoginNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeProviderError(w, http.StatusMethodNotAllowed, "", "method_not_allowed")
		return
	}
	if !providerOriginAllowed(r) {
		writeProviderError(w, http.StatusForbidden, "", "origin_rejected")
		return
	}
	if !supportedProvider(provider) {
		writeProviderError(w, http.StatusNotFound, "", auth.LoginNotFound)
		return
	}
	if pc.store == nil || pc.logins == nil {
		writeProviderError(w, http.StatusServiceUnavailable, provider, "unavailable")
		return
	}
	handle(w, r, provider)
}

// providerOriginAllowed requires a POST to come from moa's own page: exactly
// one Origin that is the serialized scheme://host[:port] this request was
// addressed to. X-Moa-Request alone is not enough for credential changes.
func providerOriginAllowed(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return false
	}
	origin := values[0]
	if origin == "" || origin == "null" || strings.ContainsAny(origin, ", \t") {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Host == "" || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	scheme, ok := providerRequestScheme(r)
	return ok && strings.EqualFold(origin, scheme+"://"+r.Host)
}

// providerRequestScheme is the scheme the browser used, by the same trust
// rule as the owner cookie (browserUsesTLS): direct TLS, or one unambiguous
// X-Forwarded-Proto from a loopback proxy. A loopback request whose header
// is repeated or malformed is refused rather than guessed. The host always
// comes from the validated Host header, never X-Forwarded-Host.
func providerRequestScheme(r *http.Request) (string, bool) {
	if r.TLS == nil && peerIsLoopback(r) {
		values := r.Header.Values("X-Forwarded-Proto")
		if len(values) > 1 {
			return "", false
		}
		if len(values) == 1 {
			if v := strings.ToLower(strings.TrimSpace(values[0])); v != "http" && v != "https" {
				return "", false
			}
		}
	}
	if browserUsesTLS(r) {
		return "https", true
	}
	return "http", true
}

// decodeProviderBody reads one JSON object of at most providersBodyLimit
// bytes with no unknown fields, writing the error response itself.
func decodeProviderBody(w http.ResponseWriter, r *http.Request, provider string, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProviderError(w, http.StatusUnsupportedMediaType, provider, "invalid_request")
		return false
	}
	limitBody(w, r, providersBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(target)
	if err == nil && decoder.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProviderError(w, http.StatusRequestEntityTooLarge, provider, "invalid_request")
		} else {
			writeProviderError(w, http.StatusBadRequest, provider, "invalid_request")
		}
		return false
	}
	return true
}

// browserBinding returns the browser's binding cookie value ("" without
// one); ok is false when the cookie is ambiguous (sent more than once).
func browserBinding(r *http.Request) (value string, ok bool) {
	n := 0
	for _, c := range r.Cookies() {
		if c.Name == providerBrowserCookie {
			value, n = c.Value, n+1
		}
	}
	return value, n <= 1
}

// ensureBrowserBinding returns the browser's binding, minting one when it
// has no well-formed one, and (re)sets the cookie for another 15 minutes. A
// valid binding is reused, so attempts for several providers can coexist.
// The manager only ever stores its hash.
func (pc *providerCredentials) ensureBrowserBinding(w http.ResponseWriter, r *http.Request) (string, bool) {
	binding, ok := browserBinding(r)
	if !ok {
		return "", false
	}
	if raw, err := base64.RawURLEncoding.DecodeString(binding); err != nil || len(raw) != 32 {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return "", false
		}
		binding = base64.RawURLEncoding.EncodeToString(buf)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     providerBrowserCookie,
		Value:    binding,
		Path:     providersPath,
		MaxAge:   providerBrowserMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   pc.secureCookie || browserUsesTLS(r),
	})
	return binding, true
}

func (pc *providerCredentials) begin(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		ExpectedGeneration *string `json:"expected_generation"`
	}
	if !decodeProviderBody(w, r, provider, &body) {
		return
	}
	if body.ExpectedGeneration == nil {
		writeProviderError(w, http.StatusBadRequest, provider, "invalid_request")
		return
	}
	binding, ok := pc.ensureBrowserBinding(w, r)
	if !ok {
		writeProviderError(w, http.StatusBadRequest, provider, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), providersRequestTimeout)
	defer cancel()
	view, err := pc.logins.Begin(ctx, provider, binding, *body.ExpectedGeneration)
	if err != nil {
		writeProviderFailure(w, provider, "begin", err)
		return
	}
	status := http.StatusCreated
	if view.Flow == "device" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, view)
}

type attemptBody struct {
	AttemptID string `json:"attempt_id"`
}

// boundAttempt decodes the attempt request and the browser binding; a request
// without a binding cannot match any attempt.
func boundAttempt(w http.ResponseWriter, r *http.Request, provider string, target any) (string, bool) {
	if !decodeProviderBody(w, r, provider, target) {
		return "", false
	}
	binding, ok := browserBinding(r)
	if !ok {
		writeProviderError(w, http.StatusBadRequest, provider, "invalid_request")
		return "", false
	}
	if binding == "" {
		writeProviderError(w, http.StatusNotFound, provider, auth.LoginNotFound)
		return "", false
	}
	return binding, true
}

func (pc *providerCredentials) complete(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		AttemptID string `json:"attempt_id"`
		Input     string `json:"input"`
	}
	binding, ok := boundAttempt(w, r, provider, &body)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), providersRequestTimeout)
	defer cancel()
	if err := pc.logins.Complete(ctx, provider, body.AttemptID, binding, body.Input); err != nil {
		// To the browser that owns it, an attempt that already ended (used,
		// canceled, replaced, expired) is gone, not unknown.
		var le *auth.LoginError
		if errors.As(err, &le) && le.Class == auth.LoginNotFound {
			if p, perr := pc.logins.Progress(provider, body.AttemptID, binding); perr == nil && p.State != "waiting" {
				err = &auth.LoginError{Provider: provider, Class: auth.LoginExpired}
			}
		}
		writeProviderFailure(w, provider, "complete", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":    pc.row(pc.store.ProviderStatus(provider), true, true),
		"next_action": "return_to_session",
	})
}

func (pc *providerCredentials) progress(w http.ResponseWriter, r *http.Request, provider string) {
	var body attemptBody
	binding, ok := boundAttempt(w, r, provider, &body)
	if !ok {
		return
	}
	p, err := pc.logins.Progress(provider, body.AttemptID, binding)
	if err != nil {
		writeProviderFailure(w, provider, "progress", err)
		return
	}
	out := struct {
		Provider       string               `json:"provider"`
		State          string               `json:"state"`
		ExpiresAt      time.Time            `json:"expires_at"`
		ErrorDetail    *providerErrorDetail `json:"error_detail,omitempty"`
		ProviderStatus *providerRow         `json:"provider_status,omitempty"`
	}{Provider: provider, State: p.State, ExpiresAt: p.ExpiresAt}
	if p.ErrorClass != "" {
		_, action, _ := providerErrorInfo(p.ErrorClass, "progress")
		out.ErrorDetail = &providerErrorDetail{Provider: provider, Class: p.ErrorClass, Action: action}
	}
	if p.State == "saved" { // the attempt's state, not a provider status
		row := pc.row(pc.store.ProviderStatus(provider), true, true)
		out.ProviderStatus = &row
	}
	writeJSON(w, http.StatusOK, out)
}

func (pc *providerCredentials) cancel(w http.ResponseWriter, r *http.Request, provider string) {
	var body attemptBody
	binding, ok := boundAttempt(w, r, provider, &body)
	if !ok {
		return
	}
	if err := pc.logins.Cancel(provider, body.AttemptID, binding); err != nil {
		writeProviderFailure(w, provider, "cancel", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (pc *providerCredentials) apiKey(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		Key                string  `json:"key"`
		ExpectedGeneration *string `json:"expected_generation"`
	}
	if !decodeProviderBody(w, r, provider, &body) {
		return
	}
	if body.ExpectedGeneration == nil {
		writeProviderError(w, http.StatusBadRequest, provider, "invalid_request")
		return
	}
	if _, err := pc.logins.SaveAPIKey(provider, *body.ExpectedGeneration, body.Key); err != nil {
		writeProviderFailure(w, provider, "api_key", err)
		return
	}
	writeJSON(w, http.StatusOK, pc.row(pc.store.ProviderStatus(provider), true, true))
}

func (pc *providerCredentials) retrySave(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct{}
	if !decodeProviderBody(w, r, provider, &body) {
		return
	}
	if pc.store.ProviderStatus(provider).Source == core.CredentialSourceEnv {
		writeProviderError(w, http.StatusConflict, provider, auth.LoginEnvManaged)
		return
	}
	if err := pc.store.RetrySave(provider); err != nil {
		writeProviderFailure(w, provider, "retry_save", err)
		return
	}
	writeJSON(w, http.StatusOK, pc.row(pc.store.ProviderStatus(provider), true, true))
}

// writeProviderFailure answers a classified failure with its fixed copy.
// Anything unclassified gets a generic answer: its text may hold upstream
// content and is never sent.
func writeProviderFailure(w http.ResponseWriter, provider, op string, err error) {
	class := "internal"
	var le *auth.LoginError
	if errors.As(err, &le) {
		class = le.Class
	} else if pe, ok := core.AsProviderCredentialError(err); ok {
		class = pe.Class
	}
	writeProviderErrorOp(w, provider, class, op)
}

func writeProviderError(w http.ResponseWriter, status int, provider, class string) {
	copyText, action, _ := providerErrorInfo(class, "")
	writeJSON(w, status, providerErrorBody{Error: copyText, ErrorDetail: providerErrorDetail{Provider: provider, Class: class, Action: action}})
}

func writeProviderErrorOp(w http.ResponseWriter, provider, class, op string) {
	copyText, action, status := providerErrorInfo(class, op)
	writeJSON(w, status, providerErrorBody{Error: copyText, ErrorDetail: providerErrorDetail{Provider: provider, Class: class, Action: action}})
}

// providerErrorInfo maps an error class to fixed next-action copy, the
// action the page offers and the HTTP status. op distinguishes a failed
// rotation retry from a failed new login or key, which are simply redone.
func providerErrorInfo(class, op string) (copyText, action string, status int) {
	switch class {
	case auth.LoginNotFound:
		return "This sign-in is no longer active here. Start again.", "start_again", http.StatusNotFound
	case auth.LoginExpired, auth.LoginCanceled:
		return "This sign-in has ended. Start again.", "start_again", http.StatusGone
	case auth.LoginSuperseded:
		return "A newer sign-in replaced this one. Start again.", "start_again", http.StatusConflict
	case auth.LoginUnsupported:
		return "This provider can't be managed here.", "none", http.StatusNotFound
	case auth.LoginEnvManaged:
		return "Managed by environment. Change it where moa runs.", "manage_environment", http.StatusConflict
	case auth.LoginInvalidInput:
		return "That isn't what the sign-in page showed. Paste it again.", "paste_again", http.StatusBadRequest
	case auth.LoginMismatch:
		return "That doesn't match this sign-in. Start again.", "start_again", http.StatusBadRequest
	case auth.LoginDenied:
		return "Sign-in was not approved. Start again.", "start_again", http.StatusBadRequest
	case auth.LoginInvalidKey, auth.LoginNotAPIKey:
		return "That doesn't look like an API key. Paste the key again.", "replace_key", http.StatusBadRequest
	case auth.LoginUnavailable:
		return "Provider sign-in isn't available right now. Try again.", "try_again", http.StatusServiceUnavailable
	case core.CredentialChanged:
		return "Credentials changed. Reload and try again.", "reload", http.StatusConflict
	case core.CredentialStoreUnavailable:
		return "The credential file can't be read. Repair it on the server.", "repair_store", http.StatusServiceUnavailable
	case core.CredentialPersistenceFailed:
		if op == "retry_save" {
			return "Could not save credentials. Retry saving.", "retry_save", http.StatusServiceUnavailable
		}
		return "Could not save credentials. Start again.", "start_again", http.StatusServiceUnavailable
	case core.CredentialSaveConflict:
		return "Credentials changed elsewhere while saving. Sign in again.", "reconnect", http.StatusConflict
	case core.CredentialTemporary:
		return "The provider didn't answer. Try again.", "try_again", http.StatusServiceUnavailable
	case core.CredentialReconnect, core.CredentialProviderUnavailable, core.CredentialMissing,
		core.CredentialKeyRejected, core.CredentialPermissions, core.CredentialQuota:
		return "The provider refused this sign-in. Start again.", "start_again", http.StatusBadGateway
	case "origin_rejected":
		return "Open Settings from moa and try again.", "reload", http.StatusForbidden
	case "invalid_request":
		return "Reload the page and try again.", "reload", http.StatusBadRequest
	case "method_not_allowed":
		return "Reload the page and try again.", "reload", http.StatusMethodNotAllowed
	}
	return "Something went wrong. Try again.", "try_again", http.StatusInternalServerError
}

// providersHeadersMiddleware runs outermost so that every Providers
// response, including Host, authentication and route-policy errors, is never
// cached, sends no referrer and is not content-sniffed.
func providersHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isProvidersPath(r.URL.Path) {
			h := w.Header()
			h.Set("Cache-Control", "no-store")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Content-Type-Options", "nosniff")
		}
		next.ServeHTTP(w, r)
	})
}

// providersRouter hands the whole Providers subtree to its own router.
func providersRouter(providers http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isProvidersPath(r.URL.Path) {
			providers.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
