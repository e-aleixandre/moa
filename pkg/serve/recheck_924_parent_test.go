package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

func TestRecheck924AttachmentRetryAfterStorageRecovers(t *testing.T) {
	t.Skip("R-B2 accepted as debt by the owner: the record is not repaired; TestVoiceJournalKeptWhenTranscriptUnsaved covers what is kept")
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	u := vaNewControllerUpstream()
	u.onMinutes = func(context.Context) error { u.events <- vaEndEvent([]string{"RECHECK-DECISION"}, nil); return nil }
	c, made := vaControllerCall(t, f, u)
	cl := f.dial(made, f.owner(), true)
	vaWaitBrief(t, f, u)
	u.events <- voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "recheck-attachment", Text: "RECHECK-ONLY-IN-JOURNAL", Final: true}
	cl.await("literal observed", func(v map[string]any) bool { return v["text"] == "RECHECK-ONLY-IN-JOURNAL" })
	goodStore := f.mgr.attachStore
	blocked := filepath.Join(t.TempDir(), "blocked")
	broken, e := attachment.New(blocked)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.RemoveAll(blocked); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(blocked, []byte("not a directory"), 0600); e != nil {
		t.Fatal(e)
	}
	f.mgr.attachStore = broken
	cl.hangup()
	vaWaitDone(t, c)
	pollUntil(t, vpDeadline, "original wake saved", func() bool { return len(vpSavedVoice(vaDisk(t, f), "minutes_wake")) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("original wake unsettled")
	}
	disk := vaDisk(t, f)
	if voiceRecordHasTranscript(f.sess, c.id+"/minutes") || len(drJournals(t, f.mgr.sessionBaseDir)) != 1 {
		t.Fatal("invalid failed-attachment fixture")
	}
	t.Logf("after_failure minutes=%d wakes=%d journals=%d", len(vpSavedVoice(disk, "minutes")), len(vpSavedVoice(disk, "minutes_wake")), len(drJournals(t, f.mgr.sessionBaseDir)))
	f.mgr.attachStore = goodStore
	f.mgr.Shutdown()
	s := vwCrashState{Root: f.root, Base: f.mgr.sessionBaseDir, Workspace: f.mgr.workspaceRoot, SessionID: f.sess.ID}
	for boot := 0; boot < 4; boot++ {
		m, p, stop := vwRestart(t, s, simpleResponseHandler("UNEXPECTED-EXTRA"))
		saved, _, err := session.FindSessionReadOnly(s.Base, s.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		// A successful write to the same real Store rules out an unrepaired destination.
		control, err := m.attachStore.PutRef(s.SessionID, []byte("STORE-HEALTHY"), attachment.PutMeta{Name: "control.txt", Mime: "text/plain", Kind: "file"})
		if err != nil {
			t.Fatal(err)
		}
		_ = control
		literal := drLiteral(t, m, saved)
		journals := len(drJournals(t, s.Base))
		t.Logf("boot=%d healthy_store=true minutes=%d wakes=%d literal=%q journals=%d runs=%d", boot, len(vpSavedVoice(saved, "minutes")), len(vpSavedVoice(saved, "minutes_wake")), literal, journals, p.calls.Load())
		if !strings.Contains(literal, "RECHECK-ONLY-IN-JOURNAL") || journals != 0 {
			t.Errorf("ATTACHMENT_RETRY_STUCK: storage recovered but existing minutes bypass conversion; boot=%d", boot)
		}
		if len(vpSavedVoice(saved, "minutes")) != 1 || len(vpSavedVoice(saved, "minutes_wake")) != 1 || p.calls.Load() != 0 {
			t.Error("retry duplicated minutes/wake")
		}
		stop()
	}
}
