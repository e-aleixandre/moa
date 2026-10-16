package auth

import (
	"context"
	"os"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// SnapshotSelection narrows which credential a snapshot may select. Text uses
// the default; the narrow modes exist for consumers that must use one kind of
// credential and never another (voice calls).
type SnapshotSelection int

const (
	// SelectDefault is the text selection: environment first, then the
	// stored primary credential, whatever its kind.
	SelectDefault SnapshotSelection = iota
	// SelectPlanOAuth selects only the stored plan sign-in, even when an
	// API-key environment variable is set. It never selects a key.
	SelectPlanOAuth
	// SelectVoiceAPIKey selects only an API key: an environment API key, else
	// the key beside the plan sign-in, else a primary API key. It never
	// selects OAuth and never refreshes.
	SelectVoiceAPIKey
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

	// selection produced this snapshot; Admit and Refresh revalidate it.
	selection SnapshotSelection
	// record is the auth.json record a stored snapshot was read from.
	record string
}

func selectionOf(provider string, sel []SnapshotSelection) (SnapshotSelection, error) {
	if len(sel) == 0 {
		return SelectDefault, nil
	}
	if len(sel) == 1 && sel[0] >= SelectDefault && sel[0] <= SelectVoiceAPIKey {
		return sel[0], nil
	}
	return 0, core.NewProviderCredentialError(provider, core.CredentialSourceStore, "resolve", core.CredentialMissing)
}

// planUnavailable is returned when SelectPlanOAuth finds no plan sign-in.
func planUnavailable(provider, generation string) error {
	return withGeneration(missingCredential(provider), generation)
}

// ResolveSnapshot selects the environment or the stored credential for the
// next request. Stored credentials are re-read from disk, so a CLI login is
// noticed without a watcher; an expired OAuth token is rotated on demand, and
// a pending unsaved rotation is persisted before any new rotation. An optional
// selection narrows the choice; without one it is the text selection. When a
// narrow selection cannot be satisfied the error is a ProviderCredentialError
// of class missing.
func (s *Store) ResolveSnapshot(ctx context.Context, provider string, sel ...SnapshotSelection) (CredentialSnapshot, error) {
	selection, err := selectionOf(provider, sel)
	if err != nil {
		return CredentialSnapshot{}, err
	}
	switch selection {
	case SelectVoiceAPIKey:
		return s.resolveAPIKey(provider, true)
	case SelectDefault:
		if snap, ok, err := envSnapshot(provider); ok {
			return snap, err
		}
	}
	planOnly := selection == SelectPlanOAuth
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
		if planOnly && cred.Type != "oauth" {
			return CredentialSnapshot{}, planUnavailable(provider, cred.Generation)
		}
		if cred.Type != "oauth" || !credentialExpired(cred) {
			return selected(storeSnapshot(provider, cred), selection), nil
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
		if planOnly && cur.Type != "oauth" {
			return false, nil, planUnavailable(provider, cur.Generation)
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
	return selected(storeSnapshot(provider, result), selection), nil
}

// resolveAPIKey resolves a SelectVoiceAPIKey snapshot. It never refreshes:
// with resolve set, an unsaved change to the selected key is first persisted
// (as the text path does), otherwise it is reported as persistence_failed.
func (s *Store) resolveAPIKey(provider string, resolve bool) (CredentialSnapshot, error) {
	if env, ok, err := envSnapshot(provider); ok && err == nil && env.Kind == "api_key" {
		return selected(env, SelectVoiceAPIKey), nil
	}
	disk, err := s.reload()
	if err != nil {
		return CredentialSnapshot{}, storeUnavailable(provider, "resolve")
	}
	cred, record, err := selectAPIKey(disk, provider)
	if err != nil {
		return CredentialSnapshot{}, err
	}
	if s.hasPending(record) {
		if !resolve {
			return CredentialSnapshot{}, persistenceFailed(provider, "resolve")
		}
		s.refreshMu.Lock()
		defer s.refreshMu.Unlock()
		err = s.transact(provider, "resolve", func(disk map[string]Credential) (bool, *pendingRotation, error) {
			var err error
			if cred, record, err = selectAPIKey(disk, provider); err != nil {
				return false, nil, err
			}
			if s.hasPending(record) {
				return false, nil, saveConflict(provider, "resolve")
			}
			return false, nil, nil
		})
		if err != nil {
			return CredentialSnapshot{}, err
		}
	}
	snap := storeSnapshot(provider, cred)
	snap.record = record
	return selected(snap, SelectVoiceAPIKey), nil
}

func selected(snap CredentialSnapshot, selection SnapshotSelection) CredentialSnapshot {
	snap.selection = selection
	return snap
}

// PeekSnapshot is ResolveSnapshot without any refresh or write, for status,
// capabilities and usage. An expired OAuth snapshot is returned as is.
func (s *Store) PeekSnapshot(provider string, sel ...SnapshotSelection) (CredentialSnapshot, error) {
	selection, err := selectionOf(provider, sel)
	if err != nil {
		return CredentialSnapshot{}, err
	}
	switch selection {
	case SelectVoiceAPIKey:
		return s.resolveAPIKey(provider, false)
	case SelectDefault:
		if snap, ok, err := envSnapshot(provider); ok {
			return snap, err
		}
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
	if selection == SelectPlanOAuth && cred.Type != "oauth" {
		return CredentialSnapshot{}, planUnavailable(provider, cred.Generation)
	}
	return selected(storeSnapshot(provider, cred), selection), nil
}

// AdmitSnapshot checks a prepared selection under the Store lock before the
// caller's short dispatch gate. Refresh is prepared outside this section. The
// selection that produced expected is the one revalidated.
func (s *Store) AdmitSnapshot(ctx context.Context, expected CredentialSnapshot, admit func(CredentialSnapshot) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if expected.selection == SelectVoiceAPIKey {
		return s.admitAPIKey(ctx, expected, admit)
	}
	if env, set, err := envSnapshot(expected.Provider); set && expected.selection == SelectDefault {
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
		return false, nil, admit(selected(storeSnapshot(expected.Provider, cur), expected.selection))
	})
}

// admitAPIKey is AdmitSnapshot for a SelectVoiceAPIKey snapshot: the same key
// must still be the one that selection picks.
func (s *Store) admitAPIKey(ctx context.Context, expected CredentialSnapshot, admit func(CredentialSnapshot) error) error {
	provider := expected.Provider
	if expected.Source == core.CredentialSourceEnv {
		env, set, err := envSnapshot(provider)
		if !set || err != nil || env.Kind != "api_key" || env.Token != expected.Token {
			return credentialsChanged(provider, "dispatch", "")
		}
		return admit(selected(env, SelectVoiceAPIKey))
	}
	if env, set, err := envSnapshot(provider); set && err == nil && env.Kind == "api_key" {
		return credentialsChanged(provider, "dispatch", expected.Generation)
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.transact(provider, "dispatch", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		cur, record, err := selectAPIKey(disk, provider)
		if err != nil || record != expected.record || cur.Generation != expected.Generation || cur.Key != expected.Token || s.hasPending(record) {
			return false, nil, credentialsChanged(provider, "dispatch", expected.Generation)
		}
		snap := storeSnapshot(provider, cur)
		snap.record = record
		return false, nil, admit(selected(snap, SelectVoiceAPIKey))
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
	if snap.selection == SelectVoiceAPIKey {
		// An API key has nothing to refresh; a rejected key must be replaced.
		return CredentialSnapshot{}, withGeneration(core.NewProviderCredentialError(provider, snap.Source, "refresh", core.CredentialKeyRejected), snap.Generation)
	}
	planOnly := snap.selection == SelectPlanOAuth
	env, envSet, _ := envSnapshot(provider)
	if snap.Source == core.CredentialSourceEnv {
		if envSet && env.Token == snap.Token {
			// The environment has no store refresh; it must be fixed there.
			return CredentialSnapshot{}, core.NewProviderCredentialError(provider, core.CredentialSourceEnv, "refresh", core.CredentialReconnect)
		}
		return CredentialSnapshot{}, credentialsChanged(provider, "refresh", "")
	}
	// Text yields to an environment credential; a plan-only snapshot chose
	// the stored sign-in regardless of it.
	if (envSet && !planOnly) || snap.Kind != "oauth" {
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
	return selected(storeSnapshot(provider, result), snap.selection), nil
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
		record:     provider,
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
