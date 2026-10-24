package session

import (
	"encoding/json"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestDeepCopyMessageIsolatesRateLimit(t *testing.T) {
	m := core.AgentMessage{Message: core.Message{Role: "assistant", RateLimit: &core.RateLimit{FiveHourUtil: 0.25, FiveHourResetAt: 1790000000}}}
	cp := DeepCopyMessage(m)
	cp.RateLimit.FiveHourUtil = 0.9
	if m.RateLimit.FiveHourUtil != 0.25 {
		t.Fatal("copy shares the quota with the original")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back core.AgentMessage
	if err := json.Unmarshal(raw, &back); err != nil || back.RateLimit == nil || back.RateLimit.FiveHourResetAt != 1790000000 {
		t.Fatalf("round trip lost the quota: %v %+v", err, back.RateLimit)
	}
}
