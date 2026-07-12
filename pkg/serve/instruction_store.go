package serve

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const maxDurableInstructionRecords = 1024

var errInstructionLedgerUnavailable = errors.New("canonical instruction ledger unavailable")

// instructionStore persists replay metadata and Pulse's write-ahead delivery
// state. It never writes instruction text, transcripts, or message content.
type instructionStore struct {
	mu          sync.Mutex
	path        string
	lock        io.Closer
	closed      bool
	unavailable bool
}

type durableInstructionState struct {
	Key     string                      `json:"key"`
	Records []durableInstructionRequest `json:"records"`
}

type durableInstructionRequest struct {
	SessionID   string    `json:"session_id"`
	RequestID   string    `json:"request_id"`
	Fingerprint string    `json:"fingerprint"`
	Action      string    `json:"action,omitempty"`
	State       string    `json:"state,omitempty"`
	Pulse       bool      `json:"pulse,omitempty"`
	At          time.Time `json:"at"`
}

func openInstructionStore(path string) (*instructionStore, durableInstructionState, error) {
	if path == "" {
		return nil, durableInstructionState{}, errors.New("canonical instruction ledger path unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, durableInstructionState{}, fmt.Errorf("create instruction directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, durableInstructionState{}, fmt.Errorf("secure instruction directory: %w", err)
	}
	lock, err := acquireInstructionStoreLock(path)
	if err != nil {
		return nil, durableInstructionState{}, err
	}
	store := &instructionStore{path: path, lock: lock}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, durableInstructionState{}, nil
	}
	if err != nil {
		_ = lock.Close()
		return nil, durableInstructionState{}, fmt.Errorf("read instruction store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lock.Close()
		return nil, durableInstructionState{}, fmt.Errorf("secure instruction store: %w", err)
	}
	var state durableInstructionState
	if err := json.Unmarshal(b, &state); err != nil {
		_ = lock.Close()
		return nil, durableInstructionState{}, fmt.Errorf("decode instruction store: %w", err)
	}
	return store, state, nil
}

// Close releases the ledger's lifetime-exclusive process lock. Manager owns
// this for its whole lifetime so another process cannot persist a stale
// snapshot over a Pulse write-ahead record.
func (s *instructionStore) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	lock := s.lock
	s.lock = nil
	s.mu.Unlock()
	if lock != nil {
		return lock.Close()
	}
	return nil
}

func (s *instructionStore) save(state durableInstructionState) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return errInstructionLedgerUnavailable
	}
	defer func() {
		if err != nil {
			s.unavailable = true
		}
	}()
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
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("sync instruction store directory: %w", err)
	}
	return nil
}

func (s *instructionStore) available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.unavailable
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

func durableInstructionStateOf(record durableInstructionRequest) string {
	if record.State == "" {
		return "accepted" // v1 records were accepted replay entries.
	}
	return record.State
}

func instructionRecordTTL(record durableInstructionRequest) time.Duration {
	if record.Pulse {
		return pulseOperationReceiptTTL
	}
	return instructionTTL
}

// normalizeDurableInstructionRecords preserves every live Pulse entry. Pulse
// operation admission bounds these entries to the operation receipt cap, so
// pruning a younger entry for a generic instruction cap would break recovery.
func normalizeDurableInstructionRecords(records []durableInstructionRequest, now time.Time) []durableInstructionRequest {
	legacy := make([]durableInstructionRequest, 0, len(records))
	pulse := make([]durableInstructionRequest, 0, len(records))
	seen := make(map[string]struct{})
	for _, record := range records {
		state := durableInstructionStateOf(record)
		if record.SessionID == "" || !validInstructionID(record.RequestID) || record.Fingerprint == "" || (state != "accepted" && state != "attempting") || !record.At.After(now.Add(-instructionRecordTTL(record))) {
			continue
		}
		if state == "accepted" && record.Action != "send" && record.Action != "steer" {
			continue
		}
		key := record.SessionID + "\x00" + record.RequestID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		record.State = state
		if record.Pulse {
			pulse = append(pulse, record)
		} else {
			legacy = append(legacy, record)
		}
	}
	sort.Slice(legacy, func(i, j int) bool { return legacy[i].At.Before(legacy[j].At) })
	if len(legacy) > maxDurableInstructionRecords {
		legacy = legacy[len(legacy)-maxDurableInstructionRecords:]
	}
	out := append(legacy, pulse...)
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}
