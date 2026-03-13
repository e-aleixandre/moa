package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	// Anthropic OAuth endpoints
	clientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	authorizeURL = "https://claude.ai/oauth/authorize"
	tokenURL     = "https://console.anthropic.com/v1/oauth/token"
	redirectURI  = "https://console.anthropic.com/oauth/code/callback"
	scopes       = "org:create_api_key user:profile user:inference"
)

var oauthClient = &http.Client{Timeout: 30 * time.Second}

// OAuthCredentials holds the result of an OAuth login/refresh.
type OAuthCredentials struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`              // Unix milliseconds
	AccountID string `json:"account_id,omitempty"` // OpenAI chatgpt_account_id
}

// LoginAnthropic runs the Anthropic OAuth PKCE flow (device code style):
// 1. Generate PKCE verifier + challenge
// 2. Open browser to Anthropic authorize URL
// 3. User approves and sees a code on Anthropic's callback page
// 4. User pastes the code back into the CLI
// 5. Exchange code for tokens
//
// promptCode is called to get the authorization code from the user.
// It receives the auth URL (for display) and should return the pasted code string.
func LoginAnthropic(openURL func(string), promptCode func() (string, error)) (*OAuthCredentials, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	// Generate a separate state parameter (not reusing PKCE verifier)
	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generating state: %w", err)
	}

	// Build authorize URL
	params := url.Values{
		"code":                  {"true"},
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	authURL := authorizeURL + "?" + params.Encode()

	// Open browser
	openURL(authURL)

	// Wait for user to paste the authorization code or callback URL
	raw, err := promptCode()
	if err != nil {
		return nil, fmt.Errorf("reading auth code: %w", err)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty authorization code")
	}

	// Parse input: supports callback URL (?code=...&state=...) or code#state
	code, returnedState := parseAuthInput(raw)

	// Validate state to prevent CSRF
	if returnedState != state {
		return nil, fmt.Errorf("authorization failed: state mismatch — paste the full callback URL or code#state value and retry")
	}

	// Exchange token using our original state (trusted local value)
	return exchangeToken(code, state, verifier)
}

// RefreshAnthropicToken refreshes an expired OAuth token.
func RefreshAnthropicToken(refreshToken string) (*OAuthCredentials, error) {
	body := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}

	resp, err := oauthClient.PostForm(tokenURL, body)
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

	return &OAuthCredentials{
		Access:  tokenResp.AccessToken,
		Refresh: tokenResp.RefreshToken,
		Expires: time.Now().UnixMilli() + int64(tokenResp.ExpiresIn)*1000 - 5*60*1000, // 5 min buffer
	}, nil
}

// --- internal ---

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeToken(code, state, verifier string) (*OAuthCredentials, error) {
	payload := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling token request: %w", err)
	}

	req, err := http.NewRequest("POST", tokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := oauthClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
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

	return &OAuthCredentials{
		Access:  tokenResp.AccessToken,
		Refresh: tokenResp.RefreshToken,
		Expires: time.Now().UnixMilli() + int64(tokenResp.ExpiresIn)*1000 - 5*60*1000,
	}, nil
}

// parseAuthInput extracts code and state from user input.
// Supports:
//  1. Full callback URL: https://...?code=ABC&state=XYZ
//  2. code#state format: ABC#XYZ
//
// Returns empty state if format is unrecognized (state validation will reject it).
func parseAuthInput(raw string) (code, state string) {
	// Try URL format first
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		q := u.Query()
		if c := q.Get("code"); c != "" {
			return c, q.Get("state")
		}
	}

	// Try code#state format
	if parts := strings.SplitN(raw, "#", 2); len(parts) == 2 {
		return parts[0], parts[1]
	}

	// Unrecognized format — return empty state (validation will reject)
	return raw, ""
}

// generatePKCE creates a PKCE verifier and S256 challenge.
func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// randomState generates a random state parameter for OAuth CSRF protection.
func randomState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// OpenBrowser opens a URL in the default browser.
func OpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	_ = cmd.Start()
}
