package serve

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
)

type preAbortGate struct {
	bus.AgentController
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (a *preAbortGate) Abort() {
	a.once.Do(func() { close(a.entered); <-a.release })
	a.AgentController.Abort()
}

func seedCompactHistory(t *testing.T, sess *ManagedSession) {
	t.Helper()
	model := sess.runtime.Context().Agent.Model()
	model.MaxInput = 200_000
	if err := sess.runtime.Context().Agent.Reconfigure(nil, model, "", 0); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		for _, role := range []string{"user", "assistant"} {
			msg := core.WrapMessage(core.Message{Role: role, Content: []core.Content{core.TextContent(strings.Repeat("history ", 4000))}})
			msg.EnsureMsgID()
			if err := sess.runtime.Context().Agent.AppendMessage(msg); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Stop cancels the bus run before Agent.Abort, so the old run can settle idle
// while Stop is still pending. A manual compaction admitted in that window
// must not become the target of the old Abort. The gate holds Stop just
// before Agent.Abort; the compaction request is sent concurrently, so a
// correct admission path may wait for Stop to finish.
func TestStopDoesNotAbortANewerManualCompaction(t *testing.T) {
	started := make(chan struct{})
	compactStarted := make(chan struct{})
	compactContexts := make(chan context.Context, 1)
	compactRelease := make(chan struct{})
	var releaseCompact sync.Once
	defer releaseCompact.Do(func() { close(compactRelease) })
	prov := newMockProvider(
		func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent)
			close(started)
			go func() { <-ctx.Done(); close(ch) }()
			return ch, nil
		},
		func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 2)
			compactContexts <- ctx
			close(compactStarted)
			go func() {
				defer close(ch)
				select {
				case <-ctx.Done():
				case <-compactRelease:
					msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("summary")}}
					ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "summary"}
					ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
				}
			}()
			return ch, nil
		},
	)
	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	seedCompactHistory(t, sess)
	abortRelease := make(chan struct{})
	var releaseAbort sync.Once
	defer releaseAbort.Do(func() { close(abortRelease) })
	wrapper := &preAbortGate{AgentController: sess.runtime.Context().Agent, entered: make(chan struct{}), release: abortRelease}
	sess.runtime.Context().Agent = wrapper
	ended := make(chan bus.RunEnded, 2)
	compactEnded := make(chan bus.CompactionEnded, 2)
	unsub := sess.runtime.Bus.Subscribe(func(e bus.RunEnded) { ended <- e })
	defer unsub()
	unsubC := sess.runtime.Bus.Subscribe(func(e bus.CompactionEnded) { compactEnded <- e })
	defer unsubC()
	if _, _, _, err = mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	stopDone := make(chan error, 1)
	go func() {
		resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel-and-recall", "")
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			stopDone <- fmt.Errorf("Stop HTTP status=%d", resp.StatusCode)
			return
		}
		stopDone <- nil
	}()
	select {
	case <-wrapper.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not reach Abort")
	}
	// On the base ordering, the bus context has not been cancelled yet and no
	// new operation can start. Release Stop first in that case.
	settledBeforeAbort := false
	select {
	case e := <-ended:
		settledBeforeAbort = true
		if !e.Cancelled || e.Err != nil {
			t.Fatalf("old RunEnded=%+v", e)
		}
	case <-time.After(time.Second):
		releaseAbort.Do(func() { close(abortRelease) })
		if err := <-stopDone; err != nil {
			t.Fatal(err)
		}
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatal("old run did not end")
		}
	}
	compactReply := make(chan *http.Response, 1)
	go func() {
		compactReply <- apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/command", `{"command":"/compact"}`)
	}()
	startedBeforeStopReturned := false
	select {
	case <-compactStarted:
		startedBeforeStopReturned = true
	case <-time.After(2 * time.Second):
		// A corrected admission path may wait on abortMu here. Let Stop
		// finish before waiting for that compact to start.
	}
	if settledBeforeAbort {
		releaseAbort.Do(func() { close(abortRelease) })
		if err := <-stopDone; err != nil {
			t.Fatal(err)
		}
	}
	if !startedBeforeStopReturned {
		select {
		case <-compactStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("new manual compact did not call provider after Stop")
		}
	}
	resp := <-compactReply
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("compact HTTP status=%d", resp.StatusCode)
	}
	out := decode[CommandResult](t, resp)
	if !out.OK {
		t.Fatalf("compact rejected: %+v", out)
	}
	compactCtx := <-compactContexts
	if err := compactCtx.Err(); err != nil {
		t.Fatalf("Stop for old run cancelled the newer compactor context: settledBeforeAbort=%v err=%v", settledBeforeAbort, err)
	}
	releaseCompact.Do(func() { close(compactRelease) })
	select {
	case e := <-compactEnded:
		if e.Err != nil {
			t.Fatalf("Stop for old run aborted newer manual compaction: settledBeforeAbort=%v err=%v", settledBeforeAbort, e.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manual compact did not settle")
	}
}

// gatedFullAgent is preAbortGate that also forwards the optional interfaces
// bus type-asserts on, so prepare-compact and handoff take their real paths.
type gatedFullAgent struct {
	*preAbortGate
	real *agent.Agent
}

func (a *gatedFullAgent) SendPrepareCompact(ctx context.Context, p string, s *sessioncheckpoint.Slot, x string) ([]core.AgentMessage, error) {
	return a.real.SendPrepareCompact(ctx, p, s, x)
}
func (a *gatedFullAgent) CompactWithCheckpoint(ctx context.Context, c, f string) (*core.CompactionPayload, error) {
	return a.real.CompactWithCheckpoint(ctx, c, f)
}
func (a *gatedFullAgent) SnapshotConversation() ([]core.AgentMessage, int) {
	return a.real.SnapshotConversation()
}
func (a *gatedFullAgent) RestoreConversation(m []core.AgentMessage, e int) error {
	return a.real.RestoreConversation(m, e)
}

type stopAdmissionKind string

const (
	admitPrepare stopAdmissionKind = "prepare"
	admitHandoff stopAdmissionKind = "handoff"
)

// Stop (cancel-and-recall over HTTP) cancels the first run's bus context and
// then parks before the real Agent.Abort. While parked, a /prepare-compact or
// /handoff must not be admitted; once the old Abort is released it must be
// admitted, unaffected by it, and complete.
func TestStopHoldsAdmissionOfPrepareAndHandoff(t *testing.T) {
	for _, kind := range []stopAdmissionKind{admitPrepare, admitHandoff} {
		t.Run(string(kind), func(t *testing.T) { runStopAdmission(t, kind) })
	}
}

func runStopAdmission(t *testing.T, kind stopAdmissionKind) {
	run1Started := make(chan struct{})
	opStarted := make(chan struct{}, 4)
	opGate := make(chan struct{})
	var openGate sync.Once
	defer openGate.Do(func() { close(opGate) })
	var opCtxErr, summaryCtxErr atomic.Value // error observed when the op's provider call ran to completion
	record := func(v *atomic.Value, err error) { v.Store(fmt.Sprint(err)) }

	gated := func(text string, rec *atomic.Value) mockHandler {
		return func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
			opStarted <- struct{}{}
			select {
			case <-opGate:
			case <-ctx.Done():
			}
			record(rec, ctx.Err())
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return simpleResponse(text), nil
		}
	}
	run1 := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent)
		close(run1Started)
		go func() { <-ctx.Done(); close(ch) }()
		return ch, nil
	}
	var prov *mockProvider
	if kind == admitPrepare {
		summary := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
			record(&summaryCtxErr, ctx.Err())
			ch := make(chan core.AssistantEvent, 2)
			msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("summary")}}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "summary"}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			close(ch)
			return ch, nil
		}
		prov = newMockProvider(run1, gated("prepared", &opCtxErr), summary)
	} else {
		prov = newMockProvider(run1, gated("## Goal\nbrief", &opCtxErr))
	}

	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	seedCompactHistory(t, sess)
	real, ok := sess.runtime.Context().Agent.(*agent.Agent)
	if !ok {
		t.Fatalf("agent is %T, want *agent.Agent", sess.runtime.Context().Agent)
	}
	abortRelease := make(chan struct{})
	var releaseAbort sync.Once
	defer releaseAbort.Do(func() { close(abortRelease) })
	wrapper := &gatedFullAgent{preAbortGate: &preAbortGate{AgentController: real, entered: make(chan struct{}), release: abortRelease}, real: real}
	// The wrapper must keep every optional interface, or the op would fail for an unrelated reason.
	var asAgent bus.AgentController = wrapper
	if _, ok := asAgent.(interface {
		SendPrepareCompact(context.Context, string, *sessioncheckpoint.Slot, string) ([]core.AgentMessage, error)
	}); !ok {
		t.Fatal("wrapper hides SendPrepareCompact")
	}
	sess.runtime.Context().Agent = wrapper

	var runStarts, compactStarts atomic.Int32
	runEnded := make(chan bus.RunEnded, 4)
	runStarted2 := make(chan bus.RunStarted, 4)
	compactEnded := make(chan bus.CompactionEnded, 2)
	handoffReady := make(chan bus.HandoffReady, 2)
	handoffSettled := make(chan bus.HandoffSettled, 2)
	b := sess.runtime.Bus
	for _, u := range []func(){
		b.Subscribe(func(e bus.RunStarted) {
			if runStarts.Add(1) >= 2 {
				runStarted2 <- e
			}
		}),
		b.Subscribe(func(e bus.RunEnded) { runEnded <- e }),
		b.Subscribe(func(bus.CompactionStarted) { compactStarts.Add(1) }),
		b.Subscribe(func(e bus.CompactionEnded) { compactEnded <- e }),
		b.Subscribe(func(e bus.HandoffReady) { handoffReady <- e }),
		b.Subscribe(func(e bus.HandoffSettled) { handoffSettled <- e }),
	} {
		defer u()
	}

	if _, _, _, err = mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-run1Started:
	case <-time.After(5 * time.Second):
		t.Fatal("run 1 did not start")
	}
	gen1, err := bus.QueryTyped[bus.GetRunGeneration, uint64](b, bus.GetRunGeneration{})
	if err != nil {
		t.Fatal(err)
	}

	stopDone := make(chan error, 1)
	go func() {
		resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel-and-recall", "")
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			stopDone <- fmt.Errorf("Stop HTTP status=%d", resp.StatusCode)
			return
		}
		stopDone <- nil
	}()
	select {
	case <-wrapper.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not reach the real Agent.Abort")
	}
	// Stop cancelled the bus context first, so run 1 settles while Stop still holds abortMu.
	select {
	case e := <-runEnded:
		if !e.Cancelled || e.Err != nil {
			t.Fatalf("RunEnded1=%+v, want cancelled without error", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunEnded1 never arrived while Stop was parked before Abort")
	}
	pollUntil(t, 5*time.Second, "session idle after RunEnded1", func() bool { return sessState(sess) == StateIdle })

	// New operation, in its own goroutine, through the real /command entry point.
	cmd := "/prepare-compact"
	if kind == admitHandoff {
		cmd = "/handoff"
	}
	opStart := make(chan struct{})
	opDone := make(chan string, 1)
	go func() {
		close(opStart)
		resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/command", fmt.Sprintf(`{"command":%q}`, cmd))
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			opDone <- fmt.Sprintf("HTTP %d", resp.StatusCode)
			return
		}
		out := decode[CommandResult](t, resp)
		if !out.OK {
			opDone <- fmt.Sprintf("rejected: %+v", out)
			return
		}
		opDone <- ""
	}()
	<-opStart

	// While Stop holds abortMu nothing may be admitted.
	held := func(format string, a ...any) {
		t.Errorf("while Stop held abortMu: "+format, a...)
	}
	deadline := time.After(1500 * time.Millisecond)
hold:
	for {
		select {
		case <-deadline:
			break hold
		case <-time.After(20 * time.Millisecond):
			if s := sessState(sess); s != StateIdle {
				held("session state=%v (operation admitted)", s)
				break hold
			}
			if n := runStarts.Load(); n != 1 {
				held("RunStarted count=%d", n)
				break hold
			}
			if g, _ := bus.QueryTyped[bus.GetRunGeneration, uint64](b, bus.GetRunGeneration{}); g != gen1 {
				held("run generation %d -> %d", gen1, g)
				break hold
			}
			if n := prov.calls.Load(); n != 1 {
				held("provider calls=%d (provider 2 reached)", n)
				break hold
			}
			if len(opDone) > 0 {
				held("operation already answered: %q", <-opDone)
				break hold
			}
		}
	}

	// Release the old Abort: Stop returns, then the operation is admitted and completes.
	releaseAbort.Do(func() { close(abortRelease) })
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after its Abort was released (deadlock?)")
	}
	select {
	case <-opStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("operation's provider call never happened after Stop finished")
	}
	select {
	case e := <-runStarted2:
		if e.RunGen <= gen1 {
			t.Errorf("RunStarted2 gen=%d, want > %d", e.RunGen, gen1)
		}
	case <-time.After(5 * time.Second):
		t.Error("RunStarted2 never arrived")
	}
	openGate.Do(func() { close(opGate) })

	wait := func(name string, fn func() bool) {
		t.Helper()
		pollUntil(t, 10*time.Second, name, fn)
	}
	switch kind {
	case admitPrepare:
		select {
		case e := <-compactEnded:
			if e.Err != nil || e.Payload == nil {
				t.Errorf("CompactionEnded err=%v payload=%v", e.Err, e.Payload != nil)
			}
		case <-time.After(10 * time.Second):
			t.Error("CompactionEnded never arrived")
		}
	case admitHandoff:
		var ready, settled bool
		for !ready || !settled {
			select {
			case <-handoffReady:
				ready = true
			case e := <-handoffSettled:
				settled = true
				if e.Cancelled || e.Err != nil {
					t.Errorf("HandoffSettled=%+v, want clean", e)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("handoff events ready=%v settled=%v", ready, settled)
			}
		}
	}
	var ended2 bus.RunEnded
	select {
	case ended2 = <-runEnded:
		if ended2.Cancelled || ended2.Err != nil {
			t.Errorf("RunEnded2=%+v, want not cancelled, no error", ended2)
		}
	case <-time.After(10 * time.Second):
		t.Error("RunEnded2 never arrived")
	}
	select {
	case msg := <-opDone:
		if msg != "" {
			t.Errorf("operation result: %s", msg)
		}
	case <-time.After(10 * time.Second):
		t.Error("operation HTTP call never returned")
	}
	if v, _ := opCtxErr.Load().(string); v != "<nil>" {
		t.Errorf("operation provider ctx err at completion=%q, want <nil> (old Abort reached it)", v)
	}
	if kind == admitPrepare {
		if v, _ := summaryCtxErr.Load().(string); v != "<nil>" {
			t.Errorf("compaction provider ctx err=%q, want <nil>", v)
		}
	}
	wait("session idle at end", func() bool { return sessState(sess) == StateIdle })
	if !sess.runtime.WaitSettled(context.Background()) {
		t.Error("runtime did not settle")
	}
}
