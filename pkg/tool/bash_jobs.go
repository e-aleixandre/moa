package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

const (
	bashJobOutputLimit = 50 << 10
	bashJobTTL         = 30 * time.Minute
	bashJobMaxRunning  = 5
)

// BashJobInfo is the UI/status-safe snapshot of a background bash command.
type BashJobInfo struct {
	JobID      string
	Command    string
	CWD        string
	Status     string
	Output     string
	StartedAt  time.Time
	FinishedAt time.Time
}

type bashJob struct {
	BashJobInfo
	cancel context.CancelFunc
}

// BashJobs owns session-scoped background bash processes. Jobs deliberately do
// not persist shell state: they receive a launch-time snapshot, but a later
// background completion must not overwrite foreground cd/export state.
type BashJobs struct {
	mu       sync.Mutex
	ctx      context.Context
	jobs     map[string]*bashJob
	onStart  func(BashJobInfo)
	onOutput func(string, string)
	onEnd    func(BashJobInfo)
}

// NewBashJobs creates a session-scoped background job manager.
func NewBashJobs(ctx context.Context, onStart func(BashJobInfo), onOutput func(string, string), onEnd func(BashJobInfo)) *BashJobs {
	if ctx == nil {
		ctx = context.Background()
	}
	return &BashJobs{ctx: ctx, jobs: make(map[string]*bashJob), onStart: onStart, onOutput: onOutput, onEnd: onEnd}
}

// Start launches run in the session context. run must return the same final
// result a synchronous bash invocation would return.
func (j *BashJobs) Start(command, cwd string, run func(context.Context, func(core.Result)) (core.Result, error)) (BashJobInfo, error) {
	j.mu.Lock()
	j.cleanupLocked()
	if j.runningLocked() >= bashJobMaxRunning {
		j.mu.Unlock()
		return BashJobInfo{}, ErrTooManyBashJobs
	}
	ctx, cancel := context.WithCancel(j.ctx)
	job := &bashJob{BashJobInfo: BashJobInfo{JobID: newBashJobID(), Command: command, CWD: cwd, Status: "running", StartedAt: time.Now()}, cancel: cancel}
	j.jobs[job.JobID] = job
	info := job.BashJobInfo
	j.mu.Unlock()
	if j.onStart != nil {
		j.onStart(info)
	}

	go func() {
		result, err := run(ctx, func(update core.Result) {
			text := bashResultText(update)
			if text == "" {
				return
			}
			j.mu.Lock()
			if live := j.jobs[job.JobID]; live != nil {
				live.Output = appendBashJobOutput(live.Output, text)
			}
			j.mu.Unlock()
			if j.onOutput != nil {
				j.onOutput(job.JobID, text)
			}
		})

		j.mu.Lock()
		live := j.jobs[job.JobID]
		if live == nil {
			j.mu.Unlock()
			return
		}
		if ctx.Err() != nil {
			live.Status = "cancelled"
		} else if err != nil || result.IsError {
			live.Status = "failed"
		} else {
			live.Status = "completed"
		}
		live.Output = bashResultText(result)
		live.FinishedAt = time.Now()
		info := live.BashJobInfo
		j.mu.Unlock()
		if j.onEnd != nil {
			j.onEnd(info)
		}
	}()
	return info, nil
}

// Snapshot returns live and recently completed jobs. Output is authoritative
// after completion and is suitable for a reconnect/status view.
func (j *BashJobs) Snapshot() []BashJobInfo {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cleanupLocked()
	out := make([]BashJobInfo, 0, len(j.jobs))
	for _, job := range j.jobs {
		out = append(out, job.BashJobInfo)
	}
	return out
}

// Cancel stops a job's process group through its execution context.
func (j *BashJobs) Cancel(jobID string) bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	job := j.jobs[jobID]
	if job == nil || job.Status != "running" {
		j.mu.Unlock()
		return job != nil
	}
	job.Status = "cancelling"
	cancel := job.cancel
	j.mu.Unlock()
	cancel()
	return true
}

// Get returns a current snapshot by ID.
func (j *BashJobs) Get(jobID string) (BashJobInfo, bool) {
	if j == nil {
		return BashJobInfo{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.jobs[jobID]
	if job == nil {
		return BashJobInfo{}, false
	}
	return job.BashJobInfo, true
}

func (j *BashJobs) runningLocked() int {
	count := 0
	for _, job := range j.jobs {
		if job.Status == "running" || job.Status == "cancelling" {
			count++
		}
	}
	return count
}

func (j *BashJobs) cleanupLocked() {
	cutoff := time.Now().Add(-bashJobTTL)
	for id, job := range j.jobs {
		if !job.FinishedAt.IsZero() && job.FinishedAt.Before(cutoff) {
			delete(j.jobs, id)
		}
	}
}

func appendBashJobOutput(old, add string) string {
	if len(old)+len(add) <= bashJobOutputLimit {
		return old + add
	}
	combined := old + add
	return "[output truncated]\n" + combined[len(combined)-bashJobOutputLimit:]
}

func bashResultText(result core.Result) string {
	var out string
	for _, content := range result.Content {
		if content.Type == "text" {
			out += content.Text
		}
	}
	return out
}

func newBashJobID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "bash-" + hex.EncodeToString(b[:])
	}
	return "bash-" + time.Now().Format("150405.000000000")
}

// ErrTooManyBashJobs is returned when the session background-job cap is hit.
var ErrTooManyBashJobs = &bashJobError{"too many concurrent background bash jobs"}

type bashJobError struct{ text string }

func (e *bashJobError) Error() string { return e.text }
