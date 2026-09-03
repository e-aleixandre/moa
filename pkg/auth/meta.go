package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The Muse Code launcher (muse-launcher.sh) pins these endpoints and client
// ID. They are undocumented: Meta documents only API-key access, so a
// subscription login is best-effort and may need updating if Muse Code moves.
const (
	metaAuthURL       = "https://auth.meta.com"
	metaDeviceAuthURL = metaAuthURL + "/oidc/device/authorization/"
	metaTokenURL      = metaAuthURL + "/oidc/device/token/"
	metaClientID      = "1031625952748946"
	metaMintURL       = "https://api.meta.ai/muse-code/key"
	metaUserAgent     = "moa muse-code/launcher-2"
)

// MetaDeviceCode is the user-facing part of a device authorization request.
type MetaDeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               time.Duration
	Interval                time.Duration
}

// MetaCredentials is a completed Muse subscription login: the OAuth tokens
// plus the Model API key minted from them. The key — not the access token — is
// what /v1/responses accepts.
type MetaCredentials struct {
	OAuthCredentials
	APIKey string
}

// metaEndpoints lets tests point the flow at an httptest server. Production
// leaves it zero, which pins every request to auth.meta.com / api.meta.ai.
type metaEndpoints struct {
	DeviceAuthURL string
	TokenURL      string
	MintURL       string
	Wait          func(context.Context, time.Duration) error
}

func (e metaEndpoints) deviceAuth() string {
	if e.DeviceAuthURL != "" {
		return e.DeviceAuthURL
	}
	return metaDeviceAuthURL
}
func (e metaEndpoints) token() string {
	if e.TokenURL != "" {
		return e.TokenURL
	}
	return metaTokenURL
}
func (e metaEndpoints) mint() string {
	if e.MintURL != "" {
		return e.MintURL
	}
	return metaMintURL
}
func (e metaEndpoints) wait() func(context.Context, time.Duration) error {
	if e.Wait != nil {
		return e.Wait
	}
	return func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
}

// metaHTTPClient never follows redirects: an OAuth token request redirected
// elsewhere would leak the device code or the token to that host.
func metaHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = oauthClient
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

func metaForm(ctx context.Context, client *http.Client, endpoint string, body url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", metaUserAgent)
	return metaHTTPClient(client).Do(req)
}

// LoginMeta runs the Muse device flow and mints a Model API key from the
// resulting access token.
func LoginMeta(ctx context.Context, openURL func(string), display func(*MetaDeviceCode)) (*MetaCredentials, error) {
	return loginMeta(ctx, nil, metaEndpoints{}, openURL, display)
}

func loginMeta(ctx context.Context, client *http.Client, endpoints metaEndpoints, openURL func(string), display func(*MetaDeviceCode)) (*MetaCredentials, error) {
	device, err := startMetaDeviceFlow(ctx, client, endpoints)
	if err != nil {
		return nil, err
	}
	if display != nil {
		display(device)
	}
	if openURL != nil {
		u := device.VerificationURIComplete
		if u == "" {
			u = device.VerificationURI
		}
		openURL(u)
	}
	tokens, err := completeMetaDeviceFlow(ctx, client, endpoints, device)
	if err != nil {
		return nil, err
	}
	key, err := mintMetaAPIKey(ctx, client, endpoints, tokens.Access)
	if err != nil {
		return nil, err
	}
	return &MetaCredentials{OAuthCredentials: *tokens, APIKey: key}, nil
}

func startMetaDeviceFlow(ctx context.Context, client *http.Client, endpoints metaEndpoints) (*MetaDeviceCode, error) {
	resp, err := metaForm(ctx, client, endpoints.deviceAuth(), url.Values{"client_id": {metaClientID}})
	if err != nil {
		return nil, fmt.Errorf("device authorization request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorization failed (HTTP %d)", resp.StatusCode)
	}
	var raw struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing device authorization response: %w", err)
	}
	if raw.DeviceCode == "" || raw.UserCode == "" || raw.VerificationURI == "" || raw.ExpiresIn <= 0 {
		return nil, fmt.Errorf("device authorization response is incomplete")
	}
	interval := time.Duration(raw.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &MetaDeviceCode{
		DeviceCode: raw.DeviceCode, UserCode: raw.UserCode,
		VerificationURI: raw.VerificationURI, VerificationURIComplete: raw.VerificationURIComplete,
		ExpiresIn: time.Duration(raw.ExpiresIn) * time.Second, Interval: interval,
	}, nil
}

func completeMetaDeviceFlow(ctx context.Context, client *http.Client, endpoints metaEndpoints, device *MetaDeviceCode) (*OAuthCredentials, error) {
	if device == nil || device.DeviceCode == "" {
		return nil, fmt.Errorf("missing device authorization")
	}
	ctx, cancel := context.WithTimeout(ctx, device.ExpiresIn)
	defer cancel()
	wait := endpoints.wait()
	interval := device.Interval
	body := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {device.DeviceCode},
		"client_id":   {metaClientID},
	}
	for {
		if err := wait(ctx, interval); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("device authorization expired")
			}
			return nil, err
		}
		resp, err := metaForm(ctx, client, endpoints.token(), body)
		if err != nil {
			return nil, fmt.Errorf("device token request: %w", err)
		}
		var token struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
			Error        string `json:"error"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&token)
		status := resp.StatusCode
		resp.Body.Close() //nolint:errcheck
		if decodeErr != nil {
			return nil, fmt.Errorf("parsing device token response: %w", decodeErr)
		}
		if status == http.StatusOK {
			return metaCredentials(token.AccessToken, token.RefreshToken, "", token.ExpiresIn)
		}
		switch token.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return nil, fmt.Errorf("device authorization denied")
		case "expired_token":
			return nil, fmt.Errorf("device authorization expired")
		default:
			return nil, fmt.Errorf("device token request failed (HTTP %d)", status)
		}
	}
}

func metaCredentials(access, refresh, previousRefresh string, expiresIn int) (*OAuthCredentials, error) {
	if access == "" || expiresIn <= 0 {
		return nil, fmt.Errorf("token response is incomplete")
	}
	if refresh == "" {
		refresh = previousRefresh
	}
	if refresh == "" {
		return nil, fmt.Errorf("token response is incomplete")
	}
	return &OAuthCredentials{
		Access:  access,
		Refresh: refresh,
		Expires: time.Now().Add(time.Duration(expiresIn)*time.Second - 5*time.Minute).UnixMilli(),
	}, nil
}

// mintMetaAPIKey exchanges an OAuth access token for a Model API key, the way
// the Muse Code client does. The request body is undocumented; an empty POST
// is what the endpoint's own error responses suggest, and this could not be
// live-tested without a Muse subscription.
func mintMetaAPIKey(ctx context.Context, client *http.Client, endpoints metaEndpoints, accessToken string) (string, error) {
	if accessToken == "" {
		return "", fmt.Errorf("missing Meta access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.mint(), nil)
	if err != nil {
		return "", fmt.Errorf("building key request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-version", "1.0.0")
	req.Header.Set("User-Agent", metaUserAgent)
	resp, err := metaHTTPClient(client).Do(req)
	if err != nil {
		return "", fmt.Errorf("key request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponse))
	if err != nil {
		return "", fmt.Errorf("reading key response: %w", err)
	}
	var minted struct {
		APIKey string `json:"api_key"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &minted)
	if resp.StatusCode != http.StatusOK {
		// The one error code the Muse client names explicitly deserves a
		// message the user can act on; everything else stays generic.
		if minted.Error.Code == "MODEL_API__ONBOARDING_REQUIRED" {
			return "", fmt.Errorf("your Meta account isn't set up for Model API yet")
		}
		return "", fmt.Errorf("could not obtain a Meta Model API key (HTTP %d)", resp.StatusCode)
	}
	if minted.APIKey == "" {
		return "", fmt.Errorf("key response is missing api_key")
	}
	return minted.APIKey, nil
}

// RefreshMetaToken renews a Muse subscription login and re-mints its Model API
// key. The refresh grant is unverified — the launcher never refreshes — so a
// failure asks for a fresh login instead of guessing further.
func RefreshMetaToken(refreshToken string) (*MetaCredentials, error) {
	return refreshMetaToken(context.Background(), nil, metaEndpoints{}, refreshToken)
}

func refreshMetaToken(ctx context.Context, client *http.Client, endpoints metaEndpoints, refreshToken string) (*MetaCredentials, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("missing refresh token")
	}
	resp, err := metaForm(ctx, client, endpoints.token(), url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {metaClientID},
	})
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&token)
	status := resp.StatusCode
	resp.Body.Close() //nolint:errcheck
	if status != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (HTTP %d)", status)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("parsing refresh response: %w", decodeErr)
	}
	creds, err := metaCredentials(token.AccessToken, token.RefreshToken, refreshToken, token.ExpiresIn)
	if err != nil {
		return nil, err
	}
	key, err := mintMetaAPIKey(ctx, client, endpoints, creds.Access)
	if err != nil {
		return nil, err
	}
	return &MetaCredentials{OAuthCredentials: *creds, APIKey: key}, nil
}
