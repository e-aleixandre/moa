package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
)

func reviewAgent(t *testing.T, provider string, inventory func() *core.Message, tool func(), handlers ...func(core.Request) (<-chan core.AssistantEvent, error)) *Agent {
	t.Helper()
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{Name: "noop", Description: "noop", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		if tool != nil {
			tool()
		}
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	mock := NewMockProvider(handlers...)
	mock.implicitCache = provider == "openai"
	a, err := New(AgentConfig{Provider: mock, Model: core.Model{ID: "review-model", Provider: provider}, SystemPrompt: "fixed", Tools: reg, MaxTurns: 10, MaxToolCallsPerTurn: 5, MaxRunDuration: 10 * time.Second, EphemeralContext: inventory})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func reviewInput(t *testing.T, r core.Request) []json.RawMessage {
	t.Helper()
	b, err := responses.BuildRequestBody(r, responses.Dialect{Provider: "openai", Model: r.Model.ID, SupportsPromptCacheKey: true})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Input
}

func TestReviewUnchangedEphemeralAcrossSendKeepsWirePrefix(t *testing.T) {
	var reqs []core.Request
	record := func(r core.Request) (<-chan core.AssistantEvent, error) {
		reqs = append(reqs, r)
		return simpleTextResponse("done")(r)
	}
	a := reviewAgent(t, "openai", func() *core.Message { m := core.NewUserMessage("inventory"); return &m }, nil, record, record)
	if _, err := a.Run(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	before, next := reviewInput(t, reqs[0]), reviewInput(t, reqs[1])
	for i, item := range before {
		if i >= len(next) || !bytes.Equal(item, next[i]) {
			t.Fatalf("new run discarded stable anchor at input[%d] (%d -> %d items)", i, len(before), len(next))
		}
	}
}

func TestReviewInventoryRefreshNoDuplicates(t *testing.T) {
	texts := []string{"Q1", "Q1", "Q2", "", "Q3"}
	boundary := 0
	inventory := func() *core.Message {
		if texts[boundary] == "" {
			return nil
		}
		m := core.NewUserMessage("<open_questions>" + texts[boundary] + "</open_questions>")
		return &m
	}
	var handlers []func(core.Request) (<-chan core.AssistantEvent, error)
	for n := range texts {
		handlers = append(handlers, func(r core.Request) (<-chan core.AssistantEvent, error) {
			count := 0
			for _, m := range r.Messages {
				if m.Ephemeral {
					count++
					if joinText(m.Content) != "<open_questions>"+texts[n]+"</open_questions>" {
						t.Fatalf("request %d stale block %v", n, m)
					}
				}
			}
			want := 1
			if texts[n] == "" {
				want = 0
			}
			if count != want {
				t.Fatalf("request %d inventory count=%d want=%d", n, count, want)
			}
			if n == len(texts)-1 {
				return simpleTextResponse("done")(r)
			}
			return toolCallResponse(fmt.Sprint("c", n), "noop", map[string]any{"boundary": n})(r)
		})
	}
	a := reviewAgent(t, "openai", inventory, func() { boundary++ }, handlers...)
	if _, err := a.Run(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	for _, m := range a.Messages() {
		if m.Ephemeral || strings.Contains(joinText(m.Content), "open_questions") {
			t.Fatal("inventory persisted")
		}
	}
}

// The continuation snapshot is intentionally stale until the first ordinary
// boundary. The latter must refresh even when it remains in the same run.
func TestReviewRepairAndPauseRefreshInsideRun(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(fmt.Sprint("pause=", pause), func(t *testing.T) {
			old := streamRepairBackoff
			streamRepairBackoff = []time.Duration{0, 0}
			defer func() { streamRepairBackoff = old }()
			scene := &ephemeralScene{text: "Q1"}
			first := partialThenError(func() { scene.set("Q2") })
			if pause {
				first = func(r core.Request) (<-chan core.AssistantEvent, error) {
					scene.set("Q2")
					return stopReasonResponse("paused", "pause_turn", "", nil)(r)
				}
			}
			var reqs []core.Request
			wrap := func(h func(core.Request) (<-chan core.AssistantEvent, error)) func(core.Request) (<-chan core.AssistantEvent, error) {
				return func(r core.Request) (<-chan core.AssistantEvent, error) { reqs = append(reqs, r); return h(r) }
			}
			a := reviewAgent(t, "openai", scene.block, nil, wrap(first), wrap(stopReasonToolCallResponse("c1", "noop", "tool_use")), wrap(simpleTextResponse("done")))
			if _, err := a.Run(context.Background(), "start"); err != nil {
				t.Fatal(err)
			}
			if len(reqs) != 3 {
				t.Fatalf("requests=%d", len(reqs))
			}
			if !slices.Contains(shape(reqs[1]), "user:EPH(Q1)") {
				t.Fatal("continuation did not freeze")
			}
			if !slices.Contains(shape(reqs[2]), "user:EPH(Q2)") || slices.Contains(shape(reqs[2]), "user:EPH(Q1)") {
				t.Fatal("next ordinary request did not refresh")
			}
		})
	}
}
