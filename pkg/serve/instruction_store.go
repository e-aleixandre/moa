package serve

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const maxDurableInstructionRecords = 1024

// instructionStore persists only replay metadata. In particular it never
// writes instruction text, transcripts, or message content.
type instructionStore struct {
	mu   sync.Mutex
	path string
}

type durableInstructionState struct {
	Key     string                      `json:"key"`
	Records []durableInstructionRequest `json:"records"`
}

type durableInstructionRequest struct {
	SessionID   string    `json:"session_id"`
	RequestID   string    `json:"request_id"`
	Fingerprint string    `json:"fingerprint"`
	Action      string    `json:"action"`
	At          time.Time `json:"at"`
}

func openInstructionStore(path string) (*instructionStore, durableInstructionState, error) {
	store := &instructionStore{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, durableInstructionState{}, fmt.Errorf("create instruction directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, durableInstructionState{}, fmt.Errorf("secure instruction directory: %w", err)
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, durableInstructionState{}, nil
	}
	if err != nil {
		return nil, durableInstructionState{}, fmt.Errorf("read instruction store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, durableInstructionState{}, fmt.Errorf("secure instruction store: %w", err)
	}
	var state durableInstructionState
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, durableInstructionState{}, fmt.Errorf("decode instruction store: %w", err)
	}
	return store, state, nil
}

func (s *instructionStore) save(state durableInstructionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".instructions-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func newInstructionKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func decodeInstructionKey(value string) ([]byte, bool) {
	key, err := base64.RawStdEncoding.DecodeString(value)
	return key, err == nil && len(key) == 32
}

func encodeInstructionKey(key []byte) string { return base64.RawStdEncoding.EncodeToString(key) }

func normalizeDurableInstructionRecords(records []durableInstructionRequest, now time.Time) []durableInstructionRequest {
	cutoff := now.Add(-instructionTTL)
	out := make([]durableInstructionRequest, 0, len(records))
	seen := make(map[string]struct{})
	for _, record := range records {
		if record.SessionID == "" || !validInstructionID(record.RequestID) || record.Fingerprint == "" || (record.Action != "send" && record.Action != "steer") || !record.At.After(cutoff) {
			continue
		}
		key := record.SessionID + "\x00" + record.RequestID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > maxDurableInstructionRecords {
		out = out[len(out)-maxDurableInstructionRecords:]
	}
	return out
}
