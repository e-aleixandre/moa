package auth

import (
	"context"
	"os"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// CredentialSnapshot is one coherent credential selection: source, kind,
// credential, account and generation read together, so a request never mixes
// the token of one login with the account or transport of another. It holds
// secrets and is server-side only; never serialize it.
type CredentialSnapshot struct {
	Provider string
	Source   string // core.CredentialSourceEnv or core.CredentialSourceStore
	Kind     string // "api_key" or "oauth"
	// Credential is the stored record, or a synthetic one for the environment.
	Credential Credential `json:"-"`
	// Token is the value the provider API accepts (access token, key or Meta
	// minted key).
	Token      string `json:"-"`
	AccountID  string
	Generation string // "" for environment and legacy records
}

// ResolveSnapshot selects the environment or the stored credential for the
// next request. Stored credentials are re-read from disk, so a CLI login is
// noticed without a watcher; an expired OAuth token is rotated on demand, and
// a pending unsaved rotation is persisted before any new rotation.
func (s *Store) ResolveSnapshot(ctx context.Context, provider string) (CredentialSnapshot, error) {
	if snap, ok, err := envSnapshot(provider); ok {
		return snap, err
	}
	disk, err := s.reload()
	if err != nil {
		return CredentialSnapshot{}, storeUnavailable(provider, "resolve")
	}
	if !s.hasPending(provider) {
		cred, ok := disk[provider]
		if !ok {
			return CredentialSnapshot{}, missingCredential(provider)
		}
		if err := checkStored(provider, cred); err != nil {
			return CredentialSnapshot{}, err
		}
		if cred.Type != "oauth" || !credentialExpired(cred) {
			return storeSnapshot(provider, cred), nil
		}
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	var result Credential
	err = s.transact(provider, "refresh", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		if s.hasPending(provider) {
			return false, nil, saveConflict(provider, "refresh")
		}
		cur, ok := disk[provider]
		if !ok {
			return false, nil, missingCredential(provider)
		}
		if err := checkStored(provider, cur); err != nil {
			return false, nil, err
		}
		if cur.Type != "oauth" || !credentialExpired(cur) {
			result = cur // a sibling already rotated it
			return false, nil, nil
		}
		next, err := s.rotate(ctx, provider, cur)
		if err != nil {
			return false, nil, err
		}
		disk[provider], result = next, next
		return true, &pendingRotation{base: cur, proposed: next}, nil
	})
	if err != nil {
		return CredentialSnapshot{}, err
	}
	return storeSnapshot(provider, result), nil
}

// PeekSnapshot is ResolveSnapshot without any refresh or write, for status,
// capabilities and usage. An expired OAuth snapshot is returned as is.
func (s *Store) PeekSnapshot(provider string) (CredentialSnapshot, error) {
	if snap, ok, err := envSnapshot(provider); ok {
		return snap, err
	}
	disk, err := s.reload()
	if err != nil {
		return CredentialSnapshot{}, storeUnavailable(provider, "resolve")
	}
	if s.hasPending(provider) {
		return CredentialSnapshot{}, persistenceFailed(provider, "resolve")
	}
	cred, ok := disk[provider]
	if !ok {
		return CredentialSnapshot{}, missingCredential(provider)
	}
	if err := checkStored(provider, cred); err != nil {
		return CredentialSnapshot{}, err
	}
	return storeSnapshot(provider, cred), nil
}

// AdmitSnapshot checks a prepared selection under the Store lock before the
// caller's short dispatch gate. Refresh is prepared outside this section.
func (s *Store) AdmitSnapshot(ctx context.Context, expected CredentialSnapshot, admit func(CredentialSnapshot) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if env, set, err := envSnapshot(expected.Provider); set {
		if err != nil {
			return err
		}
		if expected.Source != env.Source || expected.Kind != env.Kind || expected.Token != env.Token {
			return credentialsChanged(expected.Provider, "dispatch", expected.Generation)
		}
		return admit(env)
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.transact(expected.Provider, "dispatch", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		cur, ok := disk[expected.Provider]
		if !ok || expected.Source != core.CredentialSourceStore || cur.Type != expected.Kind || cur.Generation != expected.Generation || cur.AccountID != expected.AccountID || s.hasPending(expected.Provider) || (cur.Type == "oauth" && credentialExpired(cur)) || (cur.Type == "api_key" && cur.Key != expected.Token) {
			return false, nil, credentialsChanged(expected.Provider, "dispatch", expected.Generation)
		}
		return false, nil, admit(storeSnapshot(expected.Provider, cur))
	})
}

// RefreshOAuthIfGeneration handles a consumer rejecting snap's token. If the
// current selection is no longer snap's (source, generation, account), it
// returns credentials_changed without any network call: the request must be
// sent again, not retried with another login's token. If the same login was
// already rotated by someone else, that token is reused; otherwise exactly one
// refresh happens.
func (s *Store) RefreshOAuthIfGeneration(ctx context.Context, snap CredentialSnapshot, rejected string) (CredentialSnapshot, error) {
	provider := snap.Provider
	env, envSet, _ := envSnapshot(provider)
	if snap.Source == core.CredentialSourceEnv {
		if envSet && env.Token == snap.Token {
			// The environment has no store refresh; it must be fixed there.
			return CredentialSnapshot{}, core.NewProviderCredentialError(provider, core.CredentialSourceEnv, "refresh", core.CredentialReconnect)
		}
		return CredentialSnapshot{}, credentialsChanged(provider, "refresh", "")
	}
	if envSet || snap.Kind != "oauth" {
		return CredentialSnapshot{}, credentialsChanged(provider, "refresh", snap.Generation)
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	var result Credential
	var refreshErr error
	err := s.transact(provider, "refresh", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		if s.hasPending(provider) {
			return false, nil, saveConflict(provider, "refresh")
		}
		cur, ok := disk[provider]
		if !ok || cur.Type != "oauth" || cur.Generation != snap.Generation || cur.AccountID != snap.AccountID {
			return false, nil, credentialsChanged(provider, "refresh", snap.Generation)
		}
		if derivedToken(provider, cur) != rejected {
			result = cur
			return false, nil, nil
		}
		next, err := s.rotate(ctx, provider, cur)
		if err != nil {
			// Do not keep serving a token the consumer explicitly rejected:
			// persist it as expired so the next request refreshes instead.
			cur.Expires = 0
			disk[provider], refreshErr = cur, err
			return true, nil, nil
		}
		disk[provider], result = next, next
		return true, &pendingRotation{base: cur, proposed: next}, nil
	})
	if refreshErr != nil {
		return CredentialSnapshot{}, refreshErr
	}
	if err != nil {
		return CredentialSnapshot{}, err
	}
	return storeSnapshot(provider, result), nil
}

// rotate performs one network refresh of cur and returns the rotated
// credential, keeping cur's generation. Errors are always classified.
func (s *Store) rotate(ctx context.Context, provider string, cur Credential) (Credential, error) {
	if cur.Refresh == "" {
		return Credential{}, withGeneration(core.NewProviderCredentialError(provider, core.CredentialSourceStore, "refresh", core.CredentialReconnect), cur.Generation)
	}
	refreshed, err := s.refresh(ctx, provider, cur.Refresh)
	if err != nil {
		return Credential{}, withGeneration(classifyRefreshError(provider, err), cur.Generation)
	}
	next, err := oauthCredential(provider, cur, refreshed)
	if err != nil {
		return Credential{}, withGeneration(classifyRefreshError(provider, err), cur.Generation)
	}
	return next, nil
}

// envSnapshot reports whether the environment selects provider's credential.
// XAI_API_KEY and META_API_KEY are always API keys: a JWT-shaped value must
// never select an OAuth transport. An OpenAI OAuth token takes its account from
// its own JWT; without one the environment needs fixing, and the stored
// login's account is never borrowed.
func envSnapshot(provider string) (CredentialSnapshot, bool, error) {
	v := os.Getenv(envKeyForProvider(provider))
	if v == "" {
		return CredentialSnapshot{}, false, nil
	}
	snap := CredentialSnapshot{
		Provider:   provider,
		Source:     core.CredentialSourceEnv,
		Kind:       "api_key",
		Credential: Credential{Type: "api_key", Key: v},
		Token:      v,
	}
	if provider == "xai" || provider == "meta" || !IsOAuthToken(v) {
		return snap, true, nil
	}
	snap.Kind = "oauth"
	snap.Credential = Credential{Type: "oauth", Access: v}
	if provider == "openai" {
		snap.AccountID = extractOpenAIAccountID(v)
		if snap.AccountID == "" {
			return snap, true, core.NewProviderCredentialError(provider, core.CredentialSourceEnv, "resolve", core.CredentialMissing)
		}
	}
	return snap, true, nil
}

func storeSnapshot(provider string, cred Credential) CredentialSnapshot {
	token := cred.Key
	if cred.Type == "oauth" {
		token = derivedToken(provider, cred)
	}
	return CredentialSnapshot{
		Provider:   provider,
		Source:     core.CredentialSourceStore,
		Kind:       cred.Type,
		Credential: cred,
		Token:      token,
		AccountID:  cred.AccountID,
		Generation: cred.Generation,
	}
}

// checkStored rejects records that cannot select a transport. The stored type
// decides the transport; token syntax is never re-inspected.
func checkStored(provider string, cred Credential) error {
	switch {
	case cred.Type == "api_key" && cred.Key != "":
		return nil
	case cred.Type == "oauth" && (cred.Access != "" || cred.Refresh != ""):
		return nil
	}
	return withGeneration(core.NewProviderCredentialError(provider, core.CredentialSourceStore, "resolve", core.CredentialReconnect), cred.Generation)
}

func credentialExpired(cred Credential) bool {
	return time.Now().UnixMilli() >= cred.Expires
}
