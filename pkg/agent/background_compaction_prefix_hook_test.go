package agent

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
)

// Uses the real, documented ContextHook entry point, not a mutation through
// Agent.Messages or a direct edit of AgentState. No built-in registration of
// this hook was found; this proves the supported extension surface can write P,
// which is why pending jobs keep content signatures rather than trusting IDs.
func TestBackgroundCompactionContextHookMutationInvalidatesPrefix(t *testing.T) {
	f := newBGT(t, nil)
	original := f.ag.Messages()[0]
	const changed = "context hook changed the existing prefix"
	ext := &testExtension{initFunc: func(api extension.API) error {
		api.OnContext(func(ctx context.Context, msgs []core.AgentMessage) ([]core.AgentMessage, error) {
			select {
			case <-f.sum.entered:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			msgs[0].Content[0].Text = changed
			return msgs, nil
		})
		return nil
	}}
	host, ok := f.ag.hooks.(*extension.Host)
	if !ok {
		t.Fatal("harness: agent did not install the real extension host")
	}
	if err := host.Load(ext); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ag.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	current := f.ag.Messages()[0]
	if current.MsgID != original.MsgID || current.Content[0].Text != changed {
		t.Fatalf("hook did not mutate P in place with the same MsgID: %#v", current)
	}
	f.ag.mu.Lock()
	job := f.ag.bgJob
	f.ag.mu.Unlock()
	if job == nil || job.isDone() {
		t.Fatal("harness: no held job")
	}
	if job.prefix[0].Content[0].Text == changed {
		t.Fatal("private summary source was not frozen")
	}
	if sameIDs(f.ag.Messages()[:len(job.prefix)], job.prefix) == false {
		t.Fatal("harness: message IDs changed")
	}
	if hasPrefix(f.ag.Messages(), job.sigs) {
		t.Fatal("content signatures did not reject the changed prefix")
	}
	f.sum.open()
	f.ag.WaitBackgroundCompaction()
	f.ag.Drain(bgtWait)
	if hasSummary(f.ag.Messages()) {
		t.Fatal("stale prefix was adopted")
	}
	if f.ag.BackgroundCompaction().Active {
		t.Fatal("obsolete job remained active")
	}
}
