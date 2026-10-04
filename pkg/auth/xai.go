package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	xaiClientID      = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiIssuer        = "https://auth.x.ai"
	xaiDiscoveryURL  = xaiIssuer + "/.well-known/openid-configuration"
	xaiScopes        = "openid profile email offline_access grok-cli:access api:access"
	xaiReferrer      = "moa"
	maxOAuthResponse = 64 << 10
)

// XAIEndpoints is test configuration for the xAI OIDC client. Production must
// leave it empty: that pins discovery, issuer, and endpoints to auth.x.ai.
// A non-default endpoint requires an explicit host allowlist. AllowHTTP and
// Wait are intended only for local tests.
type XAIEndpoints struct {
	DiscoveryURL   string
	AllowedHosts   []string
	AllowedIssuers []string
	AllowHTTP      bool
	Wait           func(context.Context, time.Duration) error
	Now            func() time.Time
}

// XAIDeviceCode is the user-facing part of an RFC 8628 authorization request.
type XAIDeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               time.Duration
	Interval                time.Duration
	tokenEndpoint           string
	endpoints               XAIEndpoints
	issuedAt                time.Time
	wait                    func(context.Context, time.Duration) error
	now                     func() time.Time
}

type xaiDiscovery struct {
	Issuer                      string `json:"issuer"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}
type xaiTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

func xaiHTTPClient(client *http.Client, endpoints XAIEndpoints) *http.Client {
	if client == nil {
		client = oauthClient
	}
	clone := *client
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if len(via) == 0 || !sameOrigin(req.URL, via[0].URL) || !validXAIURL(req.URL, endpoints) {
			return fmt.Errorf("unsafe OIDC redirect")
		}
		return nil
	}
	return &clone
}
func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && a.Host == b.Host }
func xaiNow(endpoints XAIEndpoints) func() time.Time {
	if endpoints.Now != nil {
		return endpoints.Now
	}
	return time.Now
}
func xaiWait(endpoints XAIEndpoints) func(context.Context, time.Duration) error {
	if endpoints.Wait != nil {
		return endpoints.Wait
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
func allowedXAIHost(host string, endpoints XAIEndpoints) bool {
	if len(endpoints.AllowedHosts) == 0 {
		return host == "auth.x.ai"
	}
	for _, allowed := range endpoints.AllowedHosts {
		if host == allowed {
			return true
		}
	}
	return false
}
func allowedXAIIssuer(issuer string, endpoints XAIEndpoints) bool {
	if len(endpoints.AllowedIssuers) == 0 {
		return issuer == xaiIssuer
	}
	for _, allowed := range endpoints.AllowedIssuers {
		if issuer == allowed {
			return true
		}
	}
	return false
}
func validXAIURL(raw *url.URL, endpoints XAIEndpoints) bool {
	if raw == nil || raw.User != nil || raw.Fragment != "" || raw.Hostname() == "" {
		return false
	}
	if net.ParseIP(raw.Hostname()) != nil && len(endpoints.AllowedHosts) == 0 {
		return false
	}
	if raw.Scheme != "https" && (!endpoints.AllowHTTP || raw.Scheme != "http") {
		return false
	}
	return allowedXAIHost(raw.Hostname(), endpoints)
}
func parseXAIURL(raw string, endpoints XAIEndpoints) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !validXAIURL(u, endpoints) {
		return nil, fmt.Errorf("unsafe xAI endpoint")
	}
	return u, nil
}

// xaiVerificationHosts are the only browser destinations a verification
// link may point to. It is shown to the user and opened in a browser, never
// used for token exchange; an unexpected host fails closed until reviewed.
var xaiVerificationHosts = map[string]bool{"auth.x.ai": true, "accounts.x.ai": true, "x.ai": true}

// parseXAIVerificationURL validates a browser destination separately from the
// pinned OAuth protocol endpoints: HTTPS on an exact allowlisted host (no
// port, userinfo or fragment). Test configurations may add their own hosts.
func parseXAIVerificationURL(raw string, endpoints XAIEndpoints) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return nil, fmt.Errorf("unsafe xAI verification URL")
	}
	if u.Scheme == "https" && u.Port() == "" && xaiVerificationHosts[u.Host] {
		return u, nil
	}
	if (u.Scheme == "https" || (u.Scheme == "http" && endpoints.AllowHTTP)) && len(endpoints.AllowedHosts) > 0 && allowedXAIHost(u.Hostname(), endpoints) {
		return u, nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && endpoints.AllowHTTP && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
		return u, nil
	}
	return nil, fmt.Errorf("unsafe xAI verification URL")
}

func discoverXAI(ctx context.Context, client *http.Client, endpoints XAIEndpoints) (xaiDiscovery, error) {
	raw := endpoints.DiscoveryURL
	if raw == "" {
		raw = xaiDiscoveryURL
	}
	u, err := parseXAIURL(raw, endpoints)
	if err != nil {
		return xaiDiscovery{}, fmt.Errorf("invalid OIDC discovery endpoint: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return xaiDiscovery{}, fmt.Errorf("building OIDC discovery request: %w", err)
	}
	resp, err := xaiHTTPClient(client, endpoints).Do(req)
	if err != nil {
		return xaiDiscovery{}, fmt.Errorf("OIDC discovery request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return xaiDiscovery{}, fmt.Errorf("OIDC discovery failed (HTTP %d)", resp.StatusCode)
	}
	var d xaiDiscovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&d); err != nil {
		return xaiDiscovery{}, fmt.Errorf("parsing OIDC discovery response: %w", err)
	}
	issuer, err := parseXAIURL(d.Issuer, endpoints)
	if err != nil || !allowedXAIIssuer(issuer.String(), endpoints) {
		return xaiDiscovery{}, fmt.Errorf("OIDC discovery issuer is not trusted")
	}
	if _, err := parseXAIURL(d.TokenEndpoint, endpoints); err != nil {
		return xaiDiscovery{}, fmt.Errorf("OIDC token endpoint is not trusted")
	}
	if _, err := parseXAIURL(d.DeviceAuthorizationEndpoint, endpoints); err != nil {
		return xaiDiscovery{}, fmt.Errorf("OIDC device endpoint is not trusted")
	}
	return d, nil
}

func StartXAIDeviceFlow(ctx context.Context, client *http.Client, endpoints XAIEndpoints) (*XAIDeviceCode, error) {
	d, err := discoverXAI(ctx, client, endpoints)
	if err != nil {
		return nil, err
	}
	body := url.Values{"client_id": {xaiClientID}, "scope": {xaiScopes}, "referrer": {xaiReferrer}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.DeviceAuthorizationEndpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building device authorization request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "moa")
	resp, err := xaiHTTPClient(client, endpoints).Do(req)
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
	if _, err := parseXAIVerificationURL(raw.VerificationURI, endpoints); err != nil {
		return nil, fmt.Errorf("device verification endpoint is not trusted")
	}
	if raw.VerificationURIComplete != "" {
		if _, err := parseXAIVerificationURL(raw.VerificationURIComplete, endpoints); err != nil {
			return nil, fmt.Errorf("device verification endpoint is not trusted")
		}
	}
	interval := time.Duration(raw.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &XAIDeviceCode{DeviceCode: raw.DeviceCode, UserCode: raw.UserCode, VerificationURI: raw.VerificationURI, VerificationURIComplete: raw.VerificationURIComplete, ExpiresIn: time.Duration(raw.ExpiresIn) * time.Second, Interval: interval, tokenEndpoint: d.TokenEndpoint, endpoints: endpoints, issuedAt: xaiNow(endpoints)(), wait: xaiWait(endpoints), now: xaiNow(endpoints)}, nil
}

var (
	errXAIDenied  = errors.New("device authorization denied")
	errXAIExpired = errors.New("device authorization expired")
)

// CompleteXAIDeviceFlow uses the endpoint, endpoint configuration and expiry
// captured at authorization; it never rediscovers metadata or restarts the
// device-code lifetime. The deadline is measured on the configured clock and
// re-checked after every wait, so an injected clock and the real one are
// never mixed.
func CompleteXAIDeviceFlow(ctx context.Context, client *http.Client, _ XAIEndpoints, device *XAIDeviceCode) (*OAuthCredentials, error) {
	if device == nil || device.DeviceCode == "" || device.tokenEndpoint == "" {
		return nil, fmt.Errorf("missing device authorization")
	}
	now := device.now
	if now == nil {
		now = time.Now
	}
	wait := device.wait
	if wait == nil {
		wait = xaiWait(XAIEndpoints{})
	}
	deadline := device.issuedAt.Add(device.ExpiresIn)
	remaining := deadline.Sub(now())
	if remaining <= 0 {
		return nil, errXAIExpired
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	interval := device.Interval
	for {
		if err := wait(ctx, interval); err != nil {
			if ctx.Err() != nil {
				if ctx.Err() == context.DeadlineExceeded {
					return nil, errXAIExpired
				}
				return nil, fmt.Errorf("device authorization canceled: %w", ctx.Err())
			}
			return nil, err
		}
		if !now().Before(deadline) {
			return nil, errXAIExpired
		}
		result, pending, slow, err := pollXAIToken(ctx, client, device.endpoints, device.tokenEndpoint, device.DeviceCode)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
		if slow {
			interval += 5 * time.Second
		}
		if !pending && !slow {
			return nil, fmt.Errorf("device authorization was not completed")
		}
	}
}
func pollXAIToken(ctx context.Context, client *http.Client, endpoints XAIEndpoints, endpoint, deviceCode string) (*OAuthCredentials, bool, bool, error) {
	body := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {deviceCode}, "client_id": {xaiClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, false, false, fmt.Errorf("building device token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := xaiHTTPClient(client, endpoints).Do(req)
	if err != nil {
		return nil, false, false, fmt.Errorf("device token request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var token xaiTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&token); err != nil {
		return nil, false, false, fmt.Errorf("parsing device token response: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		c, err := xaiCredentials(token, "", true)
		return c, false, false, err
	}
	switch token.Error {
	case "authorization_pending":
		return nil, true, false, nil
	case "slow_down":
		return nil, false, true, nil
	case "access_denied":
		return nil, false, false, errXAIDenied
	case "expired_token":
		return nil, false, false, errXAIExpired
	default:
		return nil, false, false, fmt.Errorf("device token request failed (HTTP %d)", resp.StatusCode)
	}
}
func LoginXAI(ctx context.Context, openURL func(string), display func(*XAIDeviceCode)) (*OAuthCredentials, error) {
	device, err := StartXAIDeviceFlow(ctx, nil, XAIEndpoints{})
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
	return CompleteXAIDeviceFlow(ctx, nil, XAIEndpoints{}, device)
}
func RefreshXAIToken(refreshToken string) (*OAuthCredentials, error) {
	return refreshXAIToken(context.Background(), nil, XAIEndpoints{}, refreshToken)
}
func refreshXAIToken(ctx context.Context, client *http.Client, endpoints XAIEndpoints, refreshToken string) (*OAuthCredentials, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("missing refresh token")
	}
	d, err := discoverXAI(ctx, client, endpoints)
	if err != nil {
		return nil, classifyRefreshError("xai", err)
	}
	body := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {xaiClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := xaiHTTPClient(client, endpoints).Do(req)
	if err != nil {
		return nil, oauthTransportError("xai", "refresh")
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, oauthStatusError("xai", "refresh", resp.StatusCode, resp.Body)
	}
	var token xaiTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthResponse)).Decode(&token); err != nil {
		return nil, providerProtocolError("xai", "refresh")
	}
	creds, err := xaiCredentials(token, refreshToken, false)
	if err != nil {
		return nil, providerProtocolError("xai", "refresh")
	}
	return creds, nil
}
func xaiCredentials(token xaiTokenResponse, previousRefresh string, requireRefresh bool) (*OAuthCredentials, error) {
	if token.AccessToken == "" || token.ExpiresIn <= 0 || (requireRefresh && token.RefreshToken == "") {
		return nil, fmt.Errorf("token response is incomplete")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = previousRefresh
	}
	return &OAuthCredentials{Access: token.AccessToken, Refresh: token.RefreshToken, Expires: time.Now().Add(time.Duration(token.ExpiresIn)*time.Second - 5*time.Minute).UnixMilli()}, nil
}
