package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
)

func TestReviewPersistentPrefixAfterCompactionRead(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	vpSeedOwnerBook(t, f, "work/new.md", "NEW-SOURCE")
	var previous []json.RawMessage
	var reads atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		last := vpLastUser(req)
		if strings.Contains(last, "SETUP-1") {
			return simpleResponse(strings.Repeat("x", 180000)), nil
		}
		if strings.Contains(last, "SETUP-2") {
			return simpleResponse(strings.Repeat("y", 100000)), nil
		}
		body, err := responses.BuildRequestBody(req, responses.Dialect{Provider: "openai", Model: req.Model.ID})
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		if reads.Add(1) == 1 {
			previous = wire.Input
			return toolCallHandlerFor("new-read", "book_read", map[string]any{"path": "work/new.md"})(ctx, req)
		}
		for i, item := range previous {
			if i >= len(wire.Input) || !bytes.Equal(item, wire.Input[i]) {
				t.Errorf("wire prefix changed at input[%d] during successful read after compaction, without eviction", i)
				break
			}
		}
		return simpleResponse("done"), nil
	})
	cl := f.call()
	c := persistentCall(t, f, cl)
	c.runHandoff(context.Background(), "", "SETUP-1")
	c.runHandoff(context.Background(), "", "SETUP-2")
	b := c.backendConversation()
	if len(b.contextMessages()) != len(b.messages)+1 || len(b.messages) != 2 {
		t.Fatalf("compaction setup failed: context messages=%d retained=%d", len(b.contextMessages()), len(b.messages))
	}
	c.runHandoff(context.Background(), "", "READ")
	if len(b.messages) != 6 {
		t.Fatalf("unexpected eviction during READ: messages=%d", len(b.messages))
	}
	cl.hangup()
}

func TestReviewPersistentLateOverflowPreservesEarlier(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	vpSeedOwnerBook(t, f, "work/escaped.md", strings.Repeat("\x00", 16000))
	var sends atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		n := sends.Add(1)
		if n == 1 {
			return simpleResponse("EARLIER-SOURCE-" + strings.Repeat("e", 180000)), nil
		}
		return toolCallHandlerFor(fmt.Sprintf("page-%d", n), "book_read", map[string]any{"path": "work/escaped.md"})(ctx, req)
	})
	cl := f.call()
	c := persistentCall(t, f, cl)
	c.runHandoff(context.Background(), "", "first")
	answer := c.runHandoff(context.Background(), "", strings.Repeat("r", 40000))
	if !strings.Contains(answer, "context limit") {
		t.Fatalf("expected overflow: %q", answer)
	}
	if sends.Load() != 3 {
		t.Fatalf("sends=%d want 3 (first exchange, first read, second read)", sends.Load())
	}
	if !strings.Contains(persistentWire(core.Request{Messages: c.backendConversation().contextMessages()}), "EARLIER-SOURCE-") {
		t.Error("late oversized active exchange discarded earlier completed history rather than restoring it")
	}
	cl.hangup()
}

func TestReviewPersistentPreCompactionResidentBudget(t *testing.T) {
	b := &voiceBackend{}
	for i := 0; i < 100; i++ {
		b.recordRead(voiceResult("", map[string]any{"path": fmt.Sprintf("work/%03d-%s.md", i, strings.Repeat("p", 100)), "offset": 0, "next_offset": 1}))
	}
	b.messages = []core.Message{core.NewUserMessage(strings.Repeat("a", 130000)), {Role: "assistant", Content: []core.Content{core.TextContent("old done")}}, core.NewUserMessage(strings.Repeat("b", 130000))}
	start := 2
	if !b.fit(&start) {
		t.Fatal("unexpected rejection")
	}
	data, _ := json.Marshal(append([]core.Message{b.ledger()}, b.messages...))
	if len(data) > voiceBackendHistoryBytes {
		t.Errorf("history plus retained source ledger=%d > %d bytes", len(data), voiceBackendHistoryBytes)
	}
	data, _ = json.Marshal(append([]core.Message{b.ledger()}, b.contextMessages()...))
	if len(data) > voiceBackendHistoryBytes {
		t.Errorf("history plus frozen prefix and retained source ledger=%d > %d bytes", len(data), voiceBackendHistoryBytes)
	}
}

func TestReviewPersistentFailureRestoresCompactionEpoch(t *testing.T) {
	for _, mode := range []string{"error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := vpNewFixture(t, "chatgpt-plan")
			vpSeedOwnerBook(t, f, "work/escaped.md", strings.Repeat("\x00", 16000))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sends atomic.Int32
			f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				backend, minutes := vpBackendRequest(req)
				if !backend || minutes {
					return f.prov.Stream(ctx, req)
				}
				switch sends.Add(1) {
				case 1:
					return simpleResponse(strings.Repeat("x", 180000)), nil
				case 2:
					return simpleResponse("RETAINED-SOURCE-" + strings.Repeat("y", 100000)), nil
				case 3:
					return toolCallHandlerFor("accepted-read", "book_read", map[string]any{"path": "work/escaped.md"})(ctx, req)
				default:
					if mode == "cancel" {
						cancel()
						return nil, ctx.Err()
					}
					return nil, errors.New("synthetic failure after eviction")
				}
			})
			cl := f.call()
			c := persistentCall(t, f, cl)
			c.runHandoff(context.Background(), "", "first")
			c.runHandoff(context.Background(), "", "second")
			b := c.backendConversation()
			before := persistentWire(core.Request{Messages: b.contextMessages()})
			if len(b.contextMessages()) != len(b.messages)+1 {
				t.Fatal("setup did not compact")
			}
			c.runHandoff(ctx, "", strings.Repeat("r", 60000))
			if sends.Load() != 4 {
				t.Fatalf("provider sends=%d, want 4", sends.Load())
			}
			if after := persistentWire(core.Request{Messages: b.contextMessages()}); after != before {
				t.Error("failed exchange did not restore completed history and its frozen prefix")
			}
			c.mu.Lock()
			result, accepted := c.results["accepted-read"]
			c.mu.Unlock()
			if !accepted || result.IsError || !strings.Contains(strings.Join(b.reads, "\n"), "work/escaped.md") {
				t.Error("rollback lost accepted tool cache or acquired source metadata")
			}
			cl.hangup()
		})
	}
}
