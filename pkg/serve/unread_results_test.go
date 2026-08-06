package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

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
	gotRecords := len(mgr.attentionSeq)
	mgr.attentionSeqMu.Unlock()
	if gotRecords > maxAttentionSequenceRecords {
		t.Fatalf("attention sequence records = %d, want at most %d", gotRecords, maxAttentionSequenceRecords)
	}
	if got := mgr.attentionGenerationForSequence(second, maxAttentionSequenceRecords+9); got != maxAttentionSequenceRecords+107 {
		t.Fatalf("newest bounded sequence generation = %d", got)
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
