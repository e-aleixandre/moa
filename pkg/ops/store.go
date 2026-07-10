package ops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store atomically owns a durable safe Ops projection. A Store is intended to
// be owned by one Manager process; its mutex serializes service callbacks.
type Store struct {
	mu   sync.Mutex
	path string
}

// OpenStore loads path if it exists. The parent directory is created with
// private permissions; writes use rename in that same directory.
func OpenStore(path string) (*Store, DurableState, error) {
	s := &Store{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, DurableState{}, fmt.Errorf("create ops directory: %w", err)
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, DurableState{}, nil
	}
	if err != nil {
		return nil, DurableState{}, fmt.Errorf("read ops store: %w", err)
	}
	var state DurableState
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, DurableState{}, fmt.Errorf("decode ops store: %w", err)
	}
	return s, state, nil
}

// Save atomically replaces the journal. DurableState is safe structured data.
func (s *Store) Save(state DurableState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".ops-*.tmp")
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
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return nil
}
