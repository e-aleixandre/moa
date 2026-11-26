package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// Given agy whose models offer different thought levels, when a Gemini
// session runs Carbon at high, agy is asked for Carbon's high, and the model
// list gives each model its own levels and an orchestrator none (session
// ee257ca7 failed with Carbon at high).
func TestGeminiThoughtLevelsPerModel(t *testing.T) {
	mgr, sess, wire := reviewGemini(t, "plain")
	reviewSend(t, mgr, sess.ID, "first")
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)

	res := httptest.NewRecorder()
	handleListModels(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	var models []struct {
		ID               string   `json:"id"`
		ReasoningEfforts []string `json:"reasoning_efforts"`
		NoThinking       bool     `json:"no_thinking"`
	}
	if err := json.NewDecoder(res.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range models {
		got[m.ID] = strings.Join(m.ReasoningEfforts, ",")
		if m.NoThinking {
			got[m.ID] = "none"
		}
	}
	for id, want := range map[string]string{"agy.neon": "Low,Medium,High,Extra", "agy.carbon": "Low,Medium,High", "agy.orchestrator": "none"} {
		if got[id] != want {
			t.Errorf("%s levels = %q, want %q", id, got[id], want)
		}
	}

	if err := sess.runtime.Bus.Execute(bus.SwitchModel{ModelSpec: "agy.carbon", Thinking: "high"}); err != nil {
		t.Fatal(err)
	}
	reviewSend(t, mgr, sess.ID, "second")
	reviewWaitIdle(t, mgr, sess.ID, 10*time.Second)
	if info := sess.info(); info.Error != "" {
		t.Fatalf("turn failed: %s", info.Error)
	}
	b, _ := os.ReadFile(wire)
	w := string(b)
	if last := w[strings.LastIndex(w, `{"state":`):]; !strings.HasPrefix(last, `{"state":{"level":"high","model":"carbon"}}`) {
		t.Errorf("agy state at the second prompt: %s", strings.SplitN(last, "\n", 2)[0])
	}
	if strings.Contains(w, `"value":"xhigh"`) {
		t.Error("agy was asked for xhigh")
	}
}
