package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// loginAttemptTTL bounds how long a started sign-in can be completed.
const loginAttemptTTL = 15 * time.Minute

// maxPastedCallback bounds a pasted code or callback URL.
const maxPastedCallback = 8 << 10

// openaiIssuer is the only RFC 9207 iss accepted in an OpenAI callback.
const openaiIssuer = "https://auth.openai.com"

// Login error classes. Each maps to fixed copy that says what to do next;
// none carries user input or provider text.
const (
	LoginUnsupported  = "unsupported"
	LoginEnvManaged   = "env_managed"
	LoginInvalidInput = "invalid_input"
	LoginMismatch     = "mismatch"
	LoginNotFound     = "not_found"
	LoginExpired      = "expired"
	LoginDenied       = "denied"
	LoginCanceled     = "canceled"
	LoginSuperseded   = "superseded"
	LoginInvalidKey   = "invalid_key"
	LoginNotAPIKey    = "not_api_key"
	LoginUnavailable  = "unavailable"
)

// LoginError is a sign-in or key-entry failure that is safe to show.
type LoginError struct {
	Provider string
	Class    string
}

func loginError(provider, class string) error { return &LoginError{Provider: provider, Class: class} }

func (e *LoginError) Error() string {
	name := e.Provider
	switch e.Class {
	case LoginUnsupported:
		return "sign-in is not available for this provider here"
	case LoginEnvManaged:
		return name + " credentials are managed by the environment: change them there"
	case LoginInvalidInput:
		if name == "openai" {
			return "paste the full address the browser opened, starting with http://localhost:1455/auth/callback"
		}
		return "paste the code#state value or the full callback URL shown after approving"
	case LoginMismatch:
		return "that belongs to another sign-in: paste the result of the latest one"
	case LoginNotFound:
		return "this sign-in is no longer in progress: start again"
	case LoginExpired:
		return "this sign-in expired: start again"
	case LoginDenied:
		return "sign-in was not approved: start again"
	case LoginCanceled:
		return "this sign-in was canceled: start again"
	case LoginSuperseded:
		return "a newer sign-in replaced this one"
	case LoginInvalidKey:
		return "paste a single API key without spaces"
	case LoginNotAPIKey:
		return "this is a sign-in token, not an API key: use Sign in instead"
	default:
		return "sign-in is not available right now: try again"
	}
}

// CodeAttempt is one authorization-code sign-in in progress (Anthropic or
// OpenAI). It holds the PKCE verifier, so it only lives in server memory: its
// fields are unexported, it has no JSON form, and only the authorize URL
// (state and challenge, never the verifier) is meant for the browser.
type CodeAttempt struct {
	provider     string
	state        string
	verifier     string
	authorizeURL string
	deadline     time.Time
}

// AuthorizeURL is the provider page the user opens to approve the sign-in.
func (a *CodeAttempt) AuthorizeURL() string { return a.authorizeURL }

// Deadline is when the attempt stops being completable.
func (a *CodeAttempt) Deadline() time.Time { return a.deadline }

// BeginAnthropic starts an Anthropic sign-in. Nothing listens anywhere: the
// user approves in a browser and pastes back the code#state Anthropic shows,
// or the callback URL.
func BeginAnthropic() (*CodeAttempt, error) { return beginCodeAttempt("anthropic", time.Now()) }

// BeginOpenAI starts an OpenAI sign-in. The browser ends on
// http://localhost:1455/auth/callback; that full URL is what gets pasted
// back, whether or not anything listens there.
func BeginOpenAI() (*CodeAttempt, error) { return beginCodeAttempt("openai", time.Now()) }

// CompleteAnthropic validates the pasted value against a and exchanges the
// code at the pinned token endpoint. It never reads stdin or opens a browser.
func CompleteAnthropic(ctx context.Context, a *CodeAttempt, input string) (*OAuthCredentials, error) {
	return completeBeforeDeadline(ctx, "anthropic", a, input)
}

// CompleteOpenAI is CompleteAnthropic for OpenAI: input must be the full
// http://localhost:1455/auth/callback?code=...&state=... URL.
func CompleteOpenAI(ctx context.Context, a *CodeAttempt, input string) (*OAuthCredentials, error) {
	return completeBeforeDeadline(ctx, "openai", a, input)
}

func completeBeforeDeadline(ctx context.Context, provider string, a *CodeAttempt, input string) (*OAuthCredentials, error) {
	if a == nil || a.provider != provider {
		return nil, loginError(provider, LoginNotFound)
	}
	if !time.Now().Before(a.deadline) {
		return nil, loginError(provider, LoginExpired)
	}
	return completeCodeAttempt(ctx, tokenEndpoints{}, a, input)
}

func beginCodeAttempt(provider string, now time.Time) (*CodeAttempt, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, loginError(provider, LoginUnavailable)
	}
	// Independent of the verifier (as in the official Claude Code CLI), so
	// the verifier never reaches the browser or its history.
	state, err := randomToken(32)
	if err != nil {
		return nil, loginError(provider, LoginUnavailable)
	}
	a := &CodeAttempt{provider: provider, state: state, verifier: verifier, deadline: now.Add(loginAttemptTTL)}
	switch provider {
	case "anthropic":
		a.authorizeURL = authorizeURL + "?" + url.Values{
			"code":                  {"true"},
			"client_id":             {clientID},
			"response_type":         {"code"},
			"redirect_uri":          {redirectURI},
			"scope":                 {scopes},
			"code_challenge":        {challenge},
			"code_challenge_method": {"S256"},
			"state":                 {state},
		}.Encode()
	case "openai":
		a.authorizeURL = openaiAuthorizeURL + "?" + url.Values{
			"response_type":              {"code"},
			"client_id":                  {openaiClientID},
			"redirect_uri":               {openaiRedirectURI},
			"scope":                      {openaiScopes},
			"code_challenge":             {challenge},
			"code_challenge_method":      {"S256"},
			"state":                      {state},
			"id_token_add_organizations": {"true"},
			"codex_cli_simplified_flow":  {"true"},
			"originator":                 {"moa"},
		}.Encode()
	default:
		return nil, loginError(provider, LoginUnsupported)
	}
	return a, nil
}

// completeCodeAttempt parses and matches input, then exchanges the code. ep
// is zero in production (pinned endpoints); same-package tests point it at
// httptest servers.
func completeCodeAttempt(ctx context.Context, ep tokenEndpoints, a *CodeAttempt, input string) (*OAuthCredentials, error) {
	cb, err := parseCallback(a.provider, input)
	if err != nil {
		return nil, err
	}
	if err := a.match(cb); err != nil {
		return nil, err
	}
	if cb.denied {
		return nil, loginError(a.provider, LoginDenied)
	}
	return exchangeCode(ctx, ep, a, cb.code)
}

func exchangeCode(ctx context.Context, ep tokenEndpoints, a *CodeAttempt, code string) (*OAuthCredentials, error) {
	if a.provider == "anthropic" {
		return exchangeAnthropicCode(ctx, ep.client, ep.anthropic, code, a.state, a.verifier)
	}
	return exchangeOpenAICode(ctx, ep.client, ep.openai, code, a.verifier)
}

// match compares the pasted state with the attempt's in constant time.
func (a *CodeAttempt) match(cb pastedCallback) error {
	if subtle.ConstantTimeCompare([]byte(cb.state), []byte(a.state)) != 1 {
		return loginError(a.provider, LoginMismatch)
	}
	return nil
}

// pastedCallback is what a pasted value carries. denied means the provider
// answered with an OAuth error instead of a code.
type pastedCallback struct {
	code, state string
	denied      bool
}

// parseCallback strictly parses a pasted sign-in result. State is mandatory,
// URLs must be the provider's exact redirect (parsed, never fetched), and any
// ambiguity is refused rather than guessed. Errors never include the input.
//
// Anthropic: code#state, or https://console.anthropic.com/oauth/code/callback
// carrying code and state in its query or (exclusively) its fragment.
// OpenAI: only http://localhost:1455/auth/callback?code=...&state=...
func parseCallback(provider, raw string) (pastedCallback, error) {
	invalid := loginError(provider, LoginInvalidInput)
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxPastedCallback || !printableASCII(raw) {
		return pastedCallback{}, invalid
	}
	switch provider {
	case "anthropic":
		if !strings.Contains(raw, "://") {
			return codeHashState(raw, invalid)
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host != "console.anthropic.com" || !exactRedirect(u, "/oauth/code/callback") {
			return pastedCallback{}, invalid
		}
		switch {
		case u.RawQuery != "" && strings.Contains(raw, "#"):
			return pastedCallback{}, invalid // payload in both places: ambiguous
		case u.RawQuery != "":
			return callbackValues(u.RawQuery, "", invalid)
		case strings.Contains(u.EscapedFragment(), "="):
			return callbackValues(u.EscapedFragment(), "", invalid)
		case u.Fragment != "":
			return codeHashState(u.Fragment, invalid)
		}
		return pastedCallback{}, invalid
	case "openai":
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" || u.Host != "localhost:1455" || !exactRedirect(u, "/auth/callback") ||
			strings.Contains(raw, "#") || u.RawQuery == "" {
			return pastedCallback{}, invalid
		}
		return callbackValues(u.RawQuery, openaiIssuer, invalid)
	}
	return pastedCallback{}, loginError(provider, LoginUnsupported)
}

func exactRedirect(u *url.URL, path string) bool {
	return u.Opaque == "" && u.User == nil && u.EscapedPath() == path
}

func codeHashState(s string, invalid error) (pastedCallback, error) {
	parts := strings.Split(s, "#")
	if len(parts) != 2 || !validCallbackToken(parts[0]) || !validCallbackToken(parts[1]) {
		return pastedCallback{}, invalid
	}
	return pastedCallback{code: parts[0], state: parts[1]}, nil
}

// callbackValues reads code/state/error/iss from a query string. Each may
// appear at most once; malformed encoding (including ';' separators) fails.
// An error parameter is a denial even when a code is present. issuer is the
// only acceptable iss; "" means none is known, so any iss is refused.
func callbackValues(rawQuery, issuer string, invalid error) (pastedCallback, error) {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return pastedCallback{}, invalid
	}
	for _, k := range []string{"code", "state", "error", "iss"} {
		if len(q[k]) > 1 {
			return pastedCallback{}, invalid
		}
	}
	cb := pastedCallback{state: q.Get("state")}
	if !validCallbackToken(cb.state) {
		return pastedCallback{}, invalid
	}
	if iss, ok := q["iss"]; ok && (issuer == "" || iss[0] != issuer) {
		return pastedCallback{}, invalid
	}
	if _, ok := q["error"]; ok {
		cb.denied = true
		return cb, nil
	}
	cb.code = q.Get("code")
	if !validCallbackToken(cb.code) {
		return pastedCallback{}, invalid
	}
	return cb, nil
}

// validCallbackToken accepts a non-empty code or state of visible ASCII
// without '#', the code#state separator.
func validCallbackToken(s string) bool {
	return s != "" && printableASCII(s) && !strings.Contains(s, "#")
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// tokenClient returns client (or the shared OAuth client) with redirects
// disabled: a token POST redirected elsewhere would replay the code,
// verifier or refresh token to that destination. The 3xx is then handled as
// an unexpected status.
func tokenClient(client *http.Client) *http.Client {
	if client == nil {
		client = oauthClient
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

// classifyLoginError keeps classified errors and maps anything else from a
// token exchange or device flow to a class without its text, which can carry
// endpoint URLs.
func classifyLoginError(provider string, err error) error {
	var le *LoginError
	if errors.As(err, &le) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return oauthTransportError(provider, "login")
	}
	out := classifyRefreshError(provider, err)
	if pe, ok := core.AsProviderCredentialError(out); ok {
		pe.Operation = "login"
	}
	return out
}
