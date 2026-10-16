package serve

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

type vwCrashState struct {
	Root, Base, Workspace, SessionID, WakeID string
	Queued, DiskMinutes, DiskWake            int
}

// vwCrash writes the checkpoint and exits without shutdown, cleanup or a
// final flush: a process crash at this point.
func vwCrash(t *testing.T, f *vpFixture, wakeID string) {
	t.Helper()
	saved, _, err := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := vwCrashState{Root: f.root, Base: f.mgr.sessionBaseDir, Workspace: f.mgr.workspaceRoot, SessionID: f.sess.ID, WakeID: wakeID,
		DiskMinutes: len(vpSavedVoice(saved, "minutes")), DiskWake: len(vpSavedVoice(saved, "minutes_wake"))}
	for _, q := range f.sess.runtime.Context().Agent.PendingSteers() {
		if voiceDeliveryID(q.Custom) == wakeID {
			s.Queued++
		}
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
	h := func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
		if strings.Contains(vpLastUser(r), "VOICE-WAKE-HOLD-RUN") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return simpleResponse("BRIEF-READY"), nil
	}
	f := vpNewFixture(t, "grok-plan", h, h, h, h, h, h)
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
	if err := f.sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	vwCrash(t, f, wakeID)
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
	t.Helper()
	saved, _, err := session.FindSessionReadOnly(s.Base, s.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range vpSavedVoice(saved, "minutes_wake") {
		if voiceDeliveryID(m.Custom) == s.WakeID {
			wakes++
		}
	}
	return wakes, saved.Metadata[session.MetaVoiceWake] == true
}

// Minutes that must wake their session wake it exactly once, even when the
// process dies after saving them and before the wake is saved, and across
// further restarts. The session is never opened by anyone after the crash.
func TestVoiceWakeSurvivesCrash(t *testing.T) {
	for _, phase := range []string{"between_record_and_wake", "busy_minutes"} {
		t.Run(phase, func(t *testing.T) {
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
			if s.DiskMinutes != 1 || s.DiskWake != 0 || (phase == "busy_minutes" && s.Queued != 1) {
				t.Fatalf("invalid checkpoint: %+v", s)
			}

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
		})
	}
}
