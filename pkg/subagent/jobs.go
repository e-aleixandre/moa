package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

// Sentinel errors returned by promote/Promote, distinguishable by callers
// (e.g. pkg/serve maps them to specific HTTP statuses).
var (
	ErrUnknownJob = errors.New("unknown job ID")
	ErrNotSync    = errors.New("subagent is already async")
	ErrNotRunning = errors.New("subagent already finished")
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
	mu                    sync.Mutex
	id                    string
	task                  string
	model                 string
	status                string
	result                string
	err                   string
	progress              [8]string
	progIdx               int
	progLen               int
	cancel                context.CancelFunc
	done                  chan struct{}
	promoted              chan struct{}
	startedAt             time.Time
	finishedAt            time.Time
	sync                  bool // true when this job runs synchronously (blocking the parent tool call)
	waiters               int  // number of subagent_wait calls currently blocked on this job
	resultClaimed         bool // completion result owner: a waiter or async notification
	notifyAsyncCompletion bool // async notification owns the terminal result
	childAgent            *agent.Agent
	messages              []core.AgentMessage
	usage                 *core.Usage
	costUSD               float64
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
			promoted:  make(chan struct{}),
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

func (s *jobStore) setChildAgent(id string, a *agent.Agent) {
	j, ok := s.get(id)
	if !ok {
		return
	}
	j.mu.Lock()
	j.childAgent = a
	j.mu.Unlock()
}

func (j *job) getChildAgent() *agent.Agent {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.childAgent
}

// isPromoted reports whether this job's promoted channel has been closed,
// i.e. it was flipped from sync to async while running. Non-blocking.
func (j *job) isPromoted() bool {
	select {
	case <-j.promoted:
		return true
	default:
		return false
	}
}

// isSync reports whether this job is currently running in sync mode
// (blocking its parent's tool call). Reads under j.mu since promote() may
// flip it concurrently.
func (j *job) isSync() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.sync
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
	claimTerminalResultLocked(j)
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
	claimTerminalResultLocked(j)
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
	claimTerminalResultLocked(j)
}

func claimTerminalResultLocked(j *job) {
	// resultClaimed is the single one-time token for delivering the full result
	// to the model, shared by the async notification lane and subagent_wait.
	// When no waiter is registered, the async notification owns delivery and
	// claims now; when a waiter is blocked, the notification is suppressed and
	// the waiter claims on wake.
	notify := j.waiters == 0
	j.notifyAsyncCompletion = notify
	if notify {
		j.resultClaimed = true
	}
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

// promote flips a running sync job to async, unblocking its parent's
// blocking tool call while the child keeps running in the background.
// Locking j.mu here linearizes promote against setCompleted/setFailed/
// setCancelled (also taken under j.mu), so a promote-vs-finish race can never
// result in double delivery of the result.
func (s *jobStore) promote(id string) error {
	j, ok := s.get(id)
	if !ok {
		return ErrUnknownJob
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.sync {
		return ErrNotSync
	}
	if j.status != statusRunning {
		return ErrNotRunning
	}
	j.sync = false
	close(j.promoted)
	return nil
}

// claimAsyncCompletion consumes the async completion claim selected when the
// terminal state was set. This runs before done is closed, so a fast-path wait
// can only read an already-owned result, never cause a second notification.
func (j *job) claimAsyncCompletion() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.notifyAsyncCompletion {
		return false
	}
	j.notifyAsyncCompletion = false
	return true
}

// wait blocks until the job finishes, the context is cancelled, or timeout
// elapses (timeout <= 0 waits indefinitely). It returns the job snapshot and a
// delivered flag. If the job finished (even if ctx/timeout also fired) the
// snapshot is terminal.
//
// delivered reports whether THIS call owns the one-time full-result delivery to
// the model. It is true when the wait consumes the completion result (a blocked
// waiter, or the first caller to reach a terminal job before the async
// notification claimed it). It is false when the async notification already
// delivered the result — the caller returns a brief acknowledgment instead of
// re-dumping the same result the model already saw.
func (s *jobStore) wait(ctx context.Context, id string, timeout time.Duration) (jobSnapshot, bool, error) {
	j, ok := s.get(id)
	if !ok {
		return jobSnapshot{}, false, ErrUnknownJob
	}
	j.mu.Lock()
	if j.status == statusCompleted || j.status == statusFailed || j.status == statusCancelled {
		delivered := !j.resultClaimed
		j.resultClaimed = true
		snap := snapshotLocked(j)
		j.mu.Unlock()
		return snap, delivered, nil
	}
	j.waiters++
	done := j.done
	j.mu.Unlock()

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timeoutCh = timer.C
	}
	select {
	case <-done:
	case <-ctx.Done():
	case <-timeoutCh:
	}
	if timer != nil {
		timer.Stop()
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.waiters > 0 {
		j.waiters--
	}
	snap := snapshotLocked(j)
	if j.status == statusCompleted || j.status == statusFailed || j.status == statusCancelled {
		delivered := !j.resultClaimed
		j.resultClaimed = true
		return snap, delivered, nil
	}
	if ctx.Err() != nil {
		return snap, false, ctx.Err()
	}
	return snap, false, nil
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

// runningCount returns the number of live (running/cancelling) ASYNC jobs.
// Sync jobs are excluded: they block the parent tool call and shouldn't count
// against the async concurrency cap, nor against the "N agents working" async
// counter shown in the UI. A job promoted from sync to async while running is
// counted here (its sync flag flips to false), even though the cap in
// newSubagent's async path is not re-checked retroactively — promoting an
// already-running child adds no new load, it just unblocks the parent, so
// runningCount (and the cap) may legitimately exceed MaxConcurrentAsync right
// after a promotion.
func (s *jobStore) runningCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, j := range s.jobs {
		j.mu.Lock()
		running := (j.status == statusRunning || j.status == statusCancelling) && !j.sync
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

// Cancel requests cancellation of a running job. Returns false if no job with
// that ID is tracked (so callers can surface a 404); returns true if the job
// exists, whether or not it was still running (idempotent for finished jobs).
func (j *Jobs) Cancel(jobID string) bool {
	if j == nil || j.store == nil {
		return false
	}
	if _, ok := j.store.get(jobID); !ok {
		return false
	}
	jb, _, requested := j.store.requestCancel(jobID)
	if requested && jb != nil {
		jb.cancel()
	}
	return true
}

// Promote flips a running sync subagent job to async, unblocking its parent's
// blocking tool call while the child keeps running in the background.
// Propagates ErrUnknownJob, ErrNotSync, ErrNotRunning from the underlying
// store so callers (e.g. pkg/serve) can map them to specific responses.
func (j *Jobs) Promote(jobID string) error {
	if j == nil || j.store == nil {
		return ErrUnknownJob
	}
	return j.store.promote(jobID)
}

// Has reports whether a job with jobID is currently tracked.
func (j *Jobs) Has(jobID string) bool {
	if j == nil || j.store == nil {
		return false
	}
	_, ok := j.store.get(jobID)
	return ok
}

// Steer queues a message for inter-step delivery to the running child agent
// of jobID. Returns false if no job with that ID is tracked, or if the job
// has no live child agent yet (e.g. still initializing) or has already
// finished. Non-blocking; safe to call concurrently.
func (j *Jobs) Steer(jobID string, text string) bool {
	if j == nil || j.store == nil {
		return false
	}
	jb, ok := j.store.get(jobID)
	if !ok {
		return false
	}
	child := jb.getChildAgent()
	if child == nil {
		return false
	}
	// A user-initiated steer to a subagent child: not Internal (the Internal
	// flag is only for system-generated completions folded into a snapshot).
	child.Steer(core.SteerItem{ID: core.NewSteerID(), Text: text})
	return true
}
