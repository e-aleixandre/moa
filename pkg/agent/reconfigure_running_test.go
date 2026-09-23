package agent

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// scriptedProvider records every request and answers the n-th one with the
// n-th handler.
type scriptedProvider struct {
	mu       sync.Mutex
	reqs     []core.Request
	handlers []func(core.Request) (<-chan core.AssistantEvent, error)
}

func (p *scriptedProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	idx := len(p.reqs)
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	if idx >= len(p.handlers) {
		return nil, fmt.Errorf("unexpected request %d", idx)
	}
	return p.handlers[idx](req)
}

func (p *scriptedProvider) requests() []core.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]core.Request(nil), p.reqs...)
}

func respondWith(msg core.Message) func(core.Request) (<-chan core.AssistantEvent, error) {
	return func(core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		m := msg
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
		close(ch)
		return ch, nil
	}
}

// heldUntil answers like next, but only once release is closed; started is
// closed when the request reaches the provider.
func heldUntil(started, release chan struct{}, next func(core.Request) (<-chan core.AssistantEvent, error)) func(core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		close(started)
		<-release
		return next(req)
	}
}

func toolTurn(id string, usage *core.Usage) core.Message {
	return core.Message{
		Role: "assistant",
		Content: []core.Content{
			core.ThinkingContent("private reasoning"),
			core.ToolCallContent(id, "noop", map[string]any{"id": id}),
		},
		StopReason: "tool_use",
		Usage:      usage,
	}
}

func textTurn(text string, usage *core.Usage) core.Message {
	return core.Message{
		Role:       "assistant",
		Content:    []core.Content{core.ThinkingContent("more reasoning"), core.TextContent(text)},
		StopReason: "end_turn",
		Usage:      usage,
	}
}

func noopRegistry() *core.Registry {
	reg := core.NewRegistry()
	_ = reg.Register(core.Tool{
		Name: "noop",
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	})
	return reg
}

func requestHasThinking(req core.Request) bool {
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Type == "thinking" {
				return true
			}
		}
	}
	return false
}

func historyHasThinking(msgs []core.AgentMessage) bool {
	for _, m := range msgs {
		if hasThinking(m.Content) {
			return true
		}
	}
	return false
}

// hammer reads the agent's configuration from another goroutine
// for as long as the run lasts, so -race sees every access the loop makes
// concurrently with a reconfiguration.
func hammer(ag *Agent, done <-chan struct{}) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = ag.Messages()
			_ = ag.Model()
			_ = ag.ThinkingLevel()
			_ = ag.CompactionEpoch()
			// A write the loop also reads per request. Not model or thinking:
			// writing those back would race the test's own Reconfigure.
			ag.SetFast(false)
			time.Sleep(100 * time.Microsecond)
		}
	}()
	return &wg
}

func closeEnough(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A model/thinking change made while a request is in flight leaves that
// request alone and reaches the next one: its provider, model, thinking level,
// history without the other model's thinking, requested-model stamp and price.
func TestReconfigureWhileRunning_AppliesAtNextRequest(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a", MaxInput: 1_000_000, Pricing: &core.Pricing{Input: 1}}
	modelB := core.Model{ID: "model-b", Provider: "prov-b", MaxInput: 1_000_000, Pricing: &core.Pricing{Input: 10}}
	million := &core.Usage{Input: 1_000_000}

	started, release := make(chan struct{}), make(chan struct{})
	provA := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		heldUntil(started, release, respondWith(toolTurn("tc-1", million))),
	}}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(toolTurn("tc-2", million)),
		respondWith(textTurn("done", million)),
	}}
	ag, err := New(AgentConfig{
		Provider:      provA,
		Model:         modelA,
		ThinkingLevel: "low",
		Tools:         noopRegistry(),
		MaxBudget:     100,
	})
	if err != nil {
		t.Fatal(err)
	}

	var evMu sync.Mutex
	var ended []core.AgentEvent
	ag.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.AgentEventMessageEnd {
			evMu.Lock()
			ended = append(ended, e)
			evMu.Unlock()
		}
	})

	done := make(chan struct{})
	wg := hammer(ag, done)
	runErr := make(chan error, 1)
	go func() {
		_, err := ag.Send(context.Background(), "go")
		runErr <- err
	}()

	<-started
	if err := ag.Reconfigure(provB, modelB, "high", 0); err != nil {
		t.Fatalf("Reconfigure while running: %v", err)
	}
	if got := ag.Model().ID; got != "model-b" {
		t.Fatalf("Model() after reconfigure = %q, want model-b", got)
	}
	close(release)
	if err := <-runErr; err != nil {
		t.Fatal(err)
	}
	close(done)
	wg.Wait()

	reqA, reqB := provA.requests(), provB.requests()
	if len(reqA) != 1 || len(reqB) != 2 {
		t.Fatalf("requests: provider A %d, provider B %d; want 1 and 2", len(reqA), len(reqB))
	}
	if reqA[0].Model.ID != "model-a" || reqA[0].Options.ThinkingLevel != "low" {
		t.Fatalf("in-flight request = %s/%q, want model-a/low", reqA[0].Model.ID, reqA[0].Options.ThinkingLevel)
	}
	for i, req := range reqB {
		if req.Model.ID != "model-b" || req.Options.ThinkingLevel != "high" {
			t.Fatalf("request %d after reconfigure = %s/%q, want model-b/high", i+2, req.Model.ID, req.Options.ThinkingLevel)
		}
	}
	if requestHasThinking(reqB[0]) {
		t.Fatal("first request to model B replayed model A's thinking")
	}
	if !requestHasThinking(reqB[1]) {
		t.Fatal("model B's own thinking was stripped from its next request")
	}

	var requested []string
	for _, m := range ag.Messages() {
		if m.Role == "assistant" {
			requested = append(requested, m.RequestedModel)
		}
	}
	if fmt.Sprint(requested) != "[model-a model-b model-b]" {
		t.Fatalf("requested models in history = %v", requested)
	}

	if got := ag.RunCost(); !closeEnough(got, 1+10+10) {
		t.Fatalf("RunCost = %v, want 21 (one request at A's rate, two at B's)", got)
	}
	ag.Drain(time.Second)
	evMu.Lock()
	defer evMu.Unlock()
	if len(ended) != 3 || ended[0].Pricing != modelA.Pricing || ended[1].Pricing != modelB.Pricing || ended[2].Pricing != modelB.Pricing {
		t.Fatalf("message_end pricing does not follow the model that served each request: %d events", len(ended))
	}
}

// A change made during the run's last request has no later request to reach
// within that run; the history still follows it once the run releases the
// conversation, and the next run uses it.
func TestReconfigureDuringLastRequest_SyncsHistoryAfterRun(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a"}
	modelB := core.Model{ID: "model-b", Provider: "prov-b"}
	started, release := make(chan struct{}), make(chan struct{})
	provA := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		heldUntil(started, release, respondWith(textTurn("first", nil))),
	}}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(textTurn("second", nil)),
	}}
	ag, err := New(AgentConfig{Provider: provA, Model: modelA, ThinkingLevel: "low"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	wg := hammer(ag, done)
	runErr := make(chan error, 1)
	go func() {
		_, err := ag.Send(context.Background(), "one")
		runErr <- err
	}()
	<-started
	if err := ag.SetModel(provB, modelB); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-runErr; err != nil {
		t.Fatal(err)
	}
	close(done)
	wg.Wait()

	if historyHasThinking(ag.Messages()) {
		t.Fatal("model A's thinking survived in history after switching to model B")
	}
	if _, err := ag.Send(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	reqB := provB.requests()
	if len(reqB) != 1 || reqB[0].Model.ID != "model-b" || requestHasThinking(reqB[0]) {
		t.Fatalf("next run did not go to model B with a clean history: %+v", reqB)
	}
}

// The compaction check at the next boundary judges the context against the
// NEW model's window, summarizes with the new model, and charges the summary
// at its rates.
func TestReconfigureWhileRunning_CompactsWithNewModel(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a", MaxInput: 1_000_000, Pricing: &core.Pricing{Input: 1}}
	modelB := core.Model{ID: "model-b", Provider: "prov-b", MaxInput: 100, Pricing: &core.Pricing{Input: 10}}
	million := &core.Usage{Input: 1_000_000}

	started, release := make(chan struct{}), make(chan struct{})
	provA := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		heldUntil(started, release, respondWith(toolTurn("tc-1", million))),
	}}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("SUMMARY")}, StopReason: "end_turn", Usage: million}),
		respondWith(textTurn("done", nil)),
	}}
	ag, err := New(AgentConfig{
		Provider:      provA,
		Model:         modelA,
		ThinkingLevel: "low",
		Tools:         noopRegistry(),
		Compaction:    &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	var seed []core.AgentMessage
	for i := 0; i < 30; i++ {
		seed = append(seed, core.WrapMessage(core.NewUserMessage(fmt.Sprintf("message number %d", i))),
			core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("reply %d", i))}}))
	}
	if err := ag.LoadMessages(seed); err != nil {
		t.Fatal(err)
	}

	var evMu sync.Mutex
	var compactions []*core.CompactionPayload
	ag.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.AgentEventCompactionEnd && e.Compaction != nil {
			evMu.Lock()
			compactions = append(compactions, e.Compaction)
			evMu.Unlock()
		}
	})

	runErr := make(chan error, 1)
	go func() {
		_, err := ag.Send(context.Background(), "go")
		runErr <- err
	}()
	<-started
	if err := ag.Reconfigure(provB, modelB, "high", 0); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-runErr; err != nil {
		t.Fatal(err)
	}

	if got := len(provA.requests()); got != 1 {
		t.Fatalf("model A served %d requests, want only the in-flight one", got)
	}
	if got := len(provB.requests()); got != 2 {
		t.Fatalf("model B served %d requests, want the summary and the next turn", got)
	}
	if ag.CompactionEpoch() != 1 {
		t.Fatalf("compaction epoch = %d, want 1: the new window was not applied", ag.CompactionEpoch())
	}
	if got := ag.RunCost(); !closeEnough(got, 1+10) {
		t.Fatalf("RunCost = %v, want 11 (turn at A's rate, summary at B's)", got)
	}
	ag.Drain(time.Second)
	evMu.Lock()
	defer evMu.Unlock()
	if len(compactions) != 1 || compactions[0].Pricing != modelB.Pricing {
		t.Fatal("compaction_end does not carry the summarizing model's pricing")
	}
}

func TestStripThinkingFromHistory_CopyOnWrite(t *testing.T) {
	orig := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hi")),
		core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.ThinkingContent("t"), core.TextContent("x")}}),
	}
	shared := orig[1].Content
	out := stripThinkingFromHistory(orig)
	if historyHasThinking(out) {
		t.Fatal("thinking not stripped")
	}
	if len(orig[1].Content) != 2 || len(shared) != 2 || shared[0].Type != "thinking" {
		t.Fatal("stripping mutated the input history")
	}
	clean := stripThinkingFromHistory(out)
	if &clean[0] != &out[0] {
		t.Fatal("a history without thinking should be returned as is")
	}
}

func pausedTurn() core.Message {
	return core.Message{
		Role:       "assistant",
		Content:    []core.Content{core.ThinkingContent("signed by A"), core.TextContent("working on it")},
		StopReason: "pause_turn",
	}
}

func seedHistory(n int, text string) []core.AgentMessage {
	var seed []core.AgentMessage
	for i := 0; i < n; i++ {
		seed = append(seed, core.WrapMessage(core.NewUserMessage(fmt.Sprintf("%s %d", text, i))),
			core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("reply %d", i))}}))
	}
	return seed
}

// A switch while a paused turn is in flight does not reach its continuation:
// the paused response is finished by the model that started it, with its own
// thinking and without compacting, even though the new model's smaller window
// would. The next ordinary request goes to the new model, which compacts first.
func TestReconfigureDuringPauseTurn_ContinuesOnOriginalModel(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a", MaxInput: 1_000_000}
	modelB := core.Model{ID: "model-b", Provider: "prov-b", MaxInput: 100}
	started, release := make(chan struct{}), make(chan struct{})
	provA := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		heldUntil(started, release, respondWith(pausedTurn())),
		respondWith(toolTurn("tc-1", nil)),
	}}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("SUMMARY")}, StopReason: "end_turn"}),
		respondWith(textTurn("done", nil)),
	}}
	ag, err := New(AgentConfig{
		Provider:      provA,
		Model:         modelA,
		ThinkingLevel: "low",
		Tools:         noopRegistry(),
		Compaction:    &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.LoadMessages(seedHistory(30, "message number")); err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() {
		_, err := ag.Send(context.Background(), "go")
		runErr <- err
	}()
	<-started
	if err := ag.Reconfigure(provB, modelB, "high", 0); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-runErr; err != nil {
		t.Fatal(err)
	}

	reqA, reqB := provA.requests(), provB.requests()
	if len(reqA) != 2 || len(reqB) != 2 {
		t.Fatalf("requests: A %d, B %d; want 2 (turn, continuation) and 2 (summary, turn)", len(reqA), len(reqB))
	}
	cont := reqA[1]
	if cont.Model.ID != "model-a" || cont.Options.ThinkingLevel != "low" {
		t.Fatalf("continuation = %s/%q, want model-a/low", cont.Model.ID, cont.Options.ThinkingLevel)
	}
	last := cont.Messages[len(cont.Messages)-1]
	if last.Role != "assistant" || len(last.Content) != 2 || last.Content[0].Thinking != "signed by A" {
		t.Fatalf("continuation does not end with the paused response and its thinking: %+v", last)
	}
	if len(cont.Messages) < 60 {
		t.Fatalf("continuation carries %d messages: the paused conversation was compacted", len(cont.Messages))
	}
	if ag.CompactionEpoch() != 1 {
		t.Fatalf("compaction epoch = %d, want 1: model B's first ordinary request did not compact", ag.CompactionEpoch())
	}
	if reqB[1].Model.ID != "model-b" || reqB[1].Options.ThinkingLevel != "high" || requestHasThinking(reqB[1]) {
		t.Fatalf("next ordinary request = %s/%q thinking=%v, want model-b/high without A's thinking",
			reqB[1].Model.ID, reqB[1].Options.ThinkingLevel, requestHasThinking(reqB[1]))
	}
}

// When the continuation ends the run, the new model has no request to reach in
// it; the next run uses it.
func TestReconfigureDuringPauseTurn_TerminalContinuationThenNextRunOnNewModel(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a"}
	modelB := core.Model{ID: "model-b", Provider: "prov-b"}
	started, release := make(chan struct{}), make(chan struct{})
	provA := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		heldUntil(started, release, respondWith(pausedTurn())),
		respondWith(textTurn("finished", nil)),
	}}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(textTurn("second", nil)),
	}}
	ag, err := New(AgentConfig{Provider: provA, Model: modelA, ThinkingLevel: "low"})
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() {
		_, err := ag.Send(context.Background(), "go")
		runErr <- err
	}()
	<-started
	if err := ag.Reconfigure(provB, modelB, "high", 0); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-runErr; err != nil {
		t.Fatal(err)
	}
	if len(provA.requests()) != 2 || len(provB.requests()) != 0 {
		t.Fatalf("requests: A %d, B %d; want 2 and 0", len(provA.requests()), len(provB.requests()))
	}
	msgs := ag.Messages()
	if got := msgs[len(msgs)-1].RequestedModel; got != "model-a" {
		t.Fatalf("continuation stamped %q, want model-a", got)
	}
	if _, err := ag.Send(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	reqB := provB.requests()
	if len(reqB) != 1 || reqB[0].Model.ID != "model-b" || reqB[0].Options.ThinkingLevel != "high" || requestHasThinking(reqB[0]) {
		t.Fatalf("next run did not go to model-b/high with a clean history: %+v", reqB)
	}
}

// Lowering the compaction threshold (the session's own or the global one)
// after the compaction check but before the request is sent — here, while
// attachments are materialized — goes back through the check: the context is
// compacted before the request, and the abandoned preparation is not a turn.
func TestLowerCompactAtAfterCompactionCheck_CompactsBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lower func(*Agent, core.Provider, core.Model) error
	}{
		{"session", func(ag *Agent, p core.Provider, m core.Model) error { return ag.Reconfigure(p, m, "low", 1) }},
		{"global", func(ag *Agent, _ core.Provider, _ core.Model) error { ag.SetDefaultCompactAt(1); return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := core.Model{ID: "model-a", Provider: "prov-a", MaxInput: 1_000_000}
			prov := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
				respondWith(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("SUMMARY")}, StopReason: "end_turn"}),
				respondWith(textTurn("done", nil)),
			}}
			var ag *Agent
			var once sync.Once
			ag, err := New(AgentConfig{
				Provider:      prov,
				Model:         model,
				ThinkingLevel: "low",
				MaxTurns:      1, // a double-counted turn would trip this
				Compaction:    &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10},
				MaterializeContent: func(_ context.Context, msgs []core.Message) ([]core.Message, error) {
					once.Do(func() {
						if err := tc.lower(ag, prov, model); err != nil {
							t.Error(err)
						}
					})
					return msgs, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			// Well past the threshold floor (MinCompactAt), well under the window.
			seed := seedHistory(30, strings.Repeat("filler ", 200))
			if err := ag.LoadMessages(seed); err != nil {
				t.Fatal(err)
			}
			if _, err := ag.Send(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			reqs := prov.requests()
			if len(reqs) != 2 {
				t.Fatalf("provider served %d requests, want the summary and the turn", len(reqs))
			}
			if ag.CompactionEpoch() != 1 {
				t.Fatalf("compaction epoch = %d, want 1", ag.CompactionEpoch())
			}
			if n := len(reqs[1].Messages); n >= len(seed) {
				t.Fatalf("turn request carries %d messages, want the compacted context", n)
			}
		})
	}
}

// A switch that lands after the request's messages were prepared (here, while
// attachments are materialized) still reaches that request: it is sent to the
// new model, without the old model's thinking.
func TestReconfigureDuringRequestPreparation_ReachesThatRequest(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a"}
	modelB := core.Model{ID: "model-b", Provider: "prov-b"}
	provA := &scriptedProvider{}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(textTurn("done", nil)),
	}}
	var ag *Agent
	var once sync.Once
	ag, err := New(AgentConfig{
		Provider:      provA,
		Model:         modelA,
		ThinkingLevel: "low",
		MaterializeContent: func(_ context.Context, msgs []core.Message) ([]core.Message, error) {
			once.Do(func() {
				if err := ag.Reconfigure(provB, modelB, "high", 0); err != nil {
					t.Error(err)
				}
			})
			return msgs, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.LoadMessages([]core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("earlier")),
		core.WrapMessage(core.Message{Role: "assistant", RequestedModel: "model-a", Provider: "prov-a", Content: []core.Content{core.ThinkingContent("signed by A"), core.TextContent("reply")}}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	reqB := provB.requests()
	if len(provA.requests()) != 0 || len(reqB) != 1 {
		t.Fatalf("requests: A %d, B %d; want 0 and 1", len(provA.requests()), len(reqB))
	}
	if reqB[0].Model.ID != "model-b" || reqB[0].Options.ThinkingLevel != "high" || requestHasThinking(reqB[0]) {
		t.Fatalf("request = %s/%q thinking=%v, want model-b/high without A's thinking",
			reqB[0].Model.ID, reqB[0].Options.ThinkingLevel, requestHasThinking(reqB[0]))
	}
	msgs := ag.Messages()
	if got := msgs[len(msgs)-1].RequestedModel; got != "model-b" {
		t.Fatalf("requested model = %q, want model-b", got)
	}
}

// A stream repair retry is a new provider request, so a switch made while the
// failed attempt was in flight reaches the retry.
func TestReconfigureBeforeStreamRepair_ReachesTheRetry(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a", Pricing: &core.Pricing{Input: 1}}
	modelB := core.Model{ID: "model-b", Provider: "prov-b", Pricing: &core.Pricing{Input: 10}}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(textTurn("done", &core.Usage{Input: 1_000_000})),
	}}
	var ag *Agent
	provA := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		func(core.Request) (<-chan core.AssistantEvent, error) {
			if err := ag.Reconfigure(provB, modelB, "high", 0); err != nil {
				t.Error(err)
			}
			ch := make(chan core.AssistantEvent, 1)
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("stream error: connection reset")}
			close(ch)
			return ch, nil
		},
	}}
	ag, err := New(AgentConfig{
		Provider:            provA,
		Model:               modelA,
		ThinkingLevel:       "low",
		StreamRepairBackoff: []time.Duration{time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	reqB := provB.requests()
	if len(provA.requests()) != 1 || len(reqB) != 1 || reqB[0].Model.ID != "model-b" || reqB[0].Options.ThinkingLevel != "high" {
		t.Fatalf("retry did not go to model-b/high: A %d, B %+v", len(provA.requests()), reqB)
	}
	if got := ag.RunCost(); !closeEnough(got, 10) {
		t.Fatalf("RunCost = %v, want 10 at model B's rate", got)
	}
}

// A switch to a smaller window that lands after the compaction check (here,
// while attachments are materialized) must not send the new model a context it
// cannot hold: the loop goes back through the check, compacts with the new
// model, and only then sends. The abandoned preparation is not a turn.
func TestReconfigureToSmallerWindowAfterCompactionCheck_CompactsBeforeSending(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a", MaxInput: 1_000_000}
	modelB := core.Model{ID: "model-b", Provider: "prov-b", MaxInput: 100}
	provA := &scriptedProvider{}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("SUMMARY")}, StopReason: "end_turn"}),
		respondWith(textTurn("done", nil)),
	}}
	var ag *Agent
	var once sync.Once
	ag, err := New(AgentConfig{
		Provider:      provA,
		Model:         modelA,
		ThinkingLevel: "low",
		MaxTurns:      1, // a double-counted turn would trip this
		Compaction:    &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10},
		MaterializeContent: func(_ context.Context, msgs []core.Message) ([]core.Message, error) {
			once.Do(func() {
				if err := ag.Reconfigure(provB, modelB, "high", 0); err != nil {
					t.Error(err)
				}
			})
			return msgs, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var seed []core.AgentMessage
	for i := 0; i < 30; i++ {
		seed = append(seed, core.WrapMessage(core.NewUserMessage(fmt.Sprintf("message number %d", i))),
			core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("reply %d", i))}}))
	}
	if err := ag.LoadMessages(seed); err != nil {
		t.Fatal(err)
	}
	var evMu sync.Mutex
	turns := map[string]int{}
	ag.Subscribe(func(e core.AgentEvent) {
		evMu.Lock()
		turns[e.Type]++
		evMu.Unlock()
	})

	if _, err := ag.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if got := len(provA.requests()); got != 0 {
		t.Fatalf("model A served %d requests, want none", got)
	}
	reqB := provB.requests()
	if len(reqB) != 2 {
		t.Fatalf("model B served %d requests, want the summary and the turn", len(reqB))
	}
	if ag.CompactionEpoch() != 1 {
		t.Fatalf("compaction epoch = %d, want 1", ag.CompactionEpoch())
	}
	if n := len(reqB[1].Messages); n >= len(seed) {
		t.Fatalf("turn request carries %d messages, want the compacted context", n)
	}
	ag.Drain(time.Second)
	evMu.Lock()
	defer evMu.Unlock()
	if turns[core.AgentEventTurnStart] != turns[core.AgentEventTurnEnd] {
		t.Fatalf("unbalanced turns: %d starts, %d ends", turns[core.AgentEventTurnStart], turns[core.AgentEventTurnEnd])
	}
}

// A switch during stream-repair backoff after the old model streamed a partial
// response neither drops that partial nor hands it to the new model: the run
// ends through the stream-failure path, keeping the partial stamped with the
// model that wrote it, and the new model serves the next run.
func TestReconfigureDuringStreamRepairBackoff_KeepsPartialAndStops(t *testing.T) {
	modelA := core.Model{ID: "model-a", Provider: "prov-a", Pricing: &core.Pricing{Input: 1}}
	modelB := core.Model{ID: "model-b", Provider: "prov-b", Pricing: &core.Pricing{Input: 10}}
	failed := make(chan struct{})
	provA := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		func(core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 8)
			go func() {
				ch <- core.AssistantEvent{Type: core.ProviderEventThinkingDelta, ContentIndex: 0, Delta: "A reasoning"}
				ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 1, Delta: "partial from A"}
				ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("stream error: connection reset")}
				close(ch)
				close(failed)
			}()
			return ch, nil
		},
	}}
	provB := &scriptedProvider{handlers: []func(core.Request) (<-chan core.AssistantEvent, error){
		respondWith(textTurn("answer from B", nil)),
	}}
	ag, err := New(AgentConfig{
		Provider:            provA,
		Model:               modelA,
		ThinkingLevel:       "low",
		StreamRepairBackoff: []time.Duration{200 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	var evMu sync.Mutex
	var events []core.AgentEvent
	ag.Subscribe(func(e core.AgentEvent) {
		evMu.Lock()
		events = append(events, e)
		evMu.Unlock()
	})
	runErr := make(chan error, 1)
	go func() {
		_, err := ag.Send(context.Background(), "go")
		runErr <- err
	}()
	<-failed
	if err := ag.Reconfigure(provB, modelB, "high", 0); err != nil {
		t.Fatal(err)
	}
	if err := <-runErr; err == nil || !strings.Contains(err.Error(), "model was changed") {
		t.Fatalf("run error = %v, want the stream failure, not retried after the model change", err)
	}
	if len(provA.requests()) != 1 || len(provB.requests()) != 0 {
		t.Fatalf("requests: A %d, B %d; want 1 and 0 (no automatic retry)", len(provA.requests()), len(provB.requests()))
	}
	msgs := ag.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || last.RequestedModel != "model-a" ||
		strings.Count(joinText(last.Content), "partial from A") != 1 {
		t.Fatalf("recorded partial = %+v, want model A's partial once, stamped model-a", last.Message)
	}

	// Clients keep what streamed only if message_end announces it before the
	// run ends.
	ag.Drain(time.Second)
	evMu.Lock()
	var announced *core.AgentEvent
	endedFirst := false
	for i := range events {
		switch events[i].Type {
		case core.AgentEventMessageEnd:
			if announced == nil && !endedFirst {
				announced = &events[i]
			}
		case core.AgentEventEnd:
			endedFirst = endedFirst || announced == nil
		}
	}
	evMu.Unlock()
	if announced == nil {
		t.Fatal("the persisted partial was not announced with message_end before agent_end")
	}
	if announced.Message.MsgID != last.MsgID || announced.Message.RequestedModel != "model-a" ||
		announced.Pricing != modelA.Pricing || !strings.Contains(joinText(announced.Message.Content), "partial from A") {
		t.Fatalf("message_end = %+v pricing %v, want model A's persisted partial at A's pricing", announced.Message, announced.Pricing)
	}

	if _, err := ag.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	reqB := provB.requests()
	if len(reqB) != 1 || reqB[0].Model.ID != "model-b" || reqB[0].Options.ThinkingLevel != "high" || requestHasThinking(reqB[0]) {
		t.Fatalf("next run did not go to model-b/high without A's thinking: %+v", reqB)
	}
}
