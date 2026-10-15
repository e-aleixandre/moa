package auth

import (
	"errors"

	"github.com/e-aleixandre/moa/pkg/core"
)

// voiceKeySlots name the records holding the one OpenAI/xAI API key saved
// next to a plan sign-in. They are a codec detail, not model providers: text
// never selects them, only an explicit SelectVoiceAPIKey does.
var voiceKeySlots = map[string]string{
	"openai": "__voice_openai_api_v1",
	"xai":    "__voice_xai_api_v1",
}

func voiceKeySlot(provider string) (string, bool) {
	slot, ok := voiceKeySlots[provider]
	return slot, ok
}

// selectAPIKey picks the API key a SelectVoiceAPIKey snapshot uses from disk:
// the key beside the plan, else a primary API key. It returns the record name
// it came from. An OAuth primary is never an API key.
func selectAPIKey(disk map[string]Credential, provider string) (Credential, string, error) {
	if slot, ok := voiceKeySlot(provider); ok {
		if cred, ok := disk[slot]; ok {
			if cred.Type != "api_key" || cred.Key == "" {
				return Credential{}, "", withGeneration(core.NewProviderCredentialError(provider, core.CredentialSourceStore, "resolve", core.CredentialReconnect), cred.Generation)
			}
			return cred, slot, nil
		}
	}
	if cred, ok := disk[provider]; ok && cred.Type == "api_key" && cred.Key != "" {
		return cred, provider, nil
	}
	return Credential{}, "", missingCredential(provider)
}

// saveKeyBesidePlan stores key in provider's voice slot when the primary is a
// plan sign-in, leaving the sign-in untouched. It reports false, writing
// nothing, when there is no sign-in: the key is then the primary credential.
// expectedPrimary is the primary generation the caller saw.
func (s *Store) saveKeyBesidePlan(provider, slot, expectedPrimary, key string) (string, bool, error) {
	gen, err := newGeneration()
	if err != nil {
		return "", false, persistenceFailed(provider, "api_key")
	}
	besidePlan := false
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	err = s.transact(provider, "api_key", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		primary := disk[provider]
		if primary.Generation != expectedPrimary {
			return false, nil, credentialsChanged(provider, "api_key", primary.Generation)
		}
		if primary.Type != "oauth" {
			return false, nil, nil
		}
		besidePlan = true
		disk[slot] = Credential{Type: "api_key", Key: key, Generation: gen}
		return false, nil, s.saveFencedLocked(provider, slot, "api_key", disk)
	})
	if err != nil {
		return "", false, err
	}
	return gen, besidePlan, nil
}

// RemoveProviderAPIKey deletes provider's API key: the one beside the plan
// sign-in, which stays signed in, or else a primary API key. expectedKey is
// the key generation the caller saw; any other fails without writing.
func (s *Store) RemoveProviderAPIKey(provider, expectedKey string) error {
	slot, ok := voiceKeySlot(provider)
	if !ok {
		return core.NewProviderCredentialError(provider, core.CredentialSourceStore, "api_key", core.CredentialMissing)
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.transact(provider, "api_key", func(disk map[string]Credential) (bool, *pendingRotation, error) {
		record := slot
		cur, ok := disk[slot]
		if !ok {
			record, cur = provider, disk[provider]
			if cur.Type != "api_key" {
				return false, nil, credentialsChanged(provider, "api_key", cur.Generation)
			}
		}
		if cur.Generation != expectedKey {
			return false, nil, credentialsChanged(provider, "api_key", cur.Generation)
		}
		delete(disk, record)
		return false, nil, s.saveFencedLocked(provider, record, "api_key", disk)
	})
}

// ProviderAPIKeyStatus reports the key beside provider's plan sign-in:
// its generation ("" when none) and whether its last change is unsaved here.
// It never returns the key.
func (s *Store) ProviderAPIKeyStatus(provider string) (generation string, saveFailed bool, err error) {
	slot, ok := voiceKeySlot(provider)
	if !ok {
		return "", false, nil
	}
	disk, err := s.reload()
	if err != nil {
		return "", false, storeUnavailable(provider, "api_key_status")
	}
	return disk[slot].Generation, s.hasPending(slot), nil
}

// saveFencedLocked writes disk with record fenced until the write is known
// durable, as commit does for a login. A failure before the rename lifts the
// fence: the file still holds the previous record.
func (s *Store) saveFencedLocked(provider, record, op string, disk map[string]Credential) error {
	s.mu.Lock()
	s.pending[record] = pendingRotation{fence: true}
	s.mu.Unlock()
	if err := s.saveLocked(disk); err != nil {
		if errors.Is(err, errNotWritten) {
			s.dropPending(record)
		}
		return persistenceFailed(provider, op)
	}
	s.dropPending(record)
	return nil
}

// keepVoiceKey keeps provider's single API key across a primary change. A plan
// sign-in replacing a primary API key moves that key beside the plan, unless a
// key is already there (it is never overwritten). A primary API key replacing
// the primary supersedes the key beside the plan, so only one key remains.
func keepVoiceKey(disk map[string]Credential, provider string, next Credential) error {
	slot, ok := voiceKeySlot(provider)
	if !ok {
		return nil
	}
	if next.Type == "api_key" {
		delete(disk, slot)
		return nil
	}
	prev := disk[provider]
	if next.Type != "oauth" || prev.Type != "api_key" || prev.Key == "" {
		return nil
	}
	if _, exists := disk[slot]; exists {
		return nil
	}
	gen, err := newGeneration()
	if err != nil {
		return err
	}
	disk[slot] = Credential{Type: "api_key", Key: prev.Key, Generation: gen}
	return nil
}
