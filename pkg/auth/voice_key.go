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

// selectAPIKey picks the API key a SelectVoiceAPIKey snapshot uses from disk,
// as normalizeVoiceKey would leave it: a primary API key, else the key beside
// the plan (or left by one). It returns the record name it came from.
func selectAPIKey(disk map[string]Credential, provider string) (Credential, string, error) {
	if cred, ok := disk[provider]; ok && cred.Type == "api_key" && cred.Key != "" {
		return cred, provider, nil
	}
	if slot, ok := voiceKeySlot(provider); ok {
		if cred, ok := disk[slot]; ok {
			if cred.Type != "api_key" || cred.Key == "" {
				return Credential{}, "", withGeneration(core.NewProviderCredentialError(provider, core.CredentialSourceStore, "resolve", core.CredentialReconnect), cred.Generation)
			}
			return cred, slot, nil
		}
	}
	return Credential{}, "", missingCredential(provider)
}

// normalizeVoiceKey leaves provider with at most one API key: beside an OAuth
// primary, or as the primary otherwise. This binary never writes both; a
// writer that does not know the slot (an older binary) can only have saved the
// primary key after the slot existed, so the primary is the most recent key
// and the slot is dropped. A slot without a primary (a plan removed by an
// earlier build) becomes the primary. An unknown primary type is left alone.
func normalizeVoiceKey(disk map[string]Credential, provider string) {
	slot, ok := voiceKeySlot(provider)
	if !ok {
		return
	}
	key, ok := disk[slot]
	if !ok {
		return
	}
	primary, hasPrimary := disk[provider]
	switch {
	case !hasPrimary && key.Type == "api_key" && key.Key != "":
		disk[provider] = key
		delete(disk, slot)
	case hasPrimary && primary.Type == "api_key" && primary.Key != "":
		delete(disk, slot)
	}
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
		normalizeVoiceKey(disk, provider)
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

// keepVoiceKey moves a primary API key beside the plan sign-in replacing it,
// as a new record, and reports whether it did. disk is normalized, so there is
// no other key to keep.
func keepVoiceKey(disk map[string]Credential, provider string, next Credential) (bool, error) {
	slot, ok := voiceKeySlot(provider)
	prev := disk[provider]
	if !ok || next.Type != "oauth" || prev.Type != "api_key" || prev.Key == "" {
		return false, nil
	}
	gen, err := newGeneration()
	if err != nil {
		return false, err
	}
	disk[slot] = Credential{Type: "api_key", Key: prev.Key, Generation: gen}
	return true, nil
}
