package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactionPayload_EphemeralIsNotSerialized(t *testing.T) {
	data, err := json.Marshal(CompactionPayload{Summary: "s", Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "ephemeral") {
		t.Fatalf("Ephemeral leaked into JSON: %s", data)
	}
}
