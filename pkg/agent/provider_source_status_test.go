package agent

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

// The status line shows the API key from the execution's source. A new
// admission before its dispatch must not clear it, or the indicator blinks
// off between requests that keep using the key.
func TestAdmissionKeepsTheLastDispatchSource(t *testing.T) {
	model, _ := core.ResolveModel("opus")
	a, err := New(AgentConfig{Provider: anthropic.NewWithKind("offline-only-not-a-credential", true), Model: model, Tools: core.NewRegistry(), Compaction: &core.CompactionSettings{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	rev := a.configRevision
	a.mu.Unlock()
	ctx := context.Background()
	if err := a.admitProviderSource(ctx, rev, model, false, &core.ProviderSource{Kind: "api_backup"}); err != nil {
		t.Fatal(err)
	}
	if err := a.admitProvider(ctx, rev, model, false); err != nil {
		t.Fatal(err)
	}
	if got := a.ProviderExecution(); got.Phase != "awaiting_provider" || got.Source == nil || got.Source.Kind != "api_backup" {
		t.Fatalf("admission dropped the source: %+v", got)
	}
	if err := a.admitProviderSource(ctx, rev, model, false, &core.ProviderSource{Kind: "oauth"}); err != nil {
		t.Fatal(err)
	}
	if got := a.ProviderExecution(); got.Source == nil || got.Source.Kind != "oauth" {
		t.Fatalf("a plan dispatch did not replace the source: %+v", got)
	}
}
