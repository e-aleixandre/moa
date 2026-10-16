package serve

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

type vwCrashState struct {
	Root, Base, Workspace, SessionID, WakeID              string
	Queued, DiskMinutes, DiskWake, DiskRelay, DiskStopped int
	// Set by the wake_saved_* phases: saves that held the wake with the
	// marker still set, whether the marker was on disk while the wake was
	// owed, and whether it was still on disk at the checkpoint.
	Violations             int64
	MarkedOwed, MarkedLast bool
}

// vwCrash writes the checkpoint and exits without shutdown, cleanup or a
// final flush: a process crash at this point.
func vwCrash(t *testing.T, f *vpFixture, wakeID string) {
	vwCrashWith(t, f, wakeID, nil)
}

func vwCrashWith(t *testing.T, f *vpFixture, wakeID string, edit func(*vwCrashState)) {
	t.Helper()
	saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := vwCrashState{Root: f.root, Base: f.mgr.sessionBaseDir, Workspace: f.mgr.workspaceRoot, SessionID: f.sess.ID, WakeID: wakeID,
		DiskMinutes: len(vpSavedVoice(saved, "minutes")), DiskWake: len(vpSavedVoice(saved, "minutes_wake")), DiskRelay: len(vpSavedVoice(saved, "relay")), DiskStopped: len(vpSavedVoice(saved, "minutes_wake_stopped")),
		MarkedLast: saved.Metadata[session.MetaVoiceWake] == true}
	for _, q := range f.sess.runtime.Context().Agent.PendingSteers() {
		if voiceDeliveryID(q.Custom) == wakeID {
			s.Queued++
		}
	}
	if edit != nil {
		edit(&s)
	}
	data, _ := json.Marshal(s)
	if err := os.WriteFile(os.Getenv("MOA_VOICE_WAKE_MANIFEST"), data, 0600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

// TestVoiceWakeCrashChild only runs as the child process of
// TestVoiceWakeSurvivesCrash. Stores, bus and runtime are real; the doubles
// are the voice and text providers of the F1 fixture.
func TestVoiceWakeCrashChild(t *testing.T) {
	phase := os.Getenv("MOA_VOICE_WAKE_PHASE")
	if phase == "" {
		t.Skip("child process of TestVoiceWakeSurvivesCrash")
	}
	release := make(chan struct{})
	h := func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
		if strings.Contains(vpLastUser(r), "VOICE-WAKE-HOLD-RUN") {
			select {
			case <-release:
				return simpleResponse("HELD-RUN-DONE"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return simpleResponse("BRIEF-READY"), nil
	}
	f := vpNewFixture(t, "grok-plan", h, h, h, h, h, h)
	if phase == "wake_saved_idle" || phase == "wake_saved_busy" {
		vwWakeSaved(t, f, phase, release)
		return
	}
	if strings.HasPrefix(phase, "race_") {
		vwRaceChild(t, f, phase)
		return
	}
	if phase == "between_record_and_wake" {
		f.sess.runtime.Bus.Drain(2 * time.Second)
		if err := f.sess.runtime.Flush(); err != nil {
			t.Fatal(err)
		}
		f.sess.runtime.Bus.Drain(2 * time.Second)
		// The first save after this point is the minutes record's: crash
		// right after it, before the wake is admitted.
		f.sess.persister.onSaved = func() { vwCrash(t, f, voiceWakeID("vw-between/minutes")) }
		c := &voiceCall{reg: &voiceCallRegistry{mgr: f.mgr}, id: "vw-between", sessionID: f.sess.ID,
			provider: voice.GrokPlan, started: time.Now(), minutes: &voiceMinutes{decided: []string{"RECOVER-THIS-DECISION"}}}
		c.persistMinutes("hangup")
		t.Fatal("checkpoint was not reached")
	}
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	if _, _, _, err := f.mgr.Send(f.sess.ID, "VOICE-WAKE-HOLD-RUN", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, vpDeadline, "held run", func() bool { return sessState(f.sess) == StateRunning })
	if phase == "busy_relay" || phase == "canceled_relay" {
		p.tool(t, "relay", "pass_to_session", map[string]any{"summary": "RECOVER-THIS-RELAY"})
		result := p.result(t, "relay")
		delivery, _ := result["delivery_id"].(string)
		if result["status"] != "delivered" || result["queued"] != true || delivery == "" {
			t.Fatalf("relay was not accepted as queued: %v", result)
		}
		if phase == "canceled_relay" {
			// Stop drops the queued relay with the run: it is no longer owed.
			if err := f.mgr.Cancel(f.sess.ID); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(vpDeadline)
			for time.Now().Before(deadline) {
				saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
				if err == nil && saved.Metadata[session.MetaVoiceWake] != true && saved.Metadata[session.MetaVoiceRelays] == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		// Crash right after the reply: no Flush, no shutdown. Whatever makes
		// the relay survive was saved before the spokesperson heard
		// "delivered".
		vwCrash(t, f, delivery)
	}
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"RECOVER-THIS-DECISION"}, "passed": []string{}, "open": []string{}})
	c.hangup()
	wakeID := voiceWakeID(c.c.ID + "/minutes")
	pollUntil(t, vpDeadline, "wake queued as a steer", func() bool {
		for _, q := range f.sess.runtime.Context().Agent.PendingSteers() {
			if voiceDeliveryID(q.Custom) == wakeID {
				return true
			}
		}
		return false
	})
	if phase == "stopped_wake" {
		// Stop drops the queued wake with the run; the owner saw the minutes
		// and chose to stop what they set off.
		if err := f.mgr.Cancel(f.sess.ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(vpDeadline)
		for time.Now().Before(deadline) {
			saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
			if err == nil && len(vpSavedVoice(saved, "minutes_wake_stopped")) == 1 && saved.Metadata[session.MetaVoiceWake] != true {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		vwCrash(t, f, wakeID)
	}
	if err := f.sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	vwCrash(t, f, wakeID)
}

// vwWakeSaved runs a call to its minutes wake and checks every save of the
// session: the one that holds the wake must not carry the marker any more.
func vwWakeSaved(t *testing.T, f *vpFixture, phase string, release chan struct{}) {
	f.sess.runtime.Bus.Drain(2 * time.Second)
	var violations atomic.Int64
	prev := f.sess.persister.onSaved
	f.sess.persister.onSaved = func() {
		if prev != nil {
			prev()
		}
		saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
		if err == nil && len(vpSavedVoice(saved, "minutes_wake")) > 0 && saved.Metadata[session.MetaVoiceWake] == true {
			violations.Add(1)
		}
	}
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	if phase == "wake_saved_busy" {
		if _, _, _, err := f.mgr.Send(f.sess.ID, "VOICE-WAKE-HOLD-RUN", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		pollUntil(t, vpDeadline, "held run", func() bool { return sessState(f.sess) == StateRunning })
	}
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"WAKE-SAVED-DECISION"}, "passed": []string{}, "open": []string{}})
	c.hangup()
	id := voiceWakeID(c.c.ID + "/minutes")
	markedOwed := false
	if phase == "wake_saved_busy" {
		pollUntil(t, vpDeadline, "wake queued as a steer", func() bool {
			for _, q := range f.sess.runtime.Context().Agent.PendingSteers() {
				if voiceDeliveryID(q.Custom) == id {
					return true
				}
			}
			return false
		})
		saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		markedOwed = saved.Metadata[session.MetaVoiceWake] == true && len(vpSavedVoice(saved, "minutes_wake")) == 0
		close(release)
	}
	// No Flush: the session's own saves must persist the wake and drop the
	// marker. The checkpoint records whatever is on disk by the deadline.
	deadline := time.Now().Add(vpDeadline)
	for time.Now().Before(deadline) {
		saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
		if err == nil && len(vpSavedVoice(saved, "minutes_wake")) == 1 && saved.Metadata[session.MetaVoiceWake] != true {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	vwCrashWith(t, f, id, func(s *vwCrashState) {
		s.Violations = violations.Load()
		s.MarkedOwed = markedOwed
	})
}

func vwRunChild(t *testing.T, phase string) vwCrashState {
	t.Helper()
	manifest := filepath.Join(t.TempDir(), "checkpoint.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestVoiceWakeCrashChild$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "MOA_VOICE_WAKE_PHASE="+phase, "MOA_VOICE_WAKE_MANIFEST="+manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var s vwCrashState
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(s.Root) })
	return s
}

func vwRestart(t *testing.T, s vwCrashState, handlers ...mockHandler) (*Manager, *mockProvider, func()) {
	t.Helper()
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	p := newMockProvider(handlers...)
	m := NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return p, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:   s.Workspace, SessionBaseDir: s.Base, MoaCfg: cfg, ConfigLoader: isolatedTestConfigLoader(t, cfg),
		SchedulePath: filepath.Join(t.TempDir(), "schedules.json"),
	})
	var once sync.Once
	stop := func() { once.Do(m.Shutdown) }
	t.Cleanup(stop)
	return m, p, stop
}

func vwSavedWakes(t *testing.T, s vwCrashState) (wakes int, marked bool) {
	return vwSaved(t, s, "minutes_wake")
}

// vwSaved counts the saved voice records of kind carrying the checkpoint's
// delivery ID, and reports the session header's voice marker.
func vwSaved(t *testing.T, s vwCrashState, kind string) (n int, marked bool) {
	t.Helper()
	saved, _, err := session.FindSessionReadOnly(s.Base, s.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range vpSavedVoice(saved, kind) {
		if voiceDeliveryID(m.Custom) == s.WakeID {
			n++
		}
	}
	return n, saved.Metadata[session.MetaVoiceWake] == true
}

// vwAssertRecoveredOnce boots a new Manager on the crashed state: the first
// boot runs the wake once without anyone opening the session, the next two
// find nothing owed.
func vwAssertRecoveredOnce(t *testing.T, s vwCrashState) {
	t.Helper()
	first := make(chan string, 1)
	woken := func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		first <- vpLastUser(req)
		return simpleResponse("WOKEN"), nil
	}
	m, p, stop := vwRestart(t, s, woken, simpleResponseHandler("EXTRA-RUN"))
	pollUntil(t, vpDeadline, "the wake ran after the restart", func() bool { return p.calls.Load() >= 1 })
	sess, ok := m.Get(s.SessionID)
	if !ok {
		t.Fatal("the session owing a wake was not loaded at startup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("wake run did not settle")
	}
	if got := <-first; !strings.Contains(got, "RECOVER-THIS-DECISION") {
		t.Fatalf("first run after restart was not the minutes wake: %q", got)
	}
	stop()
	if n, _ := vwSavedWakes(t, s); n != 1 || p.calls.Load() != 1 {
		t.Fatalf("after the first restart: saved wakes=%d provider calls=%d, want 1 and 1", n, p.calls.Load())
	}

	for restart := 2; restart <= 3; restart++ {
		m, p, stop := vwRestart(t, s)
		vpStable(t, func() bool { return p.calls.Load() == 0 }, "a later restart woke the session again")
		if _, loaded := m.Get(s.SessionID); loaded {
			t.Fatalf("restart %d left a session that owed nothing loaded", restart)
		}
		stop()
		if n, marked := vwSavedWakes(t, s); n != 1 || marked {
			t.Fatalf("restart %d: saved wakes=%d marker=%v, want 1 and cleared", restart, n, marked)
		}
	}
}

// Minutes that must wake their session wake it exactly once, even when the
// process dies after saving them and before the wake is saved, and across
// further restarts. The session is never opened by anyone after the crash.
func TestVoiceWakeSurvivesCrash(t *testing.T) {
	for _, phase := range []string{"between_record_and_wake", "busy_minutes"} {
		t.Run(phase, func(t *testing.T) {
			s := vwRunChild(t, phase)
			if s.DiskMinutes != 1 || s.DiskWake != 0 || (phase == "busy_minutes" && s.Queued != 1) {
				t.Fatalf("invalid checkpoint: %+v", s)
			}
			vwAssertRecoveredOnce(t, s)
		})
	}
}

// Stop consumes a queued minutes wake: the session is not woken at the next
// boot, across restarts, and the minutes stay saved and readable.
func TestVoiceStoppedWakeIsNotOwed(t *testing.T) {
	s := vwRunChild(t, "stopped_wake")
	if s.DiskMinutes != 1 || s.DiskWake != 0 || s.DiskStopped != 1 || s.MarkedLast {
		t.Fatalf("the stopped wake is still owed on disk: %+v", s)
	}
	for restart := 1; restart <= 2; restart++ {
		m, p, stop := vwRestart(t, s)
		vpStable(t, func() bool { return p.calls.Load() == 0 }, "a restart woke the session for minutes the owner stopped")
		if _, loaded := m.Get(s.SessionID); loaded {
			t.Fatalf("restart %d loaded a session that owed nothing", restart)
		}
		stop()
		if n, marked := vwSaved(t, s, "minutes_wake_stopped"); n != 1 || marked {
			t.Fatalf("restart %d: stopped records=%d marker=%v, want 1 and cleared", restart, n, marked)
		}
	}
	// The minutes are still there for whoever opens the conversation, and the
	// stop record is not: nothing of it reaches /messages or read_session.
	m, _, _ := vwRestart(t, s)
	sess, err := m.ResumeSession(s.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := m.conversationSnapshot(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	minutes := 0
	for _, msg := range snap.messages {
		if msg.Kind == "minutes" {
			minutes++
		}
		if msg.Kind == "minutes_wake_stopped" {
			t.Fatal("the stop record is shown in the conversation")
		}
	}
	if minutes != 1 {
		t.Fatalf("minutes in the conversation = %d, want 1", minutes)
	}
	if text := voiceResultText(m.readSession(sess.ID, sessionReadOptions{})); strings.Contains(text, "minutes_wake_stopped") {
		t.Fatalf("read_session shows the stop record:\n%s", text)
	}
}

// The marker is dropped by the save that holds the wake, idle (the wake's
// run) or busy (the run the wake was steered into), so a later boot loads
// nothing.
func TestVoiceWakeMarkerClearedWhenWakeSaved(t *testing.T) {
	for _, phase := range []string{"wake_saved_idle", "wake_saved_busy"} {
		t.Run(phase, func(t *testing.T) {
			s := vwRunChild(t, phase)
			if s.DiskMinutes != 1 || s.DiskWake != 1 {
				t.Fatalf("the wake was not saved by the checkpoint: %+v", s)
			}
			if phase == "wake_saved_busy" && !s.MarkedOwed {
				t.Fatal("the marker was not on disk while the queued wake was owed")
			}
			if s.Violations != 0 || s.MarkedLast {
				t.Fatalf("the marker outlived the saved wake: %d saves held both, marker at checkpoint=%v", s.Violations, s.MarkedLast)
			}
			m, p, stop := vwRestart(t, s)
			vpStable(t, func() bool { return p.calls.Load() == 0 }, "a boot woke a session that owed nothing")
			if _, loaded := m.Get(s.SessionID); loaded {
				t.Fatal("a boot loaded a session that owed nothing")
			}
			stop()
			if n, marked := vwSavedWakes(t, s); n != 1 || marked {
				t.Fatalf("after the boot: saved wakes=%d marker=%v, want 1 and cleared", n, marked)
			}
		})
	}
}

// A relay the spokesperson was told is delivered while the session was busy
// (queued) reaches the session exactly once after a crash, across restarts.
func TestVoiceQueuedRelaySurvivesCrash(t *testing.T) {
	s := vwRunChild(t, "busy_relay")
	if s.DiskRelay != 0 {
		t.Fatalf("invalid checkpoint, the relay was already a saved message: %+v", s)
	}
	first := make(chan string, 1)
	relayed := func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		first <- vpLastUser(req)
		return simpleResponse("RELAYED"), nil
	}
	m, p, stop := vwRestart(t, s, relayed, simpleResponseHandler("EXTRA-RUN"))
	pollUntil(t, vpDeadline, "the relay ran after the restart", func() bool { return p.calls.Load() >= 1 })
	sess, ok := m.Get(s.SessionID)
	if !ok {
		t.Fatal("the session owing a relay was not loaded at startup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("relay run did not settle")
	}
	if got := <-first; !strings.Contains(got, "RECOVER-THIS-RELAY") {
		t.Fatalf("first run after restart was not the relay: %q", got)
	}
	stop()
	if n, marked := vwSaved(t, s, "relay"); n != 1 || marked || p.calls.Load() != 1 {
		t.Fatalf("after the first restart: saved relays=%d marker=%v provider calls=%d, want 1, cleared, 1", n, marked, p.calls.Load())
	}
	for restart := 2; restart <= 3; restart++ {
		m, p, stop := vwRestart(t, s)
		vpStable(t, func() bool { return p.calls.Load() == 0 }, "a later restart relayed again")
		if _, loaded := m.Get(s.SessionID); loaded {
			t.Fatalf("restart %d left a session that owed nothing loaded", restart)
		}
		stop()
		if n, marked := vwSaved(t, s, "relay"); n != 1 || marked {
			t.Fatalf("restart %d: saved relays=%d marker=%v, want 1 and cleared", restart, n, marked)
		}
	}
}

// Ported from the Gate B review probe
// TestGateBReviewAcceptedBusyVoiceDeliverySurvivesCrash/busy_relay: after the
// crash, retrying the acknowledged delivery ID must find its admission.
func TestVoiceQueuedRelayAdmissionSurvivesCrash(t *testing.T) {
	s := vwRunChild(t, "busy_relay")
	m, p, _ := vwRestart(t, s, simpleResponseHandler("RESTORED-RUN"), simpleResponseHandler("RESTORED-RUN"))
	sess, ok := m.Get(s.SessionID)
	if !ok {
		var err error
		if sess, err = m.ResumeSession(s.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	r, dup, err := m.admitVoice(sess.ID, s.WakeID, "RETRY-OF-ACKNOWLEDGED-DELIVERY", nil, map[string]any{"source": "voice_call", "kind": "relay", "call_id": "retry"})
	if err != nil {
		t.Fatal(err)
	}
	if !dup {
		t.Fatalf("LOST_ADMISSION: acknowledged busy relay %s was forgotten after crash: receipt=%+v calls=%d", s.WakeID, r, p.calls.Load())
	}
}

// A queued relay the owner stopped with the run is not owed: a restart
// does not deliver it.
func TestVoiceCanceledRelayIsNotOwed(t *testing.T) {
	s := vwRunChild(t, "canceled_relay")
	if s.MarkedLast {
		t.Fatalf("the stopped relay is still owed on disk: %+v", s)
	}
	m, p, stop := vwRestart(t, s)
	vpStable(t, func() bool { return p.calls.Load() == 0 }, "a restart delivered a stopped relay")
	if _, loaded := m.Get(s.SessionID); loaded {
		t.Fatal("a restart loaded a session that owed nothing")
	}
	stop()
	if n, _ := vwSaved(t, s, "relay"); n != 0 {
		t.Fatalf("saved relays=%d after a restart, want 0", n)
	}
}

// vwAppendGate is the real agent except that its first AppendRecord waits
// before reaching the real one: it only orders events, so a snapshot may land
// between the start of the minutes save and the record entering the tree.
type vwAppendGate struct {
	*agent.Agent
	reached, resume chan struct{}
	armed           bool
}

func (g *vwAppendGate) AppendRecord(m core.AgentMessage) error {
	if g.armed {
		g.armed = false
		close(g.reached)
		<-g.resume
	}
	return g.Agent.AppendRecord(m)
}

// vwRaceChild saves a minutes record (wake=true) and crashes right after the
// save that holds it, before its wake is admitted. race_flush takes a real
// runtime snapshot while the record is not in the tree yet; race_gate holds
// the append without a snapshot and race_control does not hold it: the two
// orders that never lost the wake.
func vwRaceChild(t *testing.T, f *vpFixture, phase string) {
	f.sess.runtime.Bus.Drain(2 * time.Second)
	if err := f.sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	f.sess.runtime.Bus.Drain(2 * time.Second)
	callID := "vw-" + phase
	var gate *vwAppendGate
	if phase != "race_control" {
		real, ok := f.sess.runtime.Context().Agent.(*agent.Agent)
		if !ok {
			t.Fatalf("runtime agent is %T, not *agent.Agent", f.sess.runtime.Context().Agent)
		}
		gate = &vwAppendGate{Agent: real, reached: make(chan struct{}), resume: make(chan struct{}), armed: true}
		f.sess.runtime.Context().Agent = gate
	}
	f.sess.persister.onSaved = func() {
		saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
		if err != nil || len(vpSavedVoice(saved, "minutes")) != 1 {
			return // an earlier save that does not hold the record yet
		}
		vwCrash(t, f, voiceWakeID(callID+"/minutes"))
	}
	c := &voiceCall{reg: &voiceCallRegistry{mgr: f.mgr}, id: callID, sessionID: f.sess.ID,
		provider: voice.GrokPlan, started: time.Now(), minutes: &voiceMinutes{decided: []string{"RECOVER-THIS-DECISION"}}}
	go c.persistMinutes("hangup")
	if gate != nil {
		select {
		case <-gate.reached:
		case <-time.After(10 * time.Second):
			t.Fatal("persistMinutes never reached AppendRecord")
		}
		if phase == "race_flush" {
			if err := f.sess.runtime.Flush(); err != nil {
				t.Fatal(err)
			}
		}
		close(gate.resume)
	}
	time.Sleep(10 * time.Second)
	t.Fatal("the save holding the minutes never happened")
}

// A snapshot between the start of the minutes save and the record entering
// the tree must not lose the wake: the header marker is derived from the
// saved content, so the record saved afterwards still carries it.
func TestVoiceWakeSurvivesSnapshotBeforeRecord(t *testing.T) {
	for _, phase := range []string{"race_control", "race_gate", "race_flush"} {
		t.Run(phase, func(t *testing.T) {
			s := vwRunChild(t, phase)
			if s.DiskMinutes != 1 || s.DiskWake != 0 {
				t.Fatalf("invalid checkpoint: %+v", s)
			}
			if !s.MarkedLast {
				t.Fatal("the file holds minutes (wake=true) without its wake and the header marker is not set")
			}
			vwAssertRecoveredOnce(t, s)
		})
	}
}
