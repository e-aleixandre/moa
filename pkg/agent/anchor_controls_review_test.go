package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestReviewAnchorReanchorsAfterRealModelSwitch(t *testing.T) {
	scene := &ephemeralScene{text: "Q1"}
	var a *Agent
	b := NewMockProvider(func(r core.Request) (<-chan core.AssistantEvent, error) {
		if r.Model.ID != "next-model" {
			t.Fatal("wrong model")
		}
		count := 0
		at := -1
		for i, m := range r.Messages {
			if m.Ephemeral {
				count++
				at = i
			}
		}
		if count != 1 || at != len(r.Messages)-1 {
			t.Fatalf("next model retained previous anchor: at=%d len=%d count=%d", at, len(r.Messages), count)
		}
		return simpleTextResponse("done")(r)
	})
	a = reviewAgent(t, "openai", scene.block, nil, func(r core.Request) (<-chan core.AssistantEvent, error) {
		if err := a.SetModel(b, core.Model{ID: "next-model", Provider: "openai"}); err != nil {
			t.Fatal(err)
		}
		return stopReasonToolCallResponse("c1", "noop", "tool_use")(r)
	})
	if _, err := a.Run(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
}

func TestReviewAnchorInvalidatedByAcceptedRealTrim(t *testing.T) {
	a := newTrimAgent(t, NewMockProvider(), nil)
	seedTrimmableHistory(a, 30, 16000)
	cfg := &loopConfig{state: &a.state, stateMu: &sync.Mutex{}, emitter: a.emitter, hooks: a.hooks}
	settings := trimSettings()
	estimate := core.EstimateContextTokens(a.state.Messages, "", nil, a.state.CompactionEpoch)
	m := core.NewUserMessage("inventory")
	// The old index still exists after elision; only epoch invalidates it.
	f := frozenEphemeral{msg: &m, at: 2, epoch: a.state.CompactionEpoch, model: core.Model{ID: "m"}}
	n := len(a.state.Messages)
	if !tryTrim(cfg, &settings, estimate.Tokens, trimTestWindow) {
		t.Fatal("trim not accepted")
	}
	if len(a.state.Messages) != n {
		t.Fatal("harness: trim changed message count")
	}
	if f.reuse(&m, a.state.CompactionEpoch, f.model, len(a.state.Messages)) {
		t.Fatal("accepted trim retained obsolete anchor")
	}
	for _, m := range a.state.Messages {
		if m.Ephemeral {
			t.Fatal("trim inserted inventory into durable state")
		}
	}
}
