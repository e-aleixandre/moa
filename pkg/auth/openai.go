package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	openaiClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openaiAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	openaiTokenURL     = "https://auth.openai.com/oauth/token"
	openaiRedirectURI  = "http://localhost:1455/auth/callback"
	openaiScopes       = "openid profile email offline_access"
	openaiJWTClaimPath = "https://api.openai.com/auth"
)

// LoginOpenAI runs the OpenAI PKCE sign-in for the CLI. A loopback listener
// on 127.0.0.1:1455 catches the browser redirect when moa runs on this
// machine; otherwise, after 60 seconds (or if the port is busy), promptCode
// returns the full callback URL the user copied from the browser.
func LoginOpenAI(openURL func(string), promptCode func() (string, error)) (*OAuthCredentials, error) {
	a, err := BeginOpenAI()
	if err != nil {
		return nil, err
	}
	server, err := startCallbackServer(a)
	if err != nil {
		openURL(a.AuthorizeURL())
		return openaiPromptedCallback(a, promptCode)
	}
	defer server.Close()
	openURL(a.AuthorizeURL())
	if callback := server.WaitForCallback(60 * time.Second); callback != "" {
		return CompleteOpenAI(context.Background(), a, callback)
	}
	return openaiPromptedCallback(a, promptCode)
}

func openaiPromptedCallback(a *CodeAttempt, promptCode func() (string, error)) (*OAuthCredentials, error) {
	raw, err := promptCode()
	if err != nil {
		return nil, fmt.Errorf("reading auth code: %w", err)
	}
	return CompleteOpenAI(context.Background(), a, raw)
}

// exchangeOpenAICode redeems an authorization code; client and endpoint are
// empty in production (pinned). The account comes from the access JWT.
func exchangeOpenAICode(ctx context.Context, client *http.Client, endpoint, code, verifier string) (*OAuthCredentials, error) {
	if endpoint == "" {
		endpoint = openaiTokenURL
	}
	resp, err := postForm(ctx, client, endpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {openaiClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {openaiRedirectURI},
	})
	if err != nil {
		return nil, oauthTransportError("openai", "login")
	}
	defer resp.Body.Close() //nolint:errcheck
	tok, err := decodeLoginToken("openai", resp)
	if err != nil {
		return nil, err
	}
	accountID := extractOpenAIAccountID(tok.AccessToken)
	expires, ok := openaiExpiry(tok)
	if accountID == "" || !ok {
		return nil, providerProtocolError("openai", "login")
	}
	return &OAuthCredentials{
		Access:    tok.AccessToken,
		Refresh:   tok.RefreshToken,
		Expires:   expires,
		AccountID: accountID,
	}, nil
}

// RefreshOpenAIToken refreshes an expired OpenAI OAuth token.
func RefreshOpenAIToken(refreshToken string) (*OAuthCredentials, error) {
	return refreshOpenAIToken(context.Background(), nil, "", refreshToken)
}

// refreshOpenAIToken takes the client and token URL so same-package tests can
// reach an httptest server; empty values pin production.
func refreshOpenAIToken(ctx context.Context, client *http.Client, endpoint, refreshToken string) (*OAuthCredentials, error) {
	if client == nil {
		client = oauthClient
	}
	if endpoint == "" {
		endpoint = openaiTokenURL
	}
	body := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {openaiClientID},
	}

	resp, err := postForm(ctx, client, endpoint, body)
	if err != nil {
		return nil, oauthTransportError("openai", "refresh")
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, oauthStatusError("openai", "refresh", resp.StatusCode, resp.Body)
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&tokenResp); err != nil {
		return nil, providerProtocolError("openai", "refresh")
	}

	accountID := extractOpenAIAccountID(tokenResp.AccessToken)
	if accountID == "" {
		return nil, providerProtocolError("openai", "refresh")
	}

	// Without any lifetime the token is stored as already due, so the next
	// use renews it, as before.
	expires, _ := openaiExpiry(tokenResp)
	return &OAuthCredentials{
		Access:    tokenResp.AccessToken,
		Refresh:   tokenResp.RefreshToken,
		Expires:   expires,
		AccountID: accountID,
	}, nil
}

// openaiExpiry returns when tok's access token is due for renewal (unix ms,
// five minutes early): from expires_in when sent, otherwise from the access
// JWT's exp, as the official client does not require expires_in. ok is false
// when neither yields a future lifetime.
func openaiExpiry(tok tokenResponse) (int64, bool) {
	const margin = 5 * time.Minute
	now := time.Now()
	if tok.ExpiresIn > 0 {
		return now.Add(time.Duration(tok.ExpiresIn)*time.Second - margin).UnixMilli(), true
	}
	exp, _ := openaiJWTClaims(tok.AccessToken)["exp"].(float64)
	if end := time.Unix(int64(exp), 0); exp > 0 && end.After(now) {
		return end.Add(-margin).UnixMilli(), true
	}
	return now.Add(-margin).UnixMilli(), false
}

// extractOpenAIAccountID decodes the JWT and extracts the chatgpt_account_id.
func extractOpenAIAccountID(token string) string {
	auth, ok := openaiJWTClaims(token)[openaiJWTClaimPath].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return id
}

// openaiJWTClaims decodes an access JWT's payload without verifying it; nil
// when it is not a JWT.
func openaiJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	// JWT payload is base64url-encoded (may be missing padding).
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil
	}
	return claims
}

// --- Local callback server ---

type callbackServer struct {
	server     *http.Server
	callbackCh chan string
}

// startCallbackServer catches the browser redirect for a. The request is
// rebuilt as the exact redirect URL and goes through the same strict parser
// and state check as a pasted one, so the listener accepts nothing a paste
// would not.
func startCallbackServer(a *CodeAttempt) (*callbackServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return nil, fmt.Errorf("binding :1455: %w", err)
	}

	cs := &callbackServer{
		callbackCh: make(chan string, 1),
	}

	mux := http.NewServeMux()
	mux.Handle("/auth/callback", callbackHandler(a, cs.callbackCh))

	cs.server = &http.Server{Handler: mux}
	go func() {
		_ = cs.server.Serve(listener)
	}()

	return cs, nil
}

// callbackHandler forwards the rebuilt redirect URL of a matching browser
// callback (including a denial, which Complete reports) on ch.
func callbackHandler(a *CodeAttempt, ch chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callback := "http://localhost:1455" + r.URL.RequestURI()
		cb, err := parseCallback("openai", callback)
		if err == nil {
			err = a.match(cb)
		}
		if err != nil {
			http.Error(w, "This sign-in link does not match. Return to your terminal.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><p>Return to your terminal.</p></body></html>`)
		select {
		case ch <- callback:
		default:
		}
	})
}

func (cs *callbackServer) WaitForCallback(timeout time.Duration) string {
	select {
	case callback := <-cs.callbackCh:
		return callback
	case <-time.After(timeout):
		return ""
	}
}

func (cs *callbackServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cs.server.Shutdown(ctx)
}
