package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// A call that ends while the session's run prepares a request records its
// minutes through the real recordVoice. Run under -race: the loop must read
// its history consistently with the record, and the record must be saved
// exactly once in the agent's history, the tree and the file, both while the
// run is still in flight and after it ends.
func TestVoiceRecordDuringRunIsSavedOnce(t *testing.T) {
	const sentinel = "VOICE_RECORD_DURING_RUN_MINUTES"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	first := true
	real, err := agent.New(agent.AgentConfig{
		Provider: voiceCompactOrdinary{}, Model: core.Model{ID: "local", MaxInput: 200_000}, Tools: core.NewRegistry(),
		Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10},
		// Parks the run right before it reads its history to build a
		// request. Released before the record lands; the sleep only lets the
		// record land before those reads, it orders nothing.
		ForYouSnapshot: func() (string, bool) {
			if first {
				first = false
				close(entered)
				<-release
				time.Sleep(50 * time.Millisecond)
			}
			return "", true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	persisted := store.Create()
	persister := newServePersister(persisted, store, func() (string, string, bool) { return "voice-during-run", "manual", false })
	rt, err := bus.NewSessionRuntime(bus.RuntimeConfig{Ctx: ctx, SessionID: persisted.ID, Agent: real, Persister: persister})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { real.Abort(); rt.Close() }()
	sess := &ManagedSession{ID: persisted.ID, runtime: rt, persister: persister}
	manager := &Manager{sessions: map[string]*ManagedSession{sess.ID: sess}}
	ended := make(chan bus.RunEnded, 1)
	rt.Bus.Subscribe(func(e bus.RunEnded) { ended <- e })

	if err := rt.Bus.Execute(bus.SendPrompt{SessionID: sess.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	voiceCompactWait(t, entered, "run never reached the request")
	close(release)

	minutesID := core.NewMsgID()
	record := core.AgentMessage{Message: core.Message{MsgID: minutesID, Role: "session_event", Timestamp: time.Now().Unix(), Content: []core.Content{core.TextContent("<voice_minutes>\n" + sentinel + "\n</voice_minutes>")}}, Custom: map[string]any{"source": "voice_call", "kind": "minutes", "call_id": "voice-during-run", "provider": "local"}}
	if err := manager.recordVoice(sess.ID, "voice-during-run/minutes", record); err != nil {
		t.Fatal(err)
	}

	check := func(label string) {
		t.Helper()
		history, err := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](rt.Bus, bus.GetMessages{})
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := store.LoadReadOnly(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		file := 0
		for _, e := range loaded.Entries {
			if e.Type == session.EntryMessage && e.Message.MsgID == minutesID {
				file++
			}
		}
		raw, err := os.ReadFile(filepath.Join(store.Dir(), sess.ID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		got := [4]int{
			voiceCompactCount(history, minutesID),
			voiceCompactCount(rt.Context().Tree.AllMessages(), minutesID),
			file,
			strings.Count(string(raw), sentinel),
		}
		if got != [4]int{1, 1, 1, 1} {
			t.Fatalf("%s: history/tree/file entries/raw file = %v, want each 1", label, got)
		}
	}
	check("after recordVoice")

	select {
	case e := <-ended:
		if e.Err != nil {
			t.Fatalf("run failed: %v", e.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not end")
	}
	if err := rt.Flush(); err != nil {
		t.Fatal(err)
	}
	check("after the run and Flush")
}
