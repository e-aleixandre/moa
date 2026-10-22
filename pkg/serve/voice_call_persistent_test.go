package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
	"github.com/e-aleixandre/moa/pkg/voice"
)

func persistentCall(t *testing.T, f *vpFixture, cl *vpCall) *voiceCall {
	t.Helper()
	f.mgr.voiceCallsMu.Lock()
	r := f.mgr.voiceCalls[0]
	f.mgr.voiceCallsMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.calls[cl.c.ID]
	if c == nil {
		t.Fatal("created call missing")
	}
	return c
}

func persistentWire(req core.Request) string {
	b, _ := json.Marshal(req.Messages)
	return string(b)
}

func TestVoicePersistentBackend_ReadBeyondPrefix(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	vpSeedOwnerBook(t, f, "work/long.md", strings.Repeat("é", 33640)+"LATE-DECISION"+strings.Repeat("z", 20000))
	cl := f.call()
	c := persistentCall(t, f, cl)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"range", map[string]any{"path": "work/long.md", "offset": 33640, "limit": 13}},
		{"eof", map[string]any{"path": "work/long.md", "offset": 999999, "limit": 10}},
		{"negative", map[string]any{"path": "work/long.md", "offset": -1, "limit": 10}},
		{"fraction", map[string]any{"path": "work/long.md", "offset": 1.5, "limit": 10}},
		{"huge", map[string]any{"path": "work/long.md", "offset": 0, "limit": 1e99}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.args)
			r := c.execute(context.Background(), voice.ToolCall{ID: tc.name, Name: "book_read", Arguments: b})
			if tc.name == "negative" || tc.name == "fraction" || tc.name == "huge" {
				if !r.IsError {
					t.Fatal("invalid page accepted")
				}
				return
			}
			var out map[string]any
			if err := json.Unmarshal(r.Output, &out); err != nil {
				t.Fatal(err)
			}
			if tc.name == "range" && (out["content"] != "LATE-DECISION" || out["offset"] != float64(33640) || out["next_offset"] != float64(33653) || out["truncated"] != true) {
				t.Errorf("range unavailable: content chars=%d offset=%v next=%v truncated=%v", len([]rune(fmt.Sprint(out["content"]))), out["offset"], out["next_offset"], out["truncated"])
			}
			if tc.name == "eof" && (out["content"] != "" || out["truncated"] != false) {
				t.Error("EOF page not empty")
			}
		})
	}
	for _, spec := range voice.ToolSpecs() {
		if spec.Name == "book_read" && strings.Contains(string(spec.Parameters), "offset") {
			t.Error("Live tool contract changed")
		}
	}
	cl.hangup()
}

func TestVoicePersistentBackend_FollowupCorrectionSwitchReturn(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	vpSeedOwnerBook(t, f, "work/a.md", strings.Repeat("x", 33640)+"LATE-ALPHA")
	// The fixture owns a real book; add the second file through the same factory.
	ownerID, err := f.mgr.voiceBookOwner(f.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, dir, err := f.mgr.ownerBookDir(ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work/b.md"), []byte("BETA-READ"), 0o600); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	var previous []json.RawMessage
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		body, err := responses.BuildRequestBody(req, responses.Dialect{Provider: "openai", Model: req.Model.ID, SupportsPromptCacheKey: true})
		if err != nil {
			t.Error(err)
		}
		var wire struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Error(err)
		}
		for i, item := range previous {
			if i >= len(wire.Input) || !bytes.Equal(item, wire.Input[i]) {
				t.Errorf("persistent backend changed OpenAI wire prefix at input[%d]", i)
				break
			}
		}
		previous = wire.Input
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool_result" {
			return simpleResponse(`{"answer":"source acquired"}`), nil
		}
		input, all := vpLastUser(req), persistentWire(req)
		switch {
		case strings.Contains(input, "TURN-A"):
			reads.Add(1)
			return toolCallHandlerFor("read-alpha", "book_read", map[string]any{"path": "work/a.md", "offset": 33640})(ctx, req)
		case strings.Contains(input, "TURN-B"):
			reads.Add(1)
			return toolCallHandlerFor("read-beta", "book_read", map[string]any{"path": "work/b.md"})(ctx, req)
		default:
			if !strings.Contains(all, "LATE-ALPHA") {
				t.Error("follow-up lost acquired datum past character 33640")
			}
			if strings.Contains(input, "TURN-RETURN") {
				for _, want := range []string{"BETA-READ", "CORRECTION-HYPOTHETICAL-47"} {
					if !strings.Contains(all, want) {
						t.Errorf("return lost %s", want)
					}
				}
			}
			return simpleResponse(`{"answer":"LATE-ALPHA retained"}`), nil
		}
	})
	cl := f.call()
	p := f.up.peer(t, f.provider)
	for i, input := range []string{"TURN-A", "TURN-FOLLOW: CORRECTION-HYPOTHETICAL-47", "TURN-B", "TURN-RETURN"} {
		id := fmt.Sprintf("continuity-%d", i)
		p.delegate(t, id, input)
		if r := p.result(t, id); vpError(r) {
			t.Fatalf("delegation failed: %v", r)
		}
	}
	cl.hangup()
	if n := reads.Load(); n != 2 {
		t.Errorf("book calls=%d, want 2 (zero rereads)", n)
	}
	f.backendMu.Lock()
	defer f.backendMu.Unlock()
	if n := len(f.backendPlans); n != 1 {
		t.Errorf("provider rebuilt %d times, want one per call", n)
	}
	t.Log("four turns, two book reads, zero rereads")
}

func TestVoicePersistentBackend_ContextBound(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	vpSeedOwnerBook(t, f, "work/source.md", "SOURCE-READ")
	var compacted atomic.Bool
	var calls atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		all := persistentWire(req)
		if len(all) > 256<<10 {
			t.Errorf("request exceeds history bound: %d bytes", len(all))
		}
		assertPersistentPairs(t, req.Messages)
		if strings.Contains(all, "Earlier backend exchanges were dropped") {
			compacted.Store(true)
			if !strings.Contains(all, "work/source.md") {
				t.Error("compaction lost read ledger")
			}
		}
		if calls.Add(1) == 1 {
			return toolCallHandlerFor("initial-source", "book_read", map[string]any{"path": "work/source.md"})(ctx, req)
		}
		return simpleResponse(strings.Repeat("large-answer ", 1000)), nil
	})
	cl := f.call()
	c := persistentCall(t, f, cl)
	for i := range 80 {
		c.runHandoff(context.Background(), "", fmt.Sprintf("bounded-%d", i))
		b := c.backendConversation()
		if n := len(persistentWire(core.Request{Messages: b.contextMessages()})); n > voiceBackendHistoryBytes {
			t.Fatalf("retained history exceeds bound: %d bytes", n)
		}
	}
	if !compacted.Load() {
		t.Error("long call never compacted history")
	}
	cl.hangup()
}

func TestVoicePersistentBackend_OversizedExchangePreservesEarlierState(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	var sends atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		if len(persistentWire(req)) > voiceBackendHistoryBytes {
			t.Error("oversized request reached provider")
		}
		if sends.Add(1) == 1 {
			return simpleResponse("EARLIER-STATE"), nil
		}
		if !strings.Contains(persistentWire(req), "EARLIER-STATE") {
			t.Error("oversized exchange erased earlier completed state")
		}
		return simpleResponse("small answer"), nil
	})
	cl := f.call()
	c := persistentCall(t, f, cl)
	c.runHandoff(context.Background(), "", "first")
	answer := c.runHandoff(context.Background(), "", strings.Repeat("X", voiceBackendHistoryBytes+1))
	if !strings.Contains(answer, "context limit") {
		t.Errorf("oversized exchange not refused: %q", answer)
	}
	c.runHandoff(context.Background(), "", "after overflow")
	if sends.Load() != 2 {
		t.Errorf("provider sends=%d, want 2", sends.Load())
	}
	cl.hangup()
}

func TestVoicePersistentBackend_ReadLedgerBound(t *testing.T) {
	b := &voiceBackend{}
	for i := range 1000 {
		b.recordRead(voiceResult("", map[string]any{"path": fmt.Sprintf("work/%d-%s.md", i, strings.Repeat("名", 50)), "offset": 33640, "next_offset": 49640}))
	}
	data, err := json.Marshal(b.ledger())
	if err != nil || len(data) > voiceBackendLedgerBytes {
		t.Fatalf("read ledger bytes=%d, err=%v", len(data), err)
	}
	if len(b.reads) == 0 || len(b.reads) >= 1000 || !strings.Contains(b.reads[len(b.reads)-1], "999-") {
		t.Fatal("ledger did not retain bounded newest source ranges")
	}
}

func assertPersistentPairs(t *testing.T, msgs []core.Message) {
	t.Helper()
	pending := map[string]bool{}
	for _, msg := range msgs {
		if msg.Role == "tool_result" {
			if !pending[msg.ToolCallID] {
				t.Error("orphaned tool result")
			}
			delete(pending, msg.ToolCallID)
		}
		for _, part := range msg.Content {
			if part.Type == "tool_call" {
				pending[part.ToolCallID] = true
			}
		}
	}
	if len(pending) != 0 {
		t.Error("unresolved tool calls sent to provider")
	}
}

func TestVoicePersistentBackend_HangupSerializesMinutesAndJoins(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	started, cancelled := make(chan struct{}), make(chan struct{})
	var active, overlap atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend {
			return f.prov.Stream(ctx, req)
		}
		if active.Add(1) != 1 {
			overlap.Add(1)
		}
		defer active.Add(-1)
		if minutes {
			return simpleResponse("no minutes"), nil
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	cl := f.call()
	c := persistentCall(t, f, cl)
	p := f.up.peer(t, f.provider)
	p.delegate(t, "in-flight", "lookup")
	select {
	case <-started:
	case <-time.After(vpDeadline):
		t.Fatal("lookup not started")
	}
	cl.hangup()
	vaWaitDone(t, c)
	select {
	case <-cancelled:
	default:
		t.Error("hangup did not cancel lookup")
	}
	select {
	case <-c.workerDone:
	default:
		t.Error("call done before worker joined")
	}
	if n := overlap.Load(); n != 0 {
		t.Errorf("minutes overlapped lookup %d times", n)
	}
}

func TestVoicePersistentBackend_ConcurrentDelegationsAreSerial(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	var active, overlap, sends atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		if active.Add(1) != 1 {
			overlap.Add(1)
		}
		defer active.Add(-1)
		sends.Add(1)
		return simpleResponse(`{"answer":"ok"}`), nil
	})
	cl := f.call()
	p := f.up.peer(t, f.provider)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); p.delegate(t, fmt.Sprintf("concurrent-%d", i), fmt.Sprintf("question-%d", i)) }()
	}
	wg.Wait()
	for i := range 6 {
		p.result(t, fmt.Sprintf("concurrent-%d", i))
	}
	cl.hangup()
	if sends.Load() != 6 || overlap.Load() != 0 {
		t.Errorf("sends=%d overlaps=%d", sends.Load(), overlap.Load())
	}
}

func TestVoicePersistentBackend_FailedExchangeIsNotReplayed(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	var n atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		switch n.Add(1) {
		case 1:
			return simpleResponse("EARLIER-COMPLETE"), nil
		case 2:
			return nil, errors.New("synthetic failure")
		default:
			all := persistentWire(req)
			if !strings.Contains(all, "EARLIER-COMPLETE") {
				t.Error("failed exchange erased earlier completed history")
			}
			if strings.Contains(all, "FAILED-REQUEST") {
				t.Error("failed exchange replayed")
			}
			return simpleResponse("recovered"), nil
		}
	})
	cl := f.call()
	c := persistentCall(t, f, cl)
	for _, req := range []string{"first", "FAILED-REQUEST", "third"} {
		c.runHandoff(context.Background(), "", req)
	}
	cl.hangup()
}
