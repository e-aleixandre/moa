package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Keep CompactAt below both MaxInput values: an unchanged effective soft
// window is not proof that the final ordinary request still fits hard.
func TestBackgroundCompactionReview_SameIDWindowShrinkAtFinalBarrier(t *testing.T) {
	f := newBGT(t, nil)
	p := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){respondWith(textTurn("ok", nil))}}
	if err := f.ag.Reconfigure(p, f.model, "", 40000); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	f.ag.mu.Lock()
	f.ag.config.MaterializeContent = func(_ context.Context, msgs []core.Message) ([]core.Message, error) {
		once.Do(func() {
			model := f.model
			model.MaxInput = 40500 // same ID/provider, same 40000 soft window
			if err := f.ag.Reconfigure(nil, model, "", 40000); err != nil {
				t.Error(err)
			}
		})
		return msgs, nil
	}
	f.ag.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	go func() { _, err := f.ag.Send(ctx, "go"); ended <- err }()
	f.waitEntered(t)
	f.sum.open()
	if err := <-ended; err != nil {
		t.Fatalf("run error: %v", err)
	}
	for i, req := range p.requests() {
		msgs := make([]core.AgentMessage, len(req.Messages))
		for j, m := range req.Messages {
			msgs[j] = core.AgentMessage{Message: m}
		}
		est := core.EstimateContextTokens(msgs, req.System, req.Tools, f.ag.CompactionEpoch()).Tokens
		if est > req.Model.MaxInput-1000 {
			t.Errorf("same-ID refreshed-window request exceeded hard: estimate=%d hard=%d call=%d MaxInput=%d", est, req.Model.MaxInput-1000, i+1, req.Model.MaxInput)
		}
	}
}
