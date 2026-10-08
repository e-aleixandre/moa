package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

const anthropicBackupSlot = "__anthropic_backup_v1"

// BackupRevision is a secret-free CAS tuple, not a credential or a capability.
type BackupRevision struct {
	Primary string `json:"primary_generation"`
	Key     string `json:"key_generation"`
	Policy  string `json:"policy_generation"`
}

type AnthropicBackupStatus struct {
	Revision   BackupRevision `json:"revision"`
	Configured bool           `json:"configured"`
	Enabled    bool           `json:"enabled"`
	Eligible   bool           `json:"eligible"`
	State      string         `json:"state"`
}

type AnthropicBackupSelection struct {
	Snapshot CredentialSnapshot `json:"-"`
	Revision BackupRevision
}

type backupPolicy struct {
	Version    int    `json:"version"`
	Primary    string `json:"primary_generation"`
	Generation string `json:"policy_generation"`
	Enabled    bool   `json:"enabled"`
}

func readBackup(disk map[string]Credential) (Credential, backupPolicy, error) {
	cred, ok := disk[anthropicBackupSlot]
	if !ok {
		return Credential{}, backupPolicy{}, nil
	}
	var policy backupPolicy
	raw := cred.extra["backup_policy"]
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&policy) != nil || d.Decode(&struct{}{}) != io.EOF || policy.Version != 1 || policy.Generation == "" || policy.Primary == "" || cred.Generation == "" || cred.Type != "api_key" {
		return Credential{}, backupPolicy{}, storeUnavailable("anthropic", "backup")
	}
	// Null is not an opt-out; unknown policy shapes are never authority.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 4 {
		return Credential{}, backupPolicy{}, storeUnavailable("anthropic", "backup")
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Credential{}, backupPolicy{}, storeUnavailable("anthropic", "backup")
		}
	}
	return cred, policy, nil
}

func backupView(disk map[string]Credential) (AnthropicBackupStatus, error) {
	cred, policy, err := readBackup(disk)
	if err != nil {
		return AnthropicBackupStatus{}, err
	}
	primary := disk["anthropic"]
	st := AnthropicBackupStatus{Revision: BackupRevision{Primary: primary.Generation, Key: cred.Generation, Policy: policy.Generation}, Configured: cred.Key != "", State: "not_configured"}
	_, env, _ := envSnapshot("anthropic")
	st.Eligible = !env && primary.Type == "oauth" && primary.Generation != ""
	st.Enabled = st.Configured && st.Eligible && policy.Enabled && policy.Primary == primary.Generation
	if st.Configured {
		st.State = "stored_off"
	}
	if st.Configured && policy.Primary != primary.Generation {
		st.State = "primary_changed"
	}
	if st.Enabled {
		st.State = "enabled"
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
			out.Enabled = false
			out.State = "save_failed"
			return nil
		}
		// A second Store has no pending fence. Publish enabled only after its
		// own durable acknowledgement, not just because rename made it visible.
		// Reading status never flushes an unrelated primary OAuth rotation.
		if out.Enabled {
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

func (s *Store) SaveAnthropicBackup(expected BackupRevision, key string) (AnthropicBackupStatus, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 8192 || strings.ContainsAny(key, "\r\n\t ") || IsOAuthToken(key) {
		return AnthropicBackupStatus{}, &LoginError{Provider: "anthropic", Class: LoginInvalidKey}
	}
	return s.mutateBackup(expected, "save", key, false)
}

func (s *Store) SetAnthropicBackupEnabled(expected BackupRevision, enabled bool) (AnthropicBackupStatus, error) {
	return s.mutateBackup(expected, "enable", "", enabled)
}

func (s *Store) RemoveAnthropicBackup(expected BackupRevision) (AnthropicBackupStatus, error) {
	return s.mutateBackup(expected, "remove", "", false)
}

func (s *Store) mutateBackup(expected BackupRevision, op, key string, enabled bool) (AnthropicBackupStatus, error) {
	if _, env, _ := envSnapshot("anthropic"); env {
		return AnthropicBackupStatus{}, &LoginError{Provider: "anthropic", Class: LoginEnvManaged}
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	var out AnthropicBackupStatus
	err := s.transact("anthropic", "backup_"+op, func(disk map[string]Credential) (bool, *pendingRotation, error) {
		st, err := backupView(disk)
		if err != nil {
			return false, nil, err
		}
		if st.Revision != expected {
			return false, nil, credentialsChanged("anthropic", "backup", st.Revision.Primary)
		}
		primary := disk["anthropic"]
		if primary.Type != "oauth" {
			return false, nil, missingCredential("anthropic")
		}
		// Assign a durable identity to a legacy grant without replacing it.
		if primary.Generation == "" {
			primary.Generation, err = newGeneration()
			if err != nil {
				return false, nil, persistenceFailed("anthropic", "backup")
			}
			disk["anthropic"] = primary
		}
		cred, policy, err := readBackup(disk)
		if err != nil {
			return false, nil, err
		}
		policy = backupPolicy{Version: 1, Primary: primary.Generation, Enabled: enabled}
		policy.Generation, err = newGeneration()
		if err != nil {
			return false, nil, persistenceFailed("anthropic", "backup")
		}
		switch op {
		case "save", "remove":
			cred = Credential{Type: "api_key", Key: key, extra: cred.extra}
			cred.Generation, err = newGeneration()
			if err != nil {
				return false, nil, persistenceFailed("anthropic", "backup")
			}
		case "enable":
			if enabled && cred.Key == "" {
				return false, nil, missingCredential("anthropic")
			}
		}
		raw, err := json.Marshal(policy)
		if err != nil {
			return false, nil, persistenceFailed("anthropic", "backup")
		}
		extra := make(map[string]json.RawMessage, len(cred.extra)+1)
		for name, value := range cred.extra {
			extra[name] = value
		}
		cred.extra = extra
		cred.extra["backup_policy"] = raw
		disk[anthropicBackupSlot] = cred
		s.mu.Lock()
		s.pending[anthropicBackupSlot] = pendingRotation{fence: true}
		s.mu.Unlock()
		if err := s.saveLocked(disk); err != nil {
			// A failed disable/remove must not keep paying locally. Another
			// process cannot be promised revocation before the durable ack.
			if op == "remove" || (op == "enable" && !enabled) {
				s.backupBlocked = true
			}
			if errors.Is(err, errNotWritten) {
				s.dropPending(anthropicBackupSlot)
			}
			return false, nil, persistenceFailed("anthropic", "backup")
		}
		s.backupBlocked = false
		s.dropPending(anthropicBackupSlot)
		out, err = backupView(disk)
		return false, nil, err
	})
	if op == "remove" || (op == "enable" && !enabled) {
		if pe, ok := core.AsProviderCredentialError(err); ok && (pe.Class == core.CredentialStoreUnavailable || pe.Class == core.CredentialPersistenceFailed) {
			s.backupBlocked = true
		}
	}
	return out, err
}

// AdmitAnthropicBackup linearizes a paid dispatch while holding the existing
// Store/file locks. The callback is a short Agent admission gate, never HTTP.
// No resolved key escapes this method until durability and current consent
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
		if s.backupBlocked || !st.Enabled || st.Revision != expected || s.hasPending("anthropic") {
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
