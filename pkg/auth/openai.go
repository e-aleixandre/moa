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
	openaiClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	openaiAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	openaiTokenURL     = "https://auth.openai.com/oauth/token"
	openaiRedirectURI  = "http://localhost:1455/auth/callback"
	openaiScopes       = "openid profile email offline_access"
	openaiJWTClaimPath = "https://api.openai.com/auth"
)

// LoginOpenAI runs the OpenAI PKCE OAuth flow with a local callback server.
// Returns credentials including the accountId extracted from the JWT.
//
// openURL is called to open the browser. promptCode is the fallback if the
// local server doesn't receive the callback.
func LoginOpenAI(openURL func(string), promptCode func() (string, error)) (*OAuthCredentials, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generating state: %w", err)
	}

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {openaiClientID},
		"redirect_uri":          {openaiRedirectURI},
		"scope":                 {openaiScopes},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":            {"moa"},
	}
	authURL := openaiAuthorizeURL + "?" + params.Encode()

	// Start local callback server.
	server, err := startCallbackServer(state)
	if err != nil {
		// Fall back to manual paste if server fails.
		openURL(authURL)
		return openaiManualFlow(promptCode, state, verifier)
	}
	defer server.Close()

	openURL(authURL)

	// Wait for browser callback (up to 60s).
	code := server.WaitForCode(60 * time.Second)

	if code == "" {
		// Fallback: ask user to paste.
		raw, err := promptCode()
		if err != nil {
			return nil, fmt.Errorf("reading auth code: %w", err)
		}
		code, returnedState := parseAuthInput(strings.TrimSpace(raw))
		if returnedState != "" && returnedState != state {
			return nil, fmt.Errorf("state mismatch")
		}
		if code == "" {
			return nil, fmt.Errorf("empty authorization code")
		}
	}

	return openaiExchangeToken(code, verifier)
}

func openaiManualFlow(promptCode func() (string, error), state, verifier string) (*OAuthCredentials, error) {
	raw, err := promptCode()
	if err != nil {
		return nil, fmt.Errorf("reading auth code: %w", err)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty authorization code")
	}
	code, returnedState := parseAuthInput(raw)
	if returnedState != "" && returnedState != state {
		return nil, fmt.Errorf("state mismatch")
	}
	return openaiExchangeToken(code, verifier)
}

func openaiExchangeToken(code, verifier string) (*OAuthCredentials, error) {
	body := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {openaiClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {openaiRedirectURI},
	}

	resp, err := oauthClient.PostForm(openaiTokenURL, body)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	accountID := extractOpenAIAccountID(tokenResp.AccessToken)
	if accountID == "" {
		return nil, fmt.Errorf("failed to extract accountId from token")
	}

	return &OAuthCredentials{
		Access:    tokenResp.AccessToken,
		Refresh:   tokenResp.RefreshToken,
		Expires:   time.Now().UnixMilli() + int64(tokenResp.ExpiresIn)*1000 - 5*60*1000,
		AccountID: accountID,
	}, nil
}

// RefreshOpenAIToken refreshes an expired OpenAI OAuth token.
func RefreshOpenAIToken(refreshToken string) (*OAuthCredentials, error) {
	body := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {openaiClientID},
	}

	resp, err := oauthClient.PostForm(openaiTokenURL, body)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("refresh failed (HTTP %d): %s", resp.StatusCode, string(errBody))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("parsing refresh response: %w", err)
	}

	accountID := extractOpenAIAccountID(tokenResp.AccessToken)
	if accountID == "" {
		return nil, fmt.Errorf("failed to extract accountId from refreshed token")
	}

	return &OAuthCredentials{
		Access:    tokenResp.AccessToken,
		Refresh:   tokenResp.RefreshToken,
		Expires:   time.Now().UnixMilli() + int64(tokenResp.ExpiresIn)*1000 - 5*60*1000,
		AccountID: accountID,
	}, nil
}

// extractOpenAIAccountID decodes the JWT and extracts the chatgpt_account_id.
func extractOpenAIAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	// JWT payload is base64url-encoded (may be missing padding).
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}
	auth, ok := claims[openaiJWTClaimPath].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return id
}

// --- Local callback server ---

type callbackServer struct {
	server *http.Server
	codeCh chan string
}

func startCallbackServer(expectedState string) (*callbackServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return nil, fmt.Errorf("binding :1455: %w", err)
	}

	cs := &callbackServer{
		codeCh: make(chan string, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != expectedState {
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><p>Authentication successful. Return to your terminal.</p></body></html>`)

		select {
		case cs.codeCh <- code:
		default:
		}
	})

	cs.server = &http.Server{Handler: mux}
	go func() {
		_ = cs.server.Serve(listener)
	}()

	return cs, nil
}

func (cs *callbackServer) WaitForCode(timeout time.Duration) string {
	select {
	case code := <-cs.codeCh:
		return code
	case <-time.After(timeout):
		return ""
	}
}

func (cs *callbackServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cs.server.Shutdown(ctx)
}
