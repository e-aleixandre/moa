package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

const voiceCompactSentinel = "VOICE_COMPACT_REAL_RECORDVOICE_MINUTES"

// Every controller method is the real Agent's. Only the return after the real
// AppendRecord is paused: no state, event, repository or persistence behavior is
// replaced. This models descheduling between append and recordVoice's Flush.
type voiceCompactAppendReturnGate struct {
	*agent.Agent
	appended chan struct{}
	resume   chan struct{}
}

func (a *voiceCompactAppendReturnGate) AppendRecord(m core.AgentMessage) error {
	if err := a.Agent.AppendRecord(m); err != nil {
		return err
	}
	close(a.appended)
	<-a.resume
	return nil
}

type voiceCompactSummary struct{ entered, resume chan struct{} }

func (p *voiceCompactSummary) Stream(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	close(p.entered)
	select {
	case <-p.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return voiceCompactReply("VOICE_COMPACT_RUNTIME_SUMMARY"), nil
}

type voiceCompactOrdinary struct{}

func (voiceCompactOrdinary) Stream(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return voiceCompactReply("done"), nil
}
func voiceCompactReply(text string) <-chan core.AssistantEvent {
	m := core.Message{Role: "assistant", StopReason: "end_turn", Content: []core.Content{core.TextContent(text)}}
	ch := make(chan core.AssistantEvent, 1)
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch
}

func voiceCompactWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal(what)
	}
}
func voiceCompactCount(ms []core.AgentMessage, id string) int {
	n := 0
	for _, m := range ms {
		if m.MsgID == id {
			n++
		}
	}
	return n
}

type voiceCompactPhase struct {
	Phase             string `json:"phase"`
	Agent             int    `json:"agent_records"`
	Tree              int    `json:"tree_display_records"`
	FileEntries       int    `json:"file_entry_records"`
	FileBytesContains bool   `json:"raw_file_contains_minutes"`
	ReopenedDisplay   int    `json:"reopened_tree_display_records"`
	Epoch             int    `json:"agent_epoch"`
}

// A record appended while a foreground compaction summarizes must survive
// its adoption, whatever the order of adoption and recordVoice's Flush.
func TestVoiceRecordSurvivesForegroundCompaction(t *testing.T) {
	for _, order := range []string{"adopt_before_recordVoice_flush", "recordVoice_flush_before_adopt"} {
		t.Run(order, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			summary := &voiceCompactSummary{make(chan struct{}), make(chan struct{})}
			var summaryOnce, appendOnce sync.Once
			releaseSummary := func() { summaryOnce.Do(func() { close(summary.resume) }) }
			real, err := agent.New(agent.AgentConfig{
				Provider: voiceCompactOrdinary{}, Model: core.Model{ID: "local", MaxInput: 4096}, Tools: core.NewRegistry(),
				Compaction:        &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10, TrimDisabled: true},
				CompactSummarizer: func(m core.Model) (core.Provider, core.Model, string) { return summary, m, "" },
			})
			if err != nil {
				t.Fatal(err)
			}
			gated := &voiceCompactAppendReturnGate{Agent: real, appended: make(chan struct{}), resume: make(chan struct{})}
			releaseAppend := func() { appendOnce.Do(func() { close(gated.resume) }) }
			store, err := session.NewFileStore(t.TempDir(), "")
			if err != nil {
				t.Fatal(err)
			}
			persisted := store.Create()
			tree := session.NewTree()
			for _, msg := range []core.Message{
				core.NewUserMessage(strings.Repeat("old", 10000)),
				{Role: "assistant", Content: []core.Content{core.TextContent(strings.Repeat("also-old", 1500))}},
				core.NewUserMessage("recent"),
			} {
				tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(msg)})
			}
			entries, leaf := tree.Snapshot()
			persister := newServePersister(persisted, store, func() (string, string, bool) { return "voice-compact", "manual", false })
			rt, err := bus.NewSessionRuntime(bus.RuntimeConfig{Ctx: ctx, SessionID: persisted.ID, Agent: gated, Persister: persister, InitialEntries: entries, InitialLeafID: leaf})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { releaseAppend(); releaseSummary(); real.Abort(); rt.Close() }()
			sess := &ManagedSession{ID: persisted.ID, runtime: rt, persister: persister}
			manager := &Manager{sessions: map[string]*ManagedSession{sess.ID: sess}}
			if err = rt.Flush(); err != nil {
				t.Fatal(err)
			}
			ended := make(chan bus.CompactionEnded, 2)
			rt.Bus.Subscribe(func(e bus.CompactionEnded) { ended <- e })
			if err = rt.Bus.Execute(bus.CompactSession{SessionID: sess.ID}); err != nil {
				t.Fatal(err)
			}
			voiceCompactWait(t, summary.entered, "foreground summarizer did not start")

			minutesID := core.NewMsgID()
			deliveryID := "voice-compact-call/minutes"
			record := core.AgentMessage{Message: core.Message{MsgID: minutesID, Role: "session_event", Timestamp: time.Now().Unix(), Content: []core.Content{core.TextContent("<voice_minutes>\n" + voiceCompactSentinel + "\n</voice_minutes>")}}, Custom: map[string]any{"source": "voice_call", "kind": "minutes", "call_id": "voice-compact-call", "provider": "local"}}
			voiceDone := make(chan error, 1)
			go func() { voiceDone <- manager.recordVoice(sess.ID, deliveryID, record) }()
			voiceCompactWait(t, gated.appended, "real AppendRecord never completed")
			real.Drain(5 * time.Second)
			rt.Bus.Drain(5 * time.Second)

			var phases []voiceCompactPhase
			phase := func(label string) voiceCompactPhase {
				t.Helper()
				loaded, e := store.LoadReadOnly(sess.ID)
				if e != nil {
					t.Fatal(e)
				}
				raw, e := os.ReadFile(filepath.Join(store.Dir(), sess.ID+".json"))
				if e != nil {
					t.Fatal(e)
				}
				fileCount := 0
				for _, entry := range loaded.Entries {
					if entry.Type == session.EntryMessage && entry.Message.MsgID == minutesID {
						fileCount++
					}
				}
				reopened, e := session.NewTreeFromEntries(loaded.Entries, loaded.LeafID)
				if e != nil {
					t.Fatal(e)
				}
				p := voiceCompactPhase{Phase: label, Agent: voiceCompactCount(real.Messages(), minutesID), Tree: voiceCompactCount(rt.Context().Tree.AllMessages(), minutesID), FileEntries: fileCount, FileBytesContains: strings.Contains(string(raw), voiceCompactSentinel), ReopenedDisplay: voiceCompactCount(reopened.AllMessages(), minutesID), Epoch: real.CompactionEpoch()}
				phases = append(phases, p)
				t.Logf("%s: Agent=%d Tree=%d File=%d raw_has_minutes=%v reopened=%d epoch=%d", label, p.Agent, p.Tree, p.FileEntries, p.FileBytesContains, p.ReopenedDisplay, p.Epoch)
				return p
			}
			p := phase("append_real_done_before_return")
			if p.Agent != 1 || p.Tree != 0 || p.FileEntries != 0 {
				t.Fatalf("unexpected initial visibility: %+v", p)
			}
			awaitVoice := func() {
				t.Helper()
				select {
				case e := <-voiceDone:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("recordVoice did not return")
				}
				t.Log("production recordVoice returned nil after its production Flush")
			}
			awaitAdoption := func() {
				t.Helper()
				select {
				case e := <-ended:
					if e.Err != nil || e.Payload == nil {
						t.Fatalf("compaction payload=%v error=%v", e.Payload, e.Err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("manual compaction did not finish")
				}
				real.Drain(5 * time.Second)
				rt.Bus.Drain(5 * time.Second)
				if real.CompactionEpoch() != 1 {
					t.Fatal("compaction not adopted")
				}
			}
			if order == "adopt_before_recordVoice_flush" {
				releaseSummary()
				awaitAdoption()
				phase("adopted_before_append_returns")
				releaseAppend()
				awaitVoice()
			} else {
				releaseAppend()
				awaitVoice()
				p = phase("recordVoice_flush_done_before_adopt")
				if p.Agent != 1 || p.Tree != 1 || p.FileEntries != 1 || !p.FileBytesContains || p.ReopenedDisplay != 1 {
					t.Fatalf("Flush control did not durably save minutes: %+v", p)
				}
				releaseSummary()
				awaitAdoption()
			}
			real.Drain(5 * time.Second)
			rt.Bus.Drain(5 * time.Second)
			afterVoice := phase("after_recordVoice_and_compaction")
			// A later genuine Flush cannot repair a missing record, nor should it erase
			// one already made durable in the opposite-order control.
			if err = rt.Flush(); err != nil {
				t.Fatal(err)
			}
			final := phase("after_additional_flush")
			if final.Epoch != 1 {
				t.Fatalf("compaction not adopted: %+v", final)
			}
			if afterVoice.Tree != 1 || afterVoice.FileEntries != 1 || !afterVoice.FileBytesContains || afterVoice.ReopenedDisplay != 1 || final.FileEntries != 1 || !final.FileBytesContains || final.ReopenedDisplay != 1 {
				t.Fatalf("real recordVoice returned nil but minutes are not durable/displayable: after=%+v final=%+v", afterVoice, final)
			}
		})
	}
}
