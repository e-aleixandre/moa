package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// AppendRecord is allowed while a run is in flight, so it can land while a
// stopped run reads its history to finish (the interruption marker, the
// returned snapshot, agent_end). Run under -race: every one of those reads
// must be ordered with the writer by the agent's mutex.
func TestAppendRecordWhileStoppedRunFinishes(t *testing.T) {
	for i := range 20 {
		ag := newTestAgent(&ctxBlockingProvider{})
		ends := make(chan []core.AgentMessage, 1)
		unsub := ag.Subscribe(func(e core.AgentEvent) {
			if e.Type == core.AgentEventEnd {
				ends <- e.Messages
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = ag.Send(ctx, "hold")
		}()
		deadline := time.Now().Add(2 * time.Second)
		for !ag.IsRunning() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
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
				msg := core.WrapMessage(core.Message{Role: "session_event", Content: []core.Content{core.TextContent("stop record")}})
				if err := ag.AppendRecord(msg); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		cancel()
		<-done
		wg.Wait()
		unsub()
		select {
		case msgs := <-ends:
			for range msgs { // the event's history must be safe to read
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("run %d: no agent_end", i)
		}
	}
}
