package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// telemetryProvider answers a summary that "stopped at the cap", reporting
// what it spent and the output cap it says it sent.
type telemetryProvider struct{}

func (telemetryProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	req.Options.AppliedCap.Report(0) // like the ChatGPT OAuth backend: no cap sent
	m := core.Message{
		Role: "assistant", Content: []core.Content{core.TextContent(catomicSummary)},
		StopReason: "max_tokens", Usage: &core.Usage{Input: 321, Output: 45, CacheRead: 6},
		Timestamp: time.Now().Unix(),
	}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

func TestCompactionSummarizerTelemetry_PersistsAndLoads(t *testing.T) {
	model := core.Model{ID: "tele-model", Provider: "tele", MaxInput: 512}
	ag, err := agent.New(agent.AgentConfig{Provider: telemetryProvider{}, Model: model, Tools: core.NewRegistry(), Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10}})
	if err != nil {
		t.Fatal(err)
	}
	tree := session.NewTree()
	for i := 0; i < 2; i++ {
		u := core.WrapMessage(core.NewUserMessage(strings.Repeat(fmt.Sprintf("question %d ", i), 200)))
		u.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: u})
		a := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("answer %d", i))}, StopReason: "end_turn"})
		a.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: a})
	}
	entries, leaf := tree.Snapshot()
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	p := &catomicPersister{store: store, sess: store.Create(), attempted: make(chan struct{})}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: p.sess.ID, Agent: ag, Persister: p, InitialEntries: entries, InitialLeafID: leaf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	done := make(chan CompactionEnded, 2)
	rt.Bus.Subscribe(func(e CompactionEnded) { done <- e })
	if err := rt.Bus.Execute(CompactSession{}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		if e.Err != nil {
			t.Fatal(e.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("compaction did not end")
	}
	rt.Bus.Drain(5 * time.Second)

	saved, err := store.Load(p.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got *core.CompactionSummarizer
	for _, e := range saved.Entries {
		if e.Type == session.EntryCompaction {
			got = e.Compaction.Summarizer
		}
	}
	if got == nil {
		t.Fatal("the saved compaction entry has no summarizer telemetry")
	}
	if got.Model != "tele-model" || got.Provider != "tele" || got.StopReason != "max_tokens" ||
		got.OutputCap != core.SummarizerCapNone || got.MaxOutputTokens != 0 {
		t.Errorf("summarizer = %+v", got)
	}
	if got.Usage == nil || got.Usage.Input != 321 || got.Usage.Output != 45 || got.Usage.CacheRead != 6 {
		t.Errorf("summarizer usage = %+v", got.Usage)
	}
}

// Sessions written before the field existed load with it nil, and an entry
// without it does not serialize it.
func TestCompactionSummarizerTelemetry_OldEntriesLoad(t *testing.T) {
	old := `{"summary":"s","first_kept_entry_id":"x","tokens_before":10}`
	var d session.CompactionData
	if err := json.Unmarshal([]byte(old), &d); err != nil {
		t.Fatal(err)
	}
	if d.Summarizer != nil {
		t.Fatalf("summarizer = %+v, want nil", d.Summarizer)
	}
	out, _ := json.Marshal(d)
	if strings.Contains(string(out), "summarizer") {
		t.Errorf("nil summarizer serialized: %s", out)
	}
}
