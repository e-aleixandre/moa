package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

func TestDurableReviewTwoJournalsSameSession(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	f.harnessOnly = true
	for _, id := range []string{"review-call-a", "review-call-b"} {
		c := &voiceCall{id: id, sessionID: f.sess.ID, provider: voice.GrokPlan, started: time.Now()}
		j, err := openVoiceJournal(f.sess.persister.storeDir(), c)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.transcript(voice.Event{Role: "user", ItemID: id, Text: "OBSERVED-" + id, Final: true}, false); err != nil {
			t.Fatal(err)
		}
		j.close(false)
	}
	f.mgr.Shutdown()
	s := vwCrashState{Root: f.root, Base: f.mgr.sessionBaseDir, Workspace: f.mgr.workspaceRoot, SessionID: f.sess.ID}
	for boot := 0; boot < 3; boot++ {
		m, p, stop := vwRestart(t, s, simpleResponseHandler("RECOVERED"), simpleResponseHandler("EXTRA"))
		if boot == 0 {
			pollUntil(t, vpDeadline, "recovered journal wake ran", func() bool { return p.calls.Load() > 0 })
		} else {
			vpStable(t, func() bool { return p.calls.Load() == 0 }, "a later boot recovered again")
		}
		if sess, ok := m.Get(s.SessionID); ok {
			ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
			if !sess.runtime.WaitSettled(ctx) {
				t.Fatal("not settled")
			}
			cancel()
		}
		disk, _, e := session.FindSessionReadOnly(s.Base, s.SessionID)
		if e != nil {
			t.Fatal(e)
		}
		n := len(vpSavedVoice(disk, "minutes"))
		left := len(drJournals(t, s.Base))
		t.Logf("boot=%d recovered_minutes=%d remaining_journals=%d calls=%d", boot, n, left, p.calls.Load())
		if boot == 0 && (n != 2 || left != 0) {
			t.Errorf("SAME_SESSION_RECOVERY_DEFERRED: only %d/2 calls processed on startup; %d journal left", n, left)
		}
		stop()
	}
}

func TestDurableReviewJournalSyncFailure(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	f.harnessOnly = true
	c := &voiceCall{id: "review-fail-sync", sessionID: f.sess.ID, provider: voice.GrokPlan, started: time.Now()}
	j, err := openVoiceJournal(f.sess.persister.storeDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	c.journal = j
	// A real failing file descriptor, not a mock persistence result.
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	j.mu.Lock()
	_ = j.f.Close()
	j.f = full
	j.mu.Unlock()
	err = c.addTranscript(voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "after-io-fail", Text: "LOST-AFTER-JOURNAL-IO-ERROR", Final: true})
	// The writer fails beside the call: the next event is refused.
	if werr := j.appendWait(voiceJournalLine{Kind: "ending"}); werr == nil {
		t.Fatal("control: the failing descriptor accepted a synced line")
	}
	if err == nil {
		err = c.addTranscript(voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "after-io-fail-2", Text: "NEXT", Final: true})
	}
	j.close(false)
	end, _, ok := readJournalPath(t, j.path)
	if !ok {
		t.Fatal("invalid header control")
	}
	t.Logf("addTranscript_err=%v live=%q recovered=%q logs=%s", err, c.transcript.Markdown(), end.transcript, f.log.String())
	if err == nil && !strings.Contains(end.transcript, "LOST-AFTER-JOURNAL-IO-ERROR") {
		t.Error("SILENT_NON_DURABLE_ACCEPTANCE: final accepted without persistence; captured logs are empty (Write/Sync err is shadowed)")
	}
}

func TestDurableReviewOpenJournalFailure(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	f.harnessOnly = true
	dir := voiceJournalDir(f.sess.persister.storeDir(), f.sess.ID)
	if e := os.WriteFile(dir, []byte("blocked"), 0600); e != nil {
		t.Fatal(e)
	}
	c := newVoiceCall(&voiceCallRegistry{mgr: f.mgr}, "review-open-fail", "token", f.sess.ID, voice.GrokPlan, voice.Billing{}, vaNewControllerUpstream())
	defer c.workCancel()
	c.openJournal()
	e := c.addTranscript(voice.Event{Kind: voice.EventTranscript, Role: "user", Text: "ACCEPTED-WITHOUT-JOURNAL", Final: true})
	t.Logf("journal=%v addTranscript_err=%v parent=%s logs=%s", c.journal, e, filepath.Dir(dir), f.log.String())
	if c.journal == nil && e == nil {
		t.Error("CALL_CONTINUES_WITH_NO_JOURNAL: opening persistence failed; transcript acceptance still reports success")
	}
}
