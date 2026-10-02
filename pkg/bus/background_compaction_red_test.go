package bus

// STEP2 RED tests for background compaction. They drive the real Agent, the
// real runtime bus, the real TreeSyncer and a real session.FileStore (through
// catomicPersister). Only the providers are scripted: an ordinary provider that
// records every request, and a summary provider whose single call is held on a
// channel handshake until the test releases it.
//
// Owner semantics under test: soft = core.ShouldCompact over the effective
// window (strict >), hard = model window - reserve (strict >, equality fits).
// Between soft and hard a summary is computed in the background: the ordinary
// request does not wait for it, and it is adopted only once durable. At hard
// the request waits for the durable summary.

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/compaction"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

const bgWait = 5 * time.Second

// bgNegative is the bounded window in which something that must NOT happen
// would have happened: a violating implementation answers within microseconds.
const bgNegative = 500 * time.Millisecond

type bgReq struct {
	req     core.Request
	durable bool // the compaction boundary was already durable when it arrived
}

// bgProvider is the ordinary provider: it records each request and answers
// from script (nil script, or a nil message, ends the turn).
type bgProvider struct {
	mu     sync.Mutex
	calls  int
	f      *bgFix
	script func(call int, req core.Request) *core.Message
	reqs   chan bgReq
}

func (p *bgProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	p.reqs <- bgReq{req: req, durable: p.f.durable.Load()}
	m := &core.Message{Role: "assistant", Content: []core.Content{core.TextContent("ok")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	if p.script != nil {
		if s := p.script(call, req); s != nil {
			m = s
		}
	}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: m}
	close(ch)
	return ch, nil
}

// bgSummary is the summarizer: its call is held until release is closed.
type bgSummary struct {
	mu          sync.Mutex
	requests    []core.Request
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
	releaseOnce sync.Once
}

func (s *bgSummary) open() { s.releaseOnce.Do(func() { close(s.release) }) }

func (s *bgSummary) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *bgSummary) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	s.enteredOnce.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(catomicSummary)}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

type bgCfg struct {
	window, compactAt, reserve, keep int
	initial                          []core.AgentMessage
	script                           func(call int, req core.Request) *core.Message
}

type bgFix struct {
	rt       *SessionRuntime
	ag       *agent.Agent
	reg      *core.Registry
	p        *catomicPersister
	prov     *bgProvider
	sum      *bgSummary
	settings core.CompactionSettings
	window   int
	initial  []core.AgentMessage

	durable atomic.Bool
	ended   chan RunEnded
	cuts    chan CompactionEnded
	runs    atomic.Int32

	growLen      atomic.Int64
	toolStarted  chan struct{}
	toolRelease  chan struct{}
	toolStartOne sync.Once
	toolRelOnce  sync.Once
}

func (f *bgFix) openTool() { f.toolRelOnce.Do(func() { close(f.toolRelease) }) }

func bgText(tag string, n int) string { return tag + strings.Repeat("x", n-len(tag)) }

func bgUser(tag string, chars int) core.AgentMessage {
	return core.WrapMessage(core.NewUserMessage(bgText(tag, chars)))
}

func bgAssistant(tag string, chars int) core.AgentMessage {
	return core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(bgText(tag, chars))}, StopReason: "end_turn", Timestamp: time.Now().Unix()})
}

func newBGFix(t *testing.T, c bgCfg) *bgFix {
	t.Helper()
	f := &bgFix{
		ended: make(chan RunEnded, 16), cuts: make(chan CompactionEnded, 16),
		toolStarted: make(chan struct{}), toolRelease: make(chan struct{}),
		sum:     &bgSummary{entered: make(chan struct{}), release: make(chan struct{})},
		reg:     core.NewRegistry(),
		initial: c.initial,
	}
	f.prov = &bgProvider{f: f, script: c.script, reqs: make(chan bgReq, 32)}
	// bgnoop: with {"gate":true} it blocks (once started is signalled) until
	// the test opens it; with {"grow":true} it returns growLen characters.
	if err := f.reg.Register(core.Tool{Name: "bgnoop", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
		Execute: func(ctx context.Context, args map[string]any, _ func(core.Result)) (core.Result, error) {
			if g, _ := args["gate"].(bool); g {
				f.toolStartOne.Do(func() { close(f.toolStarted) })
				select {
				case <-f.toolRelease:
				case <-ctx.Done():
				}
				return core.TextResult("noop gated"), nil
			}
			if g, _ := args["grow"].(bool); g {
				return core.TextResult(strings.Repeat("x", int(f.growLen.Load()))), nil
			}
			return core.TextResult("noop"), nil
		}}); err != nil {
		t.Fatal(err)
	}
	f.settings = core.CompactionSettings{Enabled: true, ReserveTokens: c.reserve, KeepRecent: c.keep, CompactAt: c.compactAt, TrimDisabled: true}
	model := core.Model{ID: "bg", MaxInput: c.window}
	f.window = f.settings.EffectiveWindow(c.window)
	settings := f.settings
	ag, err := agent.New(agent.AgentConfig{
		Provider: f.prov, Model: model, Tools: f.reg, Compaction: &settings, MaxTurns: 12, MaxRunDuration: 20 * time.Second,
		CompactSummarizer: func(m core.Model) (core.Provider, core.Model, string) { return f.sum, m, "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ag = ag

	tree := session.NewTree()
	for _, m := range c.initial {
		m.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: m})
	}
	entries, leaf := tree.Snapshot()
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	f.p = &catomicPersister{store: store, sess: store.Create(), attempted: make(chan struct{}),
		after: func(entries []session.Entry) {
			if catomicHasCompaction(entries) {
				f.durable.Store(true)
			}
		}}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: f.p.sess.ID, Agent: ag, Persister: f.p, InitialEntries: entries, InitialLeafID: leaf})
	if err != nil {
		t.Fatal(err)
	}
	f.rt = rt
	rt.Bus.Subscribe(func(e RunEnded) { f.ended <- e })
	rt.Bus.Subscribe(func(e CompactionEnded) { f.cuts <- e })
	rt.Bus.Subscribe(func(RunStarted) { f.runs.Add(1) })

	// One cleanup, so a failing (RED) test cannot hang or leak a run into the
	// deleted TempDir: open every gate, stop the run, wait for it to settle
	// while the bus is still alive, drain, then close.
	t.Cleanup(func() {
		f.sum.open()
		f.openTool()
		ag.Abort()
		ctx, cancel := context.WithTimeout(context.Background(), bgWait)
		defer cancel()
		if !rt.WaitSettled(ctx) {
			t.Error("cleanup: the run did not settle after Abort")
		}
		rt.Bus.Drain(bgWait)
		rt.Close()
	})
	return f
}

func (f *bgFix) send(t *testing.T, text string) {
	t.Helper()
	if err := f.rt.Bus.Execute(SendPrompt{Text: text}); err != nil {
		t.Fatal(err)
	}
}

func (f *bgFix) request(t *testing.T, what string) bgReq {
	t.Helper()
	select {
	case r := <-f.prov.reqs:
		return r
	case <-time.After(bgWait):
		t.Fatalf("no ordinary provider request: %s", what)
	}
	return bgReq{}
}

// noRequest reports that no ordinary request arrives in the negative window.
func (f *bgFix) noRequest() (bgReq, bool) {
	select {
	case r := <-f.prov.reqs:
		return r, false
	case <-time.After(bgNegative):
		return bgReq{}, true
	}
}

func (f *bgFix) waitEnded(t *testing.T, what string) RunEnded {
	t.Helper()
	select {
	case e := <-f.ended:
		return e
	case <-time.After(bgWait):
		t.Fatalf("RunEnded did not arrive: %s", what)
	}
	return RunEnded{}
}

func (f *bgFix) waitSummaryEntered(t *testing.T) {
	t.Helper()
	select {
	case <-f.sum.entered:
	case <-time.After(bgWait):
		t.Fatal("the summary provider was never called")
	}
}

// waitCut waits for the durable cut: the save that carries the boundary and
// the completion event that follows adoption.
func (f *bgFix) waitCut(t *testing.T) {
	t.Helper()
	select {
	case e := <-f.cuts:
		if e.Err != nil {
			t.Fatalf("compaction failed: %v", e.Err)
		}
	case <-time.After(bgWait):
		t.Fatal("the compaction never became durable")
	}
	f.rt.Bus.Drain(bgWait)
	if !f.durable.Load() {
		t.Fatal("compaction completed without a durable boundary save")
	}
}

func (f *bgFix) savedRaw(t *testing.T) ([]session.Entry, string) {
	t.Helper()
	saved, err := f.p.store.Load(f.p.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return saved.Entries, saved.LeafID
}

func bgShapesLLM(msgs []core.Message) []catomicMsg {
	out := make([]catomicMsg, 0, len(msgs))
	for _, m := range msgs {
		x := catomicMsgOf(core.AgentMessage{Message: m})
		x.ID = ""
		out = append(out, x)
	}
	return out
}

func bgShapes(msgs []core.AgentMessage) []catomicMsg {
	out := catomicMsgs(msgs)
	for i := range out {
		out[i].ID = ""
	}
	return out
}

func bgEstimate(f *bgFix, req core.Request) int {
	msgs := make([]core.AgentMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = core.AgentMessage{Message: m}
	}
	return core.EstimateContextTokens(msgs, req.System, req.Tools, 0).Tokens
}

// bgHasSummary reports whether the request carries the (wrapped) summary.
func bgHasSummary(req core.Request) bool { return catomicRequestCompacted(req) }

func bgToolCall(id string, args map[string]any, text string) *core.Message {
	return &core.Message{Role: "assistant", Content: []core.Content{core.TextContent(text), core.ToolCallContent(id, "bgnoop", args)}, StopReason: "tool_use", Timestamp: time.Now().Unix()}
}

// bgPrefix is the 160k-character conversation of the first RED: four 40k
// character messages (~40k tokens) over the explicit soft of 39k, and far below
// the real hard of 99k.
func bgPrefix() []core.AgentMessage {
	return []core.AgentMessage{bgUser("U0 ", 40000), bgAssistant("A0 ", 40000), bgUser("U1 ", 40000), bgAssistant("A1 ", 40000)}
}

func newBGSoftBelowHard(t *testing.T, script func(int, core.Request) *core.Message) *bgFix {
	t.Helper()
	f := newBGFix(t, bgCfg{window: 100000, compactAt: 40000, reserve: 1000, keep: 8000, initial: bgPrefix(), script: script})
	// Harness validity: the prefix really sits between the explicit soft
	// (39k) and the real hard (99k), and a cut exists.
	soft, hard := f.window-f.settings.ReserveTokens, 100000-f.settings.ReserveTokens
	est := core.EstimateContextTokens(append(append([]core.AgentMessage(nil), f.initial...), bgUser("go", 40)), f.ag.SystemPrompt(), f.reg.Specs(), 0).Tokens
	if soft != 39000 || hard != 99000 || !core.ShouldCompact(est, f.window, f.settings) || est > hard {
		t.Fatalf("harness: soft=%d hard=%d estimate=%d", soft, hard, est)
	}
	if compaction.FindCutPoint(append(append([]core.AgentMessage(nil), f.initial...), bgUser("go", 40)), est, f.window, f.settings) <= 0 {
		t.Fatal("harness: no cut point")
	}
	return f
}

// RED 1: soft crossed, hard far away. The ordinary request must leave with the
// old P while the summary is held; the foreground implementation holds it.
func TestBackgroundCompactionRED_SoftBelowHardDoesNotHoldOrdinaryRequest(t *testing.T) {
	f := newBGSoftBelowHard(t, nil)
	f.send(t, bgText("go ", 40))
	r := f.request(t, "soft crossed but below hard: the ordinary request waited for the held summary")
	if bgHasSummary(r.req) || len(r.req.Messages) != len(f.initial)+1 {
		t.Fatalf("ordinary request must carry the old P unchanged, got %v", bgShapesLLM(r.req.Messages))
	}
	f.waitSummaryEntered(t)
	f.waitEnded(t, "run must end while the summary is still held")
	f.sum.open()
	f.waitCut(t)
	if n := f.sum.calls(); n != 1 {
		t.Fatalf("summary called %d times, want exactly 1", n)
	}
}

// RED 2: the originating run ends while the summary is held; the finished job
// commits at idle without any new turn.
func TestBackgroundCompactionRED_RunEndsWhileHeldThenIdleCommit(t *testing.T) {
	f := newBGSoftBelowHard(t, nil)
	f.send(t, bgText("go ", 40))
	f.request(t, "ordinary request while the summary is held")
	f.waitEnded(t, "originating run must end while its summary job is pending")
	f.rt.Bus.Drain(bgWait)
	if f.durable.Load() {
		t.Fatal("a boundary was durable before the summary was released")
	}
	originals := catomicMsgs(f.ag.Messages())
	f.sum.open()
	f.waitCut(t)

	if _, more := f.noRequest(); !more {
		t.Fatal("idle commit sent an extra provider request")
	}
	select {
	case e := <-f.ended:
		t.Fatalf("idle commit produced a second RunEnded: %+v", e)
	default:
	}
	if f.runs.Load() != 1 || f.prov.calls != 1 || f.sum.calls() != 1 {
		t.Fatalf("idle commit created a turn: runs=%d provider=%d summary=%d", f.runs.Load(), f.prov.calls, f.sum.calls())
	}
	entries, leaf := f.savedRaw(t)
	k, err := catomicCheckNewSnapshot(entries, leaf, originals)
	if err != nil {
		t.Fatalf("durable cut: %v", err)
	}
	if want := compaction.FindCutPoint(f.initial, 0, f.window, f.settings); k < 1 || want < 1 {
		t.Fatalf("cut index %d / %d", k, want)
	}
	got := f.ag.Messages()
	if len(got) == 0 || got[0].Role != "compaction_summary" || !strings.Contains(catomicMsgOf(got[0]).Text, catomicSummary) {
		t.Fatalf("agent did not adopt the summary: %v", catomicMsgs(got))
	}
	if !equalMsgs(catomicMsgs(got[1:]), originals[k:]) {
		t.Fatalf("adopted context is not summary + literal tail:\n got %v\nwant %v", catomicMsgs(got[1:]), originals[k:])
	}
}

func equalMsgs(a, b []catomicMsg) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RED 3: everything admitted after P (two tool batches, a withdrawing steer
// and a normal steer) survives literally, once and in order, in the adopted
// context, and none of it reaches the summarizer.
func TestBackgroundCompactionRED_PostPLiteralWithSteers(t *testing.T) {
	const early, normal = "early withdrawing steer literal", "normal steer literal"
	f := newBGSoftBelowHard(t, func(call int, req core.Request) *core.Message {
		switch call {
		case 1:
			return &core.Message{Role: "assistant", Content: []core.Content{core.TextContent("post-P batch one"),
				core.ToolCallContent("bg-a1", "bgnoop", map[string]any{"gate": true}), core.ToolCallContent("bg-a2", "bgnoop", map[string]any{})},
				StopReason: "tool_use", Timestamp: time.Now().Unix()}
		case 2:
			return bgToolCall("bg-b1", map[string]any{}, "post-P batch two")
		case 3:
			return &core.Message{Role: "assistant", Content: []core.Content{core.TextContent("post-P final")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
		}
		return nil
	})
	f.send(t, bgText("go ", 40))
	f.request(t, "first ordinary request while the summary is held")
	select {
	case <-f.toolStarted:
	case <-time.After(bgWait):
		t.Fatal("first tool never started")
	}
	if err := f.ag.TrySteer(core.SteerItem{ID: "early-user", Text: early}); err != nil {
		t.Fatal(err)
	}
	if err := f.ag.TrySteer(core.SteerItem{ID: "normal-auto", Text: normal, Internal: true, Custom: map[string]any{"source": "bash_job"}}); err != nil {
		t.Fatal(err)
	}
	f.openTool()
	f.waitEnded(t, "run with two tool batches and steers must end while the summary is held")
	f.rt.Bus.Drain(bgWait)

	// Every ordinary request so far carried the old P as its literal prefix.
	pLen := len(f.initial) + 1
	pre := f.ag.Messages()
	if len(pre) <= pLen {
		t.Fatalf("nothing was admitted after P: %v", catomicMsgs(pre))
	}
	joined := ""
	for _, m := range pre {
		for _, c := range m.Content {
			joined += c.Text + "\n"
		}
	}
	for _, want := range []string{early, normal, "post-P batch two", "post-P final"} {
		if strings.Count(joined, want) != 1 {
			t.Fatalf("%q appears %d times in history, want once", want, strings.Count(joined, want))
		}
	}
	originals := catomicMsgs(pre)

	f.sum.open()
	f.waitCut(t)

	// Nothing admitted after P was summarized.
	f.sum.mu.Lock()
	for _, r := range f.sum.requests {
		for _, m := range r.Messages {
			for _, c := range m.Content {
				for _, bad := range []string{early, normal, "post-P"} {
					if strings.Contains(c.Text, bad) {
						t.Errorf("summary source contains post-P text %q", bad)
					}
				}
			}
		}
	}
	f.sum.mu.Unlock()
	if n := f.sum.calls(); n != 1 {
		t.Fatalf("summary called %d times, want 1", n)
	}

	// Durable: reopened tree is summary + kept P + every post-P message once.
	entries, leaf := f.savedRaw(t)
	k, err := catomicCheckNewSnapshot(entries, leaf, originals)
	if err != nil {
		t.Fatalf("durable cut: %v", err)
	}
	if k >= pLen {
		t.Fatalf("cut index %d moved into the post-P tail (P has %d messages)", k, pLen)
	}
	got := f.ag.Messages()
	if got[0].Role != "compaction_summary" || !equalMsgs(catomicMsgs(got[1:]), originals[k:]) {
		t.Fatalf("agent context:\n got %v\nwant summary + %v", catomicMsgs(got), originals[k:])
	}

	// The next ordinary request is summary + that literal tail + the new prompt.
	f.send(t, "follow-up after the cut")
	var r bgReq
	for {
		r = f.request(t, "request after the cut")
		if len(r.req.Messages) > 0 && r.req.Messages[len(r.req.Messages)-1].Role == "user" &&
			strings.Contains(bgShapesLLM(r.req.Messages[len(r.req.Messages)-1:])[0].Text, "follow-up") {
			break
		}
	}
	if !r.durable || !bgHasSummary(r.req) {
		t.Fatalf("request after the cut: durable=%v summary=%v", r.durable, bgHasSummary(r.req))
	}
	body := bgShapesLLM(r.req.Messages[1 : len(r.req.Messages)-1])
	if want := bgShapes(got[1:]); !equalMsgs(body, want) {
		t.Fatalf("request after the cut:\n got %v\nwant %v", body, want)
	}
	f.waitEnded(t, "follow-up run")
}

// bgExactTail returns the length of a tool result which makes the context of
// the next request estimate to exactly target tokens.
func bgExactTail(t *testing.T, f *bgFix, prompt core.AgentMessage, asst *core.Message, id string, target int) int {
	t.Helper()
	msgs := append(append([]core.AgentMessage(nil), f.initial...), prompt, core.WrapMessage(*asst))
	result := func(n int) core.AgentMessage {
		return core.WrapMessage(core.Message{Role: "tool_result", ToolCallID: id, ToolName: "bgnoop", Content: []core.Content{core.TextContent(strings.Repeat("x", n))}})
	}
	est := func(n int) int {
		return core.EstimateContextTokens(append(append([]core.AgentMessage(nil), msgs...), result(n)), f.ag.SystemPrompt(), f.reg.Specs(), 0).Tokens
	}
	base := 4 * (target - est(0))
	for a := 0; a < 4; a++ {
		if base+a >= 0 && est(base+a) == target {
			return base + a
		}
	}
	t.Fatalf("cannot build a tail estimating exactly %d", target)
	return 0
}

// RED 4: soft below the real hard, a held job, and a post-P tail that reaches
// hard. Equality fits and the request leaves; hard+1 waits for the durable
// summary, and no request over hard ever leaves.
func TestBackgroundCompactionRED_HardBoundaryWithHeldJob(t *testing.T) {
	// soft = 10000-1000 = 9000, hard = 30000-1000 = 29000.
	const hard = 29000
	for _, tc := range []struct {
		name string
		over int
		fits bool
	}{{"estimate_equals_hard_fits", 0, true}, {"estimate_hard_plus_one_waits", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := bgUser("go ", 40)
			asst := bgToolCall("bg-big", map[string]any{"grow": true}, "grow")
			f := newBGFix(t, bgCfg{window: 30000, compactAt: 10000, reserve: 1000, keep: 2000,
				initial: []core.AgentMessage{bgUser("U0 ", 9500), bgAssistant("A0 ", 9500), bgUser("U1 ", 9500), bgAssistant("A1 ", 9500)},
				script: func(call int, req core.Request) *core.Message {
					if call == 1 {
						return bgToolCall("bg-big", map[string]any{"grow": true}, "grow")
					}
					return nil
				}})
			// P = 9500 tokens + prompt: over soft 9000, far below hard.
			p := append(append([]core.AgentMessage(nil), f.initial...), prompt)
			pEst := core.EstimateContextTokens(p, f.ag.SystemPrompt(), f.reg.Specs(), 0).Tokens
			if !core.ShouldCompact(pEst, f.window, f.settings) || pEst > hard || f.window != 10000 {
				t.Fatalf("harness: P estimate %d window %d", pEst, f.window)
			}
			f.growLen.Store(int64(bgExactTail(t, f, prompt, asst, "bg-big", hard+tc.over)))
			f.send(t, bgText("go ", 40))
			f.request(t, "request 1 (P over soft, below hard) waited for the held summary")
			f.waitSummaryEntered(t)
			if tc.fits {
				r := f.request(t, "request at exactly hard waited for the held summary")
				if bgHasSummary(r.req) || bgEstimate(f, r.req) != hard {
					t.Fatalf("request at equality: summary=%v estimate=%d want %d", bgHasSummary(r.req), bgEstimate(f, r.req), hard)
				}
				f.waitEnded(t, "run at equality")
				f.sum.open()
				f.waitCut(t)
				return
			}
			if r, quiet := f.noRequest(); !quiet {
				t.Fatalf("a request estimating %d (> hard %d) left while the summary was held (durable=%v)", bgEstimate(f, r.req), hard, r.durable)
			}
			f.sum.open()
			r := f.request(t, "request after the summary became durable")
			if !r.durable || !bgHasSummary(r.req) || bgEstimate(f, r.req) > hard {
				t.Fatalf("request after hard wait: durable=%v summary=%v estimate=%d", r.durable, bgHasSummary(r.req), bgEstimate(f, r.req))
			}
			f.waitEnded(t, "run after hard wait")
		})
	}
}

// bgExactPrompt makes the first-boundary context estimate exactly target
// tokens: two 2500-token messages and a prompt carrying the rest.
func bgExactPrompt(f *bgFix, target int) string {
	over := core.EstimateContextTokens(nil, f.ag.SystemPrompt(), f.reg.Specs(), 0).Tokens
	n := target - 5000 - over
	return strings.Repeat("p", 4*n)
}

func bgSmallPrefix() []core.AgentMessage {
	return []core.AgentMessage{bgUser("U0 ", 10000), bgAssistant("A0 ", 10000)}
}

// RED 4b: soft strictness. Exactly soft does not start a job (control: passes
// today); soft+1 does, and its request does not wait (RED today).
func TestBackgroundCompactionRED_SoftIsStrict(t *testing.T) {
	for _, tc := range []struct {
		name string
		over int
	}{{"estimate_equals_soft_no_job", 0}, {"estimate_soft_plus_one_job_without_wait", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBGFix(t, bgCfg{window: 30000, compactAt: 10000, reserve: 1000, keep: 2000, initial: bgSmallPrefix()})
			text := bgExactPrompt(f, 9000+tc.over)
			p := append(append([]core.AgentMessage(nil), f.initial...), core.WrapMessage(core.NewUserMessage(text)))
			est := core.EstimateContextTokens(p, f.ag.SystemPrompt(), f.reg.Specs(), 0).Tokens
			if est != 9000+tc.over || core.ShouldCompact(est, f.window, f.settings) != (tc.over == 1) {
				t.Fatalf("harness: estimate %d window %d", est, f.window)
			}
			f.send(t, text)
			r := f.request(t, "request leaves with the old context")
			if bgHasSummary(r.req) || len(r.req.Messages) != 3 {
				t.Fatalf("request must carry the old P: %v", bgShapesLLM(r.req.Messages))
			}
			if tc.over == 0 {
				f.waitEnded(t, "run")
				if n := f.sum.calls(); n != 0 {
					t.Fatalf("estimate == soft started %d summaries", n)
				}
				return
			}
			f.waitSummaryEntered(t)
			f.waitEnded(t, "run ends while the job is held")
			f.sum.open()
			f.waitCut(t)
		})
	}
}

// Control (passes today and after): soft == hard leaves zero runway, so the
// ordinary request waits for the durable summary at hard+1 and does not at hard.
func TestBackgroundCompactionControl_ZeroRunwayStillBlocks(t *testing.T) {
	// window 12000, reserve 1000, no compact_at: soft == hard == 11000.
	for _, tc := range []struct {
		name string
		over int
	}{{"estimate_equals_hard_leaves", 0}, {"estimate_hard_plus_one_blocks", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBGFix(t, bgCfg{window: 12000, compactAt: 0, reserve: 1000, keep: 2000, initial: bgSmallPrefix()})
			text := bgExactPrompt(f, 11000+tc.over)
			p := append(append([]core.AgentMessage(nil), f.initial...), core.WrapMessage(core.NewUserMessage(text)))
			est := core.EstimateContextTokens(p, f.ag.SystemPrompt(), f.reg.Specs(), 0).Tokens
			if est != 11000+tc.over || f.window != 12000 || core.ShouldCompact(est, f.window, f.settings) != (tc.over == 1) {
				t.Fatalf("harness: estimate %d window %d", est, f.window)
			}
			f.send(t, text)
			if tc.over == 0 {
				r := f.request(t, "request at exactly hard")
				if bgHasSummary(r.req) {
					t.Fatal("equality must fit without a summary")
				}
				f.waitEnded(t, "run")
				if n := f.sum.calls(); n != 0 {
					t.Fatalf("equality started %d summaries", n)
				}
				return
			}
			f.waitSummaryEntered(t)
			if r, quiet := f.noRequest(); !quiet {
				t.Fatalf("an oversized request (%d > hard) left while the summary was held", bgEstimate(f, r.req))
			}
			f.sum.open()
			r := f.request(t, "request after the durable summary")
			if !r.durable || !bgHasSummary(r.req) {
				t.Fatalf("request after the wait: durable=%v summary=%v", r.durable, bgHasSummary(r.req))
			}
			f.waitEnded(t, "run")
		})
	}
}
