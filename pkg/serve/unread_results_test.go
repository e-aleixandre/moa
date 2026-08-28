package serve

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/e-aleixandre/moa/pkg/bus"
)

func TestUnreadAttentionAcknowledgementPreservesNewerAttentionAcrossReadRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "first"})
	pollUntil(t, time.Second, "first attention", func() bool { return mgr.sessionInfo(sess).Unseen })
	first := mgr.sessionInfo(sess).UnseenSeq
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "second"})
	pollUntil(t, time.Second, "second attention", func() bool { return mgr.sessionInfo(sess).UnseenSeq > first })
	second := mgr.sessionInfo(sess).UnseenSeq

	if err := mgr.MarkSessionRead(sess.ID, first, sess.attentionNamespace); err != nil {
		t.Fatal(err)
	}
	info := mgr.sessionInfo(sess)
	if !info.Unseen || info.UnseenSeq != second {
		t.Fatalf("old acknowledgement left attention = (unseen:%v seq:%d), want (true, %d)", info.Unseen, info.UnseenSeq, second)
	}
}

func TestSnapshotCutExcludesLaterAttention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "at-cut"})
	pollUntil(t, time.Second, "cut attention", func() bool { return mgr.sessionInfo(sess).Unseen })
	_, _, cut := sess.runtime.Context().SnapshotInFlightWithCut()
	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "after-cut"})
	pollUntil(t, time.Second, "later attention", func() bool { return mgr.sessionInfo(sess).UnseenSeq > cut })

	if err := mgr.MarkSessionRead(sess.ID, cut, sess.attentionNamespace); err != nil {
		t.Fatal(err)
	}
	if !mgr.sessionInfo(sess).Unseen {
		t.Fatal("init cut acknowledgement cleared attention published after the cut")
	}
}

func TestReadThroughBeforeTrackerSuppressesOccurrence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	processed := make(chan struct{})
	mgr.beforeAttentionMark = func() { close(entered); <-release }
	mgr.afterAttentionMark = func() { close(processed) }
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "delayed"})
	<-entered
	if err := mgr.MarkSessionRead(sess.ID, sess.runtime.Bus.LastSeq(), sess.attentionNamespace); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-processed
	if info := mgr.sessionInfo(sess); info.Unseen || info.UnseenSeq != 0 {
		t.Fatalf("attention after prior read = (unseen:%v seq:%d), want cleared", info.Unseen, info.UnseenSeq)
	}
}

func TestReadThroughRejectsFutureSequenceWithoutSuppressingAttention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.MarkSessionRead(sess.ID, 1, sess.attentionNamespace); !errors.Is(err, ErrInvalidAttentionCursor) {
		t.Fatalf("future read error = %v, want %v", err, ErrInvalidAttentionCursor)
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "after-future-read"})
	pollUntil(t, time.Second, "attention after rejected read", func() bool { return mgr.sessionInfo(sess).Unseen })
}

func TestReadThroughZeroDoesNotClearAttention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p1"})
	pollUntil(t, time.Second, "attention", func() bool { return mgr.sessionInfo(sess).Unseen })
	if err := mgr.MarkSessionRead(sess.ID, 0, sess.attentionNamespace); err != nil {
		t.Fatal(err)
	}
	if !mgr.sessionInfo(sess).Unseen {
		t.Fatal("zero cursor cleared attention")
	}
}

func TestReadThroughNamespaceRejectsStaleRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p1"})
	pollUntil(t, time.Second, "attention", func() bool { return mgr.sessionInfo(sess).Unseen })
	if err := mgr.MarkSessionRead(sess.ID, sess.runtime.Bus.LastSeq(), "old-namespace"); !errors.Is(err, ErrStaleAttentionNamespace) {
		t.Fatalf("stale namespace error = %v, want %v", err, ErrStaleAttentionNamespace)
	}
	if !mgr.sessionInfo(sess).Unseen {
		t.Fatal("stale namespace cleared attention")
	}
}

func TestAttentionEventClassifier(t *testing.T) {
	cases := []struct {
		name  string
		event any
		want  bool
	}{
		{"permission", bus.PermissionRequested{}, true},
		{"ask", bus.AskUserRequested{}, true},
		{"error state", bus.StateChanged{State: string(bus.StateError)}, true},
		{"non-error state", bus.StateChanged{State: string(bus.StateRunning)}, false},
		{"completion", bus.RunEnded{}, true},
		{"cancelled", bus.RunEnded{Cancelled: true}, false},
		{"failed run end", bus.RunEnded{Err: errors.New("boom")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := attentionEvent(tc.event); got != tc.want {
				t.Fatalf("attentionEvent(%T) = %v, want %v", tc.event, got, tc.want)
			}
		})
	}
}

func TestAttentionTrackerProcessesBurstLosslessly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	const count = 200
	var once sync.Once
	entered := make(chan struct{})
	release := make(chan struct{})
	mgr.beforeAttentionMark = func() {
		once.Do(func() { close(entered); <-release })
	}
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID})
	<-entered
	for i := 1; i < count; i++ {
		sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID})
	}
	want := sess.runtime.Bus.LastSeq()
	close(release)
	pollUntil(t, time.Second, "lossless attention burst", func() bool {
		return mgr.sessionInfo(sess).UnseenSeq == want
	})
}

func TestUnreadAttentionMarksAwayPermissionAskAndError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	sess, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}

	mark := func(event any) uint64 {
		before := mgr.sessionInfo(sess).UnseenSeq
		sess.runtime.Bus.Publish(event)
		pollUntil(t, time.Second, "unseen attention", func() bool {
			info := mgr.sessionInfo(sess)
			return info.Unseen && info.UnseenSeq > before
		})
		return mgr.sessionInfo(sess).UnseenSeq
	}

	permissionSeq := mark(bus.PermissionRequested{SessionID: sess.ID, RunGen: 1, ID: "p1", ToolName: "bash"})
	if err := mgr.MarkSessionRead(sess.ID, permissionSeq, sess.attentionNamespace); err != nil {
		t.Fatal(err)
	}
	// Completion and error later in that SAME run are distinct occurrences, not
	// suppressed by reading its earlier permission.
	completionSeq := mark(bus.RunEnded{SessionID: sess.ID, RunGen: 1})
	if completionSeq <= permissionSeq {
		t.Fatalf("completion seq %d did not follow permission %d", completionSeq, permissionSeq)
	}
	if err := mgr.MarkSessionRead(sess.ID, completionSeq, sess.attentionNamespace); err != nil {
		t.Fatal(err)
	}
	errorSeq := mark(bus.StateChanged{SessionID: sess.ID, State: string(bus.StateError), Error: "boom"})
	// The terminal RunEnded for an error is NOT a second occurrence: the error
	// state change already owns it, so the cursor must not advance again.
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 1, Err: errors.New("boom")})
	sess.runtime.Bus.Drain(time.Second)
	if got := mgr.sessionInfo(sess).UnseenSeq; got != errorSeq {
		t.Fatalf("failed run end created a second occurrence: seq %d, want %d", got, errorSeq)
	}
	// A cancelled run is not an occurrence either.
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 2, Cancelled: true})
	sess.runtime.Bus.Drain(time.Second)
	if got := mgr.sessionInfo(sess).UnseenSeq; got != errorSeq {
		t.Fatalf("cancelled run end created an occurrence: seq %d, want %d", got, errorSeq)
	}
}

func TestReadThroughHoldsRuntimeIdentityAcrossResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	sess, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.TextDelta{SessionID: sess.ID, Delta: "x"})

	entered := make(chan struct{})
	release := make(chan struct{})
	closeBlocked := make(chan struct{})
	resumed := make(chan *ManagedSession, 1)
	resumeErr := make(chan error, 1)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	mgr.beforeReadThroughAdvance = func() {
		close(entered)
		<-release
	}
	mgr.attentionRuntimeDeactivateBlocked = func() { close(closeBlocked) }
	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.MarkSessionRead(sess.ID, sess.runtime.Bus.LastSeq(), sess.attentionNamespace)
	}()
	<-entered
	go func() {
		if err := mgr.CloseSession(sess.ID); err != nil {
			resumeErr <- err
			return
		}
		fresh, err := mgr.ResumeSession(sess.ID)
		if err != nil {
			resumeErr <- err
			return
		}
		resumed <- fresh
	}()
	// Close has tried and failed to take the attention-state lock while the
	// acknowledgement holds it. Releasing the acknowledgement must complete its
	// cursor advance before Close can deactivate this runtime and resume its
	// replacement.
	select {
	case <-closeBlocked:
	case <-time.After(time.Second):
		t.Fatal("Close did not block on the attention-state lock")
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	var fresh *ManagedSession
	select {
	case err := <-resumeErr:
		t.Fatal(err)
	case fresh = <-resumed:
	}
	mgr.beforeReadThroughAdvance = nil
	processed := make(chan struct{})
	mgr.afterAttentionMark = func() { close(processed) }
	fresh.runtime.Bus.Publish(bus.PermissionRequested{SessionID: fresh.ID, ID: "after-resume"})
	<-processed
	if !mgr.sessionInfo(fresh).Unseen {
		t.Fatal("the old runtime's cursor suppressed attention on its replacement")
	}
}

func TestReadThroughResetsForResumedRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	sess, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	oldProcessed := make(chan struct{})
	mgr.afterAttentionMark = func() { close(oldProcessed) }
	for i := 0; i < 20; i++ {
		sess.runtime.Bus.Publish(bus.TextDelta{SessionID: sess.ID, Delta: "x"})
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "old-runtime"})
	<-oldProcessed
	if err := mgr.MarkSessionRead(sess.ID, sess.runtime.Bus.LastSeq(), sess.attentionNamespace); err != nil {
		t.Fatal(err)
	}
	oldNamespace := sess.attentionNamespace
	oldCursor := sess.runtime.Bus.LastSeq()
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.attentionNamespace == oldNamespace {
		t.Fatalf("resumed namespace = %q, want a new incarnation", fresh.attentionNamespace)
	}
	newProcessed := make(chan struct{})
	mgr.afterAttentionMark = func() { close(newProcessed) }
	fresh.runtime.Bus.Publish(bus.PermissionRequested{SessionID: fresh.ID, ID: "new-runtime"})
	<-newProcessed
	if info := mgr.sessionInfo(fresh); !info.Unseen || info.UnseenSeq == 0 || info.UnseenSeq >= oldCursor {
		t.Fatalf("first resumed attention = (unseen:%v seq:%d), want new low seq below old cursor %d", info.Unseen, info.UnseenSeq, oldCursor)
	}
}

func TestInitUsesAuthoritativeRunStartWhenCacheClockIsDelayed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(time.Second, "done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "running", func() bool {
		return sess.runtime.State.Current() == bus.StateRunning
	})
	// This is the old race: RunStarted is already authoritative but the
	// asynchronous cache-clock reactor has not populated its serve-side copy.
	sess.mu.Lock()
	sess.runStartedAt = time.Time{}
	sess.mu.Unlock()
	streaming, liveTools, _ := sess.runtime.Context().SnapshotInFlightWithCut()
	init := buildInitData(sess, streaming, liveTools, "")
	if init.RunStartedAtMs == 0 {
		t.Fatal("running init omitted authoritative run-start anchor")
	}
}

// A WebSocket subscription is not a presentation. The client decides what is
// on screen, so an open socket must neither clear the unread marker nor
// suppress a push: push suppression is the separate wsConns gate in
// subscribePush, and it stays live for the whole connection.
func TestSubscribedSessionIsNotSeenAndStillPushes(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	pushes := make(chan struct{}, 4)
	sess, err := mgr.CreateSession(CreateOpts{Title: "subscribed"})
	if err != nil {
		t.Fatal(err)
	}
	sess.pushUnsubs = append(sess.pushUnsubs,
		sess.runtime.Bus.Subscribe(func(bus.PermissionRequested) {
			// Mirrors subscribePush's always-notify policy for blocking events,
			// which is deliberately independent of wsConns.
			pushes <- struct{}{}
		}),
	)

	ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
	var evt Event
	if err := wsjson.Read(ctx, conn, &evt); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "live viewer registered", func() bool { return sess.wsConns.Load() == 1 })

	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "subscribed-perm", RunGen: 1})
	pollUntil(t, time.Second, "unseen attention", func() bool { return mgr.sessionInfo(sess).Unseen })

	select {
	case <-pushes:
	case <-time.After(time.Second):
		t.Fatal("a blocking event on a merely subscribed session did not notify")
	}
	if !mgr.sessionInfo(sess).Unseen {
		t.Fatal("an open subscription marked the session seen without a client acknowledgement")
	}
}
