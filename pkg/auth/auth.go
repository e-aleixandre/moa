// Package auth handles credential storage and OAuth flows for AI providers.
//
// Credentials are stored in ~/.config/moa/auth.json with mode 0600.
// Supports both API keys and OAuth tokens (Claude Max).
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
type Store struct {
	path      string
	data      map[string]Credential
	mu        sync.RWMutex
	refreshMu sync.Mutex // serializes OAuth token refresh (single-flight)

	// refresh performs the network token refresh. It is a field so tests can
	// substitute a fake; it defaults to refreshOAuthToken.
	refresh func(provider, refreshToken string) (*OAuthCredentials, error)
}

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

// NewStore creates or loads a credential store.
func NewStore(path string) *Store {
	if path == "" {
		path = DefaultStorePath()
	}
	s := &Store{
		path:    path,
		data:    make(map[string]Credential),
		refresh: refreshOAuthToken,
	}
	s.load()
	return s
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // File doesn't exist yet — that's fine
	}
	_ = json.Unmarshal(data, &s.data)
}

// withFileLock serializes auth.json updates between CLI and serve processes.
// The credential file itself is still atomically replaced; the adjacent lock
// file has a stable inode, so it remains useful across those replacements.
func (s *Store) withFileLock(fn func() error) error {
	if s.path == "" {
		return fn()
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return withPlatformFileLock(s.path+".lock", fn)
}

// loadFromDisk reads the credential file into a fresh map without mutating the
// in-memory store. Used before an OAuth refresh to pick up a token that a
// sibling process (e.g. serve + CLI) may have already rotated. The atomic
// rename in save() guarantees we read either the old or new complete file.
func (s *Store) loadFromDisk() (map[string]Credential, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	m := make(map[string]Credential)
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// adoptDisk replaces the in-memory map with the on-disk file. Callers must
// already hold the inter-process file lock: a later save() writes the whole
// map, so adopting only one provider would clobber a sibling's rotation of
// another. Anthropic (and others) rotate the refresh token on every use;
// overwriting the new one with a stale copy is a forced re-login.
func (s *Store) adoptDisk() {
	disk, err := s.loadFromDisk()
	if err != nil {
		return
	}
	s.mu.Lock()
	s.data = disk
	s.mu.Unlock()
}

func oauthCredential(provider string, previous Credential, refreshed *OAuthCredentials) (Credential, error) {
	refresh := refreshed.Refresh
	if refresh == "" {
		refresh = previous.Refresh
	}
	if refresh == "" {
		return Credential{}, fmt.Errorf("refresh response missing refresh token")
	}
	account := refreshed.AccountID
	if account == "" {
		account = previous.AccountID
	}
	key := ""
	if provider == "meta" {
		key = refreshed.APIKey
		if key == "" {
			key = previous.Key
		}
	}
	return Credential{Type: "oauth", Access: refreshed.Access, Refresh: refresh, Expires: refreshed.Expires, AccountID: account, Key: key}, nil
}

func (s *Store) save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling credentials: %w", err)
	}

	// Atomic write: unique temp file + sync + rename to prevent corruption
	tmp, err := os.CreateTemp(dir, "auth-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("syncing credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming credentials: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("syncing config dir: %w", err)
	}
	return nil
}

// Set stores a credential for a provider.
func (s *Store) Set(provider string, cred Credential) error {
	return s.withFileLock(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if disk, err := s.loadFromDisk(); err == nil {
			s.data = disk
		}
		s.data[provider] = cred
		return s.save()
	})
}

// Get retrieves a credential for a provider.
func (s *Store) Get(provider string) (Credential, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.data[provider]
	return c, ok
}

// Remove deletes a credential for a provider.
func (s *Store) Remove(provider string) error {
	return s.withFileLock(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if disk, err := s.loadFromDisk(); err == nil {
			s.data = disk
		}
		delete(s.data, provider)
		return s.save()
	})
}

// GetAPIKey resolves the API key for a provider.
// Priority:
//  1. Environment variable (ANTHROPIC_API_KEY, etc.)
//  2. OAuth token from store (auto-refreshed if expired)
//  3. API key from store
//
// Returns the key and whether it's an OAuth token.
func (s *Store) GetAPIKey(provider string) (key string, isOAuth bool, err error) {
	// 1. Environment variable
	envKey := envKeyForProvider(provider)
	if v := os.Getenv(envKey); v != "" {
		// XAI_API_KEY and META_API_KEY are always API keys. In particular, a
		// JWT-shaped value must never accidentally select an OAuth transport.
		if provider == "xai" || provider == "meta" {
			return v, false, nil
		}
		return v, IsOAuthToken(v), nil
	}

	// 2. Stored credential
	s.mu.RLock()
	cred, ok := s.data[provider]
	s.mu.RUnlock()

	if !ok {
		return "", false, fmt.Errorf("no credentials for provider %q: set %s or run --login", provider, envKey)
	}

	switch cred.Type {
	case "api_key":
		return cred.Key, false, nil

	case "oauth":
		// Fast path: token still valid, no refresh needed.
		if time.Now().UnixMilli() < cred.Expires {
			return derivedToken(provider, cred), true, nil
		}
		// Token expired — refresh, but serialize refreshes. The provider
		// rotates the refresh token on every use, so two concurrent refreshes
		// (e.g. agent + subagent) sharing the same token would invalidate each
		// other. refreshMu makes the refresh single-flight.
		s.refreshMu.Lock()
		defer s.refreshMu.Unlock()

		// Re-read under the refresh lock: another goroutine may have already
		// refreshed while we were blocked, in which case we reuse its token.
		s.mu.RLock()
		cred, ok = s.data[provider]
		s.mu.RUnlock()
		if !ok {
			return "", false, fmt.Errorf("no credentials for provider %q", provider)
		}
		if time.Now().UnixMilli() < cred.Expires {
			return derivedToken(provider, cred), true, nil
		}

		var result Credential
		var saveErr error
		err := s.withFileLock(func() error {
			// Reload the whole file after owning the inter-process lock. A
			// sibling (CLI + serve) may have rotated a *different* provider;
			// save() writes every credential, so adopting only this key would
			// put the sibling's new refresh token back to a stale copy.
			s.adoptDisk()
			s.mu.RLock()
			cred, ok = s.data[provider]
			s.mu.RUnlock()
			if !ok || cred.Type != "oauth" {
				return fmt.Errorf("no credentials for provider %q", provider)
			}
			if time.Now().UnixMilli() < cred.Expires {
				result = cred
				return nil
			}
			refreshed, err := s.refresh(provider, cred.Refresh)
			if err != nil {
				return err
			}
			result, err = oauthCredential(provider, cred, refreshed)
			if err != nil {
				return err
			}
			s.mu.Lock()
			s.data[provider] = result
			saveErr = s.save()
			s.mu.Unlock()
			return nil
		})
		if err != nil {
			return "", false, fmt.Errorf("token refresh failed: %w (run --login %s to re-authenticate)", err, provider)
		}
		if saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist refreshed %s token: %v (next start may require re-login)\n", provider, saveErr)
		}
		return derivedToken(provider, result), true, nil

	default:
		return "", false, fmt.Errorf("unknown credential type %q for provider %q", cred.Type, provider)
	}
}

// CredentialKind reports the credential origin without inspecting token
// contents. Environment values are always API keys, including JWT-shaped xAI
// values, so transport selection cannot be confused by token syntax.
func (s *Store) CredentialKind(provider string) string {
	if os.Getenv(envKeyForProvider(provider)) != "" {
		return "api_key"
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[provider].Type
}

// RefreshOAuthIfCurrent reactively rotates an OAuth token that a consumer API
// rejected. If another request already rotated it, that newer token is reused.
// It deliberately never considers environment variables: those are API keys.
func (s *Store) RefreshOAuthIfCurrent(provider, rejected string) (string, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.mu.RLock()
	cred, ok := s.data[provider]
	s.mu.RUnlock()
	if !ok || cred.Type != "oauth" {
		return "", fmt.Errorf("no OAuth credentials for provider %q", provider)
	}
	if derivedToken(provider, cred) != rejected {
		return derivedToken(provider, cred), nil
	}
	var result Credential
	err := s.withFileLock(func() error {
		s.adoptDisk()
		s.mu.RLock()
		cred, ok = s.data[provider]
		s.mu.RUnlock()
		if !ok || cred.Type != "oauth" {
			return fmt.Errorf("no OAuth credentials for provider %q", provider)
		}
		if derivedToken(provider, cred) != rejected {
			result = cred
			return nil
		}
		// Do not keep serving a token the consumer explicitly rejected. Persist
		// its invalid expiry before refreshing so a later request retries refresh
		// even if this attempt fails.
		cred.Expires = 0
		s.mu.Lock()
		s.data[provider] = cred
		s.mu.Unlock()
		refreshed, err := s.refresh(provider, cred.Refresh)
		if err != nil {
			s.mu.Lock()
			_ = s.save()
			s.mu.Unlock()
			return err
		}
		result, err = oauthCredential(provider, cred, refreshed)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.data[provider] = result
		err = s.save()
		s.mu.Unlock()
		return err
	})
	if err != nil {
		return "", fmt.Errorf("token refresh failed: %w (run --login %s to re-authenticate)", err, provider)
	}
	return derivedToken(provider, result), nil
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
	// 1. Environment variable (never refreshed; treated as always valid).
	if v := os.Getenv(envKeyForProvider(provider)); v != "" {
		if provider == "xai" || provider == "meta" {
			return "", false, false
		}
		if IsOAuthToken(v) {
			return v, true, true
		}
		return "", false, false
	}

	// 2. Stored credential — read only, never refresh.
	s.mu.RLock()
	defer s.mu.RUnlock()
	cred, ok := s.data[provider]
	if !ok || cred.Type != "oauth" {
		return "", false, false
	}
	if time.Now().UnixMilli() >= cred.Expires {
		return "", true, false // OAuth in use, but the access token has expired.
	}
	return cred.Access, true, true
}

// GetAccountID returns the stored account ID for a provider (e.g., OpenAI chatgpt_account_id).
func (s *Store) GetAccountID(provider string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cred, ok := s.data[provider]; ok {
		return cred.AccountID
	}
	return ""
}

// refreshOAuthToken dispatches to the correct provider's refresh function.
func refreshOAuthToken(provider, refreshToken string) (*OAuthCredentials, error) {
	switch provider {
	case "openai":
		return RefreshOpenAIToken(refreshToken)
	case "anthropic":
		return RefreshAnthropicToken(refreshToken)
	case "xai":
		return RefreshXAIToken(refreshToken)
	case "meta":
		creds, err := RefreshMetaToken(refreshToken)
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
