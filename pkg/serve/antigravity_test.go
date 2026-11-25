package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/provider/antigravity"
)

// An owner (or any other sender) writing to a Gemini session reaches agy
// word for word: an Antigravity session is an ordinary moa session.
func TestOwnerPromptReachesAntigravity(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wire := filepath.Join(dir, "wire.log")
	t.Setenv("MOA_AGY_BIN", exe)
	t.Setenv("FAKE_AGY", "1")
	t.Setenv("FAKE_AGY_LOG", wire)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := antigravity.Default("test")
	mgr := newTestManager(t, ctx, p)
	p.CWD = func(id string) string {
		if s, ok := mgr.Get(id); ok {
			return s.CWD
		}
		return ""
	}
	sess, err := mgr.CreateSession(CreateOpts{Model: "gemini", PermissionMode: "yolo"})
	if err != nil {
		t.Fatal(err)
	}
	custom := map[string]any{"source": "owner", "owner_id": "o1", "owner_name": "moa"}
	if _, _, _, err := mgr.sendValidated(sess.ID, "run the tests and report", nil, "", "", custom, false, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		b, _ := os.ReadFile(wire)
		if strings.Contains(string(b), `"text":"run the tests and report"`) {
			if !strings.Contains(string(b), `"name":"moa"`) {
				t.Fatalf("clientInfo is not moa:\n%s", b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agy never received the owner's prompt:\n%s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitIdle(t, mgr, sess.ID)
}

// Gemini keeps its own history: a session with history cannot switch between
// Gemini and another model; an empty one can.
func TestSwitchIntoOrOutOfGeminiNeedsAnEmptySession(t *testing.T) {
	mgr, sess, _ := reviewGemini(t, "plain")
	empty, err := mgr.CreateSession(CreateOpts{Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.runtime.Bus.Execute(bus.SwitchModel{ModelSpec: "gemini"}); err != nil {
		t.Fatalf("empty session could not switch to Gemini: %v", err)
	}
	reviewSend(t, mgr, sess.ID, "hello")
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)
	err = sess.runtime.Bus.Execute(bus.SwitchModel{ModelSpec: "sonnet"})
	if err == nil || !strings.Contains(err.Error(), "start a new session") {
		t.Fatalf("switch out of Gemini with history: err = %v", err)
	}
}

func TestFullAccessFollowsPermissionMode(t *testing.T) {
	mgr, sess, _ := reviewGemini(t, "plain") // yolo
	if !mgr.FullAccess(sess.ID) {
		t.Fatal("yolo session is not full access")
	}
	if err := sess.runtime.Bus.Execute(bus.SetPermissionMode{Mode: "ask"}); err != nil {
		t.Fatal(err)
	}
	if mgr.FullAccess(sess.ID) {
		t.Fatal("ask session is full access")
	}
}
