package auth

import (
	"context"
	"errors"

	"github.com/e-aleixandre/moa/pkg/core"
)

// anthropicBackupSlot holds the Anthropic API key saved next to a plan sign-in.
// The name is the one PR #36 shipped, so a key stored then keeps working.
const anthropicBackupSlot = "__anthropic_backup_v1"

// BackupRevision is a secret-free CAS tuple, not a credential or a capability.
type BackupRevision struct {
	Primary string `json:"primary_generation"`
	Key     string `json:"key_generation"`
}

// AnthropicBackupStatus describes the API key stored next to the plan sign-in.
// Active means it is used once the plan reports a 5h or weekly limit: storing
// the key is the whole consent, there is no separate switch.
type AnthropicBackupStatus struct {
	Revision   BackupRevision `json:"revision"`
	Configured bool           `json:"configured"`
	Active     bool           `json:"active"`
	State      string         `json:"state"`
}

type AnthropicBackupSelection struct {
	Snapshot CredentialSnapshot `json:"-"`
	Revision BackupRevision
}

// readBackup ignores the backup_policy written by PR #36: its enabled flag and
// primary association no longer gate anything, so a key saved then keeps
// working without a migration write.
func readBackup(disk map[string]Credential) (Credential, error) {
	cred, ok := disk[anthropicBackupSlot]
	if !ok || cred.Key == "" {
		return Credential{}, nil
	}
	if cred.Type != "api_key" || cred.Generation == "" {
		return Credential{}, storeUnavailable("anthropic", "backup")
	}
	return cred, nil
}

func backupView(disk map[string]Credential) (AnthropicBackupStatus, error) {
	cred, err := readBackup(disk)
	if err != nil {
		return AnthropicBackupStatus{}, err
	}
	primary := disk["anthropic"]
	st := AnthropicBackupStatus{Revision: BackupRevision{Primary: primary.Generation, Key: cred.Generation}, Configured: cred.Key != "", State: "not_configured"}
	_, env, _ := envSnapshot("anthropic")
	st.Active = st.Configured && !env && primary.Type == "oauth" && primary.Generation != ""
	switch {
	case st.Active:
		st.State = "active"
	case st.Configured:
		st.State = "inactive"
	}
	return st, nil
}

func (s *Store) AnthropicBackupStatus() (AnthropicBackupStatus, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	var out AnthropicBackupStatus
	err := s.withFileLock(func() error {
		disk, err := s.reload()
		if err != nil {
			return storeUnavailable("anthropic", "backup_status")
		}
		out, err = backupView(disk)
		if err != nil {
			return err
		}
		if s.backupBlocked || s.hasPending("anthropic") {
			out.Active = false
			out.State = "save_failed"
			return nil
		}
		// A second Store has no pending fence. Publish active only after its
		// own durable acknowledgement, not just because rename made it visible.
		// Reading status never flushes an unrelated primary OAuth rotation.
		if out.Active {
			if err := s.saveLocked(disk); err != nil {
				return persistenceFailed("anthropic", "backup_status")
			}
			s.dropPending(anthropicBackupSlot)
		}
		if rec, seen := s.observed(useKey{anthropicBackupSlot, "store", out.Revision.Key}); seen && rec.state == "api_error" {
			out.State = "api_error"
		}
		return nil
	})
	if err != nil {
		return AnthropicBackupStatus{}, storeUnavailable("anthropic", "backup_status")
	}
	return out, nil
}

// Backup results are attributed only to their secondary generation. They
// never feed the primary OAuth status or expire its access token.
func (s *Store) RecordAnthropicBackupUse(revision BackupRevision, err error) {
	disk, loadErr := s.reload()
	if loadErr != nil {
		return
	}
	st, loadErr := backupView(disk)
	if loadErr != nil || st.Revision != revision {
		return
	}
	state := StatusReady
	if err != nil {
		state = "api_error"
	}
	s.observe(useKey{anthropicBackupSlot, "store", revision.Key}, state)
}

// SaveAnthropicAPIKey stores the one Anthropic API key. Next to a stored plan
// sign-in it goes to the second slot and never replaces the sign-in; without
// one it is the primary credential, exactly as before. expectedPrimary is the
// generation the caller saw for the primary credential. The key is validated
// by the caller.
func (s *Store) SaveAnthropicAPIKey(expectedPrimary, key string) (string, error) {
	if _, env, _ := envSnapshot("anthropic"); env {
		return "", &LoginError{Provider: "anthropic", Class: LoginEnvManaged}
	}
	gen, err := newGeneration()
	if err != nil {
		return "", persistenceFailed("anthropic", "api_key")
	}
	besidePlan := false
	s.refreshMu.Lock()
	err = s.transact("anthropic", "api_key", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		primary := disk["anthropic"]
		if primary.Generation != expectedPrimary {
			return false, nil, credentialsChanged("anthropic", "api_key", primary.Generation)
		}
		if primary.Type != "oauth" {
			return false, nil, nil
		}
		besidePlan = true
		if _, err := readBackup(disk); err != nil {
			return false, nil, err
		}
		// Give a legacy grant a durable identity without replacing it.
		if primary.Generation == "" {
			var err error
			if primary.Generation, err = newGeneration(); err != nil {
				return false, nil, persistenceFailed("anthropic", "api_key")
			}
			disk["anthropic"] = primary
		}
		disk[anthropicBackupSlot] = Credential{Type: "api_key", Key: key, Generation: gen}
		s.mu.Lock()
		s.pending[anthropicBackupSlot] = pendingRotation{fence: true}
		s.mu.Unlock()
		if err := s.saveLocked(disk); err != nil {
			if errors.Is(err, errNotWritten) {
				s.dropPending(anthropicBackupSlot)
			}
			return false, nil, persistenceFailed("anthropic", "api_key")
		}
		s.backupBlocked = false
		s.dropPending(anthropicBackupSlot)
		return false, nil, nil
	})
	s.refreshMu.Unlock()
	if err != nil {
		return "", err
	}
	if besidePlan {
		return gen, nil
	}
	// The CAS on expectedPrimary rejects a plan sign-in that landed meanwhile.
	return s.CommitLogin("anthropic", expectedPrimary, Credential{Type: "api_key", Key: key})
}

// RemoveAnthropicAPIKey deletes the key stored next to the plan sign-in, which
// stays signed in. expectedKey is the key generation the caller saw.
func (s *Store) RemoveAnthropicAPIKey(expectedKey string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	err := s.transact("anthropic", "backup_remove", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		cred, err := readBackup(disk)
		if err != nil {
			return false, nil, err
		}
		if cred.Generation != expectedKey {
			return false, nil, credentialsChanged("anthropic", "backup", cred.Generation)
		}
		delete(disk, anthropicBackupSlot)
		s.mu.Lock()
		s.pending[anthropicBackupSlot] = pendingRotation{fence: true}
		s.mu.Unlock()
		if err := s.saveLocked(disk); err != nil {
			// A failed remove must not keep paying locally. Another process
			// cannot be promised revocation before the durable ack.
			s.backupBlocked = true
			if errors.Is(err, errNotWritten) {
				s.dropPending(anthropicBackupSlot)
			}
			return false, nil, persistenceFailed("anthropic", "backup")
		}
		s.backupBlocked = false
		s.dropPending(anthropicBackupSlot)
		return false, nil, nil
	})
	// transact can also fail before the callback runs (lock, pending OAuth flush).
	if pe, ok := core.AsProviderCredentialError(err); ok && (pe.Class == core.CredentialStoreUnavailable || pe.Class == core.CredentialPersistenceFailed) {
		s.backupBlocked = true
	}
	return err
}

// keepKeyBesidePlan keeps an Anthropic API key that a plan sign-in is about to
// replace as the primary credential: with both, the plan comes first and the
// key takes over at its limit. An existing second key is never overwritten.
func keepKeyBesidePlan(disk map[string]Credential, provider string, next Credential) error {
	prev := disk[provider]
	if provider != "anthropic" || next.Type != "oauth" || prev.Type != "api_key" || prev.Key == "" {
		return nil
	}
	if cur, err := readBackup(disk); err != nil || cur.Key != "" {
		return nil
	}
	gen, err := newGeneration()
	if err != nil {
		return err
	}
	disk[anthropicBackupSlot] = Credential{Type: "api_key", Key: prev.Key, Generation: gen}
	return nil
}

// AdmitAnthropicBackup linearizes a paid dispatch while holding the existing
// Store/file locks. The callback is a short Agent admission gate, never HTTP.
// No resolved key escapes this method until durability and the current key
// have both been acknowledged. Every retry calls it again after its sleep.
func (s *Store) AdmitAnthropicBackup(ctx context.Context, expected BackupRevision, admit func(AnthropicBackupSelection) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.transact("anthropic", "backup_dispatch", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		st, err := backupView(disk)
		if err != nil {
			return false, nil, err
		}
		if s.backupBlocked || !st.Active || st.Revision != expected || s.hasPending("anthropic") {
			return false, nil, credentialsChanged("anthropic", "backup", expected.Primary)
		}
		if err := s.saveLocked(disk); err != nil {
			return false, nil, persistenceFailed("anthropic", "backup_dispatch")
		}
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		cred := disk[anthropicBackupSlot]
		return false, nil, admit(AnthropicBackupSelection{Snapshot: storeSnapshot("anthropic", cred), Revision: st.Revision})
	})
}
