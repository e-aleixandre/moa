package subagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

type fingerprintEvent struct {
	jobID, resumedFrom string
	fp                 core.RequestFingerprint
}

// Sync and async children, fresh and resumed, report their final wire body
// together with the job and the job they resumed.
func TestChildRequestFingerprintCorrelatesJobsAndSources(t *testing.T) {
	isolatedResumeReplayEnvironment(t)
	model := resumeReplayModel(t, "claude-opus-5-5")
	seed := resumeReplayFixture(model, core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, false)
	store := saveResumeReplayTranscript(t, seed, model.ID, "high")
	capture, server := newResumeReplayCapture(t, model.ID, true)
	provider := anthropic.NewWithBaseURL("sk-ant-api03-synthetic-not-a-credential", server.URL)

	var mu sync.Mutex
	var events []fingerprintEvent
	cfg := resumeReplayConfig(t, model, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg.AppCtx = ctx
	cfg.TranscriptLoader = resumeReplayLoader(store)
	cfg.OnChildRequestFingerprint = func(jobID, resumedFrom string, fp core.RequestFingerprint) {
		if err := store.RecordRequestFingerprint(jobID, resumedFrom, fp); err != nil {
			t.Errorf("persist native request fingerprint: %v", err)
		}
		mu.Lock()
		events = append(events, fingerprintEvent{jobID, resumedFrom, fp})
		mu.Unlock()
	}
	tool := newSubagent(cfg, newJobStore())

	for _, params := range []map[string]any{
		{"task": "fresh sync"},
		{"task": "resume sync", "resume": "  synthetic-original-job "},
		{"task": "resume async", "resume": "synthetic-original-job", "async": true},
	} {
		if res, err := tool.Execute(ctx, params, nil); err != nil || res.IsError {
			t.Fatalf("%v: %+v %v", params["task"], res, err)
		}
	}
	waitFor(t, 5*time.Second, func() bool {
		capture.mu.Lock()
		defer capture.mu.Unlock()
		return len(capture.captured) == 3
	})
	waitFor(t, 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(events) == 3 })

	mu.Lock()
	defer mu.Unlock()
	wire := map[string]bool{}
	for _, r := range capture.requests(t, 3) {
		sum := sha256.Sum256(r.body)
		wire[hex.EncodeToString(sum[:])] = true
	}
	jobs := map[string]bool{}
	sources := map[string]int{}
	for _, e := range events {
		jobs[e.jobID] = true
		sources[e.resumedFrom]++
		if !wire[e.fp.BodySHA256] {
			t.Errorf("job %s fingerprint does not match any wire body", e.jobID)
		}
		audit, err := store.LoadCacheAudit(e.jobID)
		if err != nil {
			t.Fatalf("load native request fingerprint: %v", err)
		}
		if audit.Count != 1 || audit.ResumedFrom != e.resumedFrom ||
			audit.First.BodySHA256 != e.fp.BodySHA256 || audit.Last.BodySHA256 != e.fp.BodySHA256 {
			t.Fatalf("persisted fingerprint differs from native callback for job %s", e.jobID)
		}
	}
	if len(jobs) != 3 || sources[""] != 1 || sources["synthetic-original-job"] != 2 {
		t.Fatalf("jobs=%v sources=%v", jobs, sources)
	}
}
