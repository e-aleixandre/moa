// Package sessioncheckpoint provides the single ephemeral handoff slot for a
// session. It is intentionally separate from file-edit checkpoints.
package sessioncheckpoint

import (
	"fmt"
	"sync"
)

const MaxBytes = 16 << 10
const MetadataKey = "session_checkpoint"

type Slot struct {
	mu   sync.RWMutex
	text string
	gen  uint64
}

func New() *Slot                       { return &Slot{} }
func (s *Slot) Read() (string, uint64) { s.mu.RLock(); defer s.mu.RUnlock(); return s.text, s.gen }
func (s *Slot) Write(text string) error {
	if len([]byte(text)) > MaxBytes {
		return fmt.Errorf("checkpoint exceeds %d KiB", MaxBytes>>10)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text = text
	s.gen++
	return nil
}
func (s *Slot) Clear() { s.mu.Lock(); s.text = ""; s.gen++; s.mu.Unlock() }
func (s *Slot) ClearIfGeneration(gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return false
	}
	s.text = ""
	s.gen++
	return true
}
func (s *Slot) Restore(meta map[string]any) {
	if v, ok := meta[MetadataKey].(string); ok {
		_ = s.Write(v)
	}
}
func (s *Slot) SaveToMetadata() map[string]any {
	text, _ := s.Read()
	if text == "" {
		return nil
	}
	return map[string]any{MetadataKey: text}
}
