package auth

import (
	"bytes"
	"context"
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
	clientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
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
	// APIKey is a provider key derived from the OAuth session instead of the
	// access token: Meta mints a Model API key, and api.meta.ai does not
	// accept the access token itself.
	APIKey string `json:"api_key,omitempty"`
}

// LoginAnthropic runs the Anthropic OAuth PKCE sign-in for the CLI: it opens
// the authorize URL, then promptCode returns what the user pasted from
// Anthropic's page (code#state or the callback URL; state is required).
func LoginAnthropic(openURL func(string), promptCode func() (string, error)) (*OAuthCredentials, error) {
	a, err := BeginAnthropic()
	if err != nil {
		return nil, err
	}
	openURL(a.AuthorizeURL())
	raw, err := promptCode()
	if err != nil {
		return nil, fmt.Errorf("reading auth code: %w", err)
	}
	return CompleteAnthropic(context.Background(), a, raw)
}

// RefreshAnthropicToken refreshes an expired OAuth token.
func RefreshAnthropicToken(refreshToken string) (*OAuthCredentials, error) {
	return refreshAnthropicToken(context.Background(), nil, "", refreshToken)
}

// refreshAnthropicToken takes the client and token URL so same-package tests
// can reach an httptest server; empty values pin production.
func refreshAnthropicToken(ctx context.Context, client *http.Client, endpoint, refreshToken string) (*OAuthCredentials, error) {
	if client == nil {
		client = oauthClient
	}
	if endpoint == "" {
		endpoint = tokenURL
	}
	body := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}

	resp, err := postForm(ctx, client, endpoint, body)
	if err != nil {
		return nil, oauthTransportError("anthropic", "refresh")
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, oauthStatusError("anthropic", "refresh", resp.StatusCode, resp.Body)
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&tokenResp); err != nil || tokenResp.AccessToken == "" {
		return nil, providerProtocolError("anthropic", "refresh")
	}

	return &OAuthCredentials{
		Access:  tokenResp.AccessToken,
		Refresh: tokenResp.RefreshToken,
		Expires: time.Now().UnixMilli() + int64(tokenResp.ExpiresIn)*1000 - 5*60*1000, // 5 min buffer
	}, nil
}

// --- internal ---

func postForm(ctx context.Context, client *http.Client, endpoint string, body url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return tokenClient(client).Do(req)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// exchangeAnthropicCode redeems an authorization code. The JSON body carries
// the attempt's state and verifier as separate fields, as the official CLI
// does; client and endpoint are empty in production (pinned).
func exchangeAnthropicCode(ctx context.Context, client *http.Client, endpoint, code, state, verifier string) (*OAuthCredentials, error) {
	if endpoint == "" {
		endpoint = tokenURL
	}
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	})
	if err != nil {
		return nil, providerProtocolError("anthropic", "login")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, providerProtocolError("anthropic", "login")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := tokenClient(client).Do(req)
	if err != nil {
		return nil, oauthTransportError("anthropic", "login")
	}
	defer resp.Body.Close() //nolint:errcheck
	tok, err := decodeLoginToken("anthropic", resp)
	if err != nil {
		return nil, err
	}
	if tok.ExpiresIn <= 0 {
		return nil, providerProtocolError("anthropic", "login")
	}
	return &OAuthCredentials{
		Access:  tok.AccessToken,
		Refresh: tok.RefreshToken,
		Expires: time.Now().UnixMilli() + int64(tok.ExpiresIn)*1000 - 5*60*1000,
	}, nil
}

// decodeLoginToken reads a bounded code-exchange response. A sign-in must
// yield an access token and a refresh token; anything else is a protocol
// error rather than a credential that cannot be renewed. Where the lifetime
// comes from is provider-specific, so each exchange checks it.
func decodeLoginToken(provider string, resp *http.Response) (tokenResponse, error) {
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, oauthStatusError(provider, "login", resp.StatusCode, resp.Body)
	}
	var tok tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&tok); err != nil ||
		tok.AccessToken == "" || tok.RefreshToken == "" {
		return tokenResponse{}, providerProtocolError(provider, "login")
	}
	return tok, nil
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
