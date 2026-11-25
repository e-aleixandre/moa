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

// Given a Gemini session, when agy reports context usage, the indicator
// shows it; Compact sends agy exactly /compact and shows a compaction; Start
// fresh opens a new agy conversation with only what follows, and the
// transcript keeps the earlier messages.
func TestGeminiContextCompactAndStartFresh(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "plain")
	reviewSend(t, mgr, sess.ID, "first message")
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)
	if info := sess.info(); info.ContextPercent != 25 || info.ContextWindow != 100000 {
		t.Fatalf("context = %d%% of %d, want 25%% of 100000", info.ContextPercent, info.ContextWindow)
	}

	if err := sess.runtime.Bus.Execute(bus.CompactSession{}); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 10*time.Second, "compaction", func() bool {
		for _, m := range sess.runtime.Context().Agent.Messages() {
			if m.Role == "compaction_summary" {
				return true
			}
		}
		return false
	})
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)
	if pct := sess.info().ContextPercent; pct != 2 {
		t.Errorf("context after compact = %d%%, want 2%%", pct)
	}
	if n := reviewPrompts(wire, `"text":"/compact"`); n != 1 {
		t.Errorf("/compact sent %d times", n)
	}

	reviewSend(t, mgr, sess.ID, "second message")
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)
	if err := sess.runtime.Bus.Execute(bus.StartFreshSession{}); err != nil {
		t.Fatalf("start fresh: %v", err)
	}
	reviewSend(t, mgr, sess.ID, "hola")
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)

	b, _ := os.ReadFile(wire)
	var last string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, `"session/prompt"`) {
			last = l
		}
	}
	if strings.Count(string(b), `"session/new"`) != 2 || !strings.Contains(last, `"prompt":[{"text":"hola","type":"text"}]`) {
		t.Errorf("start fresh: want a second session/new and a prompt of only hola, got last prompt %s", last)
	}
	for _, text := range []string{"first message", "second message"} {
		if n := reviewPrompts(wire, `"text":"`+text+`"`); n != 1 {
			t.Errorf("%q sent %d times", text, n)
		}
	}
	seen := map[string]bool{}
	for _, m := range sess.History() {
		seen[f1MsgText(m)] = true
	}
	if !seen["first message"] || !seen["second message"] || !seen["hola"] {
		t.Error("the transcript lost earlier messages")
	}
}
