package serve

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// These probes use the real controller, HTTP WS/hangup routes, runtime,
// FileStore and attachment store. Only the upstream and text model are fakes.
// No test changes production behavior or calls an external provider.
type vaControllerUpstream struct {
	events    chan voice.Event
	audio     chan []byte
	injected  chan string
	onMinutes func(context.Context) error
	onClose   func(context.Context, string) error
	once      sync.Once
}

func vaNewControllerUpstream() *vaControllerUpstream {
	return &vaControllerUpstream{events: make(chan voice.Event, 32), audio: make(chan []byte, 8), injected: make(chan string, 8)}
}
func (u *vaControllerUpstream) Events() <-chan voice.Event { return u.events }
func (u *vaControllerUpstream) SendAudio(_ context.Context, pcm []byte) error {
	u.audio <- append([]byte(nil), pcm...)
	return nil
}
func (u *vaControllerUpstream) SendToolResults(context.Context, []voice.ToolResult) error {
	return nil
}
func (u *vaControllerUpstream) InjectContext(_ context.Context, text string) error {
	u.injected <- text
	return nil
}
func (u *vaControllerUpstream) RequestMinutes(ctx context.Context) error {
	if u.onMinutes != nil {
		return u.onMinutes(ctx)
	}
	return nil
}
func (u *vaControllerUpstream) Close(ctx context.Context, reason string) error {
	var err error
	u.once.Do(func() {
		if u.onClose != nil {
			err = u.onClose(ctx, reason)
		}
		close(u.events)
	})
	return err
}
func vaEndEvent(decided, open []string) voice.Event {
	args, _ := json.Marshal(map[string]any{"decided": decided, "passed": []string{}, "open": open})
	return voice.Event{Kind: voice.EventToolBatch, Tools: []voice.ToolCall{{ID: "va-end", Name: "end_call", Arguments: args}}}
}

// Publish a local fake through the registry already owned by NewServer, so
// dial/hangup still traverse the production authenticated HTTP routes.
func vaControllerCall(t *testing.T, f *vpFixture, u *vaControllerUpstream) (*voiceCall, vpCreated) {
	t.Helper()
	f.harnessOnly = true
	f.mgr.voiceCallsMu.Lock()
	regs := append([]*voiceCallRegistry(nil), f.mgr.voiceCalls...)
	f.mgr.voiceCallsMu.Unlock()
	if len(regs) != 1 {
		t.Fatalf("control: registered voice registries=%d, want 1", len(regs))
	}
	r := regs[0]
	slot, closed := r.reserve("token")
	if closed || slot == nil {
		t.Fatal("control: fake call reservation failed")
	}
	id := newVoiceCallID()
	c := newVoiceCall(r, id, "token", f.sess.ID, voice.GrokPlan, voice.Billing{Kind: "plan_unverified"}, u)
	c.slot = slot
	if !r.register(c, nil, voiceModel()) {
		t.Fatal("control: fake call registration failed")
	}
	go c.run()
	return c, vpCreated{ID: id, Provider: "grok-plan", WSURL: "/api/voice/calls/" + id + "/ws"}
}
func vaWaitDone(t *testing.T, c *voiceCall) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(vpDeadline):
		t.Fatal("controller did not finish before deadline")
	}
}
func vaWaitBrief(t *testing.T, f *vpFixture, u *vaControllerUpstream) {
	t.Helper()
	select {
	case text := <-u.injected:
		if !strings.Contains(text, "BRIEF-READY") {
			t.Fatalf("control: unexpected brief %q", text)
		}
	case <-time.After(vpDeadline):
		t.Fatal("control: brief was not injected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("control: brief run did not settle")
	}
}
func vaDisk(t *testing.T, f *vpFixture) *session.Session {
	t.Helper()
	saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestVoiceAbruptAuditController_CloseNowCollectsMinutesWhileBusy(t *testing.T) {
	for _, kind := range []string{"decided", "open", "passed_only"} {
		t.Run(kind, func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)
			h := func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				if strings.Contains(vpLastUser(req), "VA-HOLD-RUN") {
					select {
					case <-release:
						return simpleResponse("HELD-DONE"), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return simpleResponse("BRIEF-READY"), nil
			}
			f := vpNewFixture(t, "grok-plan", h, h, h, h)
			u := vaNewControllerUpstream()
			var decided, open []string
			if kind == "decided" {
				decided = []string{"VA-DECIDED"}
			}
			if kind == "open" {
				open = []string{"VA-STILL-OPEN"}
			}
			u.onMinutes = func(context.Context) error { u.events <- vaEndEvent(decided, open); return nil }
			c, made := vaControllerCall(t, f, u)
			cl := f.dial(made, f.owner(), true)
			cl.await("listening", func(v map[string]any) bool { return v["state"] == "listening" })
			vaWaitBrief(t, f, u)
			u.events <- voice.Event{Kind: voice.EventTranscript, ItemID: "before-cut", Role: "user", Text: "VA-OBSERVED-BEFORE-CUT", Final: true}
			cl.await("transcript ingestion", func(v map[string]any) bool { return v["text"] == "VA-OBSERVED-BEFORE-CUT" })
			if _, _, _, err := f.mgr.Send(f.sess.ID, "VA-HOLD-RUN", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			pollUntil(t, vpDeadline, "busy session", func() bool { return sessState(f.sess) == StateRunning })
			if kind == "passed_only" {
				r := c.passToSession("va-pass", "VA-ALREADY-PASSED")
				if r.IsError {
					t.Fatalf("control: relay rejected: %s", r.Output)
				}
			}
			if err := cl.conn.CloseNow(); err != nil {
				t.Fatal(err)
			}
			vaWaitDone(t, c)
			saved := vaDisk(t, f) // No Flush: finishing must already have saved it.
			recs := vpSavedVoice(saved, "minutes")
			if len(recs) != 1 || c.endReason() != "client_lost" {
				t.Fatalf("abrupt close: disk minutes=%d reason=%q", len(recs), c.endReason())
			}
			if text := vpTranscriptAttachment(t, f, recs[0]); !strings.Contains(text, "VA-OBSERVED-BEFORE-CUT") {
				t.Fatalf("observed transcript missing from durable download: %q", text)
			}
			wakeCount, relayCount := 0, 0
			for _, q := range f.sess.runtime.Context().Agent.PendingSteers() {
				switch q.Custom["kind"] {
				case "minutes_wake":
					wakeCount++
				case "relay":
					relayCount++
				}
			}
			wantWake, wantRelay := 1, 0
			if kind == "passed_only" {
				wantWake, wantRelay = 0, 1
			}
			if wakeCount != wantWake || relayCount != wantRelay || len(vpSavedVoice(saved, "minutes_wake")) != 0 || saved.Metadata[session.MetaVoiceWake] != true {
				t.Fatalf("busy durable record/queue: wakes=%d relays=%d disk wakes=%d marker=%v", wakeCount, relayCount, len(vpSavedVoice(saved, "minutes_wake")), saved.Metadata[session.MetaVoiceWake])
			}
			t.Logf("CONTROL: reason=client_lost disk_minutes=1 queued_wakes=%d queued_relays=%d disk_wakes=0 marker=true", wakeCount, relayCount)
		})
	}
}

func TestVoiceAbruptAuditController_InflightAudioTranscriptBoundary(t *testing.T) {
	for _, phase := range []string{"during_minutes_control", "after_minutes_during_close", "after_minutes_budget_during_close"} {
		t.Run(phase, func(t *testing.T) {
			f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
			u := vaNewControllerUpstream()
			final := voice.Event{Kind: voice.EventTranscript, ItemID: "accepted-audio", Role: "user", Text: "VA-AUDIO-PREFIX VA-LAST-WORD-BEFORE-CUT", Final: true}
			u.onMinutes = func(context.Context) error {
				if phase == "during_minutes_control" {
					u.events <- final
				}
				if phase != "after_minutes_budget_during_close" {
					u.events <- vaEndEvent([]string{"VA-AUDIO-DECISION"}, nil)
				}
				return nil
			}
			u.onClose = func(context.Context, string) error {
				if phase != "during_minutes_control" {
					u.events <- final
				}
				return nil
			}
			c, made := vaControllerCall(t, f, u)
			cl := f.dial(made, f.owner(), true)
			cl.await("listening", func(v map[string]any) bool { return v["state"] == "listening" })
			vaWaitBrief(t, f, u)
			u.events <- voice.Event{Kind: voice.EventTranscript, ItemID: "accepted-audio", Role: "user", Text: "VA-AUDIO-PREFIX", Final: false}
			cl.await("prefix ingested", func(v map[string]any) bool { return v["text"] == "VA-AUDIO-PREFIX" })
			ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
			defer cancel()
			if err := cl.conn.Write(ctx, websocket.MessageBinary, vpPCM()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-u.audio: // Last audio reached the upstream before the client vanished.
			case <-ctx.Done():
				t.Fatal("control: pre-cut audio was not accepted")
			}
			_ = cl.conn.CloseNow()
			vaWaitDone(t, c)
			recs := vpSavedVoice(vaDisk(t, f), "minutes")
			if len(recs) != 1 || c.endReason() != "client_lost" {
				t.Fatalf("control: disk minutes=%d reason=%q", len(recs), c.endReason())
			}
			text := vpTranscriptAttachment(t, f, recs[0])
			if !strings.Contains(text, final.Text) {
				t.Fatalf("LOST_INFLIGHT_TRANSCRIPT: audio accepted before cut; normalized final delivered during %s; durable transcript=%q", phase, text)
			}
		})
	}
}

func TestVoiceAbruptAuditController_HTTPHangupPositive(t *testing.T) {
	for _, path := range []string{"http", "ws_control"} {
		t.Run(path, func(t *testing.T) {
			f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
			u := vaNewControllerUpstream()
			u.onMinutes = func(context.Context) error { u.events <- vaEndEvent(nil, nil); return nil }
			c, made := vaControllerCall(t, f, u)
			cl := f.dial(made, f.owner(), true)
			cl.await("listening", func(v map[string]any) bool { return v["state"] == "listening" })
			vaWaitBrief(t, f, u)
			if live, _ := filepath.Glob(filepath.Join(f.mgr.sessionBaseDir, "*", "*"+voiceJournalDirSuffix, "*.jsonl")); len(live) != 1 {
				t.Fatalf("control: a live call keeps one journal, found %v", live)
			}
			if path == "http" {
				cl.hangup()
			} else {
				cl.send(map[string]any{"type": "hangup"})
			}
			vaWaitDone(t, c)
			if n := len(vpSavedVoice(vaDisk(t, f), "minutes")); n != 1 || c.endReason() != "hangup" {
				t.Fatalf("normal hangup: disk minutes=%d reason=%q", n, c.endReason())
			}
			if left, _ := filepath.Glob(filepath.Join(f.mgr.sessionBaseDir, "*", "*"+voiceJournalDirSuffix, "*")); len(left) != 0 {
				t.Fatalf("a saved call left its journal: %v", left)
			}
		})
	}
}

func TestVoiceAbruptAuditController_SilentSocketLiveness(t *testing.T) {
	interval, timeout := wsPingInterval, wsPongTimeout
	wsPingInterval, wsPongTimeout = 50*time.Millisecond, 20*time.Millisecond
	defer func() { wsPingInterval, wsPongTimeout = interval, timeout }()
	vInterval, vTimeout := voicePingInterval, voicePongTimeout
	voicePingInterval, voicePongTimeout = 50*time.Millisecond, 20*time.Millisecond
	defer func() { voicePingInterval, voicePongTimeout = vInterval, vTimeout }()
	for _, route := range []string{"session_ws_control", "voice_ws", "voice_ws_reading_control"} {
		t.Run(route, func(t *testing.T) {
			f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
			f.harnessOnly = true
			if route == "session_ws_control" {
				ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
				defer cancel()
				conn, _, err := websocket.Dial(ctx, f.srv.URL+"/api/sessions/"+f.sess.ID+"/ws", &websocket.DialOptions{HTTPHeader: f.owner()})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow() //nolint:errcheck
				pollUntil(t, vpDeadline, "session socket registered", func() bool { return f.sess.wsConns.Load() == 1 })
				// An open transport whose peer never reads also never answers WS pings.
				pollUntil(t, time.Second, "silent session socket dropped", func() bool { return f.sess.wsConns.Load() == 0 })
				return
			}
			u := vaNewControllerUpstream()
			u.onMinutes = func(context.Context) error { u.events <- vaEndEvent([]string{"VA-SILENT-DECISION"}, nil); return nil }
			c, made := vaControllerCall(t, f, u)
			if route == "voice_ws_reading_control" {
				// A client that reads answers every ping and keeps its call.
				cl := f.dial(made, f.owner(), true)
				vaWaitBrief(t, f, u)
				select {
				case <-c.done:
					t.Fatalf("a live client lost its call to the heartbeat: reason=%q", c.endReason())
				case <-time.After(time.Second):
				}
				cl.hangup()
				vaWaitDone(t, c)
				return
			}
			cl := f.dial(made, f.owner(), false)
			vaWaitBrief(t, f, u)
			select {
			case <-c.done:
				if c.endReason() == "hangup" || len(vpSavedVoice(vaDisk(t, f), "minutes")) != 1 {
					t.Fatalf("silent socket must finish without explicit hangup, reason=%q", c.endReason())
				}
			case <-time.After(time.Second):
				t.Errorf("NO_VOICE_LIVENESS: peer answered no pings for 1s (session control closes with 50ms ping/20ms pong budgets); Grace=%s only applies after detach; disk_minutes=%d", f.limits.grace, len(vpSavedVoice(vaDisk(t, f), "minutes")))
			}
			// Deliberate cleanup only after the observation, never its success oracle.
			cl.hangup()
		})
	}
}

// Each checkpoint exits the actual test process without Shutdown or Flush
// after call finish. The parent opens the real on-disk state with a new Manager.
func TestVoiceAbruptAuditControllerCrashChild(t *testing.T) {
	phase := os.Getenv("MOA_VA_CONTROLLER_PHASE")
	if phase == "" {
		t.Skip("subprocess checkpoint only")
	}
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	u := vaNewControllerUpstream()
	u.onMinutes = func(context.Context) error { u.events <- vaEndEvent([]string{"VA-CRASH-DECISION"}, nil); return nil }
	c, made := vaControllerCall(t, f, u)
	if phase == "after_minutes_before_persist" {
		u.onClose = func(context.Context, string) error {
			if !c.hasMinutes() || c.endReason() != "client_lost" {
				t.Errorf("invalid checkpoint: hasMinutes=%v reason=%q", c.hasMinutes(), c.endReason())
				os.Exit(2)
			}
			vwCrash(t, f, voiceWakeID(c.id+"/minutes"))
			return nil
		}
	}
	cl := f.dial(made, f.owner(), true)
	cl.await("listening", func(v map[string]any) bool { return v["state"] == "listening" })
	vaWaitBrief(t, f, u)
	u.events <- voice.Event{Kind: voice.EventTranscript, ItemID: "crash-observed", Role: "user", Text: "VA-CRASH-OBSERVED-LITERAL", Final: true}
	cl.await("literal ingested before crash", func(v map[string]any) bool { return v["text"] == "VA-CRASH-OBSERVED-LITERAL" })
	if err := f.sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	if phase == "active_transcript" {
		vwCrash(t, f, voiceWakeID(c.id+"/minutes"))
	}
	if phase != "after_minutes_before_persist" {
		t.Fatalf("unknown checkpoint %q", phase)
	}
	_ = cl.conn.CloseNow()
	select {
	case <-c.done:
		t.Fatal("checkpoint was bypassed")
	case <-time.After(vpDeadline):
		t.Fatal("checkpoint not reached")
	}
}
func vaRunControllerCrash(t *testing.T, phase string) vwCrashState {
	t.Helper()
	manifest := filepath.Join(t.TempDir(), "checkpoint.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestVoiceAbruptAuditControllerCrashChild$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "MOA_VA_CONTROLLER_PHASE="+phase, "MOA_VOICE_WAKE_MANIFEST="+manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkpoint child failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var s vwCrashState
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(s.Root) })
	return s
}

func TestVoiceAbruptAuditController_CrashRetainsObservedCall(t *testing.T) {
	for _, phase := range []string{"active_transcript", "after_minutes_before_persist"} {
		t.Run(phase, func(t *testing.T) {
			s := vaRunControllerCrash(t, phase)
			t.Logf("checkpoint=%s disk_minutes=%d disk_wakes=%d marker=%v", phase, s.DiskMinutes, s.DiskWake, s.MarkedLast)
			m, p, stop := vwRestart(t, s, vpBriefHandlers()...)
			// Startup recovery may already have loaded and woken the session.
			sess, loaded := m.Get(s.SessionID)
			if !loaded {
				var err error
				if sess, err = m.ResumeSession(s.SessionID); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
			defer cancel()
			if !sess.runtime.WaitSettled(ctx) {
				t.Fatal("recovered session did not settle")
			}
			saved, _, err := session.FindSessionReadOnly(s.Base, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			var transcript strings.Builder
			for _, msg := range vpSavedMessages(saved) {
				for _, content := range msg.Content {
					if content.Filename != "transcript.md" || content.AttachmentID == "" {
						continue
					}
					r, _, err := m.attachStore.Open(sess.ID, content.AttachmentID)
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(r)
					_ = r.Close()
					if err != nil {
						t.Fatal(err)
					}
					transcript.Write(data)
				}
			}
			if !strings.Contains(transcript.String(), "VA-CRASH-OBSERVED-LITERAL") {
				t.Errorf("LOST_OBSERVED_CALL_AFTER_CRASH: phase=%s client had received literal transcript; resumed disk minutes=%d wakes=%d marker=%v provider_calls=%d; downloadable transcript=%q", phase, len(vpSavedVoice(saved, "minutes")), len(vpSavedVoice(saved, "minutes_wake")), saved.Metadata[session.MetaVoiceWake], p.calls.Load(), transcript.String())
			}
			if phase == "after_minutes_before_persist" && !strings.Contains(vpJSON(vpSavedVoice(saved, "minutes")), "VA-CRASH-DECISION") {
				t.Error("LOST_COMPLETED_MINUTES_AFTER_CRASH: end_call was accepted before the checkpoint; its Decided item is not recoverable on resume")
			}
			minutes := vpSavedVoice(saved, "minutes")
			if len(minutes) != 1 || minutes[0].Custom["reason"] != "server_lost" || minutes[0].Custom["wake"] != true {
				t.Fatalf("recovered minutes: n=%d record=%s", len(minutes), vpJSON(minutes))
			}
			if phase == "active_transcript" && !strings.Contains(vpJSON(minutes), voiceInterruptedOpen) {
				t.Error("minimal minutes of a lost call must stay open")
			}
			pollUntil(t, vpDeadline, "recovered wake ran", func() bool { return p.calls.Load() >= 1 })
			if !sess.runtime.WaitSettled(ctx) {
				t.Fatal("recovered wake did not settle")
			}
			calls := p.calls.Load()
			stop()
			woken, _, err := session.FindSessionReadOnly(s.Base, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(vpSavedVoice(woken, "minutes_wake")); n != 1 || woken.Metadata[session.MetaVoiceWake] == true || calls != 1 {
				t.Fatalf("recovered wake: disk wakes=%d marker=%v model calls=%d, want one wake, no marker, one run", n, woken.Metadata[session.MetaVoiceWake], calls)
			}
			// A second boot finds nothing left: the journal went with the record.
			m2, p2, stop2 := vwRestart(t, s, vpBriefHandlers()...)
			_ = m2
			stop2()
			again, _, err := session.FindSessionReadOnly(s.Base, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(vpSavedVoice(again, "minutes")) != 1 || len(vpSavedVoice(again, "minutes_wake")) != 1 || p2.calls.Load() != 0 {
				t.Fatalf("second boot: minutes=%d wakes=%d model calls=%d (first boot %d), want 1,1,0", len(vpSavedVoice(again, "minutes")), len(vpSavedVoice(again, "minutes_wake")), p2.calls.Load(), calls)
			}
		})
	}
}

func TestVoiceAbruptAuditController_SavedMarkerRecoveryPositive(t *testing.T) {
	s := vwRunChild(t, "between_record_and_wake")
	if s.DiskMinutes != 1 || s.DiskWake != 0 || !s.MarkedLast {
		t.Fatalf("invalid persisted-minutes crash checkpoint: %+v", s)
	}
	m, p, stop := vwRestart(t, s, simpleResponseHandler("RECOVERED"), simpleResponseHandler("UNEXPECTED-EXTRA"))
	sess, loaded := m.Get(s.SessionID)
	if loaded {
		t.Log("OBSERVATION: NewManager itself resumes marked sessions at startup; recovery is not deferred to a later user-initiated ResumeSession")
	}
	if !loaded {
		var err error
		if sess, err = m.ResumeSession(s.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	pollUntil(t, vpDeadline, "saved decision recovered", func() bool { return p.calls.Load() >= 1 })
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("recovery did not settle")
	}
	stop()
	if n, marker := vwSavedWakes(t, s); n != 1 || marker || p.calls.Load() != 1 {
		t.Fatalf("recovery control: disk_wakes=%d marker=%v model_calls=%d, want 1,false,1", n, marker, p.calls.Load())
	}
	t.Log("CONTROL: persisted minutes/marker survived crash; recovered once; marker cleared with saved wake")
}
