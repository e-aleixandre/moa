package tool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestBashJobsStartStreamsAndCompletes(t *testing.T) {
	ended := make(chan BashJobInfo, 1)
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) { ended <- info })
	job, err := jobs.Start("echo hello", "/tmp", func(_ context.Context, update func(core.Result)) (core.Result, error) {
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

func TestBashJobsCancel(t *testing.T) {
	ended := make(chan BashJobInfo, 1)
	jobs := NewBashJobs(context.Background(), nil, nil, func(info BashJobInfo) { ended <- info })
	job, err := jobs.Start("sleep", "/tmp", func(ctx context.Context, _ func(core.Result)) (core.Result, error) {
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
