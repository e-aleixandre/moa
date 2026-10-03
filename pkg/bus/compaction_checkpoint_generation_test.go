package bus

// The checkpoint a summary embeds is acknowledged by generation in the very
// snapshot that makes its boundary durable, on the real FileStore. Text is not
// the identity: the same text written again is a new checkpoint.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
)

func ckptRestored(s *session.Session) string {
	slot := sessioncheckpoint.New()
	slot.Restore(s.Metadata)
	text, _ := slot.Read()
	return text
}

// A checkpoint written after the summary captured K but before the boundary
// is saved is a new handoff: the boundary embeds K, while the live slot and
// the file reopened at that boundary keep the newer one literally.
func TestCompactionCheckpoint_NewerGenerationSurvivesBoundary(t *testing.T) {
	for _, newer := range []string{"NEWER-CHECKPOINT", pr28Ckpt} {
		name := "new_text"
		if newer == pr28Ckpt {
			name = "same_text_new_generation"
		}
		t.Run(name, func(t *testing.T) {
			slot := sessioncheckpoint.New()
			if err := slot.Write(pr28Ckpt); err != nil {
				t.Fatal(err)
			}
			var disk *session.Session
			var mu sync.Mutex
			f := newPR28CkptFix(t, slot, func(s *session.Session) { mu.Lock(); disk = s; mu.Unlock() })
			f.send(t, bgText("go ", 40))
			f.request(t, "ordinary request while the summary is held")
			f.waitSummaryEntered(t)
			if err := slot.Write(newer); err != nil {
				t.Fatal(err)
			}
			f.waitEnded(t, "originating run")
			f.rt.Bus.Drain(bgWait)
			f.sum.open()
			f.waitCut(t)

			mu.Lock()
			s := disk
			mu.Unlock()
			if s == nil {
				t.Fatal("harness: no compaction snapshot observed")
			}
			summary := pr28BoundarySummary(s.Entries, s.LeafID)
			if !strings.Contains(summary, pr28Ckpt) || (newer != pr28Ckpt && strings.Contains(summary, newer)) {
				t.Fatalf("boundary does not embed exactly the captured checkpoint: %q", summary)
			}
			if got := ckptRestored(s); got != newer {
				t.Fatalf("file at the boundary restores %q, want the newer checkpoint %q", got, newer)
			}
			if got, _ := slot.Read(); got != newer {
				t.Fatalf("live slot holds %q, want the newer checkpoint %q", got, newer)
			}
		})
	}
}

// A checkpoint written once the boundary's metadata was read (here, while the
// foreground commit saves) is not acknowledged by it: adoption must not clear
// it from the live slot.
func TestCompactionCheckpoint_WrittenDuringForegroundSaveStaysLive(t *testing.T) {
	slot := sessioncheckpoint.New()
	if err := slot.Write(pr28Ckpt); err != nil {
		t.Fatal(err)
	}
	f := newPR28CkptFix(t, slot, func(*session.Session) {})
	f.ag.SetBackgroundCompaction(nil, nil) //nolint:staticcheck // A nil lifetime disables background compaction for this foreground case.
	var once sync.Once
	f.p.before = func(entries []session.Entry) error {
		if catomicHasCompaction(entries) {
			once.Do(func() { _ = slot.Write(pr28Ckpt) })
		}
		return nil
	}
	f.sum.open()
	f.send(t, "go")
	if e := f.waitEnded(t, "run"); e.Err != nil {
		t.Fatal(e.Err)
	}
	f.waitCut(t)
	if got, _ := slot.Read(); got != pr28Ckpt {
		t.Fatalf("live slot holds %q: a checkpoint newer than the boundary's was consumed", got)
	}
}

// A rejected boundary save adopts nothing and consumes nothing: the live
// conversation and checkpoint stay as they were, the previous durable file is
// untouched, and the failure carries the unsaved payload.
func TestCompactionCheckpoint_RejectedSaveKeepsCheckpoint(t *testing.T) {
	slot := sessioncheckpoint.New()
	if err := slot.Write(pr28Ckpt); err != nil {
		t.Fatal(err)
	}
	f := newPR28CkptFix(t, slot, func(*session.Session) {})
	f.send(t, bgText("go ", 40))
	f.request(t, "ordinary request while the summary is held")
	f.waitSummaryEntered(t)
	f.waitEnded(t, "originating run")
	f.rt.Bus.Drain(bgWait)
	_, genBefore := slot.Read()
	before := catomicMsgs(f.ag.Messages())
	prevEntries, prevLeaf := f.savedRaw(t)
	f.p.before = func(entries []session.Entry) error {
		if catomicHasCompaction(entries) {
			return errors.New("injected disk failure")
		}
		return nil
	}
	f.sum.open()
	var ended CompactionEnded
	select {
	case ended = <-f.cuts:
	case <-time.After(bgWait):
		t.Fatal("the failed compaction never ended")
	}
	var notSaved *core.CompactionNotSavedError
	if !errors.As(ended.Err, &notSaved) || ended.Payload == nil {
		t.Fatalf("failure is not an unsaved compaction: err=%v payload=%v", ended.Err, ended.Payload)
	}
	f.ag.WaitBackgroundCompaction()
	if text, gen := slot.Read(); text != pr28Ckpt || gen != genBefore {
		t.Fatalf("slot after a rejected save = %q gen %d, want %q gen %d", text, gen, pr28Ckpt, genBefore)
	}
	if got := catomicMsgs(f.ag.Messages()); !equalMsgs(got, before) {
		t.Fatalf("agent changed by an unsaved cut:\n got %v\nwant %v", got, before)
	}
	entries, leaf := f.savedRaw(t)
	if catomicHasCompaction(entries) || leaf != prevLeaf || len(entries) != len(prevEntries) {
		t.Fatal("the previous durable snapshot was replaced by a rejected save")
	}
	disk, err := f.p.store.Load(f.p.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ckptRestored(disk); got != pr28Ckpt {
		t.Fatalf("previous file restores %q, want the pending checkpoint", got)
	}
}

// A boundary save accepted before Stop still finishes: the conversation is
// adopted and the checkpoint it embeds is consumed with it, live and on disk.
func TestCompactionCheckpoint_AcceptedSaveThenStopAcknowledges(t *testing.T) {
	slot := sessioncheckpoint.New()
	if err := slot.Write(pr28Ckpt); err != nil {
		t.Fatal(err)
	}
	f := newPR28CkptFix(t, slot, func(*session.Session) {})
	saving, hold := bgHoldSave(t, f, false)
	defer func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	}()
	f.prov.script = func(call int, req core.Request) *core.Message {
		if call == 1 {
			f.waitSummaryEntered(t)
			f.sum.open()
			// Let the worker publish its outcome before the next boundary.
			time.Sleep(200 * time.Millisecond)
			return bgToolCall("c1", map[string]any{}, "tool")
		}
		return nil
	}
	f.send(t, bgText("go ", 40))
	f.request(t, "first")
	bgWaitClosed(t, saving, "the boundary cut never entered its accepted save")
	var recalled []core.SteerItem
	if err := f.rt.Bus.Execute(AbortAndRecall{RunGen: 1, DiscardedSteers: &recalled}); err != nil {
		t.Fatal(err)
	}
	close(hold)
	f.waitCut(t)
	if e := f.waitEnded(t, "stopped run"); e.Err != nil && !errors.Is(e.Err, context.Canceled) {
		t.Fatalf("unexpected stopped-run error: %v", e.Err)
	}
	f.rt.Bus.Drain(bgWait)
	if msgs := f.ag.Messages(); len(msgs) == 0 || msgs[0].Role != "compaction_summary" {
		t.Fatalf("accepted cut not adopted after Stop: %v", catomicMsgs(msgs))
	}
	if text, _ := slot.Read(); text != "" {
		t.Fatalf("accepted boundary did not consume its checkpoint: %q", text)
	}
	disk, err := f.p.store.Load(f.p.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pr28BoundarySummary(disk.Entries, disk.LeafID), pr28Ckpt) {
		t.Fatal("durable boundary does not embed the checkpoint")
	}
	if got := ckptRestored(disk); got != "" {
		t.Fatalf("file after the accepted cut still restores the embedded checkpoint: %q", got)
	}
}
