package serve

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
)

type gatedFenceSubscription struct {
	bus.SeqFenceSubscription
	markerQueued chan struct{}
	release      chan struct{}
	once         sync.Once
}

// orderedFenceSubscription holds fence placement itself (rather than merely
// fence completion) to reproduce two snapshots racing on either side of a
// dropped occurrence.
type orderedFenceSubscription struct {
	bus.SeqFenceSubscription
	firstEntered  chan struct{}
	firstRelease  chan struct{}
	secondEntered chan struct{}
	secondRelease chan struct{}
	calls         atomic.Int32
}

func (s *orderedFenceSubscription) FenceBefore(deadline time.Time) (<-chan struct{}, uint64, bool) {
	switch s.calls.Add(1) {
	case 1:
		close(s.firstEntered)
		if !waitForFenceRelease(s.firstRelease, deadline) {
			return nil, 0, false
		}
	case 2:
		close(s.secondEntered)
		if !waitForFenceRelease(s.secondRelease, deadline) {
			return nil, 0, false
		}
	}
	return s.SeqFenceSubscription.FenceBefore(deadline)
}

func waitForFenceRelease(release <-chan struct{}, deadline time.Time) bool {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-release:
		return time.Now().Before(deadline)
	case <-timer.C:
		return false
	}
}

func (s *gatedFenceSubscription) FenceBefore(deadline time.Time) (<-chan struct{}, uint64, bool) {
	done, version, ok := s.SeqFenceSubscription.FenceBefore(deadline)
	if !ok {
		return nil, 0, false
	}
	s.once.Do(func() { close(s.markerQueued) })
	gated := make(chan struct{})
	go func() {
		<-done
		<-s.release
		close(gated)
	}()
	return gated, version, true
}

type deferredClearSubscription struct {
	bus.SeqFenceSubscription
	clearAttempted chan struct{}
	clearStarted   chan struct{}
	clearRelease   chan struct{}
	clearComplete  chan struct{}
	once           sync.Once
	startedOnce    sync.Once
	completeOnce   sync.Once
}

func (s *deferredClearSubscription) ClearOverflowThroughBefore(_ uint64, deadline time.Time) bool {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	<-timer.C
	s.once.Do(func() { close(s.clearAttempted) })
	return false
}

func (s *deferredClearSubscription) ClearOverflowThrough(version uint64) {
	<-s.clearRelease
	s.SeqFenceSubscription.ClearOverflowThrough(version)
	s.completeOnce.Do(func() { close(s.clearComplete) })
}

func (s *deferredClearSubscription) ClearOverflowThroughContext(ctx context.Context, version uint64) bool {
	s.startedOnce.Do(func() {
		if s.clearStarted != nil {
			close(s.clearStarted)
		}
	})
	select {
	case <-s.clearRelease:
		s.SeqFenceSubscription.ClearOverflowThrough(version)
		s.completeOnce.Do(func() { close(s.clearComplete) })
		return true
	case <-ctx.Done():
		return false
	case <-s.Done():
		return false
	}
}

func TestInitialAttentionCutBindsOnlyPristineEmptyBus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, cut, overflow, cleared, initial, captured := sess.runtime.Context().SnapshotInFlightWithAttentionCut(sess.attentionSeqSub)
	if !captured || cut != 0 || !initial {
		t.Fatalf("fresh capture = (cut:%d initial:%v captured:%v), want (0, true, true)", cut, initial, captured)
	}
	if gen, ok := mgr.attentionGenerationAtCutWithOverflowBefore(ctx, sess, cut, overflow, cleared, initial, time.Now().Add(time.Second)); !ok || gen != 0 {
		t.Fatalf("fresh empty bound = (%d, %v), want (0, true)", gen, ok)
	}
	if _, ok := mgr.attentionGenerationAtCutWithOverflowBefore(ctx, sess, 0, 0, 0, false, time.Now().Add(time.Second)); ok {
		t.Fatal("ambiguous zero cut supplied an attention bound")
	}
}

func TestUnreadResultIsProcessLocalAndClearedOnRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	sess, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "unread completion", func() bool {
		return mgr.sessionInfo(sess).Unseen
	})
	if err := mgr.MarkSessionRead(sess.ID, 0); err != nil {
		t.Fatal(err)
	}
	if mgr.sessionInfo(sess).Unseen {
		t.Fatal("read result remained unseen")
	}
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

	mark := func(gen uint64, event any) uint64 {
		before := mgr.sessionInfo(sess).UnseenGen
		sess.runtime.Context().RunGenAtomic.Store(gen)
		sess.runtime.Bus.Publish(event)
		pollUntil(t, time.Second, "unseen attention", func() bool {
			info := mgr.sessionInfo(sess)
			return info.Unseen && info.UnseenGen > before
		})
		return mgr.sessionInfo(sess).UnseenGen
	}

	permissionGen := mark(1, bus.PermissionRequested{SessionID: sess.ID, RunGen: 1, ID: "p1", ToolName: "bash"})
	if err := mgr.MarkSessionRead(sess.ID, permissionGen); err != nil {
		t.Fatal(err)
	}
	// Completion and error later in that SAME run are distinct attention
	// occurrences, not suppressed by reading its earlier permission.
	completionGen := mark(1, bus.RunEnded{SessionID: sess.ID, RunGen: 1})
	if completionGen <= permissionGen {
		t.Fatalf("completion generation %d did not follow permission %d", completionGen, permissionGen)
	}
	if err := mgr.MarkSessionRead(sess.ID, completionGen); err != nil {
		t.Fatal(err)
	}
	mark(1, bus.StateChanged{SessionID: sess.ID, State: string(bus.StateError), Error: "boom"})
	// The terminal RunEnded for an error carries the same occurrence, not a
	// second unread transition.
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 1, Err: errors.New("boom")})
	sess.runtime.Bus.Drain(time.Second)
}

func TestUnreadAttentionAcknowledgementPreservesNewerAttentionAcrossReadRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	sess, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}

	// An acknowledgement for an old occurrence cannot clear a later one.
	sess.runtime.Context().RunGenAtomic.Store(2)
	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, RunGen: 2, ID: "a1"})
	pollUntil(t, time.Second, "first unseen ask", func() bool { return mgr.sessionInfo(sess).Unseen })
	firstGen := mgr.sessionInfo(sess).UnseenGen
	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, RunGen: 2, ID: "a2"})
	pollUntil(t, time.Second, "second unseen ask", func() bool { return mgr.sessionInfo(sess).UnseenGen > firstGen })
	askGen := mgr.sessionInfo(sess).UnseenGen
	if err := mgr.MarkSessionRead(sess.ID, firstGen); err != nil {
		t.Fatal(err)
	}
	if !mgr.sessionInfo(sess).Unseen {
		t.Fatal("stale read cleared newer attention")
	}

	// A newly delivered occurrence wins over an acknowledgement racing after a
	// prior occurrence, even when both belong to the same runtime run.
	if err := mgr.MarkSessionRead(sess.ID, askGen); err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.StateChanged{SessionID: sess.ID, State: string(bus.StateError), Error: "boom"})
	sess.runtime.Bus.Drain(time.Second)
	pollUntil(t, time.Second, "late attention", func() bool { return mgr.sessionInfo(sess).Unseen })
}

func TestSnapshotCutCapturesAttentionGenerationExcludingLaterAttention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// This is the websocket-init interleaving: ask_user installs its pending
	// request before publishing its event. The event lands after an old
	// generation sample but before the sequence cut, so init must bind the
	// rendered pending request to its occurrence generation.
	stale := sess.attentionGen.Load()
	baseline := sess.runtime.Bus.LastSeq()
	askTool := askuser.NewTool(sess.runtime.Context().AskBridge)
	go func() {
		_, _ = askTool.Execute(context.Background(), map[string]any{
			"questions": []any{map[string]any{"question": "continue?"}},
		}, nil)
	}()
	pollUntil(t, time.Second, "pending ask publication", func() bool {
		return sess.runtime.Bus.LastSeq() > baseline
	})
	streaming, liveTools, cut := sess.runtime.Context().SnapshotInFlightWithCut()
	bound := mgr.attentionGenerationForSequence(sess, cut)
	if cut <= baseline {
		t.Fatalf("snapshot cut = %d, want ask publication after %d", cut, baseline)
	}
	// A later event must not be included in the init acknowledgement bound.
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p2"})
	sess.runtime.Bus.Drain(time.Second)

	if stale != 0 {
		t.Fatalf("stale generation = %d, want 0", stale)
	}
	init := buildInitDataAtAttentionGen(sess, streaming, liveTools, bound)
	if init.PendingAsk == nil {
		t.Fatal("init did not render the pending ask")
	}
	if init.UnseenGen != init.PendingAsk.UnseenGen || init.UnseenGen != 1 {
		t.Fatalf("init bound = %d, pending ask bound = %d; want matching generation 1", init.UnseenGen, init.PendingAsk.UnseenGen)
	}
	if latest := sess.attentionGen.Load(); latest != 2 {
		t.Fatalf("latest generation = %d, want post-cut generation 2", latest)
	}
	if err := sess.runtime.Context().Approvals.ResolveAskUser(init.PendingAsk.ID, []string{"yes"}); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadAttentionResetsWhenSessionIsResumed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	sess, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 1})
	pollUntil(t, time.Second, "unseen completion", func() bool { return mgr.sessionInfo(sess).Unseen })
	if err := mgr.MarkSessionRead(sess.ID, mgr.sessionInfo(sess).UnseenGen); err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed.runtime.Bus.Publish(bus.PermissionRequested{SessionID: resumed.ID, RunGen: 1, ID: "p1"})
	pollUntil(t, time.Second, "post-resume attention", func() bool { return mgr.sessionInfo(resumed).Unseen })
}

func TestPendingAttentionSnapshotCarriesOccurrenceGeneration(t *testing.T) {
	permission, ask := pendingAttentionData(bus.PendingApprovalInfo{
		Permission: &bus.PendingPermissionInfo{ID: "p1"},
		Ask:        &bus.PendingAskInfo{ID: "a1"},
	}, 42)
	if permission.UnseenGen != 42 || ask.UnseenGen != 42 {
		t.Fatalf("pending unseen generations = permission:%d ask:%d", permission.UnseenGen, ask.UnseenGen)
	}
}

func TestReadBeforeOccurrenceDoesNotSuppressIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	sess, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	// A delayed/impossible future acknowledgement is not retained as a cursor.
	if err := mgr.MarkSessionRead(sess.ID, 999); err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, RunGen: 1, ID: "p1"})
	pollUntil(t, time.Second, "attention after early read", func() bool { return mgr.sessionInfo(sess).Unseen })
}

func TestMarkSessionReadScopesGenerationToServerInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, RunGen: 1, ID: "p1"})
	pollUntil(t, time.Second, "unseen attention", func() bool { return mgr.sessionInfo(sess).Unseen })
	gen := mgr.sessionInfo(sess).UnseenGen

	// A generation from another process can be numerically newer but belongs
	// to a separate namespace, so it must not clear this process's occurrence.
	if err := mgr.MarkSessionRead(sess.ID, gen+100, "previous-instance"); err != nil {
		t.Fatal(err)
	}
	if !mgr.sessionInfo(sess).Unseen {
		t.Fatal("mismatched server instance cleared unseen attention")
	}
	if err := mgr.MarkSessionRead(sess.ID, gen, mgr.serverInstance); err != nil {
		t.Fatal(err)
	}
	if mgr.sessionInfo(sess).Unseen {
		t.Fatal("matching server instance did not clear unseen attention")
	}
}

func TestAttentionSequenceMappingIsNamespacedAndBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	first, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}

	// LocalBus sequence values restart for every session, so equal sequence
	// numbers must remain isolated by runtime identity.
	mgr.recordAttentionSequence(first, 1, 11)
	mgr.recordAttentionSequence(second, 1, 22)
	if got := mgr.attentionGenerationForSequence(first, 1); got != 11 {
		t.Fatalf("first sequence generation = %d, want 11", got)
	}
	if got := mgr.attentionGenerationForSequence(second, 1); got != 22 {
		t.Fatalf("second sequence generation = %d, want 22", got)
	}

	// Closing/resuming creates a different runtime whose sequence 1 cannot read
	// the old runtime's mapping. Cleanup also frees its retained pointer.
	mgr.forgetAttentionSequences(first)
	resumedRuntime := &ManagedSession{}
	mgr.recordAttentionSequence(resumedRuntime, 1, 33)
	if got := mgr.attentionGenerationForSequence(resumedRuntime, 1); got != 33 {
		t.Fatalf("resumed sequence generation = %d, want 33", got)
	}

	for i := 0; i < maxAttentionSequenceRecords+8; i++ {
		mgr.recordAttentionSequence(second, uint64(i+2), uint64(i+100))
	}
	mgr.attentionSeqMu.Lock()
	gotRecords := len(mgr.attentionSeqOrder[second])
	mgr.attentionSeqMu.Unlock()
	if gotRecords > maxAttentionSequenceRecords {
		t.Fatalf("per-session attention sequence records = %d, want at most %d", gotRecords, maxAttentionSequenceRecords)
	}
	if got := mgr.attentionGenerationForSequence(second, maxAttentionSequenceRecords+9); got != maxAttentionSequenceRecords+107 {
		t.Fatalf("newest bounded sequence generation = %d", got)
	}
}

func TestAttentionSequenceCutSurvivesCrossSessionChurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), t.TempDir())
	target, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	churn, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// This is the old stranded-dot failure: init held target's cut while other
	// sessions' attention records exhausted the single global 512-record FIFO.
	// The target's record must remain available until its own bounded history,
	// not unrelated traffic, evicts it.
	mgr.recordAttentionSequence(target, 1, 11)
	cut := uint64(1)
	for i := 0; i < maxAttentionSequenceRecords+1; i++ {
		mgr.recordAttentionSequence(churn, uint64(i+1), uint64(i+100))
	}
	if got := mgr.attentionGenerationForSequence(target, cut); got != 11 {
		t.Fatalf("cut generation after cross-session churn = %d, want 11", got)
	}
}

func TestLateCancelledRunAfterUnreadUnsubscribeDoesNotBlockResolver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	mgr.recordAttentionSequence(sess, 1, 1)
	result := make(chan uint64, 1)
	go func() { result <- mgr.attentionGenerationForSequence(sess, 99) }()
	select {
	case <-result:
		t.Fatal("resolver returned before teardown")
	case <-time.After(20 * time.Millisecond):
	}
	sess.unreadUnsub() // close path removes the tracker before LocalBus.Close.
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 2, Cancelled: true})
	mgr.forgetAttentionSequences(sess)
	select {
	case got := <-result:
		if got != 0 {
			t.Fatalf("teardown resolver = %d, want 0", got)
		}
	case <-time.After(time.Second):
		t.Fatal("teardown did not wake resolver")
	}
}

func TestFirstAttentionSequenceWaitsForItsOccurrence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan uint64, 1)
	go func() { result <- mgr.attentionGenerationForSequence(sess, 1) }()
	select {
	case <-result:
		t.Fatal("first resolver returned before its occurrence was recorded")
	case <-time.After(20 * time.Millisecond):
	}
	mgr.recordAttentionSequence(sess, 1, 77)
	select {
	case got := <-result:
		if got != 77 {
			t.Fatalf("first occurrence generation = %d, want 77", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first resolver did not wake after occurrence record")
	}
}

func TestAttentionFenceSurvivesDroppedDeltaCutWithoutStrandingOrOverAcknowledging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// This reproduces the old failure exactly: the ordinary SubscribeAllSeq
	// queue fills while its handler is unable to record, and the cut is a delta
	// that it would have dropped. The filtered tracker queues only the
	// permission occurrence, followed by its lossless fence.
	mgr.attentionSeqMu.Lock()
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "p1"})
	for i := 0; i < 300; i++ {
		sess.runtime.Bus.Publish(bus.TextDelta{SessionID: sess.ID, Delta: "x"})
	}
	cut := sess.runtime.Bus.CaptureSeq()
	result := make(chan struct {
		gen uint64
		ok  bool
	}, 1)
	go func() {
		gen, ok := mgr.attentionGenerationAtCut(ctx, sess, cut)
		result <- struct {
			gen uint64
			ok  bool
		}{gen, ok}
	}()
	time.Sleep(20 * time.Millisecond)
	mgr.attentionSeqMu.Unlock()

	var bound struct {
		gen uint64
		ok  bool
	}
	select {
	case bound = <-result:
	case <-time.After(time.Second):
		t.Fatal("dropped delta cut left attention fence blocked")
	}
	if !bound.ok || bound.gen == 0 {
		t.Fatalf("attention bound = (%d, %v), want known non-zero occurrence", bound.gen, bound.ok)
	}
	if err := mgr.MarkSessionRead(sess.ID, bound.gen); err != nil {
		t.Fatal(err)
	}
	if mgr.sessionInfo(sess).Unseen {
		t.Fatal("snapshot acknowledgement stranded the represented occurrence")
	}

	// A newer post-cut occurrence must survive the old snapshot's acknowledgement.
	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "a2"})
	sess.runtime.Bus.Drain(time.Second)
	if err := mgr.MarkSessionRead(sess.ID, bound.gen); err != nil {
		t.Fatal(err)
	}
	if !mgr.sessionInfo(sess).Unseen {
		t.Fatal("pre-cut acknowledgement cleared a post-cut occurrence")
	}
}

func TestAttentionFenceStopsOnRequestOrBusClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	for _, stop := range []struct {
		name string
		stop func(context.CancelFunc)
	}{
		{name: "request close", stop: func(cancel context.CancelFunc) { cancel() }},
		{name: "bus close", stop: func(_ context.CancelFunc) { go sess.runtime.Bus.Close() }},
	} {
		t.Run(stop.name, func(t *testing.T) {
			requestCtx, requestCancel := context.WithCancel(context.Background())
			defer requestCancel()
			mgr.attentionSeqMu.Lock()
			sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: stop.name})
			cut := sess.runtime.Bus.CaptureSeq()
			result := make(chan bool, 1)
			go func() {
				_, ok := mgr.attentionGenerationAtCut(requestCtx, sess, cut)
				result <- ok
			}()
			time.Sleep(20 * time.Millisecond)
			stop.stop(requestCancel)
			select {
			case ok := <-result:
				if ok {
					t.Fatal("stopped fence supplied an unsafe attention bound")
				}
			case <-time.After(time.Second):
				t.Fatal("stopped fence did not unblock")
			}
			mgr.attentionSeqMu.Unlock()
		})
	}
}

func TestAttentionOverflowAfterFenceRemainsUnboundForFollowingInit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// First leave a historical overflow pending, then drain its queue. The
	// first init is allowed to clear only this overflow, not one later than its
	// fence marker.
	mgr.attentionSeqMu.Lock()
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "before"})
	time.Sleep(20 * time.Millisecond) // let the handler block on the recorder
	for i := 0; i < 80; i++ {
		sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "before"})
	}
	overflowed := sess.attentionSeqSub.Overflowed()
	mgr.attentionSeqMu.Unlock()
	if !overflowed {
		t.Fatal("initial attention queue did not overflow")
	}
	sess.runtime.Bus.Drain(time.Second)

	gate := &gatedFenceSubscription{
		SeqFenceSubscription: sess.attentionSeqSub,
		markerQueued:         make(chan struct{}),
		release:              make(chan struct{}),
	}
	sess.attentionSeqSub = gate
	cut := sess.runtime.Bus.CaptureSeq()
	first := make(chan bool, 1)
	go func() {
		_, ok := mgr.attentionGenerationAtCut(ctx, sess, cut)
		first <- ok
	}()
	select {
	case <-gate.markerQueued:
	case <-time.After(time.Second):
		t.Fatal("first init did not enqueue its fence")
	}

	// These are published after the fence marker. The recorder is held so the
	// first event blocks its handler and the rest fill the bounded queue; at
	// least one attention occurrence is dropped after the cut.
	mgr.attentionSeqMu.Lock()
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "after"})
	time.Sleep(20 * time.Millisecond) // let the handler block on the recorder
	for i := 0; i < 80; i++ {
		sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "after"})
	}
	mgr.attentionSeqMu.Unlock()
	close(gate.release)
	select {
	case ok := <-first:
		if ok {
			t.Fatal("overflowed init supplied an attention bound")
		}
	case <-time.After(time.Second):
		t.Fatal("first overflowed init did not finish")
	}
	sess.runtime.Bus.Drain(time.Second)

	// The post-fence overflow must survive the first clear. A subsequent init
	// therefore has no safe bound; returning the preceding recorded generation
	// here would swallow the dropped occurrence and strand its attention dot.
	_, bound := mgr.attentionGenerationAtCut(ctx, sess, sess.runtime.Bus.CaptureSeq())
	if bound {
		t.Fatal("following init bound attention after a post-fence overflow")
	}
}

func TestAttentionOverflowClearConvergesAfterDeadlineContention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// Block the recorder until the bounded attention queue drops an occurrence,
	// then let it drain. The overflow watermark remains pending independently of
	// the queue contents.
	mgr.attentionSeqMu.Lock()
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "first"})
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 80; i++ {
		sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "overflow"})
	}
	mgr.attentionSeqMu.Unlock()
	sess.runtime.Bus.Drain(time.Second)
	if !sess.attentionSeqSub.Overflowed() {
		t.Fatal("attention queue did not overflow")
	}

	// Keep the post-deadline clear blocked, as a contended publishMu would be.
	// The init still returns unbound on time, but the independent blocking clear
	// must settle the exact captured watermark once contention releases.
	gate := &deferredClearSubscription{
		SeqFenceSubscription: sess.attentionSeqSub,
		clearAttempted:       make(chan struct{}),
		clearRelease:         make(chan struct{}),
		clearComplete:        make(chan struct{}),
	}
	sess.attentionSeqSub = gate
	_, _, cut, overflow, cleared, _, captured := sess.runtime.Context().SnapshotInFlightWithAttentionCut(gate)
	if !captured || overflow == cleared {
		t.Fatal("init did not capture pending overflow")
	}
	result := make(chan bool, 1)
	go func() {
		_, ok := mgr.attentionGenerationAtCutWithOverflow(ctx, sess, cut, overflow, cleared)
		result <- ok
	}()
	select {
	case <-gate.clearAttempted:
	case <-time.After(time.Second):
		t.Fatal("bounded overflow clear did not reach its deadline")
	}
	select {
	case ok := <-result:
		if ok {
			t.Fatal("overflowed init supplied an attention bound")
		}
	case <-time.After(time.Second):
		t.Fatal("overflowed init did not return after its deadline")
	}
	if !gate.Overflowed() {
		t.Fatal("overflow cleared before sustained contention was released")
	}
	close(gate.clearRelease)
	select {
	case <-gate.clearComplete:
	case <-time.After(time.Second):
		t.Fatal("deferred overflow clear did not converge after contention released")
	}
	if gate.Overflowed() {
		t.Fatal("deferred overflow clear left its captured watermark pending")
	}
}

func TestOverflowClearWorkerCoalescesAndShutdownWaitsForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	gate := &deferredClearSubscription{
		SeqFenceSubscription: sess.attentionSeqSub,
		clearStarted:         make(chan struct{}),
		clearRelease:         make(chan struct{}),
		clearComplete:        make(chan struct{}),
	}
	sess.attentionSeqSub = gate
	mgr.scheduleOverflowClear(sess, gate, 1)
	select {
	case <-gate.clearStarted:
	case <-time.After(time.Second):
		t.Fatal("overflow clear worker did not start")
	}
	sess.overflowClearMu.Lock()
	firstDone := sess.overflowClearDone
	sess.overflowClearMu.Unlock()
	for i := uint64(2); i <= 100; i++ {
		mgr.scheduleOverflowClear(sess, gate, i)
		sess.overflowClearMu.Lock()
		if !sess.overflowClearRunning || sess.overflowClearDone != firstDone {
			sess.overflowClearMu.Unlock()
			t.Fatal("repeated timed-out inits spawned another overflow clear worker")
		}
		sess.overflowClearMu.Unlock()
	}

	shutdown := make(chan struct{})
	go func() {
		mgr.Shutdown()
		close(shutdown)
	}()
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("manager shutdown did not cancel and wait for overflow clear worker")
	}
	select {
	case <-firstDone:
	default:
		t.Fatal("shutdown returned before the overflow clear worker exited")
	}
}

func TestOverflowClearWorkerSessionCloseCancelsAndWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	gate := &deferredClearSubscription{
		SeqFenceSubscription: sess.attentionSeqSub,
		clearStarted:         make(chan struct{}),
		clearRelease:         make(chan struct{}),
		clearComplete:        make(chan struct{}),
	}
	sess.attentionSeqSub = gate
	mgr.scheduleOverflowClear(sess, gate, 1)
	select {
	case <-gate.clearStarted:
	case <-time.After(time.Second):
		t.Fatal("overflow clear worker did not start")
	}
	sess.overflowClearMu.Lock()
	done := sess.overflowClearDone
	sess.overflowClearMu.Unlock()
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("session close returned before the overflow clear worker exited")
	}
}

func TestConcurrentAttentionInitsKeepOverflowWithTheSnapshotThatContainsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// Establish a recorded generation that would be an unsafe preceding bound
	// for the second init if the first init were allowed to clear its overflow.
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "recorded"})
	sess.runtime.Bus.Drain(time.Second)

	gate := &orderedFenceSubscription{
		SeqFenceSubscription: sess.attentionSeqSub,
		firstEntered:         make(chan struct{}),
		firstRelease:         make(chan struct{}),
		secondEntered:        make(chan struct{}),
		secondRelease:        make(chan struct{}),
	}
	sess.attentionSeqSub = gate
	_, _, cutA, overflowA, clearedA, _, captured := sess.runtime.Context().SnapshotInFlightWithAttentionCut(gate)
	if !captured {
		t.Fatal("first init did not capture attention cut")
	}
	first := make(chan struct {
		gen uint64
		ok  bool
	}, 1)
	go func() {
		gen, ok := mgr.attentionGenerationAtCutWithOverflowBefore(ctx, sess, cutA, overflowA, clearedA, false, time.Now().Add(time.Second))
		first <- struct {
			gen uint64
			ok  bool
		}{gen, ok}
	}()
	select {
	case <-gate.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first init did not begin placing its fence")
	}

	// A matching occurrence is dropped after A's cut but before A's fence is
	// released. B captures after that loss, so its own cut must be unbound even
	// if A reaches its fence and completes first.
	mgr.attentionSeqMu.Lock()
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "dropped"})
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 80; i++ {
		sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "dropped"})
	}
	mgr.attentionSeqMu.Unlock()
	_, _, cutB, overflowB, clearedB, _, captured := sess.runtime.Context().SnapshotInFlightWithAttentionCut(gate)
	if !captured || overflowB == clearedB {
		t.Fatal("second init did not atomically capture the dropped occurrence")
	}
	// Let the tracker consume its bounded backlog while both fence placements
	// remain held. The overflow watermark remains pending, but A's marker can
	// now be queued so this exercises overflow clearing rather than a full
	// marker queue.
	sess.runtime.Bus.Drain(time.Second)
	second := make(chan bool, 1)
	go func() {
		_, ok := mgr.attentionGenerationAtCutWithOverflowBefore(ctx, sess, cutB, overflowB, clearedB, false, time.Now().Add(time.Second))
		second <- ok
	}()
	select {
	case <-gate.secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second init did not begin placing its fence")
	}

	close(gate.firstRelease)
	select {
	case got := <-first:
		if !got.ok || got.gen == 0 {
			t.Fatalf("first init = (%d, %v), want its valid preceding bound", got.gen, got.ok)
		}
	case <-time.After(time.Second):
		t.Fatal("first init did not finish")
	}
	close(gate.secondRelease)
	select {
	case ok := <-second:
		if ok {
			t.Fatal("second init bound attention despite a dropped occurrence in its cut")
		}
	case <-time.After(time.Second):
		t.Fatal("second init did not finish")
	}
}

func TestAttentionFenceDeadlineIncludesFinalSequenceLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "recorded"})
	sess.runtime.Bus.Drain(time.Second)
	gate := &gatedFenceSubscription{
		SeqFenceSubscription: sess.attentionSeqSub,
		markerQueued:         make(chan struct{}),
		release:              make(chan struct{}),
	}
	sess.attentionSeqSub = gate
	_, _, cut, overflow, cleared, _, captured := sess.runtime.Context().SnapshotInFlightWithAttentionCut(gate)
	if !captured {
		t.Fatal("init did not capture attention cut")
	}
	started := time.Now()
	result := make(chan bool, 1)
	go func() {
		_, ok := mgr.attentionGenerationAtCutWithOverflow(ctx, sess, cut, overflow, cleared)
		result <- ok
	}()
	select {
	case <-gate.markerQueued:
	case <-time.After(time.Second):
		t.Fatal("init did not place its fence")
	}
	mgr.attentionSeqMu.Lock()
	close(gate.release)
	select {
	case ok := <-result:
		if ok {
			mgr.attentionSeqMu.Unlock()
			t.Fatal("contended final lookup supplied a bound")
		}
		if elapsed := time.Since(started); elapsed > attentionSequenceWait+100*time.Millisecond {
			mgr.attentionSeqMu.Unlock()
			t.Fatalf("init waited %s after its 100ms deadline", elapsed)
		}
	case <-time.After(attentionSequenceWait + time.Second):
		mgr.attentionSeqMu.Unlock()
		t.Fatal("contended final lookup exceeded the fence deadline")
	}
	mgr.attentionSeqMu.Unlock()
}

func TestAttentionTrackerBoundsRapidRunsAndDiscardsOnTeardown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// Hold the recorder, simulating scheduler/lock contention while automatic
	// runs finish quickly. Cancelled runs do not create attention and therefore
	// must not consume this queue at all.
	mgr.attentionSeqMu.Lock()
	for i := 0; i < 1000; i++ {
		sess.runtime.Bus.Publish(bus.RunEnded{Cancelled: true, FinalText: string(make([]byte, 1<<20))})
	}
	if sess.attentionSeqSub.Overflowed() {
		mgr.attentionSeqMu.Unlock()
		t.Fatal("cancelled runs entered the attention tracker")
	}
	// The first event occupies the blocked handler, so the queue trips one
	// publication after its capacity. Publish until it does rather than
	// encoding that off-by-one, with a hard ceiling so a regression still fails.
	for i := 0; i < 1000 && !sess.attentionSeqSub.Overflowed(); i++ {
		sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: uint64(i), FinalText: string(make([]byte, 1<<10))})
	}
	if !sess.attentionSeqSub.Overflowed() {
		mgr.attentionSeqMu.Unlock()
		t.Fatal("rapid attention runs did not trip the bounded queue")
	}

	// Unsubscribe must discard the backlog rather than drain those final-text
	// events during teardown. Releasing the in-flight handler then settles fast.
	sess.unreadUnsub()
	mgr.attentionSeqMu.Unlock()
	sess.runtime.Bus.Drain(time.Second)
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
	init := buildInitDataAtAttentionGen(sess, streaming, liveTools, 0)
	if init.RunStartedAtMs == 0 {
		t.Fatal("running init omitted authoritative run-start anchor")
	}
}
