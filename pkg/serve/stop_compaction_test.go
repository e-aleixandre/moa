package serve

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
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
