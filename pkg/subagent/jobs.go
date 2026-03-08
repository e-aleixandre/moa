package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	statusRunning    = "running"
	statusCancelling = "cancelling"
	statusCompleted  = "completed"
	statusFailed     = "failed"
	statusCancelled  = "cancelled"
)

var fallbackJobCounter atomic.Uint64

type job struct {
	mu         sync.Mutex
	id         string
	task       string
	model      string
	status     string
	result     string
	err        string
	progress   [8]string
	progIdx    int
	progLen    int
	cancel     context.CancelFunc
	done       chan struct{}
	finishedAt time.Time
}

type jobSnapshot struct {
	ID         string
	Task       string
	Model      string
	Status     string
	Result     string
	Error      string
	Progress   []string
	FinishedAt time.Time
}

type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*job
}

func newJobStore() *jobStore {
	return &jobStore{jobs: make(map[string]*job)}
}

func (s *jobStore) create(task, model string, cancel context.CancelFunc) *job {
	for {
		id := randomJobID()
		j := &job{
			id:     id,
			task:   task,
			model:  model,
			status: statusRunning,
			cancel: cancel,
			done:   make(chan struct{}),
		}

		s.mu.Lock()
		if _, exists := s.jobs[id]; !exists {
			s.jobs[id] = j
			s.mu.Unlock()
			return j
		}
		s.mu.Unlock()
	}
}

func (s *jobStore) get(id string) (*job, bool) {
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	return j, ok
}

func (s *jobStore) snapshot(id string) (jobSnapshot, bool) {
	j, ok := s.get(id)
	if !ok {
		return jobSnapshot{}, false
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	return snapshotLocked(j), true
}

func snapshotLocked(j *job) jobSnapshot {
	progress := make([]string, 0, j.progLen)
	if j.progLen > 0 {
		start := (j.progIdx - j.progLen + len(j.progress)) % len(j.progress)
		for i := 0; i < j.progLen; i++ {
			idx := (start + i) % len(j.progress)
			progress = append(progress, j.progress[idx])
		}
	}
	return jobSnapshot{
		ID:         j.id,
		Task:       j.task,
		Model:      j.model,
		Status:     j.status,
		Result:     j.result,
		Error:      j.err,
		Progress:   progress,
		FinishedAt: j.finishedAt,
	}
}

func (s *jobStore) addProgress(id, line string) {
	j, ok := s.get(id)
	if !ok {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.progress[j.progIdx] = line
	j.progIdx = (j.progIdx + 1) % len(j.progress)
	if j.progLen < len(j.progress) {
		j.progLen++
	}
}

func (s *jobStore) setCompleted(id, result string) {
	j, ok := s.get(id)
	if !ok {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status == statusCancelled || j.status == statusFailed || j.status == statusCompleted {
		return
	}
	j.status = statusCompleted
	j.result = result
	j.err = ""
	j.finishedAt = time.Now()
}

func (s *jobStore) setFailed(id, err string) {
	j, ok := s.get(id)
	if !ok {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status == statusCancelled || j.status == statusCompleted || j.status == statusFailed {
		return
	}
	j.status = statusFailed
	j.err = err
	j.finishedAt = time.Now()
}

func (s *jobStore) setCancelled(id string) {
	j, ok := s.get(id)
	if !ok {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status == statusCompleted || j.status == statusFailed || j.status == statusCancelled {
		return
	}
	j.status = statusCancelled
	j.err = ""
	j.finishedAt = time.Now()
}

func (s *jobStore) requestCancel(id string) (*job, jobSnapshot, bool) {
	j, ok := s.get(id)
	if !ok {
		return nil, jobSnapshot{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	snap := snapshotLocked(j)
	if j.status != statusRunning {
		return j, snap, false
	}
	j.status = statusCancelling
	return j, snapshotLocked(j), true
}

func (s *jobStore) cleanup(olderThan time.Duration) {
	if olderThan <= 0 {
		return
	}
	cutoff := time.Now().Add(-olderThan)

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		j.mu.Lock()
		finishedAt := j.finishedAt
		status := j.status
		j.mu.Unlock()
		if (status == statusCompleted || status == statusFailed || status == statusCancelled) && !finishedAt.IsZero() && finishedAt.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}

func randomJobID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sa-fallback-%d", fallbackJobCounter.Add(1))
	}
	return "sa-" + hex.EncodeToString(b[:])
}
