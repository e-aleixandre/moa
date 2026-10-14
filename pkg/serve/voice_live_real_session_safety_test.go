package serve

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestVoiceLiveRealSessionDumpRespectsProtectedRoots(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := session.NewFileStore(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Messages = []core.AgentMessage{core.WrapMessage(core.NewUserMessage("safe synthetic fixture"))}
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"incoming HOME", "incoming override", "temporary output"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			root := t.TempDir()
			env := []string{
				"HOME=" + home,
				"TMPDIR=" + t.TempDir(),
				"MOA_VOICE_LIVE_SESSIONS_DIR=" + dir,
				"MOA_VOICE_LIVE_SESSION_ID=" + saved.ID,
			}
			switch name {
			case "incoming HOME":
				root = filepath.Join(home, ".config", "moa")
			case "incoming override":
				env = append(env, "MOA_CONFIG_DIR="+root)
			}
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			dump := filepath.Join(root, "tail.json")
			before := []byte("keep this file unchanged")
			if err := os.WriteFile(dump, before, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestVoiceLiveInputFitsARealSession$", "-test.count=1", "-test.v")
			cmd.Env = append(env, "MOA_VOICE_LIVE_DUMP="+dump)
			out, runErr := cmd.CombinedOutput()
			got, err := os.ReadFile(dump)
			if err != nil {
				t.Fatal(err)
			}
			if name != "temporary output" {
				if runErr == nil || !strings.Contains(string(out), "testenv:") || !strings.Contains(string(out), dump) {
					t.Errorf("protected dump must fail at the path guard: %v\n%s", runErr, out)
				}
				if !bytes.Equal(got, before) {
					t.Errorf("protected dump was overwritten: %q", got)
				}
				return
			}
			if runErr != nil {
				t.Fatalf("temporary dump: %v\n%s", runErr, out)
			}
			var items []map[string]string
			if err := json.Unmarshal(got, &items); err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0]["role"] != "user" || items[0]["text"] != "safe synthetic fixture" {
				t.Fatalf("temporary dump = %s", got)
			}
		})
	}
}
