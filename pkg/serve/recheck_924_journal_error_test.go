package serve

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/voice"
)

func TestRecheck924WriteErrorEndsAndPreservesObservedFinal(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	u := vaNewControllerUpstream()
	u.onMinutes = func(context.Context) error { u.events <- vaEndEvent([]string{"ERROR-CALL-DECISION"}, nil); return nil }
	c, made := vaControllerCall(t, f, u)
	cl := f.dial(made, f.owner(), true)
	vaWaitBrief(t, f, u)
	j := c.journalLine()
	if j == nil {
		t.Fatal("journal not opened")
	}
	full, e := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	j.mu.Lock()
	_ = j.f.Close()
	j.f = full
	j.mu.Unlock()
	u.events <- voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "write-error-final", Text: "FINAL-WHEN-WRITE-FAILS", Final: true}
	vaWaitDone(t, c)
	disk := vaDisk(t, f)
	minutes := vpSavedVoice(disk, "minutes")
	if c.endReason() != "journal_failed" || len(minutes) != 1 || minutes[0].Custom["reason"] != "journal_failed" {
		t.Fatalf("failure not surfaced: reason=%s minutes=%s", c.endReason(), vpJSON(minutes))
	}
	if !strings.Contains(f.log.String(), "journal write failed") || !strings.Contains(drLiteral(t, f.mgr, disk), "FINAL-WHEN-WRITE-FAILS") {
		t.Fatalf("failure hidden/literal lost: logs=%s minutes=%s", f.log.String(), vpJSON(minutes))
	}
	t.Logf("end_reason=%s saved_literal=true warning=true journals=%d", c.endReason(), len(drJournals(t, f.mgr.sessionBaseDir)))
	_ = cl
}

func TestRecheck924OpenErrorEndsCall(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	dir := voiceJournalDir(f.sess.persister.storeDir(), f.sess.ID)
	if e := os.WriteFile(dir, []byte("not a directory"), 0600); e != nil {
		t.Fatal(e)
	}
	u := vaNewControllerUpstream()
	u.onMinutes = func(context.Context) error { u.events <- vaEndEvent(nil, nil); return nil }
	c, _ := vaControllerCall(t, f, u)
	vaWaitDone(t, c)
	if c.endReason() != "journal_failed" || !strings.Contains(f.log.String(), "journal not opened") {
		t.Fatalf("open failure not ended: reason=%s logs=%s", c.endReason(), f.log.String())
	}
	t.Logf("end_reason=%s minutes=%d", c.endReason(), len(vpSavedVoice(vaDisk(t, f), "minutes")))
}
