package antigravity

// REVIEW5 (worktree only): every moa level on every model reaches agy as a
// level that model offers, or none for an orchestrator; never an error.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestReview5MappingEveryModelEveryLevel(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "plain")
	want := map[string]map[string]string{
		"neon":         {"off": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh"},
		"carbon":       {"off": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "high"},
		"orchestrator": {"off": "", "low": "", "medium": "", "high": "", "xhigh": ""},
	}
	var msgs []core.Message
	for _, model := range []string{"neon", "carbon", "orchestrator", "neon"} {
		for _, level := range core.ThinkingLevels {
			msgs = append(msgs, core.NewUserMessage(model+"/"+level))
			req := request("map1", msgs...)
			req.Model.ID = "agy." + model
			req.Options.ThinkingLevel = level
			ch, err := p.Stream(context.Background(), req)
			if err != nil {
				t.Fatalf("%s/%s: %v", model, level, err)
			}
			reply, _ := drain(t, ch)
			msgs = append(msgs, *reply)
			lines := strings.Split(strings.TrimSpace(readWire(wire)), "\n")
			var st string
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.HasPrefix(lines[i], `{"state":`) {
					st = lines[i]
					break
				}
			}
			if !strings.Contains(st, fmt.Sprintf(`"model":"%s"`, model)) {
				t.Errorf("%s/%s: agy state %s", model, level, st)
			}
			if w := want[model][level]; w != "" && !strings.Contains(st, fmt.Sprintf(`"level":"%s"`, w)) {
				t.Errorf("%s/%s: want level %s, agy state %s", model, level, w, st)
			}
		}
	}
	if strings.Contains(readWire(wire), `"error"`) {
		t.Errorf("agy answered an error somewhere")
	}
	c := loadModelsCache()
	t.Logf("cached levels: %v", c.Levels)
}

func TestReview5OldAndCorruptCache(t *testing.T) {
	for name, content := range map[string]string{
		"old":     `{"models":[{"id":"agy.neon","provider":"antigravity","name":"Neon (Antigravity)"},{"id":"agy.carbon","provider":"antigravity","name":"Carbon (Antigravity)"}],"thought_levels":["Low","Medium","High","Extra"]}`,
		"corrupt": `{"models":[{"id":`,
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := startFake(t)
			t.Setenv("FAKE_AGY_MODE", "plain")
			if err := os.WriteFile(modelsPath(), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			LoadCachedModels()
			t.Logf("before open: carbon efforts=%q", core.ReasoningEffortsForModel(core.Model{ID: "agy.carbon", Provider: ProviderName}))
			req := request("c-"+name, core.NewUserMessage("A"))
			req.Options.ThinkingLevel = "xhigh"
			ch, err := p.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			drain(t, ch)
			got := core.ReasoningEffortsForModel(core.Model{ID: "agy.carbon", Provider: ProviderName})
			if strings.Join(got, ",") != "Low,Medium,High" {
				t.Errorf("after open: carbon efforts=%q", got)
			}
			b, _ := os.ReadFile(modelsPath())
			if !strings.Contains(string(b), `"levels"`) {
				t.Errorf("cache not rewritten in the new format: %s", b)
			}
		})
	}
}
