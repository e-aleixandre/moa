package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// mcpPendingTTL bounds how long a started authorization can be completed.
const mcpPendingTTL = 15 * time.Minute

var (
	// ErrMCPOAuthNoPending means a pasted callback URL does not belong to an
	// in-flight authorization of this server (wrong/unknown/expired/used
	// state, wrong redirect target, or wrong issuer).
	ErrMCPOAuthNoPending = errors.New("that link doesn't match an authorization in progress for this server")
	// ErrMCPOAuthDenied means the authorization server returned an error
	// instead of a code (typically the user declined).
	ErrMCPOAuthDenied = errors.New("authorization was denied")
)

// mcpPending is an authorization started by Begin and not yet completed. It
// lives only in memory: the verifier must never touch disk.
type mcpPending struct {
	key      string
	rec      MCPOAuthRecord // client, endpoints, redirect, scopes; no tokens yet
	verifier string
	created  time.Time
}

// Begin discovers the server's authorization server, registers a fresh client
// and returns the URL the user must open. Nothing listens on the loopback
// redirect: the browser fails to load it and the user pastes that URL into
// Complete, which works the same when moa runs on another machine.
func (s *MCPOAuthStore) Begin(ctx context.Context, serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("invalid MCP server URL")
	}
	key := MCPOAuthKey(serverURL)
	client, strict := s.clientFor(serverURL)

	prm, challengeScopes, err := s.discoverResource(ctx, client, strict, serverURL)
	if err != nil {
		return "", err
	}
	asURL := prm.AuthorizationServers[0]
	if err := requireHTTPS(strict, "authorization server", asURL); err != nil {
		return "", err
	}
	asm, err := sdkauth.GetAuthServerMetadata(ctx, asURL, client)
	if err != nil {
		return "", fmt.Errorf("fetching authorization server metadata: %v", err)
	}
	if asm == nil {
		// 2025-03-26 spec fallback: fixed endpoints under the server.
		asm = &oauthex.AuthServerMeta{
			Issuer:                asURL,
			AuthorizationEndpoint: asURL + "/authorize",
			TokenEndpoint:         asURL + "/token",
			RegistrationEndpoint:  asURL + "/register",
		}
	}
	if len(asm.CodeChallengeMethodsSupported) > 0 && !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return "", errors.New("the authorization server does not support PKCE S256")
	}
	if asm.RegistrationEndpoint == "" {
		return "", errors.New("this server needs a pre-registered OAuth client; that is not supported")
	}
	for what, endpoint := range map[string]string{
		"authorization endpoint": asm.AuthorizationEndpoint,
		"token endpoint":         asm.TokenEndpoint,
		"registration endpoint":  asm.RegistrationEndpoint,
	} {
		if err := requireHTTPS(strict, what, endpoint); err != nil {
			return "", err
		}
	}

	scopes := challengeScopes
	if len(scopes) == 0 {
		scopes = prm.ScopesSupported
	}
	redirectURI, err := loopbackRedirectURI()
	if err != nil {
		return "", err
	}
	reg, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{redirectURI},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		ClientName:              "moa",
		Scope:                   strings.Join(scopes, " "),
		ApplicationType:         "native",
	}, client)
	if err != nil {
		return "", describeRegistrationError(err)
	}
	authStyle, err := registrationAuthStyle(reg)
	if err != nil {
		return "", err
	}

	rec := MCPOAuthRecord{
		ServerURL:             serverURL,
		Resource:              prm.Resource,
		Issuer:                asm.Issuer,
		AuthorizationEndpoint: asm.AuthorizationEndpoint,
		TokenEndpoint:         asm.TokenEndpoint,
		ClientID:              reg.ClientID,
		ClientSecret:          reg.ClientSecret,
		AuthStyle:             int(authStyle),
		RedirectURI:           redirectURI,
		Scopes:                scopes,
	}
	verifier := oauth2.GenerateVerifier()
	state, err := mcpRandomState()
	if err != nil {
		return "", err
	}
	authURL := rec.oauthConfig().AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", rec.Resource),
	)

	now := time.Now()
	s.mu.Lock()
	for st, p := range s.pending {
		// A new Connect supersedes the previous one for the same server.
		if p.key == key || now.Sub(p.created) > mcpPendingTTL {
			delete(s.pending, st)
		}
	}
	s.pending[state] = &mcpPending{key: key, rec: rec, verifier: verifier, created: now}
	s.mu.Unlock()
	return authURL, nil
}

// Complete finishes the authorization Begin started for serverURL, from the
// URL the browser was redirected to. On success the tokens are stored and
// subscribers are notified.
func (s *MCPOAuthStore) Complete(ctx context.Context, serverURL, pastedURL string) error {
	key := MCPOAuthKey(serverURL)
	cb, err := url.Parse(strings.TrimSpace(pastedURL))
	if err != nil {
		return ErrMCPOAuthNoPending
	}
	q := cb.Query()
	state := q.Get("state")
	if state == "" {
		return ErrMCPOAuthNoPending
	}

	// A state that is unknown or belongs to another server consumes nothing, so
	// a stray paste cannot cancel the real authorization. A matching state is
	// single use from here on, whatever the outcome.
	s.mu.Lock()
	p, ok := s.pending[state]
	if !ok || p.key != key {
		s.mu.Unlock()
		return ErrMCPOAuthNoPending
	}
	delete(s.pending, state)
	s.mu.Unlock()

	if time.Since(p.created) > mcpPendingTTL {
		return ErrMCPOAuthNoPending
	}
	if !sameRedirectTarget(cb, p.rec.RedirectURI) {
		return ErrMCPOAuthNoPending
	}
	// RFC 9207: an iss that differs from the server we started with is a
	// mix-up attempt; reject it even on an error response.
	if iss := q.Get("iss"); iss != "" && iss != p.rec.Issuer {
		return ErrMCPOAuthNoPending
	}
	if e := q.Get("error"); e != "" {
		if code := oauthErrorCode(e); code != "" {
			return fmt.Errorf("%w (%s)", ErrMCPOAuthDenied, code)
		}
		return ErrMCPOAuthDenied
	}
	code := q.Get("code")
	if code == "" {
		return ErrMCPOAuthNoPending
	}

	rec := p.rec
	client, _ := s.clientFor(rec.ServerURL)
	tok, err := rec.oauthConfig().Exchange(context.WithValue(ctx, oauth2.HTTPClient, client), code,
		oauth2.VerifierOption(p.verifier),
		oauth2.SetAuthURLParam("resource", rec.Resource),
	)
	if err != nil {
		return fmt.Errorf("exchanging the authorization code: %s", describeOAuthError(err))
	}
	if tok.AccessToken == "" {
		return errors.New("exchanging the authorization code: no access token in the response")
	}
	applyToken(&rec, tok, time.Now())

	if err := s.withFileLock(ctx, func() error {
		if err := s.adoptDisk(); err != nil {
			return err
		}
		s.put(key, rec)
		return s.save()
	}); err != nil {
		return fmt.Errorf("saving MCP credentials: %v", err)
	}
	s.notify(key)
	return nil
}

// discoverResource finds the protected resource metadata of serverURL, plus
// any scopes the server asked for in a WWW-Authenticate challenge.
func (s *MCPOAuthStore) discoverResource(ctx context.Context, client *http.Client, strict bool, serverURL string) (*oauthex.ProtectedResourceMetadata, []string, error) {
	for _, c := range prmCandidates(serverURL) {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, c.url, c.resource, client)
		if err != nil || prm == nil {
			continue
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, nil, errors.New("the server's resource metadata lists no authorization server")
		}
		return prm, nil, nil
	}

	challenges := probeChallenges(ctx, client, serverURL)
	scopes := challengeScopes(challenges)
	if metaURL := challengeParam(challenges, "resource_metadata"); metaURL != "" {
		if err := requireHTTPS(strict, "resource metadata URL", metaURL); err != nil {
			return nil, nil, err
		}
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, metaURL, serverURL, client)
		if err == nil && prm != nil {
			if len(prm.AuthorizationServers) == 0 {
				return nil, nil, errors.New("the server's resource metadata lists no authorization server")
			}
			return prm, scopes, nil
		}
	}

	// 2025-03-26 spec fallback: the server's origin is the authorization server.
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, nil, errors.New("invalid MCP server URL")
	}
	origin := url.URL{Scheme: u.Scheme, Host: u.Host}
	return &oauthex.ProtectedResourceMetadata{
		Resource:             serverURL,
		AuthorizationServers: []string{origin.String()},
	}, scopes, nil
}

type prmCandidate struct{ url, resource string }

// prmCandidates mirrors the SDK's well-known lookup order: at the endpoint's
// path (resource = the server URL), then at the root (resource = origin).
func prmCandidates(serverURL string) []prmCandidate {
	ru, err := url.Parse(serverURL)
	if err != nil {
		return nil
	}
	mu := *ru
	mu.RawQuery = ""
	mu.Fragment = ""
	mu.RawPath = ""
	mu.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(ru.Path, "/")
	out := []prmCandidate{{url: mu.String(), resource: serverURL}}
	mu.Path = "/.well-known/oauth-protected-resource"
	origin := url.URL{Scheme: ru.Scheme, Host: ru.Host}
	return append(out, prmCandidate{url: mu.String(), resource: origin.String()})
}

// probeChallenges sends one unauthenticated initialize to learn the server's
// WWW-Authenticate challenge when no well-known metadata exists.
func probeChallenges(ctx context.Context, client *http.Client, serverURL string) []oauthex.Challenge {
	const body = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"moa","version":"0.1.0"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()                                      //nolint:errcheck
	defer io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) //nolint:errcheck
	if resp.StatusCode != http.StatusUnauthorized {
		return nil
	}
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return nil
	}
	return challenges
}

func challengeParam(cs []oauthex.Challenge, name string) string {
	for _, c := range cs {
		if v := c.Params[name]; v != "" {
			return v
		}
	}
	return ""
}

func challengeScopes(cs []oauthex.Challenge) []string {
	for _, c := range cs {
		if c.Scheme == "bearer" && c.Params["scope"] != "" {
			return strings.Fields(c.Params["scope"])
		}
	}
	return nil
}

// registrationAuthStyle maps the registered token endpoint auth method to an
// oauth2 style, like the SDK does. An AS that does not echo the method keeps
// the "none" we asked for unless it issued a secret. Any other method
// (private_key_jwt, tls_client_auth, ...) cannot be honored.
func registrationAuthStyle(reg *oauthex.ClientRegistrationResponse) (oauth2.AuthStyle, error) {
	method := reg.TokenEndpointAuthMethod
	if method == "" {
		// RFC 7591 §2: an omitted method defaults to client_secret_basic.
		method = "client_secret_basic"
		if reg.ClientSecret == "" {
			method = "none"
		}
	}
	switch method {
	case "none", "client_secret_post":
		return oauth2.AuthStyleInParams, nil
	case "client_secret_basic":
		return oauth2.AuthStyleInHeader, nil
	default:
		return 0, errors.New("the authorization server registered an unsupported client authentication method")
	}
}

// describeRegistrationError drops the SDK's message, which can embed the raw
// registration response (and so a client secret), keeping only the code.
func describeRegistrationError(err error) error {
	var re *oauthex.ClientRegistrationError
	if errors.As(err, &re) {
		if code := oauthErrorCode(re.ErrorCode); code != "" {
			return fmt.Errorf("client registration was rejected (%s)", code)
		}
		return errors.New("client registration was rejected")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errors.New("client registration timed out")
	}
	return errors.New("client registration failed")
}

// sameRedirectTarget reports whether cb points at exactly the registered
// loopback redirect (scheme, host, port and path).
func sameRedirectTarget(cb *url.URL, redirectURI string) bool {
	want, err := url.Parse(redirectURI)
	if err != nil {
		return false
	}
	return cb.Scheme == want.Scheme && cb.Host == want.Host && cb.Path == want.Path
}

// loopbackRedirectURI builds a native-app loopback redirect (some servers
// register nothing else). Nothing binds the port — the user pastes the URL the
// browser fails to load — so any port in the ephemeral range will do.
func loopbackRedirectURI() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(65535-49152+1))
	if err != nil {
		return "", fmt.Errorf("generating redirect port: %w", err)
	}
	return "http://127.0.0.1:" + strconv.Itoa(49152+int(n.Int64())) + "/callback", nil
}

// mcpRandomState returns 32 random bytes, base64url-encoded.
func mcpRandomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
