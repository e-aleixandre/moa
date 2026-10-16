package serve

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestDurableReviewRecoveryCrashChild(t *testing.T) {
	manifest := os.Getenv("MOA_REVIEW_RECOVERY_MANIFEST")
	if manifest == "" {
		t.Skip("subprocess only")
	}
	b, e := os.ReadFile(manifest)
	if e != nil {
		t.Fatal(e)
	}
	var s vwCrashState
	if e = json.Unmarshal(b, &s); e != nil {
		t.Fatal(e)
	}
	t.Setenv("MOA_CONFIG_DIR", filepath.Join(s.Root, "config"))
	phase := os.Getenv("MOA_REVIEW_RECOVERY_CRASH")
	p := newMockProvider(func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if !strings.Contains(vpLastUser(req), "<voice_minutes>") {
			t.Fatal("invalid wake request")
		}
		if phase == "after_saved_wake" {
			pollUntil(t, vpDeadline, "wake persisted before crash", func() bool {
				d, _, err := session.FindSessionReadOnly(s.Base, s.SessionID)
				return err == nil && len(vpSavedVoice(d, "minutes_wake")) == 1 && d.Metadata[session.MetaVoiceWake] != true
			})
			os.Exit(0)
		}
		return simpleResponse("RECOVERED"), nil
	})
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	m := NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) {
			if phase == "before_resume" {
				os.Exit(0)
			}
			return p, nil
		},
		DefaultModel: core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"}, WorkspaceRoot: s.Workspace, SessionBaseDir: s.Base,
		MoaCfg: cfg, ConfigLoader: isolatedTestConfigLoader(t, cfg), SchedulePath: filepath.Join(t.TempDir(), "schedules.json"),
	})
	defer m.Shutdown()
	if phase == "after_saved_wake" {
		sess, ok := m.Get(s.SessionID)
		if !ok {
			t.Fatal("recovery did not resume session")
		}
		pollUntil(t, vpDeadline, "wake reached agent before flush", func() bool {
			for _, msg := range sess.runtime.Context().Agent.Messages() {
				if msg.Custom["kind"] == "minutes_wake" {
					return true
				}
			}
			return false
		})
		if err := sess.runtime.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	<-time.After(vpDeadline)
	t.Fatal("recovery checkpoint not reached")
}
func drRecoveryCrash(t *testing.T, s vwCrashState, phase string) {
	t.Helper()
	b, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "recovery-state.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDurableReviewRecoveryCrashChild$", "-test.timeout=15s")
	cmd.Env = append(os.Environ(), "MOA_REVIEW_RECOVERY_MANIFEST="+path, "MOA_REVIEW_RECOVERY_CRASH="+phase)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("recovery child %s: %v\n%s", phase, e, out)
	}
}
func TestDurableReviewRepeatedInterruptedRecovery(t *testing.T) {
	for _, initial := range []string{"observed_final", "record_before_wake"} {
		t.Run(initial, func(t *testing.T) {
			s := drCrash(t, initial)
			for i := 0; i < 3; i++ {
				drRecoveryCrash(t, s, "before_resume")
			}
			drRecoveryCrash(t, s, "after_saved_wake")
			for boot := 0; boot < 3; boot++ {
				m, p, stop := vwRestart(t, s, simpleResponseHandler("UNEXPECTED"))
				_ = m
				disk, _, e := session.FindSessionReadOnly(s.Base, s.SessionID)
				if e != nil {
					t.Fatal(e)
				}
				if len(vpSavedVoice(disk, "minutes")) != 1 || len(vpSavedVoice(disk, "minutes_wake")) != 1 || disk.Metadata[session.MetaVoiceWake] == true || p.calls.Load() != 0 {
					t.Fatalf("boot=%d minutes=%d wakes=%d marker=%v calls=%d", boot, len(vpSavedVoice(disk, "minutes")), len(vpSavedVoice(disk, "minutes_wake")), disk.Metadata[session.MetaVoiceWake], p.calls.Load())
				}
				t.Logf("boot=%d one durable wake, no re-admission; remaining journals=%d", boot, len(drJournals(t, s.Base)))
				stop()
			}
		})
	}
}
