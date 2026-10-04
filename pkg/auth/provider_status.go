package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Provider status states, as shown in Settings → Providers.
const (
	StatusMissing          = "missing"
	StatusSaved            = "saved"
	StatusReady            = "ready"
	StatusRenewOnUse       = "renew_on_use"
	StatusReconnect        = "reconnect"
	StatusKeyRejected      = "key_rejected"
	StatusTemporary        = "temporary"
	StatusSaveFailed       = "save_failed"
	StatusStoreUnavailable = "store_unavailable"
)

// ProviderStatus is the secret-free state of a provider's selected
// credential. Zero times mean "not known in this server lifetime".
type ProviderStatus struct {
	Provider    string
	Source      string // core.CredentialSourceEnv or core.CredentialSourceStore
	Kind        string // "api_key", "oauth" or "" when unknown
	Generation  string
	State       string
	PendingSave bool // an unsaved rotation that RetrySave can persist
	ChangedAt   time.Time
	RenewAt     time.Time
	LastUseOKAt time.Time
}

// NeedsAttention reports a problem only the owner can resolve.
func (st ProviderStatus) NeedsAttention() bool {
	switch st.State {
	case StatusReconnect, StatusKeyRejected, StatusSaveFailed, StatusStoreUnavailable:
		return true
	}
	return false
}

// ProviderStatus reports provider's current state from a refresh-free read
// of the selection plus what this process has observed about it. It makes no
// network call and writes nothing.
func (s *Store) ProviderStatus(provider string) ProviderStatus {
	st := ProviderStatus{Provider: provider, Source: core.CredentialSourceStore}
	snap, err := s.PeekSnapshot(provider)
	if err != nil {
		pe, ok := core.AsProviderCredentialError(err)
		if !ok {
			st.State = StatusStoreUnavailable
			return st
		}
		st.Source = pe.Source
		switch pe.Class {
		case core.CredentialPersistenceFailed:
			// A rotation the provider accepted is waiting to be saved.
			st.State, st.PendingSave = StatusSaveFailed, true
			st.Generation, _ = s.StoredGeneration(provider)
		case core.CredentialMissing:
			st.State = StatusMissing
		case core.CredentialReconnect:
			st.State, st.Generation = StatusReconnect, pe.Generation
		default:
			st.State = StatusStoreUnavailable
		}
		return st
	}
	st.Source, st.Kind, st.Generation = snap.Source, snap.Kind, snap.Generation
	stored := snap.Source == core.CredentialSourceStore && snap.Kind == "oauth"
	if stored && snap.Credential.Expires > 0 {
		st.RenewAt = time.UnixMilli(snap.Credential.Expires)
	}
	rec, seen := s.observed(useKey{provider, snap.Source, snap.Generation})
	switch {
	case seen && rec.state != StatusReady && rec.state != StatusSaved:
		st.State = rec.state // reconnect, key_rejected or temporary
	case stored && credentialExpired(snap.Credential):
		st.State = StatusRenewOnUse
	case seen:
		st.State = rec.state
	default:
		st.State = StatusSaved
	}
	if seen {
		st.LastUseOKAt = rec.lastOK
		if st.State == rec.state {
			st.ChangedAt = rec.changedAt
		}
	}
	return st
}

// useKey identifies one credential selection: a login generation is never
// reused, so results of an earlier one cannot describe the current one.
type useKey struct{ provider, source, generation string }

type useRecord struct {
	state     string
	changedAt time.Time
	lastOK    time.Time
}

// RecordUse records the outcome of one provider request made with snap: a
// success, or a classified credential failure the owner or user can act on.
// Other failures (quota, permissions, credentials changed mid-request, store
// problems that the status reads directly) are not recorded. A result is
// kept only while snap is still the provider's selection, so a late answer
// to an older login can neither set nor clear the current one's state. For a
// failure before any snapshot existed, pass a snapshot with only Provider
// set: the classified error says which selection failed.
func (s *Store) RecordUse(snap CredentialSnapshot, err error) {
	state := useState(err)
	if state == "" {
		return
	}
	key := useKey{snap.Provider, snap.Source, snap.Generation}
	if key.source == "" {
		pe, ok := core.AsProviderCredentialError(err)
		if !ok {
			return
		}
		key.source, key.generation = pe.Source, pe.Generation
	}
	if !s.isSelected(key) {
		return
	}
	if s.useSelected != nil {
		s.useSelected(key)
	}
	s.observe(key, state)
}

func useState(err error) string {
	if err == nil {
		return StatusReady
	}
	pe, ok := core.AsProviderCredentialError(err)
	if !ok {
		return ""
	}
	switch pe.Class {
	case core.CredentialReconnect:
		return StatusReconnect
	case core.CredentialKeyRejected:
		return StatusKeyRejected
	case core.CredentialTemporary, core.CredentialProviderUnavailable:
		return StatusTemporary
	}
	return ""
}

// isSelected reports, without refreshing or writing, whether key is the
// provider's current selection.
func (s *Store) isSelected(key useKey) bool {
	if _, env, _ := envSnapshot(key.provider); env {
		return key.source == core.CredentialSourceEnv
	}
	if key.source != core.CredentialSourceStore {
		return false
	}
	disk, err := s.reload()
	if err != nil {
		return false
	}
	cred, ok := disk[key.provider]
	return ok && cred.Generation == key.generation
}

// observe sets key's state. Records of earlier selections are kept rather
// than pruned here: a late result may reach this point after a newer
// selection was recorded, and pruning would then erase the current record.
// Generations are never reused, so stale records are inert.
func (s *Store) observe(key useKey, state string) {
	now := time.Now()
	s.useMu.Lock()
	defer s.useMu.Unlock()
	if s.uses == nil {
		s.uses = map[useKey]useRecord{}
	}
	rec := s.uses[key]
	if rec.state != state {
		rec.state, rec.changedAt = state, now
	}
	if state == StatusReady {
		rec.lastOK = now
	}
	s.uses[key] = rec
}

func (s *Store) observed(key useKey) (useRecord, bool) {
	s.useMu.Lock()
	defer s.useMu.Unlock()
	rec, ok := s.uses[key]
	return rec, ok
}

// NewProviderLoginManagerWithHTTPClient is NewProviderLoginManager whose
// token exchanges and device flow go through client. The fixed production
// endpoints are unchanged: this is a server-side Go seam for tests that
// route those origins to a local server, never configuration.
func NewProviderLoginManagerWithHTTPClient(ctx context.Context, store *Store, client *http.Client) *ProviderLoginManager {
	m := NewProviderLoginManager(ctx, store)
	m.endpoints.client = client
	return m
}
