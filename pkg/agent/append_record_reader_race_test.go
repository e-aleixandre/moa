package agent

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// AppendRecord may land while the loop prepares a request: a voice call that
// ends records its minutes while the owner's run estimates and converts its
// history. Run under -race. The gate parks the loop in the ephemeral context
// callback, which runs right before the compaction estimate reads the history;
// the record is appended while the loop sleeps there, after the release, so no
// channel orders the write before the loop's reads: only the state lock may.
// Both shapes of append are covered: into the backing array's spare capacity,
// and through a reallocation.
func TestAppendRecordWhileLoopReadsHistory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spare int // free slots left after Send appends the prompt
	}{
		{"spare_capacity", 2},
		{"realloc", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan int, 1)
			release := make(chan struct{})
			first := true
			var ag *Agent
			ag, err := New(AgentConfig{
				Provider:     &alwaysProvider{text: "done"},
				Model:        core.Model{ID: "test-model", Provider: "mock", MaxInput: 200_000},
				SystemPrompt: "You are a test agent.",
				Tools:        core.NewRegistry(),
				MaxTurns:     10,
				Compaction:   &core.CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecent: 1000},
				EphemeralContext: func() *core.Message {
					if !first {
						return nil
					}
					first = false
					ag.mu.Lock()
					free := cap(ag.state.Messages) - len(ag.state.Messages)
					ag.mu.Unlock()
					entered <- free
					<-release
					// Not a synchronization: it only lets the record land
					// before the reads that follow this callback.
					time.Sleep(50 * time.Millisecond)
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			const n = 4
			hist := make([]core.AgentMessage, 0, n+1+tc.spare)
			for i := range n / 2 {
				_ = i
				hist = append(hist,
					core.WrapMessage(core.NewUserMessage("earlier question")),
					core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("earlier answer")}, StopReason: "end_turn"}),
				)
			}
			if err := ag.LoadMessages(hist); err != nil {
				t.Fatal(err)
			}

			ends := make(chan []core.AgentMessage, 1)
			unsub := ag.Subscribe(func(e core.AgentEvent) {
				if e.Type == core.AgentEventEnd {
					ends <- e.Messages
				}
			})
			defer unsub()

			done := make(chan error, 1)
			go func() {
				_, err := ag.Send(context.Background(), "hello")
				done <- err
			}()

			var free int
			select {
			case free = <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("loop never reached the gate")
			}
			if free != tc.spare {
				t.Fatalf("free capacity at the gate = %d, want %d", free, tc.spare)
			}
			close(release)
			record := core.WrapMessage(core.Message{Role: "session_event", Content: []core.Content{core.TextContent("voice minutes")}})
			record.EnsureMsgID()
			if err := ag.AppendRecord(record); err != nil {
				t.Fatal(err)
			}

			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("run did not finish")
			}

			countRecord := func(where string, msgs []core.AgentMessage) {
				t.Helper()
				got := 0
				for _, m := range msgs {
					if m.MsgID == record.MsgID {
						got++
					}
				}
				if got != 1 {
					t.Errorf("%s: record appears %d times, want 1", where, got)
				}
			}
			countRecord("Messages", ag.Messages())
			select {
			case msgs := <-ends:
				countRecord("agent_end", msgs)
			case <-time.After(5 * time.Second):
				t.Fatal("no agent_end")
			}
		})
	}
}
