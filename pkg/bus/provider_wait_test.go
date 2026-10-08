package bus

import (
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestProviderExecutionSnapshotCutOrdersUpdateAndClear(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := &SessionContext{SessionID: "wait-session", Bus: b}
	var mu sync.Mutex
	seqByEpoch := map[uint64]uint64{}
	b.SubscribeAllSeq(func(seq uint64, event any) {
		if e, ok := event.(ProviderExecutionChanged); ok {
			mu.Lock()
			seqByEpoch[e.State.Epoch] = seq
			mu.Unlock()
		}
	})
	for epoch := uint64(1); epoch <= 100; epoch++ {
		state := core.ProviderExecution{Generation: 1, Epoch: epoch, Phase: "provider_wait", Model: "A", Provider: "anthropic", Wait: &core.ProviderWait{Kind: "quota_confirmed", Scope: "seven_day", ObservedAt: time.Now(), NextAttemptAt: time.Now().Add(18 * time.Hour)}}
		if epoch%2 == 0 {
			state.Phase = ""
			state.Wait = nil
		}
		done := make(chan struct{})
		go func() {
			bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventProviderExecution, ProviderExecution: &state})
			close(done)
		}()
		streaming, _, cut := sctx.SnapshotInFlightWithCut()
		<-done
		b.Drain(time.Second)
		if streaming.ProviderExecution.Epoch != 0 {
			mu.Lock()
			seq := seqByEpoch[streaming.ProviderExecution.Epoch]
			mu.Unlock()
			if seq == 0 || seq > cut {
				t.Fatalf("snapshot epoch=%d at seq=%d crossed cut=%d", streaming.ProviderExecution.Epoch, seq, cut)
			}
		}
		final, _, finalCut := sctx.SnapshotInFlightWithCut()
		if final.ProviderExecution.Epoch != epoch || final.ProviderExecution.Phase != state.Phase {
			t.Fatalf("lost authoritative state=%+v", final.ProviderExecution)
		}
		mu.Lock()
		seq := seqByEpoch[epoch]
		mu.Unlock()
		if seq > finalCut {
			t.Fatal("published state outside final cut")
		}
	}
}

func TestProviderExecutionEventsAreStructuralIncludingChild(t *testing.T) {
	state := core.ProviderExecution{Generation: 1, Epoch: 2, Phase: "provider_wait"}
	event := ProviderExecutionChanged{State: state}
	if isLossyEvent(event) || isLossyEvent(SubagentEvent{Inner: event}) {
		t.Fatal("wait state is lossy")
	}
	translated := TranslateAgentEvent("child", 0, core.AgentEvent{Type: core.AgentEventProviderExecution, ProviderExecution: &state}, nil)
	if len(translated) != 1 || translated[0].(ProviderExecutionChanged).State.Epoch != 2 {
		t.Fatalf("child translation=%v", translated)
	}
}

func TestProviderExecutionChildSnapshotSharesRootCutAndTerminalCleanup(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := &SessionContext{SessionID: "parent", Bus: b}
	var mu sync.Mutex
	seqByEpoch := map[uint64]uint64{}
	b.SubscribeAllSeq(func(seq uint64, event any) {
		if e, ok := event.(SubagentEvent); ok {
			state := e.Inner.(ProviderExecutionChanged).State
			mu.Lock()
			seqByEpoch[state.Epoch] = seq
			mu.Unlock()
		}
	})
	for epoch := uint64(1); epoch <= 100; epoch++ {
		state := core.ProviderExecution{Generation: 1, Epoch: epoch, Phase: "provider_wait"}
		if epoch%2 == 0 {
			state.Phase = "awaiting_provider"
		}
		done := make(chan struct{})
		go func() {
			sctx.PublishSubagentEvent(SubagentEvent{SessionID: "parent", JobID: "J1", Inner: ProviderExecutionChanged{State: state}})
			close(done)
		}()
		snapshot, _, cut := sctx.SnapshotInFlightWithCut()
		<-done
		b.Drain(time.Second)
		captured := snapshot.SubagentProviderExecutions["J1"]
		if captured.Epoch != 0 {
			mu.Lock()
			seq := seqByEpoch[captured.Epoch]
			mu.Unlock()
			if seq == 0 || seq > cut {
				t.Fatalf("child epoch=%d seq=%d crossed root cut=%d", captured.Epoch, seq, cut)
			}
		}
		latest, _, latestCut := sctx.SnapshotInFlightWithCut()
		if latest.SubagentProviderExecutions["J1"].Epoch != epoch {
			t.Fatal("missing child snapshot")
		}
		mu.Lock()
		seq := seqByEpoch[epoch]
		mu.Unlock()
		if seq > latestCut {
			t.Fatal("child update outside root cut")
		}
		if snapshot.SubagentProviderExecutions["J1"].Epoch != captured.Epoch {
			t.Fatal("snapshot map mutated after capture")
		}
	}
	sctx.PublishSubagentEnded(SubagentEnded{SessionID: "parent", JobID: "J1", Status: "completed"})
	terminal, _, cut := sctx.SnapshotInFlightWithCut()
	if len(terminal.SubagentProviderExecutions) != 0 || cut != b.CaptureSeq() {
		t.Fatal("terminal child projection leaked")
	}
}
