package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
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
	startedAt  time.Time
	finishedAt time.Time
	sync       bool // true when this job runs synchronously (blocking the parent tool call)
	messages   []core.AgentMessage
	usage      *core.Usage
	costUSD    float64
}

type jobSnapshot struct {
	ID         string
	Task       string
	Model      string
	Status     string
	Result     string
	Error      string
	Progress   []string
	StartedAt  time.Time
	FinishedAt time.Time
	Sync       bool
	Usage      *core.Usage
	CostUSD    float64
}

type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*job
}

func newJobStore() *jobStore {
	return &jobStore{jobs: make(map[string]*job)}
}

func (s *jobStore) create(task, model string, cancel context.CancelFunc) *job {
	return s.createJob(task, model, cancel, false)
}

// createSync creates a job for a synchronous subagent run (sync=true), so
// that live-agent tracking (bandeja, subagent_status, count) has a single
// source of truth across sync and async subagents.
func (s *jobStore) createSync(task, model string, cancel context.CancelFunc) *job {
	return s.createJob(task, model, cancel, true)
}

func (s *jobStore) createJob(task, model string, cancel context.CancelFunc, sync bool) *job {
	for {
		id := randomJobID()
		j := &job{
			id:        id,
			task:      task,
			model:     model,
			status:    statusRunning,
			cancel:    cancel,
			done:      make(chan struct{}),
			startedAt: time.Now(),
			sync:      sync,
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
		StartedAt:  j.startedAt,
		FinishedAt: j.finishedAt,
		Sync:       j.sync,
		Usage:      j.usage,
		CostUSD:    j.costUSD,
	}
}

// setMessages stores a defensive deep copy of the child's message list.
// Kept separate from snapshotLocked so that subagent_status (called
// frequently while polling) never copies the full transcript.
func (s *jobStore) setMessages(id string, msgs []core.AgentMessage) {
	j, ok := s.get(id)
	if !ok {
		return
	}
	copied := make([]core.AgentMessage, len(msgs))
	for i, m := range msgs {
		copied[i] = session.DeepCopyMessage(m)
	}
	j.mu.Lock()
	j.messages = copied
	j.mu.Unlock()
}

// messages returns a defensive deep copy of the stored message list for id.
func (s *jobStore) messages(id string) []core.AgentMessage {
	j, ok := s.get(id)
	if !ok {
		return nil
	}
	j.mu.Lock()
	src := j.messages
	j.mu.Unlock()
	copied := make([]core.AgentMessage, len(src))
	for i, m := range src {
		copied[i] = session.DeepCopyMessage(m)
	}
	return copied
}

// setUsage records the child's aggregated usage/cost on the job, so that
// subagent_status can surface tokens/cost while the job is still running or
// after it completes.
func (s *jobStore) setUsage(id string, usage *core.Usage, costUSD float64) {
	j, ok := s.get(id)
	if !ok {
		return
	}
	j.mu.Lock()
	j.usage = usage
	j.costUSD = costUSD
	j.mu.Unlock()
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

// delete immediately removes a job from the store, regardless of TTL. Used
// for synchronous subagents once their (blocking) tool call has returned:
// they have no async status to poll, so there is no reason to keep them
// around consuming memory until the TTL cleanup sweep.
func (s *jobStore) delete(id string) {
	s.mu.Lock()
	delete(s.jobs, id)
	s.mu.Unlock()
}

func (s *jobStore) runningCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, j := range s.jobs {
		j.mu.Lock()
		running := j.status == statusRunning || j.status == statusCancelling
		j.mu.Unlock()
		if running {
			count++
		}
	}
	return count
}

func randomJobID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sa-fallback-%d", fallbackJobCounter.Add(1))
	}
	return "sa-" + hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// Jobs — exported handle for external consumers (bandeja, init snapshot,
// cancellation). Wraps the package-private jobStore.
// ---------------------------------------------------------------------------

// JobInfo describes a live (or recently finished) subagent job.
type JobInfo struct {
	JobID      string
	Task       string
	Model      string
	Status     string
	Async      bool
	StartedAt  time.Time
	FinishedAt time.Time
}

// Jobs is a handle onto the subagent job store, returned by RegisterAll.
type Jobs struct {
	store *jobStore
}

// Snapshot lists all jobs currently tracked (live and recently finished,
// subject to the store's TTL cleanup).
func (j *Jobs) Snapshot() []JobInfo {
	if j == nil || j.store == nil {
		return nil
	}
	j.store.mu.RLock()
	ids := make([]string, 0, len(j.store.jobs))
	for id := range j.store.jobs {
		ids = append(ids, id)
	}
	j.store.mu.RUnlock()

	infos := make([]JobInfo, 0, len(ids))
	for _, id := range ids {
		snap, ok := j.store.snapshot(id)
		if !ok {
			continue
		}
		infos = append(infos, JobInfo{
			JobID:      snap.ID,
			Task:       snap.Task,
			Model:      snap.Model,
			Status:     snap.Status,
			Async:      !snap.Sync,
			StartedAt:  snap.StartedAt,
			FinishedAt: snap.FinishedAt,
		})
	}
	return infos
}

// Messages returns a defensive deep copy of the stored transcript for jobID.
func (j *Jobs) Messages(jobID string) []core.AgentMessage {
	if j == nil || j.store == nil {
		return nil
	}
	return j.store.messages(jobID)
}

// Cancel requests cancellation of a running job. No-op for unknown/finished jobs.
func (j *Jobs) Cancel(jobID string) {
	if j == nil || j.store == nil {
		return
	}
	jb, _, requested := j.store.requestCancel(jobID)
	if requested && jb != nil {
		jb.cancel()
	}
}
