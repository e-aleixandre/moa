package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	callbackPort = 54545
	callbackPath = "/callback"
	scopes       = "org:create_api_key user:profile user:inference"
)

// OAuthCredentials holds the result of an OAuth login/refresh.
type OAuthCredentials struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"` // Unix milliseconds
}

// LoginAnthropic runs the full OAuth PKCE flow:
// 1. Start local callback server
// 2. Open browser to Anthropic authorize URL
// 3. Wait for user to approve and redirect back
// 4. Exchange authorization code for tokens
func LoginAnthropic(ctx context.Context) (*OAuthCredentials, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	// Generate a random state parameter for CSRF protection
	oauthState, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("generating state: %w", err)
	}

	// Start local callback server — bind to localhost only
	codeCh := make(chan callbackResult, 1)
	redirectURI := fmt.Sprintf("http://localhost:%d%s", callbackPort, callbackPath)

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort))
	if err != nil {
		return nil, fmt.Errorf("starting callback server on port %d: %w", callbackPort, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errParam := r.URL.Query().Get("error")

		if errParam != "" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, "<html><body><h2>Authentication failed</h2><p>%s</p><p>You can close this tab.</p></body></html>", errParam)
			codeCh <- callbackResult{err: fmt.Errorf("OAuth error: %s", errParam)}
			return
		}

		// Validate state to prevent CSRF attacks
		if state != oauthState {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body><h2>Authentication failed</h2><p>Invalid state parameter.</p></body></html>")
			codeCh <- callbackResult{err: fmt.Errorf("OAuth state mismatch (possible CSRF)")}
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h2>✓ Authentication successful</h2><p>You can close this tab and return to the terminal.</p></body></html>")
		codeCh <- callbackResult{code: code}
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	// Build authorize URL
	params := url.Values{
		"code":                  {"true"},
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {oauthState},
	}
	authURL := authorizeURL + "?" + params.Encode()

	// Open browser
	fmt.Println("\nOpening browser for Anthropic authentication...")
	fmt.Printf("If the browser doesn't open, visit:\n%s\n\n", authURL)
	openBrowser(authURL)

	// Wait for callback or context cancellation
	var result callbackResult
	select {
	case result = <-codeCh:
		if result.err != nil {
			return nil, result.err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("login timed out after 5 minutes")
	}

	// Exchange code for tokens
	return exchangeToken(result.code, oauthState, redirectURI, verifier)
}

// RefreshAnthropicToken refreshes an expired OAuth token.
func RefreshAnthropicToken(refreshToken string) (*OAuthCredentials, error) {
	body := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}

	resp, err := http.PostForm(tokenURL, body)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

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

type callbackResult struct {
	code string
	err  error
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeToken(code, state, redirectURI, verifier string) (*OAuthCredentials, error) {
	payload := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(tokenURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

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

// generateState creates a random state token for CSRF protection.
func generateState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
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

// openBrowser opens a URL in the default browser.
func openBrowser(url string) {
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
