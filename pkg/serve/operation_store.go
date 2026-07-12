package serve

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	pulseOperationTTL        = 5 * time.Minute
	pulseOperationReceiptTTL = time.Hour
	maxPulseOperationRecords = 512
)

var (
	errOperationNotFound         = errors.New("Pulse operation not found")
	errOperationDeviceMismatch   = errors.New("Pulse operation belongs to another device")
	errOperationStoreUnavailable = errors.New("Pulse operation store unavailable")
)

// operationStore is deliberately a small, private ledger rather than an
// append-only audit log. Pending instructions are retained only until their
// review expires; final records retain only a bounded receipt for safe retry.
type operationStore struct {
	mu          sync.Mutex
	path        string
	lock        io.Closer
	state       durableOperationState
	now         func() time.Time
	closed      bool
	unavailable bool
	executing   map[string]chan struct{}
}

type durableOperationState struct {
	Key        string             `json:"key"`
	Operations []durableOperation `json:"operations"`
}

type durableOperation struct {
	ID             string                 `json:"id"`
	DeviceID       string                 `json:"device_id"`
	Kind           string                 `json:"kind"`
	PayloadDigest  string                 `json:"payload_digest"`
	Target         opsInstructionTarget   `json:"target,omitempty"`
	Text           string                 `json:"text,omitempty"`
	ExpectedAction string                 `json:"expected_action,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	ExpiresAt      time.Time              `json:"expires_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	State          string                 `json:"state"`
	Receipt        *pulseOperationReceipt `json:"receipt,omitempty"`
}

func openOperationStore(path string) (*operationStore, error) {
	if path == "" {
		return nil, errors.New("Pulse operation storage path unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create Pulse operation directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secure Pulse operation directory: %w", err)
	}
	lock, err := acquireOperationStoreLock(path)
	if err != nil {
		return nil, err
	}
	store := &operationStore{path: path, lock: lock, now: time.Now, executing: make(map[string]chan struct{})}
	contents, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		_ = lock.Close()
		return nil, fmt.Errorf("read Pulse operation store: %w", err)
	}
	if len(contents) > 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("secure Pulse operation store: %w", err)
		}
		if err := json.Unmarshal(contents, &store.state); err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("decode Pulse operation store: %w", err)
		}
	}
	if _, ok := decodeOperationKey(store.state.Key); !ok {
		key, err := newOperationKey()
		if err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("create Pulse operation key: %w", err)
		}
		store.state.Key = encodeOperationKey(key)
	}
	store.pruneLocked(store.now().UTC())
	if err := store.saveLocked(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return store, nil
}

func (s *operationStore) Close() error {
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

func (s *operationStore) digest(values ...string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return "", errOperationStoreUnavailable
	}
	key, ok := decodeOperationKey(s.state.Key)
	if !ok {
		return "", errOperationStoreUnavailable
	}
	mac := hmac.New(sha256.New, key)
	for _, value := range values {
		_, _ = mac.Write([]byte(value))
		_, _ = mac.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *operationStore) create(operation durableOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return errOperationStoreUnavailable
	}
	now := s.now().UTC()
	s.pruneLocked(now)
	operation.CreatedAt = now
	operation.UpdatedAt = now
	operation.ExpiresAt = now.Add(pulseOperationTTL)
	operation.State = "pending"
	s.state.Operations = append(s.state.Operations, operation)
	return s.saveLocked()
}

func (s *operationStore) get(id, deviceID string) (durableOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return durableOperation{}, errOperationStoreUnavailable
	}
	s.pruneLocked(s.now().UTC())
	if err := s.saveLocked(); err != nil {
		return durableOperation{}, err
	}
	operation, ok := s.findLocked(id)
	if !ok {
		return durableOperation{}, errOperationNotFound
	}
	if operation.DeviceID != deviceID {
		return durableOperation{}, errOperationDeviceMismatch
	}
	return operation, nil
}

type operationConfirmStart struct {
	Operation durableOperation
	Receipt   *pulseOperationReceipt
	Execute   bool
	Wait      <-chan struct{}
}

func (s *operationStore) beginConfirm(id, deviceID string) (operationConfirmStart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return operationConfirmStart{}, errOperationStoreUnavailable
	}
	now := s.now().UTC()
	s.pruneLocked(now)
	index, operation, ok := s.findWithIndexLocked(id)
	if !ok {
		if err := s.saveLocked(); err != nil {
			return operationConfirmStart{}, err
		}
		return operationConfirmStart{}, errOperationNotFound
	}
	if operation.DeviceID != deviceID {
		return operationConfirmStart{}, errOperationDeviceMismatch
	}
	if operation.Receipt != nil {
		if err := s.saveLocked(); err != nil {
			return operationConfirmStart{}, err
		}
		return operationConfirmStart{Receipt: operation.Receipt}, nil
	}
	if operation.State == "pending" {
		if !operation.ExpiresAt.After(now) {
			receipt := rejectedOperationReceipt(operation, now, "review_expired")
			s.finalizeLocked(index, receipt, now)
			if err := s.saveLocked(); err != nil {
				return operationConfirmStart{}, err
			}
			return operationConfirmStart{Receipt: &receipt}, nil
		}
		operation.State = "confirming"
		operation.UpdatedAt = now
		s.state.Operations[index] = operation
		if err := s.saveLocked(); err != nil {
			return operationConfirmStart{}, err
		}
		wait := make(chan struct{})
		s.executing[id] = wait
		return operationConfirmStart{Operation: operation, Execute: true}, nil
	}
	if operation.State != "confirming" {
		return operationConfirmStart{}, errOperationStoreUnavailable
	}
	if wait := s.executing[id]; wait != nil {
		return operationConfirmStart{Wait: wait}, nil
	}
	// A process can fail after persisting confirming and before it stores the
	// receipt. Recover only while the review is still valid; the canonical
	// delivery primitive receives the same deterministic request ID.
	if !operation.ExpiresAt.After(now) {
		receipt := rejectedOperationReceipt(operation, now, "review_expired")
		s.finalizeLocked(index, receipt, now)
		if err := s.saveLocked(); err != nil {
			return operationConfirmStart{}, err
		}
		return operationConfirmStart{Receipt: &receipt}, nil
	}
	wait := make(chan struct{})
	s.executing[id] = wait
	return operationConfirmStart{Operation: operation, Execute: true}, nil
}

func (s *operationStore) finishConfirm(id string, receipt pulseOperationReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, operation, ok := s.findWithIndexLocked(id)
	if !ok || operation.State != "confirming" || operation.Receipt != nil {
		return errOperationStoreUnavailable
	}
	now := s.now().UTC()
	s.finalizeLocked(index, receipt, now)
	err := s.saveLocked()
	if wait := s.executing[id]; wait != nil {
		delete(s.executing, id)
		close(wait)
	}
	return err
}

func (s *operationStore) finalizedReceipt(id, deviceID string) (pulseOperationReceipt, error) {
	operation, err := s.get(id, deviceID)
	if err != nil {
		return pulseOperationReceipt{}, err
	}
	if operation.Receipt == nil {
		return pulseOperationReceipt{}, errOperationStoreUnavailable
	}
	return *operation.Receipt, nil
}

func (s *operationStore) findLocked(id string) (durableOperation, bool) {
	_, operation, ok := s.findWithIndexLocked(id)
	return operation, ok
}

func (s *operationStore) findWithIndexLocked(id string) (int, durableOperation, bool) {
	for index, operation := range s.state.Operations {
		if operation.ID == id {
			return index, operation, true
		}
	}
	return 0, durableOperation{}, false
}

func (s *operationStore) finalizeLocked(index int, receipt pulseOperationReceipt, now time.Time) {
	operation := s.state.Operations[index]
	operation.State = "final"
	operation.UpdatedAt = now
	operation.Receipt = &receipt
	// Instruction text is only needed to execute a still-pending review. It
	// never remains in a replay record after the receipt is durable.
	operation.Text = ""
	s.state.Operations[index] = operation
}

func (s *operationStore) pruneLocked(now time.Time) {
	kept := make([]durableOperation, 0, len(s.state.Operations))
	for index := range s.state.Operations {
		operation := s.state.Operations[index]
		if operation.State == "pending" && !operation.ExpiresAt.After(now) {
			receipt := rejectedOperationReceipt(operation, now, "review_expired")
			operation.State = "final"
			operation.UpdatedAt = now
			operation.Receipt = &receipt
			operation.Text = ""
		}
		if operation.State == "final" && !operation.UpdatedAt.After(now.Add(-pulseOperationReceiptTTL)) {
			continue
		}
		kept = append(kept, operation)
	}
	if len(kept) > maxPulseOperationRecords {
		kept = kept[len(kept)-maxPulseOperationRecords:]
	}
	s.state.Operations = kept
}

func (s *operationStore) saveLocked() (err error) {
	if s.closed || s.unavailable {
		return errOperationStoreUnavailable
	}
	defer func() {
		if err != nil {
			s.unavailable = true
		}
	}()
	contents, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".pulse-operations-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(contents); err != nil {
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
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("sync Pulse operation store directory: %w", err)
	}
	return nil
}

func newOperationKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func decodeOperationKey(value string) ([]byte, bool) {
	key, err := base64.RawStdEncoding.DecodeString(value)
	return key, err == nil && len(key) == 32
}

func encodeOperationKey(key []byte) string { return base64.RawStdEncoding.EncodeToString(key) }
