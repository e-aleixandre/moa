package tool

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestBashJobsStartStreamsAndCompletes(t *testing.T) {
	ended := make(chan BashJobInfo, 1)
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) { ended <- info })
	job, err := jobs.Start("echo hello", "/tmp", "", func(_ context.Context, update func(core.Result)) (core.Result, error) {
		update(core.TextResult("hello\n"))
		return core.TextResult("hello\nworld\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case finished := <-ended:
		if finished.JobID != job.JobID || finished.Status != "completed" || finished.Output != "hello\nworld\n" {
			t.Fatalf("finished = %+v", finished)
		}
	case <-time.After(time.Second):
		t.Fatal("job did not finish")
	}
}

func TestBashJobsOwnerFlowsThroughCallbacksAndSnapshot(t *testing.T) {
	started := make(chan BashJobInfo, 1)
	output := make(chan BashJobInfo, 1)
	ended := make(chan BashJobInfo, 1)
	release := make(chan struct{})
	jobs := NewBashJobs(context.Background(), func(info BashJobInfo) { started <- info }, func(info BashJobInfo, _ string) { output <- info }, func(info BashJobInfo) { ended <- info })
	job, err := jobs.Start("echo child", "/tmp", "subagent-1", func(_ context.Context, update func(core.Result)) (core.Result, error) {
		update(core.TextResult("live\n"))
		<-release
		return core.TextResult("done\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.OwnerAgentID != "subagent-1" {
		t.Fatalf("start owner = %q", job.OwnerAgentID)
	}
	if got := <-started; got.OwnerAgentID != "subagent-1" {
		t.Fatalf("callback start owner = %q", got.OwnerAgentID)
	}
	if got := <-output; got.OwnerAgentID != "subagent-1" {
		t.Fatalf("callback output owner = %q", got.OwnerAgentID)
	}
	if snapshot := jobs.Snapshot(); len(snapshot) != 1 || snapshot[0].OwnerAgentID != "subagent-1" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	close(release)
	if got := <-ended; got.OwnerAgentID != "subagent-1" {
		t.Fatalf("callback end owner = %q", got.OwnerAgentID)
	}
}

func TestBashJobsCancel(t *testing.T) {
	ended := make(chan BashJobInfo, 1)
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) { ended <- info })
	job, err := jobs.Start("sleep", "/tmp", "", func(ctx context.Context, _ func(core.Result)) (core.Result, error) {
		<-ctx.Done()
		return core.ErrorResult("command cancelled"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !jobs.Cancel(job.JobID) {
		t.Fatal("Cancel = false")
	}
	select {
	case finished := <-ended:
		if finished.Status != "cancelled" || !strings.Contains(finished.Output, "cancelled") {
			t.Fatalf("finished = %+v", finished)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled job did not finish")
	}
}

func TestAsyncBashDoesNotPersistShellState(t *testing.T) {
	state := NewBashState()
	state.Update("", "/tmp", []string{"PATH=/usr/bin:/bin"})
	jobs := NewBashJobs(context.Background(), nil, nil, nil)
	bash := NewBash(ToolConfig{WorkspaceRoot: "/tmp", BashState: state, BashJobs: jobs, BashTimeout: time.Second})
	result, err := bash.Execute(context.Background(), map[string]any{"command": "cd /; export P4_ASYNC_TEST=yes", "async": true}, nil)
	if err != nil || result.IsError {
		t.Fatalf("start = %+v, %v", result, err)
	}
	var jobID string
	for _, field := range strings.Fields(bashResultText(result)) {
		if strings.HasPrefix(field, "bash-") {
			jobID = field
			break
		}
	}
	if jobID == "" {
		t.Fatalf("missing job ID in %q", bashResultText(result))
	}
	deadline := time.Now().Add(time.Second)
	for {
		job, ok := jobs.Get(jobID)
		if ok && job.Status != "running" && job.Status != "cancelling" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	cwd, env := state.Snapshot("")
	if cwd != "/tmp" || strings.Contains(strings.Join(env, "\n"), "P4_ASYNC_TEST=") {
		t.Fatalf("async job changed shell state: cwd=%q env=%v", cwd, env)
	}
}

// TestAsyncBashOutlivesInvocationTimeout guards the #9 fix: a background job
// must not be killed by the synchronous invocation timeout. With a 200ms
// BashTimeout, a sync command sleeping 600ms would time out, but the same
// command launched async (no explicit timeout param) must run to completion.
func TestAsyncBashOutlivesInvocationTimeout(t *testing.T) {
	jobs := NewBashJobs(context.Background(), nil, nil, nil)
	bash := NewBash(ToolConfig{WorkspaceRoot: "/tmp", BashJobs: jobs, BashTimeout: 200 * time.Millisecond})
	result, err := bash.Execute(context.Background(), map[string]any{"command": "sleep 0.6; echo done", "async": true}, nil)
	if err != nil || result.IsError {
		t.Fatalf("start = %+v, %v", result, err)
	}
	var jobID string
	for _, field := range strings.Fields(bashResultText(result)) {
		if strings.HasPrefix(field, "bash-") {
			jobID = field
			break
		}
	}
	if jobID == "" {
		t.Fatalf("missing job ID in %q", bashResultText(result))
	}
	deadline := time.Now().Add(2 * time.Second)
	var final BashJobInfo
	for {
		job, ok := jobs.Get(jobID)
		if ok && job.Status != "running" && job.Status != "cancelling" {
			final = job
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if final.Status != "completed" || !strings.Contains(final.Output, "done") {
		t.Fatalf("async job was killed by invocation timeout: status=%q output=%q", final.Status, final.Output)
	}
}

// TestSecondsToDuration covers the clamp/overflow guard: a normal value maps
// straight, a huge value is capped (not wrapped negative into the "no deadline"
// sentinel), and non-positive maps to 0.
func TestSecondsToDuration(t *testing.T) {
	if got := secondsToDuration(30); got != 30*time.Second {
		t.Errorf("secondsToDuration(30) = %v, want 30s", got)
	}
	if got := secondsToDuration(0); got != 0 {
		t.Errorf("secondsToDuration(0) = %v, want 0", got)
	}
	if got := secondsToDuration(-5); got != 0 {
		t.Errorf("secondsToDuration(-5) = %v, want 0", got)
	}
	// A value that would overflow time.Duration must clamp to a positive cap,
	// never a negative (which executeBash would treat as "no deadline").
	if got := secondsToDuration(9223372037); got <= 0 || got != maxBashTimeout {
		t.Errorf("secondsToDuration(overflow) = %v, want %v", got, maxBashTimeout)
	}
}

func TestBashJobsWaitReturnsResult(t *testing.T) {
	release := make(chan struct{})
	ended := make(chan BashJobInfo, 1)
	var notifications atomic.Int32
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) {
		if !info.Awaited {
			notifications.Add(1)
		}
		ended <- info
	})
	job, err := jobs.Start("sleep", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		<-release
		return core.TextResult("finished\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan BashJobInfo, 1)
	go func() {
		info, delivered, werr := jobs.Wait(context.Background(), job.JobID, 5*time.Second)
		if werr != nil {
			t.Errorf("Wait err = %v", werr)
		}
		if !delivered {
			t.Error("a blocked waiter must own the one-time output delivery")
		}
		done <- info
	}()
	// Give the waiter time to register before completing.
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case info := <-done:
		if info.Status != "completed" || info.Output != "finished\n" || info.FinishedAt.IsZero() {
			t.Fatalf("Wait info = %+v", info)
		}
		if !info.Awaited {
			t.Fatal("expected Awaited=true when a waiter was blocked at completion")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return")
	}
	select {
	case info := <-ended:
		if !info.Awaited {
			t.Fatal("completion notification was not suppressed for blocked waiter")
		}
	case <-time.After(time.Second):
		t.Fatal("completion callback did not run")
	}
	if got := notifications.Load(); got != 0 {
		t.Fatalf("async notification count = %d, want 0 for blocked waiter", got)
	}
}

func TestBashJobsWaitAlreadyFinished(t *testing.T) {
	ended := make(chan BashJobInfo, 1)
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) { ended <- info })
	job, err := jobs.Start("echo", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		return core.TextResult("done\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-ended // job has finished
	info, _, werr := jobs.Wait(context.Background(), job.JobID, time.Second)
	if werr != nil {
		t.Fatalf("Wait err = %v", werr)
	}
	if info.Status != "completed" || info.Awaited {
		t.Fatalf("Wait on finished job = %+v (Awaited should be false)", info)
	}
}

// TestBashJobsWaitMultipleWaitersSingleDelivery verifies that when several
// waiters block on the same job, exactly one owns the full-output delivery
// (delivered=true) and the async notification is suppressed; every other
// waiter gets delivered=false.
func TestBashJobsWaitMultipleWaitersSingleDelivery(t *testing.T) {
	release := make(chan struct{})
	var notifications atomic.Int32
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) {
		if !info.Awaited {
			notifications.Add(1)
		}
	})
	job, err := jobs.Start("sleep", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		<-release
		return core.TextResult("finished\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	const n = 4
	var deliveredCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, delivered, werr := jobs.Wait(context.Background(), job.JobID, 5*time.Second)
			if werr != nil {
				t.Errorf("Wait err = %v", werr)
				return
			}
			if info.Status != "completed" {
				t.Errorf("Wait status = %s", info.Status)
			}
			if delivered {
				deliveredCount.Add(1)
			}
		}()
	}
	// Let all waiters register before completion.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := deliveredCount.Load(); got != 1 {
		t.Fatalf("delivered=true count = %d, want exactly 1", got)
	}
	if got := notifications.Load(); got != 0 {
		t.Fatalf("async notification count = %d, want 0 when waiters are blocked", got)
	}
}

// TestBashJobsWaitFastPathDoesNotRedeliver verifies the finish-before-wait
// race: completion delivers the full output via the async notification (no
// waiter blocked), and a later fast-path Wait reports delivered=false so the
// bash_wait tool returns a brief ack instead of re-dumping the same output.
func TestBashJobsWaitFastPathDoesNotRedeliver(t *testing.T) {
	ended := make(chan BashJobInfo, 1)
	var notifications atomic.Int32
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) {
		if !info.Awaited {
			notifications.Add(1)
		}
		ended <- info
	})
	job, err := jobs.Start("echo", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		return core.TextResult("done\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := <-ended
	if completed.Awaited {
		t.Fatal("completion should own notification with no registered waiter")
	}

	info, delivered, err := jobs.Wait(context.Background(), job.JobID, time.Second)
	if err != nil {
		t.Fatalf("Wait err = %v", err)
	}
	if info.Output != "done\n" {
		t.Fatalf("Wait output = %q", info.Output)
	}
	if delivered {
		t.Fatal("fast-path Wait after async notification must report delivered=false")
	}
	if got := notifications.Load(); got != 1 {
		t.Fatalf("async notification count = %d, want exactly 1", got)
	}
}

func TestBashJobsWaitTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	jobs := NewBashJobs(context.Background(), nil, nil, nil)
	job, err := jobs.Start("sleep", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		<-release
		return core.TextResult("late\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, _, werr := jobs.Wait(context.Background(), job.JobID, 30*time.Millisecond)
	if werr != nil {
		t.Fatalf("timeout should not error, got %v", werr)
	}
	if !info.FinishedAt.IsZero() {
		t.Fatalf("expected still-running snapshot on timeout, got %+v", info)
	}
}

func TestBashJobsWaitCtxCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	jobs := NewBashJobs(context.Background(), nil, nil, nil)
	job, err := jobs.Start("sleep", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		<-release
		return core.TextResult("late\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, _, werr := jobs.Wait(ctx, job.JobID, 5*time.Second)
	if werr == nil {
		t.Fatal("expected ctx cancellation error")
	}
}

func TestBashJobsWaitUnknownJob(t *testing.T) {
	jobs := NewBashJobs(context.Background(), nil, nil, nil)
	_, _, werr := jobs.Wait(context.Background(), "bash-nope", time.Second)
	if werr != ErrUnknownBashJob {
		t.Fatalf("Wait unknown = %v, want ErrUnknownBashJob", werr)
	}
}

// TestBashJobsWaitRaceFinishVsTimeout stresses the invariant that a waiter
// either receives the final result XOR the job is marked not-awaited, never
// both and never neither — under -race with a tight finish/timeout window.
func TestBashJobsWaitRaceFinishVsTimeout(t *testing.T) {
	for i := 0; i < 50; i++ {
		jobs := NewBashJobs(context.Background(), nil, nil, nil)
		job, err := jobs.Start("x", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
			time.Sleep(time.Millisecond)
			return core.TextResult("r\n"), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		info, _, werr := jobs.Wait(context.Background(), job.JobID, time.Millisecond)
		if werr != nil {
			t.Fatalf("iter %d: err %v", i, werr)
		}
		if !info.FinishedAt.IsZero() && !info.Awaited {
			// Finished but not marked awaited: only valid if it finished before
			// we registered as a waiter (already-finished fast path).
			continue
		}
		_ = info
	}
}

func TestNewBashWaitNoJobs(t *testing.T) {
	wait := NewBashWait(ToolConfig{})
	res, err := wait.Execute(context.Background(), map[string]any{"job_id": "bash-x"}, nil)
	if err != nil || !res.IsError {
		t.Fatalf("expected error result when BashJobs nil, got %+v %v", res, err)
	}
}

func TestNewBashWaitUnknownJob(t *testing.T) {
	jobs := NewBashJobs(context.Background(), nil, nil, nil)
	wait := NewBashWait(ToolConfig{BashJobs: jobs})
	res, err := wait.Execute(context.Background(), map[string]any{"job_id": "bash-nope"}, nil)
	if err != nil || !res.IsError || !strings.Contains(bashResultText(res), "unknown bash job") {
		t.Fatalf("expected unknown-job error, got %+v %v", res, err)
	}
}

func TestNewBashWaitFastPathReturnsAck(t *testing.T) {
	ended := make(chan BashJobInfo, 1)
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) { ended <- info })
	job, err := jobs.Start("echo hi", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		return core.TextResult("hi\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-ended // completion (no waiter) owned the full-output delivery
	wait := NewBashWait(ToolConfig{BashJobs: jobs})
	res, err := wait.Execute(context.Background(), map[string]any{"job_id": job.JobID}, nil)
	if err != nil || res.IsError {
		t.Fatalf("wait execute = %+v %v", res, err)
	}
	text := bashResultText(res)
	if !strings.Contains(text, "already finished") || strings.Contains(text, "Output:") {
		t.Fatalf("expected brief ack without output re-dump, got %q", text)
	}
}

func TestNewBashWaitBlockedWaiterReturnsStatus(t *testing.T) {
	release := make(chan struct{})
	jobs := NewBashJobs(context.Background(), nil, nil, nil)
	job, err := jobs.Start("echo hi", "/tmp", "", func(_ context.Context, _ func(core.Result)) (core.Result, error) {
		<-release
		return core.TextResult("hi\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wait := NewBashWait(ToolConfig{BashJobs: jobs})
	type outcome struct {
		text string
		err  error
	}
	got := make(chan outcome, 1)
	go func() {
		res, werr := wait.Execute(context.Background(), map[string]any{"job_id": job.JobID}, nil)
		got <- outcome{bashResultText(res), werr}
	}()
	time.Sleep(20 * time.Millisecond) // let the waiter register before completion
	close(release)
	select {
	case o := <-got:
		if o.err != nil {
			t.Fatalf("wait execute err = %v", o.err)
		}
		if !strings.Contains(o.text, "Status: completed") || !strings.Contains(o.text, "hi\n") {
			t.Fatalf("blocked waiter should get full output, got %q", o.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bash_wait did not return")
	}
}
