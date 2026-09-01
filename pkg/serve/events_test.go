package serve

import (
	"encoding/json"
	"testing"
)

func TestConfigChangeDataFastUsesPresence(t *testing.T) {
	withoutFast, err := json.Marshal(ConfigChangeData{Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	var absent map[string]any
	if err := json.Unmarshal(withoutFast, &absent); err != nil {
		t.Fatal(err)
	}
	if _, ok := absent["fast"]; ok {
		t.Fatalf("unrelated config change serialized fast: %s", withoutFast)
	}

	fast := false
	note := ""
	withFast, err := json.Marshal(ConfigChangeData{Fast: &fast, FastNote: &note})
	if err != nil {
		t.Fatal(err)
	}
	var present map[string]any
	if err := json.Unmarshal(withFast, &present); err != nil {
		t.Fatal(err)
	}
	if got, ok := present["fast"]; !ok || got != false {
		t.Fatalf("explicit false fast missing from %s", withFast)
	}
	if got, ok := present["fast_note"]; !ok || got != "" {
		t.Fatalf("explicit empty fast_note missing from %s", withFast)
	}
}
