package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
