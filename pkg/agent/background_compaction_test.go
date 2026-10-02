package agent

// Agent-level tests for background compaction (no runtime): the pending job is
// discarded by every conversation replacement, Stop and real model change but
// survives a same-model refresh; its known cost is debited once to the run
// that started it and never to a later one; the checkpoint is consumed only by
// a durable adoption; a failed save keeps the previous conversation.

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
)

const bgtWait = 5 * time.Second

var bgtPricing = &core.Pricing{Input: 1000, Output: 1000}

// bgtSummary holds its FIRST call until release, ignoring cancellation like a
// provider that answers anyway; later calls (a manual compaction) answer at
// once. Every call reports known usage.
type bgtSummary struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	relOnce sync.Once
}

func (s *bgtSummary) open() { s.relOnce.Do(func() { close(s.release) }) }

func (s *bgtSummary) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("BG SUMMARY")}, StopReason: "end_turn",
		Usage: &core.Usage{Input: 1000, Output: 100}, Timestamp: time.Now().Unix()}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

// bgtProvider answers from script; nil ends the turn.
type bgtProvider struct {
	calls  atomic.Int32
	script func(call int) *core.Message
}

func (p *bgtProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	call := int(p.calls.Add(1))
	m := &core.Message{Role: "assistant", Content: []core.Content{core.TextContent("ok")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	if p.script != nil {
		if s := p.script(call); s != nil {
			m = s
		}
	}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: m}
	close(ch)
	return ch, nil
}

type bgtFix struct {
	ag    *Agent
	prov  *bgtProvider
	sum   *bgtSummary
	slot  *sessioncheckpoint.Slot
	model core.Model

	mu     sync.Mutex
	events []core.AgentEvent
	usage  chan struct{}
	tool   chan struct{}
}

func (f *bgtFix) count(match func(core.AgentEvent) bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if match(e) {
			n++
		}
	}
	return n
}

func isBGEnd(e core.AgentEvent) bool {
	return e.Type == core.AgentEventCompactionEnd && e.BackgroundJobID != 0
}

func isUsage(e core.AgentEvent) bool { return e.Type == core.AgentEventCompactionUsage }

func bgtMsg(role, tag string, n int) core.AgentMessage {
	return core.WrapMessage(core.Message{Role: role, Content: []core.Content{core.TextContent(tag + strings.Repeat("x", n))}, Timestamp: time.Now().Unix()})
}

// newBGT builds an agent whose loaded conversation (~40k tokens) is over the
// soft threshold (39k) and far below hard (99k).
func newBGT(t *testing.T, script func(call int) *core.Message) *bgtFix {
	t.Helper()
	f := &bgtFix{
		prov:  &bgtProvider{script: script},
		sum:   &bgtSummary{entered: make(chan struct{}), release: make(chan struct{})},
		slot:  sessioncheckpoint.New(),
		model: core.Model{ID: "bgt", MaxInput: 100000},
		usage: make(chan struct{}, 4),
		tool:  make(chan struct{}),
	}
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{Name: "gate", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			select {
			case <-f.tool:
			case <-ctx.Done():
			}
			return core.TextResult("gated"), nil
		}}); err != nil {
		t.Fatal(err)
	}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecent: 8000, CompactAt: 40000, TrimDisabled: true}
	ag, err := New(AgentConfig{
		Provider: f.prov, Model: f.model, Tools: reg, Compaction: &settings, MaxTurns: 10, MaxRunDuration: 20 * time.Second,
		SessionCheckpoint: f.slot,
		CompactSummarizer: func(m core.Model) (core.Provider, core.Model, string) {
			m.Pricing = bgtPricing
			return f.sum, m, ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ag = ag
	ag.Subscribe(func(e core.AgentEvent) {
		f.mu.Lock()
		f.events = append(f.events, e)
		f.mu.Unlock()
		if isUsage(e) {
			f.usage <- struct{}{}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	ag.SetBackgroundCompaction(ctx, nil)
	if err := ag.LoadState([]core.AgentMessage{
		bgtMsg("user", "U0 ", 40000), bgtMsg("assistant", "A0 ", 40000), bgtMsg("user", "U1 ", 40000), bgtMsg("assistant", "A1 ", 40000),
	}, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.sum.open()
		select {
		case <-f.tool:
		default:
			close(f.tool)
		}
		ag.Abort()
		cancel()
		ag.WaitBackgroundCompaction()
	})
	return f
}

func (f *bgtFix) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-f.sum.entered:
	case <-time.After(bgtWait):
		t.Fatal("background summary never started")
	}
}

func hasSummary(msgs []core.AgentMessage) bool {
	return len(msgs) > 0 && msgs[0].Role == "compaction_summary"
}

// Every replacement of the source, Stop and a real model change discard a
// pending summary before it is accepted; a same-model refresh keeps it. Known
// usage is reported exactly once either way, and the checkpoint is consumed
// only by the adoption.
func TestBackgroundCompaction_ObsoleteGeneration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		act   func(t *testing.T, f *bgtFix)
		keeps bool
	}{
		{"stop", func(t *testing.T, f *bgtFix) { f.ag.Abort() }, false},
		{"idle_cancel", func(t *testing.T, f *bgtFix) {
			if !f.ag.CancelBackgroundCompaction() {
				t.Fatal("no pending job to cancel")
			}
		}, false},
		{"model_change", func(t *testing.T, f *bgtFix) {
			if err := f.ag.SetModel(nil, core.Model{ID: "other", MaxInput: 100000}); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"window_change", func(t *testing.T, f *bgtFix) {
			if err := f.ag.Reconfigure(nil, core.Model{ID: "bgt", MaxInput: 200000}, "", 40000); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"fresh", func(t *testing.T, f *bgtFix) {
			if p, err := f.ag.StartFresh(); err != nil || p == nil {
				t.Fatalf("fresh: %v %v", p, err)
			}
		}, false},
		{"clear", func(t *testing.T, f *bgtFix) {
			if err := f.ag.Reset(); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"load", func(t *testing.T, f *bgtFix) {
			if err := f.ag.LoadState([]core.AgentMessage{bgtMsg("user", "other branch", 10)}, 0); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"restore", func(t *testing.T, f *bgtFix) {
			msgs, epoch := f.ag.SnapshotConversation()
			if err := f.ag.RestoreConversation(msgs, epoch); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"manual_compact", func(t *testing.T, f *bgtFix) {
			if _, err := f.ag.Compact(context.Background(), ""); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"same_model_refresh", func(t *testing.T, f *bgtFix) {
			if err := f.ag.Reconfigure(nil, f.model, "high", 40000); err != nil {
				t.Fatal(err)
			}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBGT(t, nil)
			if err := f.slot.Write("CHECKPOINT NOTE"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.ag.Send(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			f.waitEntered(t)
			if s := f.ag.BackgroundCompaction(); !s.Active || s.JobID == 0 {
				t.Fatalf("no pending job after the run: %+v", s)
			}
			before := f.ag.Messages()
			tc.act(t, f)
			f.sum.open()
			select {
			case <-f.usage:
			case <-time.After(bgtWait):
				t.Fatal("known usage was not reported")
			}
			f.ag.WaitBackgroundCompaction()
			f.ag.Drain(bgtWait)

			if n := f.count(isUsage); n != 1 {
				t.Fatalf("usage reported %d times, want once", n)
			}
			if s := f.ag.BackgroundCompaction(); s.Active {
				t.Fatalf("job still pending: %+v", s)
			}
			text, _ := f.slot.Read()
			got := f.ag.Messages()
			if !tc.keeps {
				if n := f.count(isBGEnd); n != 0 {
					t.Fatalf("obsolete job produced %d completions", n)
				}
				for _, m := range got {
					for _, c := range m.Content {
						if strings.Contains(c.Text, "BG SUMMARY") && tc.name != "manual_compact" {
							t.Fatal("obsolete summary was adopted")
						}
					}
				}
				if tc.name != "manual_compact" && text != "CHECKPOINT NOTE" {
					t.Fatalf("obsolete job consumed the checkpoint: %q", text)
				}
				return
			}
			if n := f.count(isBGEnd); n != 1 {
				t.Fatalf("refresh: %d completions, want 1", n)
			}
			if !hasSummary(got) || !strings.Contains(got[0].Content[0].Text, "CHECKPOINT NOTE") {
				t.Fatalf("refresh: summary not adopted with its checkpoint: %v", got[0].Content)
			}
			if text != "" {
				t.Fatalf("adoption did not consume the checkpoint: %q", text)
			}
			// summary + literal kept tail: the tail is a suffix of the source.
			tail := got[1:]
			src := before[len(before)-len(tail):]
			for i := range tail {
				if tail[i].MsgID != src[i].MsgID {
					t.Fatalf("kept tail is not literal at %d", i)
				}
			}
		})
	}
}

// The summary's known cost is debited once to the budget of the invocation
// that started it, at its next boundary, and never to a later invocation.
func TestBackgroundCompaction_DebitsOwningRunOnce(t *testing.T) {
	f := newBGT(t, func(call int) *core.Message {
		if call == 1 {
			return &core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("g1", "gate", map[string]any{})}, StopReason: "tool_use", Timestamp: time.Now().Unix()}
		}
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := f.ag.Send(context.Background(), "go")
		done <- err
	}()
	f.waitEntered(t)
	f.sum.open()
	select {
	case <-f.usage:
	case <-time.After(bgtWait):
		t.Fatal("usage not reported")
	}
	// The worker records its outcome after reporting usage; the owner is
	// still in its tool, so the result waits for its next boundary.
	f.ag.WaitBackgroundCompaction()
	close(f.tool)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := bgtPricing.Cost(core.Usage{Input: 1000, Output: 100})
	if got := f.ag.RunCost(); math.Abs(got-want) > 1e-9 {
		t.Fatalf("owning run cost=%v, want the summary's %v", got, want)
	}
	if !hasSummary(f.ag.Messages()) {
		t.Fatal("ready summary was not adopted at the owning run's boundary")
	}
	if _, err := f.ag.Send(context.Background(), "later"); err != nil {
		t.Fatal(err)
	}
	if got := f.ag.RunCost(); got != 0 {
		t.Fatalf("later run was debited %v", got)
	}
}

// A save that fails after acceptance leaves the previous conversation and
// drops the result; a refusal before acceptance is not a failure.
func TestBackgroundCompaction_IdleCommitOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuse  bool
		wantErr bool
	}{{"save_fails", false, true}, {"obsolete_before_accept", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBGT(t, nil)
			var accepts atomic.Int32
			f.ag.SetCompactionCommit(func(ctx context.Context, c core.CompactionCommit) error {
				if c.Accept == nil {
					t.Error("background commit without a cut transaction")
					return errors.New("no accept")
				}
				// Only the first job's save is injected; a later job (the
				// "next" run below) is simply not kept.
				if tc.refuse || accepts.Load() > 0 {
					return core.ErrCompactionObsolete
				}
				_, release, err := c.Accept()
				if err != nil {
					return err
				}
				accepts.Add(1)
				defer release()
				return errors.New("injected disk failure")
			})
			if _, err := f.ag.Send(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			before := f.ag.Messages()
			f.waitEntered(t)
			f.sum.open()
			f.ag.WaitBackgroundCompaction()
			f.ag.Drain(bgtWait)
			got := f.ag.Messages()
			if hasSummary(got) || len(got) != len(before) {
				t.Fatal("conversation changed although the cut was not saved")
			}
			failures := f.count(func(e core.AgentEvent) bool { return isBGEnd(e) && e.Error != nil })
			if tc.wantErr != (failures == 1) || f.count(func(e core.AgentEvent) bool { return isBGEnd(e) && e.Error == nil }) != 0 {
				t.Fatalf("failure events=%d", failures)
			}
			if tc.wantErr && accepts.Load() != 1 {
				t.Fatal("cut was never accepted")
			}
			if s := f.ag.BackgroundCompaction(); tc.wantErr && s.Active {
				t.Fatal("failed job kept pending")
			}
			// The session keeps working: the next run is admitted at once.
			if _, err := f.ag.Send(context.Background(), "next"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A summary that finishes after its owning invocation ended is not owed by
// anyone: no debit may stay pending for a configuration that will never run
// again.
func TestBackgroundCompaction_NoOrphanDebitAfterOwnerEnded(t *testing.T) {
	f := newBGT(t, nil)
	if _, err := f.ag.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.waitEntered(t)
	f.sum.open()
	select {
	case <-f.usage:
	case <-time.After(bgtWait):
		t.Fatal("usage not reported")
	}
	f.ag.WaitBackgroundCompaction()
	f.ag.mu.Lock()
	pending := len(f.ag.bgDebits)
	f.ag.mu.Unlock()
	if pending != 0 {
		t.Fatalf("%d debits pending for an invocation that already ended", pending)
	}
	if _, err := f.ag.Send(context.Background(), "later"); err != nil {
		t.Fatal(err)
	}
	if got := f.ag.RunCost(); got != 0 {
		t.Fatalf("later run was debited %v", got)
	}
}

// The prefix handed to the summarizer is frozen: mutating nested metadata of
// the live message changes neither the copy nor its signature, while the live
// message's signature does change.
func TestBackgroundCompaction_PrefixCloneIsDeep(t *testing.T) {
	m := core.WrapMessage(core.NewUserMessage("hi"))
	m.Custom = map[string]any{"a": map[string]any{"b": []any{"x"}}}
	c := cloneAgentMessage(m)
	sig := mustSig(t, c)
	m.Custom["a"].(map[string]any)["b"].([]any)[0] = "changed"
	if got := c.Custom["a"].(map[string]any)["b"].([]any)[0]; got != "x" {
		t.Fatalf("copy shares nested metadata: %v", got)
	}
	if mustSig(t, c) != sig {
		t.Fatal("copy's signature changed")
	}
	if mustSig(t, m) == sig {
		t.Fatal("signature does not see a nested metadata change")
	}
	// Metadata that cannot be serialized has no stable signature.
	m.Custom["bad"] = math.NaN()
	if _, err := messageSig(m); err == nil {
		t.Fatal("an unserializable message was given a signature")
	}
	if hasPrefix([]core.AgentMessage{m}, []string{sig}) {
		t.Fatal("an unserializable message matched a captured prefix")
	}
}

func mustSig(t *testing.T, m core.AgentMessage) string {
	t.Helper()
	s, err := messageSig(m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
