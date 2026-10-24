package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// snapScene is a For you state the test sets by hand, and the requests the
// provider received, one label per message.
type snapScene struct {
	mu    sync.Mutex
	text  string // "" is the empty state
	reqs  [][]string
	saves []int // provider calls made when each acknowledged save happened
	calls int
}

func (s *snapScene) set(text string) {
	s.mu.Lock()
	s.text = text
	s.mu.Unlock()
}

func (s *snapScene) snapshot() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "SNAP(" + s.text + ")", s.text == ""
}

func snapShape(req core.Request) []string {
	var out []string
	for _, m := range req.Messages {
		text := joinText(m.Content)
		switch {
		case strings.Contains(text, "transport error"):
			text = "HINT"
		case m.Role == "tool_result":
			text = m.ToolCallID
		case m.Role == "assistant" && text == "":
			var ids []string
			for _, c := range m.Content {
				if c.Type == "tool_call" {
					ids = append(ids, c.ToolCallID)
				}
			}
			text = "calls(" + strings.Join(ids, ",") + ")"
		}
		out = append(out, m.Role+":"+text)
	}
	return out
}

func (s *snapScene) requests() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.reqs...)
}

func (s *snapScene) wrap(handlers ...func(core.Request) (<-chan core.AssistantEvent, error)) []func(core.Request) (<-chan core.AssistantEvent, error) {
	out := make([]func(core.Request) (<-chan core.AssistantEvent, error), len(handlers))
	for i, h := range handlers {
		out[i] = func(req core.Request) (<-chan core.AssistantEvent, error) {
			s.mu.Lock()
			s.calls++
			s.reqs = append(s.reqs, snapShape(req))
			s.mu.Unlock()
			return h(req)
		}
	}
	return out
}

func newSnapAgent(t *testing.T, scene *snapScene, cfg AgentConfig, handlers ...func(core.Request) (<-chan core.AssistantEvent, error)) (*Agent, *MockProvider) {
	t.Helper()
	prov := NewMockProvider(scene.wrap(handlers...)...)
	cfg.Provider = prov
	if cfg.Model.ID == "" {
		cfg.Model = core.Model{ID: "test-model", Provider: "mock"}
	}
	if cfg.Tools == nil {
		cfg.Tools = core.NewRegistry()
	}
	cfg.SystemPrompt = "test"
	cfg.MaxTurns = 10
	cfg.MaxToolCallsPerTurn = 5
	cfg.MaxRunDuration = 30 * time.Second
	cfg.ForYouSnapshot = scene.snapshot
	ag, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ag.SetProviderWaitSave(func(context.Context) error {
		scene.mu.Lock()
		scene.saves = append(scene.saves, scene.calls)
		scene.mu.Unlock()
		return nil
	})
	return ag, prov
}

func snapshotsIn(msgs []core.AgentMessage) []core.AgentMessage {
	var out []core.AgentMessage
	for _, m := range msgs {
		if core.IsForYouSnapshot(m) {
			out = append(out, m)
		}
	}
	return out
}

func partialThenFail(onPartial func()) func(core.Request) (<-chan core.AssistantEvent, error) {
	return func(core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "Hello"}
		if onPartial != nil {
			onPartial()
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("openai: unknown error")}
		close(ch)
		return ch, nil
	}
}

func twoToolCalls(a, b string) func(core.Request) (<-chan core.AssistantEvent, error) {
	return func(core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		msg := core.Message{Role: "assistant", StopReason: "tool_use", Timestamp: time.Now().Unix(),
			Content: []core.Content{core.ToolCallContent(a, "noop", nil), core.ToolCallContent(b, "noop", nil)}}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}
}

// An unchanged state is sent once, across tool steps and runs; a change adds
// a new snapshot at the end and leaves the earlier one where it was.
func TestForYouSnapshotAppendedOnlyWhenItChanges(t *testing.T) {
	scene := &snapScene{text: "Q1"}
	ag, _ := newSnapAgent(t, scene, AgentConfig{Tools: snapNoopRegistry(t, nil)},
		stopReasonToolCallResponse("c1", "noop", "tool_use"),
		simpleTextResponse("done"),
		simpleTextResponse("again"),
		simpleTextResponse("third"),
	)
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Send(context.Background(), "more"); err != nil {
		t.Fatal(err)
	}
	if n := len(snapshotsIn(ag.Messages())); n != 1 {
		t.Fatalf("unchanged state: %d snapshots, want 1", n)
	}
	scene.set("")
	if _, err := ag.Send(context.Background(), "last"); err != nil {
		t.Fatal(err)
	}
	reqs := scene.requests()
	want := []string{"user:go", "user:SNAP(Q1)", "assistant:calls(c1)", "tool_result:c1", "assistant:done",
		"user:more", "assistant:again", "user:last", "user:SNAP()"}
	if got := reqs[len(reqs)-1]; !slices.Equal(got, want) {
		t.Fatalf("last request = %v\nwant %v", got, want)
	}
}

// With nothing to tell and nothing told before, no snapshot is added.
func TestForYouEmptyStateWithoutHistoryAddsNothing(t *testing.T) {
	scene := &snapScene{}
	ag, _ := newSnapAgent(t, scene, AgentConfig{}, simpleTextResponse("hi"))
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if n := len(snapshotsIn(ag.Messages())); n != 0 {
		t.Fatalf("%d snapshots, want 0", n)
	}
	if len(scene.saves) != 0 {
		t.Fatalf("an empty state forced %d saves", len(scene.saves))
	}
}

// A change during a batch of parallel calls lands after every result of the
// batch, so no codec sees an interrupted tool_use/tool_result run.
func TestForYouSnapshotFollowsTheWholeToolBatch(t *testing.T) {
	scene := &snapScene{text: "Q1"}
	ag, _ := newSnapAgent(t, scene, AgentConfig{Tools: snapNoopRegistry(t, func() { scene.set("Q2") })},
		twoToolCalls("c1", "c2"),
		simpleTextResponse("done"),
	)
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	want := []string{"user:go", "user:SNAP(Q1)", "assistant:calls(c1,c2)", "tool_result:c1", "tool_result:c2", "user:SNAP(Q2)"}
	if got := scene.requests()[1]; !slices.Equal(got, want) {
		t.Fatalf("request after the batch = %v\nwant %v", got, want)
	}
	var wire []core.Message
	for _, m := range ag.Messages() {
		wire = append(wire, m.Message)
	}
	if got := core.DropUnansweredToolCalls(wire); len(got) != len(wire) {
		t.Fatalf("orphan repair dropped messages: %d of %d", len(got), len(wire))
	}
}

// A continuation (pause_turn) resends what it continues: the change it saw
// waits for the next ordinary request.
func TestForYouSnapshotNotAddedToAContinuation(t *testing.T) {
	scene := &snapScene{text: "Q1"}
	ag, _ := newSnapAgent(t, scene, AgentConfig{},
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			scene.set("Q2")
			return stopReasonResponse("Thinking...", "pause_turn", "", nil)(req)
		},
		simpleTextResponse("answer"),
		simpleTextResponse("next"),
	)
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	reqs := scene.requests()
	if want := []string{"user:go", "user:SNAP(Q1)", "assistant:Thinking..."}; !slices.Equal(reqs[1], want) {
		t.Fatalf("resubmit = %v, want %v", reqs[1], want)
	}
	if _, err := ag.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	last := scene.requests()[2]
	if last[len(last)-1] != "user:SNAP(Q2)" || last[1] != "user:SNAP(Q1)" {
		t.Fatalf("next ordinary request = %v", last)
	}
}

// A transport repair finishes the request already sent: no snapshot between
// the partial and its hint, the change goes to the next ordinary request.
func TestForYouSnapshotNotAddedToATransportRepair(t *testing.T) {
	old := streamRepairBackoff
	streamRepairBackoff = []time.Duration{0, 0}
	t.Cleanup(func() { streamRepairBackoff = old })
	scene := &snapScene{text: "Q1"}
	ag, _ := newSnapAgent(t, scene, AgentConfig{},
		partialThenFail(func() { scene.set("") }),
		simpleTextResponse(" world"),
		simpleTextResponse("next"),
	)
	if _, err := ag.Run(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"user:start", "user:SNAP(Q1)", "assistant:Hello", "user:HINT"}; !slices.Equal(scene.requests()[1], want) {
		t.Fatalf("repair = %v, want %v", scene.requests()[1], want)
	}
	if _, err := ag.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	last := scene.requests()[2]
	if last[len(last)-1] != "user:SNAP()" {
		t.Fatalf("the emptied state must be stated, not dropped: %v", last)
	}
}

// The snapshot is on disk before the request that reads it; a failed save
// means no request.
func TestForYouSnapshotSavedBeforeInference(t *testing.T) {
	scene := &snapScene{text: "Q1"}
	ag, _ := newSnapAgent(t, scene, AgentConfig{}, simpleTextResponse("hi"))
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(scene.saves) != 1 || scene.saves[0] != 0 {
		t.Fatalf("acknowledged saves at provider calls %v, want [0]", scene.saves)
	}

	scene = &snapScene{text: "Q1"}
	ag, _ = newSnapAgent(t, scene, AgentConfig{}, simpleTextResponse("hi"))
	ag.SetProviderWaitSave(func(context.Context) error { return errors.New("disk full") })
	if _, err := ag.Run(context.Background(), "go"); !errors.Is(err, ErrAdmissionNotSaved) {
		t.Fatalf("err = %v, want ErrAdmissionNotSaved", err)
	}
	if scene.calls != 0 {
		t.Fatalf("%d provider calls with an unsaved snapshot", scene.calls)
	}
}

// A restored conversation carries its snapshots where they were: an unchanged
// state adds nothing; an epoch that moved since (a compaction adopted before
// a crash) re-seeds even if the state is the same.
func TestForYouSnapshotAfterRestore(t *testing.T) {
	saved := func(epoch int) []core.AgentMessage {
		snap := core.WrapMessage(core.NewUserMessage("SNAP(Q1)"))
		snap.Custom = map[string]any{"source": core.SourceForYouSnapshot, core.CustomSnapshotEpoch: float64(epoch)}
		return []core.AgentMessage{
			core.WrapMessage(core.NewUserMessage("go")), snap,
			core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("ok")}, StopReason: "end_turn"}),
		}
	}
	for _, tc := range []struct {
		name      string
		loadEpoch int
		wantTail  string
		wantSnaps int
	}{
		{"same epoch", 2, "user:next", 1},
		{"epoch moved", 3, "user:SNAP(Q1)", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scene := &snapScene{text: "Q1"}
			ag, _ := newSnapAgent(t, scene, AgentConfig{}, simpleTextResponse("hi"))
			if err := ag.LoadState(saved(2), tc.loadEpoch); err != nil {
				t.Fatal(err)
			}
			if _, err := ag.Send(context.Background(), "next"); err != nil {
				t.Fatal(err)
			}
			req := scene.requests()[0]
			if want := []string{"user:go", "user:SNAP(Q1)", "assistant:ok", "user:next"}; !slices.Equal(req[:4], want) {
				t.Fatalf("restored prefix = %v", req)
			}
			if req[len(req)-1] != tc.wantTail || len(snapshotsIn(ag.Messages())) != tc.wantSnaps {
				t.Fatalf("request = %v, snapshots = %d", req, len(snapshotsIn(ag.Messages())))
			}
		})
	}
}

// A compaction re-seeds the state from its source before the next request,
// even unchanged, and the summarizer never reads a snapshot.
func TestForYouSnapshotReseededAfterCompaction(t *testing.T) {
	scene := &snapScene{text: "Q1"}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	ag, _ := newSnapAgent(t, scene, AgentConfig{Model: core.Model{ID: "test-model", Provider: "mock", MaxInput: 1000}, Compaction: &settings},
		largeTextResponse(strings.Repeat("x", 2000), &core.Usage{TotalTokens: 950}),
		simpleTextResponse("## Goal\nsummary"),
		simpleTextResponse("second"),
	)
	if _, err := ag.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Send(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	reqs := scene.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want first, summary, second", len(reqs))
	}
	if strings.Contains(strings.Join(reqs[1], "\n"), "SNAP(") {
		t.Fatalf("the summarizer read a snapshot: %v", reqs[1])
	}
	last := reqs[2]
	if last[len(last)-1] != "user:SNAP(Q1)" {
		t.Fatalf("post-compaction request = %v, want a fresh snapshot last", last)
	}
	snaps := snapshotsIn(ag.Messages())
	if got := snapshotEpoch(snaps[len(snaps)-1]); got != ag.CompactionEpoch() || got == 0 {
		t.Fatalf("re-seeded snapshot epoch = %d, agent epoch = %d", got, ag.CompactionEpoch())
	}
}

// A snapshot is state, not input: it never makes a conversation owe a run.
func TestOwesContinuationIgnoresSnapshot(t *testing.T) {
	snap := core.WrapMessage(core.NewUserMessage("SNAP"))
	snap.Custom = map[string]any{"source": core.SourceForYouSnapshot}
	done := core.WrapMessage(core.Message{Role: "assistant", StopReason: "end_turn"})
	if OwesContinuation([]core.AgentMessage{done, snap}) {
		t.Fatal("a trailing snapshot owes a continuation")
	}
}

func snapNoopRegistry(t *testing.T, onCall func()) *core.Registry {
	t.Helper()
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{Name: "noop", Description: "noop", Parameters: []byte(`{"type":"object"}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		if onCall != nil {
			onCall()
		}
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// Continue over a restored conversation that ends in an unfinished reply
// resends it as it stands: the state change waits for the next ordinary
// request.
func TestForYouSnapshotNotAddedWhenContinuingARestoredContinuation(t *testing.T) {
	for _, reason := range []string{"pause_turn", "continue"} {
		t.Run(reason, func(t *testing.T) {
			scene := &snapScene{text: "Q2"}
			ag, _ := newSnapAgent(t, scene, AgentConfig{}, simpleTextResponse("finished"))
			snap := core.WrapMessage(core.NewUserMessage("SNAP(Q1)"))
			snap.Custom = map[string]any{"source": core.SourceForYouSnapshot, core.CustomSnapshotEpoch: 0}
			paused := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("Thinking...")}, StopReason: reason})
			if err := ag.LoadState([]core.AgentMessage{core.WrapMessage(core.NewUserMessage("task")), snap, paused}, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := ag.Continue(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := []string{"user:task", "user:SNAP(Q1)", "assistant:Thinking..."}
			if got := scene.requests()[0]; !slices.Equal(got, want) {
				t.Fatalf("continuation = %v, want %v", got, want)
			}
		})
	}
}

// Past the soft threshold, a restored continuation is still resent as it
// stands: not compacted (which would summarize its snapshot away), so the
// state it was sent with stays in the request.
func TestRestoredContinuationIsNotCompacted(t *testing.T) {
	scene := &snapScene{text: "Q1"}
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	ag, _ := newSnapAgent(t, scene, AgentConfig{Model: core.Model{ID: "test-model", Provider: "mock", MaxInput: 1000}, Compaction: &settings},
		simpleTextResponse("## Goal\nsummary"), simpleTextResponse("finished"))
	snap := core.WrapMessage(core.NewUserMessage("SNAP(Q1)"))
	snap.Custom = map[string]any{"source": core.SourceForYouSnapshot, core.CustomSnapshotEpoch: 0}
	paused := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(strings.Repeat("x", 3000))}, StopReason: "pause_turn"})
	if err := ag.LoadState([]core.AgentMessage{core.WrapMessage(core.NewUserMessage(strings.Repeat("u", 600))), snap, paused}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Continue(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := scene.requests()
	if len(reqs) != 1 || reqs[0][1] != "user:SNAP(Q1)" || ag.CompactionEpoch() != 0 {
		t.Fatalf("requests = %v, epoch = %d: want the resend alone, uncompacted", reqs, ag.CompactionEpoch())
	}
}
