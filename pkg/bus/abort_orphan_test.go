package bus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/secretaction"
)

func TestAbortOrphanRecoveryPreservesBusyOwner(t *testing.T) {
	for _, owner := range []string{"admitted run", "subagent", "bash", "auto verify", "goal verify", "permission", "sync question", "sync question behind async", "secret"} {
		t.Run(owner, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			sctx := newTestSessionContextWithState(b, &fakeAgent{})
			sctx.Approvals = NewApprovalManager(b, sctx.State, sctx.SessionID)
			RegisterHandlers(sctx)
			if err := sctx.State.Transition(StateRunning); err != nil {
				t.Fatal(err)
			}
			if err := sctx.State.Transition(StatePermission); err != nil {
				t.Fatal(err)
			}
			switch owner {
			case "admitted run":
				// Includes the terminal-publication window after Agent.Send returns.
				sctx.markRunAdmitted(1)
			case "subagent":
				sctx.trackBackgroundEvent(SubagentStarted{JobID: "child"})
			case "bash":
				sctx.trackBackgroundEvent(BashJobStarted{JobID: "bash"})
			case "auto verify":
				sctx.autoVerifyRunning = 1
			case "goal verify":
				sctx.goalVerifyRunning = 1
			case "permission":
				sctx.Approvals.perms["p"] = &PendingPermission{ID: "p"}
			case "sync question", "sync question behind async":
				sctx.Approvals.asks["sync"] = &PendingAsk{ID: "sync", RequestedAt: time.Now()}
				if owner == "sync question behind async" {
					sctx.Approvals.AddAsync(PendingAskInfo{ID: "async", RequestedAt: time.Unix(1, 0)})
				}
			case "secret":
				broker := secretaction.NewBroker(sctx.SessionID)
				sctx.SecretBroker = broker
				requested := make(chan struct{})
				broker.SetHooks(func(secretaction.Request) { close(requested) }, nil)
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = broker.Request(ctx, secretaction.Request{Kind: secretaction.KindCommand})
				}()
				defer func() { cancel(); <-done }()
				select {
				case <-requested:
				case <-time.After(time.Second):
					t.Fatal("secret request not published")
				}
			}
			if err := b.Execute(AbortRun{}); err != nil {
				t.Fatal(err)
			}
			if got := sctx.State.Current(); got != StatePermission {
				t.Fatalf("Stop marked %s idle before its owner settled: %s", owner, got)
			}
		})
	}
}

func TestAbortOrphanRecoveryPreservesAsyncQuestion(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContextWithState(b, &fakeAgent{})
	sctx.Approvals = NewApprovalManager(b, sctx.State, sctx.SessionID)
	sctx.Approvals.AddAsync(PendingAskInfo{ID: "async"})
	RegisterHandlers(sctx)
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := b.Execute(AbortRun{}); err != nil {
		t.Fatal(err)
	}
	if got := sctx.State.Current(); got != StateIdle {
		t.Fatalf("orphan with an async question did not recover: %s", got)
	}
	if asks := sctx.Approvals.AsyncAsks(); len(asks) != 1 || asks[0].ID != "async" {
		t.Fatalf("Stop changed the durable question projection: %+v", asks)
	}
}

func TestAbortOrphanRecoveryRejectsStaleGeneration(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	sctx.RunGenAtomic.Store(2)
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := b.Execute(AbortAndRecall{RunGen: 1}); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("stale Stop = %v, want busy", err)
	}
	if sctx.State.Current() != StateRunning || fa.wasAborted() {
		t.Fatal("stale Stop changed the newer generation")
	}
}

func TestAbortOrphanRecoveryWaitsForBeforeFirstRun(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent: fas,
		BeforeFirstRun: func(ctx context.Context) error {
			close(entered)
			<-release
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if !rt.WaitSettled(ctx) {
			t.Error("cancelled first-run gate did not settle after release")
		}
		rt.Close()
	})
	if err := rt.Bus.Execute(SendPrompt{Text: "start"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first-run gate not entered")
	}
	if rt.Context().Agent.IsRunning() {
		t.Fatal("test requires an admitted run not yet inside Agent.Send")
	}
	if err := rt.Bus.Execute(AbortRun{}); err != nil {
		t.Fatal(err)
	}
	if got := rt.State.Current(); got != StateRunning {
		t.Fatalf("Stop prematurely settled the admitted run: %s", got)
	}
}

func TestPermissionRunPanicSettlesError(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContextWithState(b, &fakeAgent{})
	ended := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { ended <- e })
	if err := reserveRunSlot(sctx); err != nil {
		t.Fatal(err)
	}
	launchRun(sctx, "panic with a pending child", func(context.Context) ([]core.AgentMessage, error) {
		if err := sctx.State.Transition(StatePermission); err != nil {
			return nil, err
		}
		panic("provider panic")
	})
	select {
	case e := <-ended:
		if e.Err == nil || sctx.State.Current() != StateError {
			t.Fatalf("panic failed to settle permission: state=%s err=%v", sctx.State.Current(), e.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("panicked run did not publish its outcome")
	}
}

func TestAbortOrphanRecoveryWaitsForManualVerify(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".moa"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"checks":[{"name":"gate","command":"touch verify-started; while [ ! -f verify-release ]; do sleep 0.01; done","timeout":"10s"}]}`
	if err := os.WriteFile(filepath.Join(dir, ".moa", "verify.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewLocalBus()
	t.Cleanup(b.Close)
	sctx := newTestSessionContextWithState(b, &fakeAgent{})
	sctx.CWD = dir
	RegisterHandlers(sctx)
	done := make(chan error, 1)
	go func() { done <- b.Execute(RunManualVerify{}) }()
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(dir, "verify-release"), nil, 0o644); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("manual verify did not finish cleanly: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("manual verify did not settle after release")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "verify-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manual verify did not reach its gate")
		}
		time.Sleep(time.Millisecond)
	}
	if err := b.Execute(AbortRun{}); err != nil {
		t.Fatal(err)
	}
	if got := sctx.State.Current(); got != StateRunning {
		t.Errorf("Stop settled a manual verify still executing: %s", got)
	}
	rt := &SessionRuntime{sctx: sctx, State: sctx.State}
	if rt.AdmitCloseIfQuiescent(func() {}) {
		t.Error("close admitted while manual verify still executing")
	}
	if err := b.Execute(SendPrompt{Text: "must wait for verify"}); err == nil {
		t.Error("new run admitted while manual verify still executing")
	}
}

func TestAbortOrphanRecoveryWaitsForCompactionTail(t *testing.T) {
	b := NewLocalBus()
	t.Cleanup(b.Close)
	fa := &fakeAgent{compactPayload: &core.CompactionPayload{}}
	sctx := newTestSessionContextWithState(b, fa)
	entered, release := make(chan struct{}), make(chan struct{})
	sctx.ReloadPrompt = func() []string { close(entered); <-release; return nil }
	RegisterHandlers(sctx)
	ended := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { ended <- e })
	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Error("compaction did not settle after release")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("compaction did not reach its prompt-reload tail")
	}
	if !sctx.Compacting() || sctx.Agent.IsRunning() {
		t.Fatal("test requires compaction settling outside the agent run slot")
	}
	if err := b.Execute(AbortRun{}); err != nil {
		t.Fatal(err)
	}
	if got := sctx.State.Current(); got != StateRunning {
		t.Errorf("Stop settled the compaction tail prematurely: %s", got)
	}
	rt := &SessionRuntime{sctx: sctx, State: sctx.State}
	if rt.AdmitCloseIfQuiescent(func() {}) {
		t.Error("close admitted while compaction tail still executing")
	}
}
