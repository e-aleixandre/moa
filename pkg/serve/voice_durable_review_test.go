package serve

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// Review probes only. All stores/transports are the isolated production fixtures.
func TestDurableReviewCrashChild(t *testing.T) {
	phase := os.Getenv("MOA_DURABLE_REVIEW_PHASE")
	if phase == "" {
		t.Skip("subprocess only")
	}
	if phase == "chatgpt_partial" {
		f := vpNewFixture(t, "chatgpt-plan", vpBriefHandlers()...)
		cl := f.call()
		p := f.up.peer(t, f.provider)
		vpAwaitBrief(t, f, p)
		p.emit(t, map[string]any{"type": "input_transcript.added", "item": map[string]any{"text": "REVIEW-OBSERVED-LITERAL"}})
		cl.await("partial from actual ChatGPT adapter", func(v map[string]any) bool { return v["text"] == "REVIEW-OBSERVED-LITERAL" && v["final"] == false })
		vwCrash(t, f, voiceWakeID(cl.c.ID+"/minutes"))
	}
	hold := make(chan struct{})
	h := func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if strings.Contains(vpLastUser(req), "REVIEW-HOLD") {
			select {
			case <-hold:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return simpleResponse("BRIEF-READY"), nil
	}
	f := vpNewFixture(t, "grok-plan", h, h, h, h, h, h)
	u := vaNewControllerUpstream()
	u.onMinutes = func(context.Context) error { u.events <- vaEndEvent([]string{"REVIEW-DECISION"}, nil); return nil }
	c, made := vaControllerCall(t, f, u)
	cl := f.dial(made, f.owner(), true)
	cl.await("listening", func(v map[string]any) bool { return v["state"] == "listening" })
	vaWaitBrief(t, f, u)
	final := phase != "observed_partial"
	u.events <- voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "review-turn", Text: "REVIEW-OBSERVED-LITERAL", Final: final}
	cl.await("observed transcript", func(v map[string]any) bool { return v["text"] == "REVIEW-OBSERVED-LITERAL" })
	if phase == "observed_final" || phase == "observed_partial" {
		vwCrash(t, f, voiceWakeID(c.id+"/minutes"))
	}
	if phase == "minutes_accepted" || phase == "closing_before_minutes" {
		if phase == "closing_before_minutes" {
			u.onMinutes = func(context.Context) error { vwCrash(t, f, voiceWakeID(c.id+"/minutes")); return nil }
		} else {
			u.onClose = func(context.Context, string) error {
				if !c.hasMinutes() {
					t.Fatal("minutes barrier")
				}
				vwCrash(t, f, voiceWakeID(c.id+"/minutes"))
				return nil
			}
		}
	}
	if phase == "record_before_wake" {
		f.sess.runtime.Bus.Drain(time.Second)
		if err := f.sess.runtime.Flush(); err != nil {
			t.Fatal(err)
		}
		f.sess.runtime.Bus.Drain(time.Second)
		f.sess.persister.onSaved = func() {
			disk := vaDisk(t, f)
			if len(vpSavedVoice(disk, "minutes")) == 1 {
				vwCrash(t, f, voiceWakeID(c.id+"/minutes"))
			}
		}
	}
	if phase == "wake_queued" {
		if _, _, _, err := f.mgr.Send(f.sess.ID, "REVIEW-HOLD", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		pollUntil(t, vpDeadline, "held run", func() bool { return sessState(f.sess) == StateRunning })
	}
	cl.hangup()
	vaWaitDone(t, c)
	if phase == "wake_queued" {
		pollUntil(t, vpDeadline, "queued wake", func() bool {
			for _, q := range f.sess.runtime.Context().Agent.PendingSteers() {
				if voiceDeliveryID(q.Custom) == voiceWakeID(c.id+"/minutes") {
					return true
				}
			}
			return false
		})
		vwCrash(t, f, voiceWakeID(c.id+"/minutes"))
	}
	if phase == "finished" {
		ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
		defer cancel()
		if !f.sess.runtime.WaitSettled(ctx) {
			t.Fatal("not settled")
		}
		vwCrash(t, f, voiceWakeID(c.id+"/minutes"))
	}
	t.Fatalf("unreached phase %s", phase)
}

func drCrash(t *testing.T, phase string) vwCrashState {
	t.Helper()
	manifest := filepath.Join(t.TempDir(), "crash.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDurableReviewCrashChild$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "MOA_DURABLE_REVIEW_PHASE="+phase, "MOA_VOICE_WAKE_MANIFEST="+manifest)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("crash child: %v\n%s", e, b)
	}
	b, e := os.ReadFile(manifest)
	if e != nil {
		t.Fatal(e)
	}
	var s vwCrashState
	if e = json.Unmarshal(b, &s); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.RemoveAll(s.Root) })
	return s
}
func drLiteral(t *testing.T, m *Manager, s *session.Session) string {
	t.Helper()
	var b strings.Builder
	for _, msg := range vpSavedVoice(s, "minutes") {
		for _, c := range msg.Content {
			if c.Filename == "transcript.md" {
				r, _, e := m.attachStore.Open(s.ID, c.AttachmentID)
				if e != nil {
					t.Fatal(e)
				}
				raw, e := io.ReadAll(r)
				_ = r.Close()
				if e != nil {
					t.Fatal(e)
				}
				b.Write(raw)
			}
		}
	}
	return b.String()
}

func TestDurableReviewCrashMatrix(t *testing.T) {
	for _, phase := range []string{"observed_final", "closing_before_minutes", "minutes_accepted", "record_before_wake", "wake_queued", "finished"} {
		t.Run(phase, func(t *testing.T) {
			s := drCrash(t, phase)
			totalCalls := int64(0)
			for boot := 0; boot < 4; boot++ {
				m, p, stop := vwRestart(t, s, simpleResponseHandler("REVIEW-RECOVERED"), simpleResponseHandler("EXTRA"))
				sess, ok := m.Get(s.SessionID)
				if !ok {
					var err error
					sess, err = m.ResumeSession(s.SessionID)
					if err != nil {
						t.Fatal(err)
					}
				}
				pollUntil(t, vpDeadline, "wake durably saved", func() bool {
					saved, _, err := session.FindSessionReadOnly(s.Base, s.SessionID)
					return err == nil && len(vpSavedVoice(saved, "minutes_wake")) == 1 && saved.Metadata[session.MetaVoiceWake] != true
				})
				ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
				if !sess.runtime.WaitSettled(ctx) {
					t.Fatal("recovery not settled")
				}
				cancel()
				disk, _, err := session.FindSessionReadOnly(s.Base, s.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				mins, wakes := len(vpSavedVoice(disk, "minutes")), len(vpSavedVoice(disk, "minutes_wake"))
				if mins != 1 || wakes != 1 || disk.Metadata[session.MetaVoiceWake] == true {
					t.Fatalf("boot=%d mins=%d wakes=%d marker=%v", boot, mins, wakes, disk.Metadata[session.MetaVoiceWake])
				}
				if !strings.Contains(drLiteral(t, m, disk), "REVIEW-OBSERVED-LITERAL") {
					t.Fatal("observed literal lost")
				}
				calls := p.calls.Load()
				totalCalls += int64(calls)
				t.Logf("boot=%d minutes=%d wakes=%d model_calls=%d journals=%v", boot, mins, wakes, calls, drJournals(t, s.Base))
				if boot > 0 && calls != 0 {
					t.Fatal("wake repeated")
				}
				stop()
			}
			want := int64(1)
			if phase == "finished" {
				want = 0
			}
			if totalCalls != want {
				t.Fatalf("recovered model calls=%d want=%d", totalCalls, want)
			}
		})
	}
}
func drJournals(t *testing.T, base string) []string {
	t.Helper()
	var out []string
	for _, p := range []string{"*.voice-calls/*.jsonl", "*/*.voice-calls/*.jsonl"} {
		f, e := filepath.Glob(filepath.Join(base, p))
		if e != nil {
			t.Fatal(e)
		}
		out = append(out, f...)
	}
	return out
}
func TestDurableReviewObservedPartialCrash(t *testing.T) {
	for _, phase := range []string{"observed_partial", "chatgpt_partial"} {
		t.Run(phase, func(t *testing.T) { drAssertObservedPartial(t, phase) })
	}
}
func drAssertObservedPartial(t *testing.T, phase string) {
	s := drCrash(t, phase)
	m, _, stop := vwRestart(t, s, simpleResponseHandler("RECOVERED"))
	defer stop()
	sess, ok := m.Get(s.SessionID)
	if !ok {
		t.Fatal("no recovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("not settled")
	}
	disk, _, e := session.FindSessionReadOnly(s.Base, s.SessionID)
	if e != nil {
		t.Fatal(e)
	}
	literal := drLiteral(t, m, disk)
	if !strings.Contains(literal, "REVIEW-OBSERVED-LITERAL") {
		t.Fatalf("LOST_SERVER_OBSERVED_PARTIAL: client received it before crash; recovered transcript=%q", literal)
	}
}

func TestDurableReviewAttachmentFailureRetainsJournal(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	u := vaNewControllerUpstream()
	u.onMinutes = func(context.Context) error { u.events <- vaEndEvent([]string{"REVIEW-DECISION"}, nil); return nil }
	c, made := vaControllerCall(t, f, u)
	cl := f.dial(made, f.owner(), true)
	vaWaitBrief(t, f, u)
	u.events <- voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "attachment-fail", Text: "ONLY-DURABLE-IN-JOURNAL", Final: true}
	cl.await("observed", func(v map[string]any) bool { return v["text"] == "ONLY-DURABLE-IN-JOURNAL" })
	// A non-directory causes real PutRef I/O to fail without filling the shared disk.
	blocked := filepath.Join(t.TempDir(), "blocked")
	store, err := attachment.New(blocked)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(blocked); err != nil {
		t.Fatal(err)
	}
	if e := os.WriteFile(blocked, []byte("not a directory"), 0600); e != nil {
		t.Fatal(e)
	}
	f.mgr.attachStore = store
	cl.hangup()
	vaWaitDone(t, c)
	disk := vaDisk(t, f)
	transcriptAttached := false
	for _, msg := range vpSavedVoice(disk, "minutes") {
		for _, v := range msg.Content {
			if v.Filename == "transcript.md" {
				transcriptAttached = true
			}
		}
	}
	journals := drJournals(t, f.mgr.sessionBaseDir)
	t.Logf("minutes=%d transcript_attached=%v remaining_journals=%d logs=%s", len(vpSavedVoice(disk, "minutes")), transcriptAttached, len(journals), f.log.String())
	if !transcriptAttached && len(journals) == 0 {
		t.Fatal("LAST_DURABLE_COPY_DELETED: PutRef failed, minutes saved without transcript, journal removed")
	}
}

func TestDurableReviewLateFinalHasNoWake(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	u := vaNewControllerUpstream()
	u.onMinutes = func(context.Context) error { u.events <- vaEndEvent(nil, nil); return nil }
	u.onClose = func(context.Context, string) error {
		u.events <- voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "late-decision", Text: "LATE-DECISION-AFTER-MINUTES", Final: true}
		return nil
	}
	c, made := vaControllerCall(t, f, u)
	cl := f.dial(made, f.owner(), true)
	vaWaitBrief(t, f, u)
	cl.hangup()
	vaWaitDone(t, c)
	disk := vaDisk(t, f)
	mins := vpSavedVoice(disk, "minutes")
	if len(mins) != 1 || !strings.Contains(drLiteral(t, f.mgr, disk), "LATE-DECISION-AFTER-MINUTES") {
		t.Fatal("invalid late-final control")
	}
	t.Logf("minutes=%s wakes=%d", vpJSON(mins), len(vpSavedVoice(disk, "minutes_wake")))
	if mins[0].Custom["wake"] != true {
		t.Fatal("LATE_TRANSCRIPT_NOT_PROCESSED: transcript survived but existing empty minutes cause no wake")
	}
	if !strings.Contains(vpJSON(mins), voiceLateAfterMinutesOpen) {
		t.Fatalf("late turns must leave a Still open item: %s", vpJSON(mins))
	}
	pollUntil(t, vpDeadline, "late-final wake saved", func() bool { return len(vpSavedVoice(vaDisk(t, f), "minutes_wake")) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("late-final wake did not settle")
	}
	// Saving the same call again wakes nothing more.
	if _, err := f.mgr.saveVoiceMinutes(f.sess, voiceCallEnd{callID: c.id}); err != nil {
		t.Fatal(err)
	}
	vpStable(t, func() bool { return len(vpSavedVoice(vaDisk(t, f), "minutes_wake")) == 1 }, "the late-final wake ran twice")
}
