package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// fingerprintingProvider answers every request and, like the Anthropic
// provider, reports a fingerprint to the callback it is handed.
type fingerprintingProvider struct{}

func (fingerprintingProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if cb := req.Options.OnRequestFingerprint; cb != nil {
		h := strings.Repeat("d", 64)
		cb(func() (core.RequestFingerprint, error) {
			return core.RequestFingerprint{BodySHA256: h, OptionsSHA256: h}, nil
		})
	}
	msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

// A real BuildSession wires SessionConfig.OnSubagentRequestFingerprint into the
// registered subagent tool: invoking the tool lands a fingerprint in a real
// session store.
func TestBuildSessionSubagentFingerprintReachesStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := minimalConfig(t)
	cfg.Provider = fingerprintingProvider{}
	cfg.ProviderFactory = func(core.Model) (core.Provider, error) { return fingerprintingProvider{}, nil }
	store := session.NewSubagentStore(t.TempDir(), "parent")
	cfg.OnSubagentRequestFingerprint = func(jobID, resumedFrom string, count uint64, first core.RequestFingerprint, last *core.RequestFingerprint) {
		if err := store.SaveCacheAudit(session.SubagentCacheAudit{JobID: jobID, ResumedFrom: resumedFrom, Count: count, First: first, Last: last}); err != nil {
			t.Errorf("save: %v", err)
		}
	}
	sess, err := BuildSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sub, ok := sess.ToolReg.Get("subagent")
	if !ok {
		t.Fatal("subagent tool not registered")
	}
	res, err := sub.Execute(context.Background(), map[string]any{"task": "look"}, nil)
	if err != nil || res.IsError {
		t.Fatalf("subagent: %+v %v", res, err)
	}
	jobID, _ := res.Custom["subagent_job_id"].(string)
	if jobID == "" {
		t.Fatal("no job id in result")
	}
	audit, err := store.LoadCacheAudit(jobID)
	if err != nil || audit.Count != 1 || audit.Last == nil {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}
}
