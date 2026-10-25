package voice

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestChatGPTSessionModelSwitch(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	session := func() (map[string]any, string) {
		b, mode := chatGPTSessionBody("sdp", "i", "v")
		var m struct{ Session map[string]any }
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m.Session, mode
	}
	s, mode := session()
	if s["model"] != chatGPTVoiceModel || mode != chatGPTVoiceModel {
		t.Fatalf("default: %v %s", s["model"], mode)
	}
	if err := os.WriteFile(core.ConfigSubdir("voice-model-server-default"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, mode = session()
	if _, ok := s["model"]; ok || mode != "server-default" {
		t.Fatalf("switch: %v %s", s, mode)
	}
}

func TestChatGPTSessionModelSwitchRemovedAndDirectory(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	path := core.ConfigSubdir("voice-model-server-default")
	model := func() (any, string) {
		b, mode := chatGPTSessionBody("sdp", "i", "v")
		var m struct{ Session map[string]any }
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m.Session["model"], mode
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if m, _ := model(); m != nil {
		t.Fatalf("file present: model %v", m)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if m, mode := model(); m != chatGPTVoiceModel || mode != chatGPTVoiceModel {
		t.Fatalf("removed: %v %s", m, mode)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if m, mode := model(); m != chatGPTVoiceModel || mode != chatGPTVoiceModel {
		t.Fatalf("directory: %v %s", m, mode)
	}
}
