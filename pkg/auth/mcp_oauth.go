package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/e-aleixandre/moa/pkg/core"
)

// MCP OAuth credentials live in their own file, apart from auth.json: they are
// keyed by server URL and carry a dynamically registered client, an issuer and
// endpoints, none of which fit the provider-keyed Credential. Keeping them
// separate also keeps MCP refresh tokens out of the provider file.
const mcpOAuthFileName = "mcp-oauth.json"

const (
	// mcpRefreshSkew refreshes an access token this long before it expires, so
	// a request never leaves with a token that dies in flight. Short-lived
	// tokens use a tenth of their lifetime instead (see refreshSkew), so they
	// are not refreshed on every request.
	mcpRefreshSkew = 2 * time.Minute
	// mcpFreshTokenWindow: a token obtained this recently that the server still
	// rejects is not stale — refreshing again would loop, so sign-in is needed.
	mcpFreshTokenWindow = 60 * time.Second
	// mcpHTTPTimeout bounds every discovery, registration, exchange and refresh
	// request. The SDK asks for a token while holding its connection mutex, so
	// an unbounded refresh would wedge the whole MCP session.
	mcpHTTPTimeout = 30 * time.Second
	// mcpLockMaxWait bounds waiting for a sibling process's file lock, for the
	// same reason as mcpHTTPTimeout.
	mcpLockMaxWait = 10 * time.Second
)

// errMCPLockBusy is a transient failure: another process holds the file lock.
var errMCPLockBusy = errors.New("MCP credentials are locked by another moa process; try again")

// ErrMCPAuthRequired means the MCP server needs the user to (re)authorize:
// there is no usable token and no way to get one without a human.
var ErrMCPAuthRequired = errors.New("sign-in required")

// MCPOAuthRecord is the persisted OAuth state of one remote MCP server.
type MCPOAuthRecord struct {
	ServerURL             string   `json:"server_url"`
	Resource              string   `json:"resource,omitempty"`
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	ClientID              string   `json:"client_id"`
	ClientSecret          string   `json:"client_secret,omitempty"`
	AuthStyle             int      `json:"auth_style"`
	RedirectURI           string   `json:"redirect_uri"`
	Scopes                []string `json:"scopes,omitempty"`
	AccessToken           string   `json:"access_token,omitempty"`
	RefreshToken          string   `json:"refresh_token,omitempty"`
	TokenType             string   `json:"token_type,omitempty"`
	// ExpiresAt is the access token expiry in unix ms; 0 means unknown.
	ExpiresAt int64 `json:"expires_at"`
	// ObtainedAt is when the current access token was issued (unix ms).
	ObtainedAt  int64 `json:"obtained_at"`
	NeedsReauth bool  `json:"needs_reauth,omitempty"`
}

func (r MCPOAuthRecord) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     r.ClientID,
		ClientSecret: r.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   r.AuthorizationEndpoint,
			TokenURL:  r.TokenEndpoint,
			AuthStyle: oauth2.AuthStyle(r.AuthStyle),
		},
		RedirectURL: r.RedirectURI,
		Scopes:      r.Scopes,
	}
}

func (r MCPOAuthRecord) token() *oauth2.Token {
	t := &oauth2.Token{AccessToken: r.AccessToken, TokenType: "Bearer"}
	if r.ExpiresAt != 0 {
		t.Expiry = time.UnixMilli(r.ExpiresAt)
	}
	return t
}

func (r MCPOAuthRecord) nearExpiry(now time.Time) bool {
	return r.ExpiresAt != 0 && now.Add(r.refreshSkew()).UnixMilli() >= r.ExpiresAt
}

// refreshSkew is min(mcpRefreshSkew, lifetime/10). No floor: any fixed floor
// exceeds some very short lifetime and would refresh every new token at once.
func (r MCPOAuthRecord) refreshSkew() time.Duration {
	if r.ObtainedAt == 0 || r.ExpiresAt <= r.ObtainedAt {
		return mcpRefreshSkew
	}
	skew := time.Duration(r.ExpiresAt-r.ObtainedAt) * time.Millisecond / 10
	return min(skew, mcpRefreshSkew)
}

// MCPOAuthStore persists OAuth tokens for remote MCP servers and runs the
// authorization flow. One instance exists per file path in the process (see
// MCPOAuthStoreAt) so every session's MCP manager shares its refresh
// single-flight and change notifications.
type MCPOAuthStore struct {
	path   string
	client *http.Client // flows of local (loopback/private) servers
	// strictClient only dials addresses addrPublic accepts; used for every
	// flow of a public server.
	strictClient *http.Client
	addrPublic   func(netip.AddrPort) bool

	mu       sync.Mutex
	data     map[string]MCPOAuthRecord
	keyLocks map[string]*sync.Mutex
	pending  map[string]*mcpPending // by OAuth state
	subs     map[uint64]func(string)
	nextSub  uint64
}

var mcpStores = struct {
	sync.Mutex
	m map[string]*MCPOAuthStore
}{m: map[string]*MCPOAuthStore{}}

// MCPOAuthStoreAt returns the process-wide store for path, creating and
// loading it on first use. An empty path yields a store that holds nothing
// and refuses to save, rather than writing tokens relative to the cwd.
func MCPOAuthStoreAt(path string) *MCPOAuthStore {
	mcpStores.Lock()
	defer mcpStores.Unlock()
	if s, ok := mcpStores.m[path]; ok {
		return s
	}
	s := &MCPOAuthStore{
		path:       path,
		client:     newMCPHTTPClient(nil, nil),
		addrPublic: isPublicAddr,
		data:       map[string]MCPOAuthRecord{},
		keyLocks:   map[string]*sync.Mutex{},
		pending:    map[string]*mcpPending{},
		subs:       map[uint64]func(string){},
	}
	s.strictClient = newMCPHTTPClient(nil, func(ap netip.AddrPort) bool { return s.addrPublic(ap) })
	// A corrupt file leaves memory empty; every mutation re-reads it under the
	// lock (adoptDisk) and refuses to overwrite it.
	if disk, err := s.loadFromDisk(); err == nil {
		s.data = disk
	} else if path != "" {
		slog.Warn("could not load MCP credentials", "error", err)
	}
	mcpStores.m[path] = s
	return s
}

// DefaultMCPOAuthStore returns the store at <config dir>/mcp-oauth.json.
func DefaultMCPOAuthStore() *MCPOAuthStore {
	dir := core.ConfigDir()
	if dir == "" {
		return MCPOAuthStoreAt("")
	}
	return MCPOAuthStoreAt(filepath.Join(dir, mcpOAuthFileName))
}

// MCPOAuthKey normalizes a server URL into the key its tokens are stored
// under, so trivially different spellings of one endpoint share credentials.
func MCPOAuthKey(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return rawURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	switch {
	case port != "":
		u.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		u.Host = "[" + host + "]"
	default:
		u.Host = host
	}
	u.Fragment = ""
	u.RawFragment = ""
	if u.Path == "/" {
		u.Path = ""
		u.RawPath = ""
	}
	return u.String()
}

func (s *MCPOAuthStore) loadFromDisk() (map[string]MCPOAuthRecord, error) {
	if s.path == "" {
		return nil, errors.New("no config directory")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]MCPOAuthRecord{}, nil
		}
		return nil, fmt.Errorf("reading MCP credentials: %w", err)
	}
	m := map[string]MCPOAuthRecord{}
	if err := json.Unmarshal(data, &m); err != nil {
		// The decoder's message can quote file content (tokens): omit it.
		return nil, fmt.Errorf("MCP credential file %s is corrupt; fix or delete it", s.path)
	}
	return m, nil
}

// withFileLock serializes updates with sibling processes (CLI + serve). The
// caller re-reads the file under it (adoptDisk) because save rewrites every
// record: adopting only one key would put back a sibling's stale rotation.
func (s *MCPOAuthStore) withFileLock(ctx context.Context, fn func() error) error {
	if s.path == "" {
		return errors.New("cannot store MCP credentials: no config directory")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return withBoundedFileLock(ctx, s.path+".lock", mcpLockMaxWait, fn)
}

// withBoundedFileLock is withPlatformFileLock that gives up after maxWait or
// when ctx ends, with errMCPLockBusy: refresh runs under the SDK's connection
// mutex, so a sibling holding the lock must not wedge the session.
func withBoundedFileLock(ctx context.Context, path string, maxWait time.Duration, fn func() error) error {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening credential lock: %w", err)
	}
	defer lock.Close() //nolint:errcheck
	deadline := time.Now().Add(maxWait)
	for backoff := 5 * time.Millisecond; ; backoff = min(backoff*2, 200*time.Millisecond) {
		ok, err := tryLockFile(lock)
		if err != nil {
			return fmt.Errorf("locking credentials: %w", err)
		}
		if ok {
			break
		}
		if time.Now().Add(backoff).After(deadline) {
			return errMCPLockBusy
		}
		select {
		case <-ctx.Done():
			return errMCPLockBusy
		case <-time.After(backoff):
		}
	}
	defer unlockFile(lock)
	return fn()
}

// adoptDisk replaces the in-memory records with the file. Callers hold the
// file lock. An unreadable file is an error: the caller must not save, since
// save rewrites the whole file.
func (s *MCPOAuthStore) adoptDisk() error {
	disk, err := s.loadFromDisk()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.data = disk
	s.mu.Unlock()
	return nil
}

// save writes every record. Callers hold the file lock.
func (s *MCPOAuthStore) save() error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshaling MCP credentials: %w", err)
	}
	return writeCredentialFile(s.path, data)
}

func (s *MCPOAuthStore) get(key string) (MCPOAuthRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	return rec, ok
}

func (s *MCPOAuthStore) put(key string, rec MCPOAuthRecord) {
	s.mu.Lock()
	s.data[key] = rec
	s.mu.Unlock()
}

func (s *MCPOAuthStore) keyLock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.keyLocks[key]
	if !ok {
		l = &sync.Mutex{}
		s.keyLocks[key] = l
	}
	return l
}

// Has reports whether a record exists for key and whether it is waiting for
// the user to sign in again. It never touches the network.
func (s *MCPOAuthStore) Has(key string) (exists, needsReauth bool) {
	rec, ok := s.get(key)
	return ok, ok && rec.NeedsReauth
}

// Token returns the access token for key, refreshing it first when it is
// about to expire. It returns (nil, nil) when no record exists, so a server
// without OAuth is left to its static headers, and ErrMCPAuthRequired when
// only a human can produce a new token.
func (s *MCPOAuthStore) Token(ctx context.Context, key string) (*oauth2.Token, error) {
	rec, ok := s.get(key)
	if !ok {
		return nil, nil
	}
	if rec.NeedsReauth || rec.AccessToken == "" {
		return nil, ErrMCPAuthRequired
	}
	if !rec.nearExpiry(time.Now()) {
		return rec.token(), nil
	}
	return s.refresh(ctx, key, "", false)
}

// RefreshIfCurrent reacts to a server rejecting rejectedAccess. If the stored
// token already differs, someone rotated it meanwhile and nothing is done.
func (s *MCPOAuthStore) RefreshIfCurrent(ctx context.Context, key, rejectedAccess string) error {
	_, err := s.refresh(ctx, key, rejectedAccess, true)
	return err
}

// refresh is the single-flight refresh shared by the proactive (Token) and
// reactive (RefreshIfCurrent) paths. It holds the per-key lock and the file
// lock, and re-reads the file first, so concurrent callers in this or a
// sibling process adopt one rotation instead of spending the refresh token
// twice (providers that rotate it invalidate the loser).
func (s *MCPOAuthStore) refresh(ctx context.Context, key, rejected string, reactive bool) (*oauth2.Token, error) {
	kl := s.keyLock(key)
	kl.Lock()
	defer kl.Unlock()

	var tok *oauth2.Token
	err := s.withFileLock(ctx, func() error {
		if err := s.adoptDisk(); err != nil {
			return err
		}
		rec, ok := s.get(key)
		if !ok {
			return nil
		}
		if rec.NeedsReauth || rec.AccessToken == "" {
			return ErrMCPAuthRequired
		}
		now := time.Now()
		if reactive {
			if rec.AccessToken != rejected {
				tok = rec.token()
				return nil
			}
			if now.UnixMilli()-rec.ObtainedAt < mcpFreshTokenWindow.Milliseconds() {
				return s.markNeedsReauth(key, rec)
			}
		} else if !rec.nearExpiry(now) {
			tok = rec.token()
			return nil
		}
		if rec.RefreshToken == "" {
			return s.markNeedsReauth(key, rec)
		}

		cfg := rec.oauthConfig()
		client, _ := s.clientFor(rec.ServerURL)
		fresh, err := cfg.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, client),
			&oauth2.Token{RefreshToken: rec.RefreshToken}).Token()
		if err != nil {
			if isPermanentRefreshError(err) {
				return s.markNeedsReauth(key, rec)
			}
			return fmt.Errorf("refreshing MCP token: %s", describeOAuthError(err))
		}
		if fresh.AccessToken == "" {
			return errors.New("refreshing MCP token: token endpoint returned no access token")
		}
		applyToken(&rec, fresh, now)
		s.put(key, rec)
		if err := s.save(); err != nil {
			// The rotated token is live in memory; losing it on disk only
			// costs a re-authorization after a restart, so keep serving.
			slog.Warn("could not persist refreshed MCP token", "server", rec.ServerURL, "error", err)
		}
		tok = rec.token()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tok, nil
}

// markNeedsReauth persists that key needs a human, dropping the dead access
// token so nothing keeps presenting it. Caller holds the file lock.
func (s *MCPOAuthStore) markNeedsReauth(key string, rec MCPOAuthRecord) error {
	rec.NeedsReauth = true
	rec.AccessToken = ""
	s.put(key, rec)
	if err := s.save(); err != nil {
		slog.Warn("could not persist MCP sign-in state", "server", rec.ServerURL, "error", err)
	}
	return ErrMCPAuthRequired
}

// applyToken copies a token response into rec. A response without a refresh
// token keeps the previous one (rotation is optional for the server).
func applyToken(rec *MCPOAuthRecord, t *oauth2.Token, now time.Time) {
	rec.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		rec.RefreshToken = t.RefreshToken
	}
	rec.TokenType = t.TokenType
	rec.ExpiresAt = 0
	if !t.Expiry.IsZero() {
		rec.ExpiresAt = t.Expiry.UnixMilli()
	}
	rec.ObtainedAt = now.UnixMilli()
	rec.NeedsReauth = false
}

// isPermanentRefreshError reports whether a refresh failure means the grant is
// gone (a human must re-authorize) rather than a transient outage.
func isPermanentRefreshError(err error) bool {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return false
	}
	switch re.ErrorCode {
	case "invalid_grant", "invalid_client", "unauthorized_client":
		return true
	}
	if re.Response != nil {
		switch re.Response.StatusCode {
		case http.StatusBadRequest, http.StatusUnauthorized:
			return true
		}
	}
	return false
}

// describeOAuthError renders a token-endpoint failure from its status code and
// OAuth error code only. oauth2.RetrieveError.Error() embeds the raw response
// body, which may echo codes or tokens, so it must never reach a message.
func describeOAuthError(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		msg := "token endpoint returned an error"
		if re.Response != nil {
			msg = "token endpoint returned HTTP " + strconv.Itoa(re.Response.StatusCode)
		}
		if code := oauthErrorCode(re.ErrorCode); code != "" {
			msg += " (" + code + ")"
		}
		return msg
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "request timed out or was cancelled"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "request to the token endpoint timed out"
		}
		var ne *net.OpError
		if errors.As(ue.Err, &ne) {
			return "could not reach the token endpoint: " + ne.Err.Error()
		}
		return "could not reach the token endpoint"
	}
	return "token request failed"
}

// oauthErrorCode keeps an OAuth error code only when it looks like one (RFC
// 6749 codes are short ASCII tokens), so a server cannot smuggle arbitrary
// text into our messages through it.
func oauthErrorCode(code string) string {
	if code == "" || len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return ""
		}
	}
	return code
}

// Subscribe registers fn to be called with a key after new tokens for it are
// stored by Complete. The returned func unregisters it.
func (s *MCPOAuthStore) Subscribe(fn func(key string)) (unsubscribe func()) {
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

func (s *MCPOAuthStore) notify(key string) {
	s.mu.Lock()
	fns := make([]func(string), 0, len(s.subs))
	for _, fn := range s.subs {
		fns = append(fns, fn)
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn(key)
	}
}
