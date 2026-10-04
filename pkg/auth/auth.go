// Package auth handles credential storage and OAuth flows for AI providers.
//
// Credentials are stored in ~/.config/moa/auth.json with mode 0600.
// Supports both API keys and OAuth tokens (Claude Max).
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Credential represents a stored credential for a provider.
type Credential struct {
	Type      string `json:"type"`                 // "api_key" or "oauth"
	Key       string `json:"key,omitempty"`        // API key (type=api_key), or a key minted from an OAuth session (Meta)
	Access    string `json:"access,omitempty"`     // OAuth access token (type=oauth)
	Refresh   string `json:"refresh,omitempty"`    // OAuth refresh token (type=oauth)
	Expires   int64  `json:"expires,omitempty"`    // OAuth token expiry (unix ms) (type=oauth)
	AccountID string `json:"account_id,omitempty"` // Provider-specific account ID (e.g., OpenAI chatgpt_account_id)
	// Generation identifies one login or key replacement. OAuth rotation keeps
	// it, so a request can tell "same login, newer token" from "another login".
	Generation string `json:"generation,omitempty"`

	// extra keeps fields this binary does not know, so rewriting the shared
	// file never drops what a newer binary stored.
	extra map[string]json.RawMessage
}

// derivedToken returns the credential value a provider API actually accepts.
// Most OAuth providers accept the access token itself; Meta mints a separate
// Model API key from the OAuth session and stores it in Key.
func derivedToken(provider string, cred Credential) string {
	if provider == "meta" && cred.Type == "oauth" && cred.Key != "" {
		return cred.Key
	}
	return cred.Access
}

// IsOAuthToken returns true if the given key looks like an OAuth token
// rather than a standard API key. Detects Anthropic OAuth (sk-ant-oat)
// and JWT tokens (three dot-separated segments, as used by OpenAI OAuth).
func IsOAuthToken(key string) bool {
	if strings.HasPrefix(key, "sk-ant-oat") {
		return true
	}
	// JWTs have exactly 3 dot-separated parts.
	parts := strings.Split(key, ".")
	return len(parts) == 3 && len(parts[0]) > 10
}

// Store manages credentials on disk.
//
// Lock order for every mutation (Set, Remove, CommitLogin, RetrySave and both
// rotation paths): refreshMu → adjacent file lock → mu. refreshMu serializes
// mutations and makes rotation single-flight inside a process; the file lock
// does the same across processes. mu only guards short in-memory access and
// is never held across network calls.
type Store struct {
	path string

	mu   sync.RWMutex
	data map[string]Credential
	// loadErr is why the last disk read failed. While set, effective reads of
	// stored credentials fail; writers re-read under lock and fail too unless
	// the file has become valid again.
	loadErr error
	// seen records that a valid auth.json existed. From then on a missing file
	// means it was removed under us, not that the store is new.
	seen bool
	// pending holds OAuth rotations the provider accepted but that could not
	// be saved. Losing one can mean a forced re-login (the old refresh token
	// may be spent), so it is kept until it is saved or superseded.
	// It also holds replacement fences (see commit).
	pending map[string]pendingRotation

	refreshMu sync.Mutex

	// refresh performs the network token refresh. It defaults to the pinned
	// production endpoints; same-package tests point it at httptest servers.
	refresh func(ctx context.Context, provider, refreshToken string) (*OAuthCredentials, error)
	// writeFile is writeCredentialFile; tests wrap it to report a failure
	// after the real rename, which permissions cannot produce.
	writeFile func(path string, data []byte) error

	// uses is what this process observed about each credential selection
	// (see RecordUse); it lives for the process and is never persisted.
	useMu sync.Mutex
	uses  map[useKey]useRecord
	// useSelected, when set by a test, runs in RecordUse between the
	// selection check and publication, to hold a result in that window.
	useSelected func(useKey)
}

// pendingRotation is an unsaved rotation together with the exact disk record
// it consumed, so it is only written over that record. A fence instead marks
// a provider whose record may be on disk without being durable: it is lifted
// only by this Store writing and syncing whatever the record now is.
type pendingRotation struct {
	base     Credential
	proposed Credential
	fence    bool
}

var errNoConfigDir = errors.New("no config directory")

// errNotWritten marks a credential write that failed before its rename, so
// the file still holds what it held before.
var errNotWritten = errors.New("credential file not replaced")

// configDir returns the directory for storing credentials.
// Honors MOA_CONFIG_DIR env var for container/custom deployments.
//
// Returns "" when it cannot be resolved. Writing credentials relative to the
// current directory — the previous behaviour — drops an auth.json inside
// whatever repository the user happened to be in, where it can be committed or
// shared; failing to authenticate is the safer outcome, and MOA_CONFIG_DIR or
// an API key in the environment both still work.
func configDir() string {
	return core.ConfigDir()
}

// DefaultStorePath returns the default path for the auth store, or "" when no
// config directory can be resolved.
func DefaultStorePath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "auth.json")
}

// NewStore creates or loads a credential store. It always returns a Store:
// an unreadable or invalid file is retained as LoadError, so environment
// credentials keep working while every stored read and write fails closed.
func NewStore(path string) *Store {
	if path == "" {
		path = DefaultStorePath()
	}
	s := &Store{
		path:      path,
		data:      make(map[string]Credential),
		pending:   make(map[string]pendingRotation),
		refresh:   tokenEndpoints{}.refresh,
		writeFile: writeCredentialFile,
	}
	_, _ = s.reload()
	return s
}

// NewStoreWithHTTPClient is NewStore whose token refreshes go through client.
// The fixed production endpoints are unchanged: this is a server-side Go seam
// for tests that route those origins to a local server, never configuration.
func NewStoreWithHTTPClient(path string, client *http.Client) *Store {
	s := NewStore(path)
	s.refresh = tokenEndpoints{client: client}.refresh
	return s
}

// LoadError reports why the credential file could not be used, or nil.
func (s *Store) LoadError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadErr == nil {
		return nil
	}
	return storeUnavailable("", "load")
}

// withFileLock serializes auth.json updates between CLI and serve processes.
// The credential file itself is still atomically replaced; the adjacent lock
// file has a stable inode, so it remains useful across those replacements.
func (s *Store) withFileLock(fn func() error) error {
	if s.path == "" {
		return errNoConfigDir
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return withPlatformFileLock(s.path+".lock", fn)
}

// loadFromDisk strictly decodes the whole credential file into a fresh map.
// Only a file this Store has never seen may be missing; that is an empty
// store. The atomic rename in writeCredentialFile guarantees a complete file.
func (s *Store) loadFromDisk() (map[string]Credential, bool, error) {
	if s.path == "" {
		return nil, false, errNoConfigDir
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		s.mu.RLock()
		seen := s.seen
		s.mu.RUnlock()
		if os.IsNotExist(err) && !seen {
			return map[string]Credential{}, false, nil
		}
		return nil, false, err
	}
	creds, err := decodeCredentials(data)
	if err != nil {
		return nil, false, err
	}
	return creds, true, nil
}

// reload reads the disk and, if valid, installs it as the cache. A failure is
// retained so the stale cache is never served. Pending rotations are kept:
// adopting a sibling's write must not discard them.
func (s *Store) reload() (map[string]Credential, error) {
	disk, exists, err := s.loadFromDisk()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.loadErr = err
		return nil, err
	}
	s.loadErr = nil
	s.seen = s.seen || exists
	s.data = cloneCredentials(disk)
	return disk, nil
}

// saveLocked writes next and installs it as the cache. Callers hold refreshMu
// and the file lock. On failure the cache keeps the last valid disk state.
func (s *Store) saveLocked(next map[string]Credential) error {
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling credentials: %w", err)
	}
	if err := s.writeFile(s.path, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.data = cloneCredentials(next)
	s.seen = true
	s.loadErr = nil
	s.mu.Unlock()
	return nil
}

// transact is the single write discipline. With refreshMu held by the caller
// it takes the file lock, reloads the whole valid file, first persists any
// pending rotation that still applies, and only then lets fn stage its change
// on disk. fn may call the network (rotation); it returns whether disk must be
// written and, for a rotation, what to keep pending if that write fails.
func (s *Store) transact(provider, op string, fn func(disk map[string]Credential) (bool, *pendingRotation, error)) error {
	err := s.withFileLock(func() error {
		disk, err := s.reload()
		if err != nil {
			return storeUnavailable(provider, op)
		}
		if staged := s.stagePendingLocked(disk); len(staged) > 0 {
			if err := s.saveLocked(disk); err != nil {
				return persistenceFailed(provider, op)
			}
			s.mu.Lock()
			for _, p := range staged {
				delete(s.pending, p)
			}
			s.mu.Unlock()
		}
		write, rotation, err := fn(disk)
		if err != nil || !write {
			return err
		}
		if err := s.saveLocked(disk); err != nil {
			if rotation != nil {
				s.mu.Lock()
				s.pending[provider] = *rotation
				s.mu.Unlock()
			}
			return persistenceFailed(provider, op)
		}
		return nil
	})
	if _, ok := core.AsProviderCredentialError(err); err != nil && !ok {
		return storeUnavailable(provider, op)
	}
	return err
}

// stagePendingLocked folds every pending rotation whose base is still on disk
// into disk and returns those providers. A record from another generation (or
// a removal) means a newer login superseded the rotation, so it is dropped.
// A same-generation record that is neither base nor proposal is a conflict:
// another rotation won, and guessing which refresh token is live could lose
// the account, so the pending rotation is kept and not written.
func (s *Store) stagePendingLocked(disk map[string]Credential) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var staged []string
	for provider, p := range s.pending {
		cur, ok := disk[provider]
		switch {
		case p.fence:
			// Rewriting the current record (or its absence) and syncing it
			// is the only proof of durability, whoever wrote it.
			staged = append(staged, provider)
		case ok && (sameCredential(cur, p.base) || sameCredential(cur, p.proposed)):
			// Equal to the proposal: a write landed but was not confirmed
			// durable; rewriting it costs nothing and syncs it.
			disk[provider] = p.proposed
			staged = append(staged, provider)
		case !ok || cur.Generation != p.base.Generation:
			delete(s.pending, provider)
		}
	}
	return staged
}

func (s *Store) hasPending(provider string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.pending[provider]
	return ok
}

func (s *Store) dropPending(provider string) {
	s.mu.Lock()
	delete(s.pending, provider)
	s.mu.Unlock()
}

func newGeneration() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func oauthCredential(provider string, previous Credential, refreshed *OAuthCredentials) (Credential, error) {
	if refreshed.Access == "" {
		return Credential{}, providerProtocolError(provider, "refresh")
	}
	refresh := refreshed.Refresh
	if refresh == "" {
		refresh = previous.Refresh
	}
	if refresh == "" {
		return Credential{}, providerProtocolError(provider, "refresh")
	}
	account := refreshed.AccountID
	if account == "" {
		account = previous.AccountID
	}
	// A generation is one login: a refresh that lands on another account is
	// not a rotation of it and must go through a new login instead.
	if previous.AccountID != "" && account != previous.AccountID {
		return Credential{}, core.NewProviderCredentialError(provider, core.CredentialSourceStore, "refresh", core.CredentialReconnect)
	}
	key := ""
	if provider == "meta" {
		key = refreshed.APIKey
		if key == "" {
			key = previous.Key
		}
	}
	return Credential{
		Type: "oauth", Access: refreshed.Access, Refresh: refresh, Expires: refreshed.Expires,
		AccountID: account, Key: key, Generation: previous.Generation, extra: previous.extra,
	}, nil
}

// writeCredentialFile atomically replaces a credential file with data (mode
// 0600, parent dir 0700): unique temp file + sync + rename + dir sync, so a
// crash or a concurrent reader never observes a truncated file. Shared by every
// credential store in this package. Failures before the rename wrap
// errNotWritten; a later one leaves the new file in place, not yet durable.
func writeCredentialFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("%w: creating config dir: %w", errNotWritten, err)
	}

	// Atomic write: unique temp file + sync + rename to prevent corruption
	tmp, err := os.CreateTemp(dir, strings.TrimSuffix(filepath.Base(path), ".json")+"-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: creating temp file: %w", errNotWritten, err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%w: writing credentials: %w", errNotWritten, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%w: syncing credentials: %w", errNotWritten, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%w: closing temp file: %w", errNotWritten, err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%w: setting permissions: %w", errNotWritten, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%w: renaming credentials: %w", errNotWritten, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("syncing config dir: %w", err)
	}
	return nil
}

// Set stores a credential for a provider unconditionally, as a new login
// generation (any Generation in cred is replaced). Prefer CommitLogin, which
// refuses to overwrite a login newer than the caller expected.
func (s *Store) Set(provider string, cred Credential) error {
	_, err := s.commit(provider, "save", nil, cred)
	return err
}

// CommitLogin durably stores cred as a new login generation, provided the
// stored generation for provider is still expectedGeneration ("" for none or a
// legacy record). It returns the new generation. It governs the store only:
// whether an environment credential takes precedence is the caller's concern.
// A durable commit supersedes an unsaved rotation of the previous login.
func (s *Store) CommitLogin(provider, expectedGeneration string, cred Credential) (string, error) {
	return s.commit(provider, "login", &expectedGeneration, cred)
}

func (s *Store) commit(provider, op string, expected *string, cred Credential) (string, error) {
	gen, err := newGeneration()
	if err != nil {
		return "", persistenceFailed(provider, op)
	}
	cred.Generation = gen
	cred.extra = nil
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	err = s.transact(provider, op, func(disk map[string]Credential) (bool, *pendingRotation, error) {
		if expected != nil && disk[provider].Generation != *expected {
			return false, nil, credentialsChanged(provider, op, disk[provider].Generation)
		}
		disk[provider] = cred
		// Fence the provider before the rename can expose the new login:
		// until the write is confirmed durable no request may use it.
		s.mu.Lock()
		prev, hadPrev := s.pending[provider]
		s.pending[provider] = pendingRotation{fence: true}
		s.mu.Unlock()
		if err := s.saveLocked(disk); err != nil {
			if errors.Is(err, errNotWritten) {
				s.mu.Lock()
				delete(s.pending, provider)
				if hadPrev {
					s.pending[provider] = prev
				}
				s.mu.Unlock()
			}
			return false, nil, persistenceFailed(provider, op)
		}
		return false, nil, nil
	})
	if err != nil {
		return "", err
	}
	s.dropPending(provider)
	s.observe(useKey{provider, core.CredentialSourceStore, gen}, StatusSaved)
	return gen, nil
}

// StoredGeneration returns the generation currently saved for provider ("" for
// none or a legacy record), reading the file and ignoring the environment.
func (s *Store) StoredGeneration(provider string) (string, error) {
	disk, err := s.reload()
	if err != nil {
		return "", storeUnavailable(provider, "resolve")
	}
	return disk[provider].Generation, nil
}

// RetrySave persists a pending rotation for provider without any network
// call. It reconciles under lock with whatever other processes wrote: a newer
// login supersedes the rotation (nil, nothing written over it), and a
// same-generation divergence is a save conflict that keeps it pending.
func (s *Store) RetrySave(provider string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.transact(provider, "retry_save", func(map[string]Credential) (bool, *pendingRotation, error) {
		if s.hasPending(provider) {
			return false, nil, saveConflict(provider, "retry_save")
		}
		return false, nil, nil
	})
}

// Get retrieves the cached credential for a provider. It is best-effort and
// internal: inference must resolve through ResolveSnapshot, which re-reads the
// file and fails closed.
func (s *Store) Get(provider string) (Credential, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.data[provider]
	return c, ok
}

// Remove deletes a credential for a provider.
func (s *Store) Remove(provider string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	err := s.transact(provider, "remove", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		delete(disk, provider)
		return true, nil, nil
	})
	if err != nil {
		return err
	}
	s.dropPending(provider)
	return nil
}

// GetAPIKey resolves the API key for a provider.
// Priority:
//  1. Environment variable (ANTHROPIC_API_KEY, etc.)
//  2. OAuth token from store (auto-refreshed if expired)
//  3. API key from store
//
// Returns the key and whether it's an OAuth token.
func (s *Store) GetAPIKey(provider string) (key string, isOAuth bool, err error) {
	snap, err := s.ResolveSnapshot(context.Background(), provider)
	if err != nil {
		return "", false, err
	}
	return snap.Token, snap.Kind == "oauth", nil
}

// CredentialKind reports the credential origin without inspecting token
// contents. Environment values are always API keys, including JWT-shaped xAI
// values, so transport selection cannot be confused by token syntax.
func (s *Store) CredentialKind(provider string) string {
	if os.Getenv(envKeyForProvider(provider)) != "" {
		return "api_key"
	}
	disk, err := s.reload()
	if err != nil {
		return ""
	}
	return disk[provider].Type
}

// PeekOAuthToken returns the current OAuth access token for a provider WITHOUT
// triggering a refresh. It is for read-only, best-effort callers (e.g. the plan
// usage widget) that must never rotate the shared refresh token.
//
//   - isOAuth is true when an OAuth credential is in use for the provider.
//   - valid is true only when a non-expired access token is available.
//
// When isOAuth is true but valid is false, the token has expired: the caller
// should treat usage as temporarily unavailable rather than refresh, and let a
// real API call renew the token on demand.
func (s *Store) PeekOAuthToken(provider string) (token string, isOAuth, valid bool) {
	snap, err := s.PeekSnapshot(provider)
	if err != nil || snap.Kind != "oauth" {
		return "", false, false
	}
	if snap.Source == core.CredentialSourceEnv {
		return snap.Token, true, true // never refreshed; treated as always valid
	}
	if credentialExpired(snap.Credential) {
		return "", true, false // OAuth in use, but the access token has expired.
	}
	return snap.Credential.Access, true, true
}

// GetAccountID returns the account ID of the effective credential (e.g. the
// OpenAI chatgpt_account_id). An environment OAuth token answers from its own
// JWT and never borrows the stored login's account.
func (s *Store) GetAccountID(provider string) string {
	if os.Getenv(envKeyForProvider(provider)) != "" {
		snap, _, _ := envSnapshot(provider)
		return snap.AccountID
	}
	snap, err := s.PeekSnapshot(provider)
	if err != nil {
		return ""
	}
	return snap.AccountID
}

// tokenEndpoints selects where refresh requests go. Production uses the zero
// value, which pins every provider's fixed token endpoint; there is no
// environment or file override on purpose. Same-package tests fill it with
// httptest URLs.
type tokenEndpoints struct {
	client    *http.Client
	anthropic string
	openai    string
	xai       XAIEndpoints
	meta      metaEndpoints
}

// refresh dispatches to the correct provider's refresh function.
func (e tokenEndpoints) refresh(ctx context.Context, provider, refreshToken string) (*OAuthCredentials, error) {
	switch provider {
	case "openai":
		return refreshOpenAIToken(ctx, e.client, e.openai, refreshToken)
	case "anthropic":
		return refreshAnthropicToken(ctx, e.client, e.anthropic, refreshToken)
	case "xai":
		return refreshXAIToken(ctx, e.client, e.xai, refreshToken)
	case "meta":
		creds, err := refreshMetaToken(ctx, e.client, e.meta, refreshToken)
		if err != nil {
			return nil, err
		}
		refreshed := creds.OAuthCredentials
		refreshed.APIKey = creds.APIKey
		return &refreshed, nil
	default:
		return nil, fmt.Errorf("unsupported OAuth provider %q", provider)
	}
}

func envKeyForProvider(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	default:
		return strings.ToUpper(provider) + "_API_KEY"
	}
}
