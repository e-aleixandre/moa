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
)

// Credential represents a stored credential for a provider.
type Credential struct {
	Type      string `json:"type"`                        // "api_key" or "oauth"
	Key       string `json:"key,omitempty"`                // API key (type=api_key)
	Access    string `json:"access,omitempty"`             // OAuth access token (type=oauth)
	Refresh   string `json:"refresh,omitempty"`            // OAuth refresh token (type=oauth)
	Expires   int64  `json:"expires,omitempty"`            // OAuth token expiry (unix ms) (type=oauth)
	AccountID string `json:"account_id,omitempty"`         // Provider-specific account ID (e.g., OpenAI chatgpt_account_id)
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
	path string
	data map[string]Credential
	mu   sync.RWMutex
}

// configDir returns the directory for storing credentials.
// Honors MOA_CONFIG_DIR env var for container/custom deployments.
func configDir() string {
	if dir := os.Getenv("MOA_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "moa")
}

// DefaultStorePath returns the default path for the auth store.
func DefaultStorePath() string {
	return filepath.Join(configDir(), "auth.json")
}

// NewStore creates or loads a credential store.
func NewStore(path string) *Store {
	if path == "" {
		path = DefaultStorePath()
	}
	s := &Store{
		path: path,
		data: make(map[string]Credential),
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
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("syncing credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming credentials: %w", err)
	}
	return nil
}

// Set stores a credential for a provider.
func (s *Store) Set(provider string, cred Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[provider] = cred
	return s.save()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, provider)
	return s.save()
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
		// Check if token needs refresh
		if time.Now().UnixMilli() >= cred.Expires {
			refreshed, err := refreshOAuthToken(provider, cred.Refresh)
			if err != nil {
				return "", false, fmt.Errorf("token refresh failed: %w (run --login %s to re-authenticate)", err, provider)
			}
			cred = Credential{
				Type:      "oauth",
				Access:    refreshed.Access,
				Refresh:   refreshed.Refresh,
				Expires:   refreshed.Expires,
				AccountID: refreshed.AccountID,
			}
			// Save refreshed token (ignore save errors — the token is still usable)
			s.mu.Lock()
			s.data[provider] = cred
			_ = s.save()
			s.mu.Unlock()
		}
		return cred.Access, true, nil

	default:
		return "", false, fmt.Errorf("unknown credential type %q for provider %q", cred.Type, provider)
	}
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
	default:
		return RefreshAnthropicToken(refreshToken)
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
