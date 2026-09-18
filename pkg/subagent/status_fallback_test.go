package subagent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func statusText(t *testing.T, tool core.Tool, jobID string) (string, bool) {
	t.Helper()
	res, err := tool.Execute(context.Background(), map[string]any{"job_id": jobID}, nil)
	if err != nil {
		t.Fatalf("subagent_status: %v", err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		sb.WriteString(c.Text)
	}
	return sb.String(), res.IsError
}

// A job ID only stays in memory for jobTTL, and a synchronous child is dropped
// the moment it delivers. Once a trim has elided the report, the job ID in the
// placeholder is the model's only route back to the verdict — so it has to
// resolve against the stored transcript, not just the live map.
func TestSubagentStatus_FallsBackToThePersistedOutcome(t *testing.T) {
	cfg := Config{OutcomeLoader: func(jobID string) (PersistedOutcome, error) {
		if jobID != "job-1" {
			return PersistedOutcome{}, fmt.Errorf("not found")
		}
		return PersistedOutcome{Status: statusCompleted, Task: "review the diff", Result: "the verdict"}, nil
	}}
	tool := newSubagentStatus(newJobStore(), cfg)

	text, isErr := statusText(t, tool, "job-1")
	if isErr {
		t.Fatalf("status reported an error: %s", text)
	}
	if !strings.Contains(text, "the verdict") {
		t.Fatalf("status = %q, want the stored result", text)
	}
	if !strings.Contains(text, "review the diff") {
		t.Fatalf("status = %q, want the task it ran", text)
	}
}

// A live job wins: the stored header is a snapshot of a finished run, while the
// job in memory is the thing actually executing.
func TestSubagentStatus_PrefersTheLiveJob(t *testing.T) {
	jobs := newJobStore()
	j := jobs.create("running task", "model-x", func() {})
	cfg := Config{OutcomeLoader: func(string) (PersistedOutcome, error) {
		return PersistedOutcome{Status: statusCompleted, Result: "stale stored result"}, nil
	}}
	tool := newSubagentStatus(jobs, cfg)

	text, _ := statusText(t, tool, j.id)
	if strings.Contains(text, "stale stored result") {
		t.Fatalf("status = %q, want the live job", text)
	}
	if !strings.Contains(text, "running task") {
		t.Fatalf("status = %q, want the running job's task", text)
	}
}

// A sidecar left in "running" by a crash describes nothing that is executing.
// Reporting it as running would have the parent wait for a result that can
// never arrive.
func TestSubagentStatus_ReportsCrashedJobsAsInterrupted(t *testing.T) {
	cfg := Config{OutcomeLoader: func(string) (PersistedOutcome, error) {
		return PersistedOutcome{Status: statusRunning, Task: "long job"}, nil
	}}
	tool := newSubagentStatus(newJobStore(), cfg)

	text, isErr := statusText(t, tool, "job-1")
	if isErr {
		t.Fatalf("status reported an error: %s", text)
	}
	if strings.Contains(text, "Status: running") {
		t.Fatalf("status = %q, want it not presented as a live job", text)
	}
	if !strings.Contains(text, "interrupted") {
		t.Fatalf("status = %q, want an interrupted job", text)
	}
}

// An ID belonging to another session (or to nothing) must stay unknown: job IDs
// are identifiers, not capabilities, and the loader is session-scoped.
func TestSubagentStatus_UnknownStaysUnknown(t *testing.T) {
	cfg := Config{OutcomeLoader: func(string) (PersistedOutcome, error) {
		return PersistedOutcome{}, fmt.Errorf("session: subagent %q: not found", "x")
	}}
	tool := newSubagentStatus(newJobStore(), cfg)

	text, isErr := statusText(t, tool, "someone-elses-job")
	if !isErr || !strings.Contains(text, "unknown job ID") {
		t.Fatalf("status = %q (isError=%v), want an unknown job", text, isErr)
	}
}

// Without a loader wired (the CLI, embedders), behaviour is exactly as before.
func TestSubagentStatus_WithoutLoaderBehavesAsBefore(t *testing.T) {
	tool := newSubagentStatus(newJobStore(), Config{})
	text, isErr := statusText(t, tool, "job-1")
	if !isErr || !strings.Contains(text, "unknown job ID") {
		t.Fatalf("status = %q (isError=%v)", text, isErr)
	}
}
