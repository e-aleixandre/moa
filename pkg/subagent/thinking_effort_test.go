package subagent

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Sol maps selector positions onto shifted efforts (high -> xhigh, xhigh ->
// max). Each path must apply that mapping exactly once: a second pass would run
// the child one notch higher than requested and bill it accordingly.
func TestSubagentSolThinkingMapsOnce(t *testing.T) {
	sol, ok := core.ResolveModel("gpt-6.1-sol")
	if !ok {
		t.Fatal("gpt-6.1-sol must resolve")
	}
	opus, ok := core.ResolveModel("claude-opus-5-5")
	if !ok {
		t.Fatal("claude-opus-5-5 must resolve")
	}
	cases := []struct {
		name           string
		parent         core.Model
		parentThinking string
		loaded         ResumedTranscript
		params         map[string]any
		want           string
	}{
		{"explicit high", opus, "medium", ResumedTranscript{},
			map[string]any{"task": "x", "model": "gpt-6.1-sol", "thinking": "high"}, "xhigh"},
		{"explicit medium", opus, "medium", ResumedTranscript{},
			map[string]any{"task": "x", "model": "gpt-6.1-sol", "thinking": "medium"}, "high"},
		{"explicit xhigh", opus, "medium", ResumedTranscript{},
			map[string]any{"task": "x", "model": "gpt-6.1-sol", "thinking": "xhigh"}, "max"},
		{"resume keeps stored effort", opus, "medium",
			ResumedTranscript{Messages: priorTranscript(), Model: "gpt-6.1-sol", Thinking: "xhigh"},
			map[string]any{"task": "x", "resume": "job-1"}, "xhigh"},
		{"inherits Sol parent effort", sol, "xhigh", ResumedTranscript{},
			map[string]any{"task": "x"}, "xhigh"},
		{"Opus parent position onto Sol child", opus, "high", ResumedTranscript{},
			map[string]any{"task": "x", "model": "gpt-6.1-sol"}, "xhigh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runResumeCase(t, tc.parent, tc.parentThinking, tc.loaded, tc.params)
			if got.executeErrRes {
				t.Fatalf("execute failed: %s", got.resultText)
			}
			if got.requestThink != tc.want {
				t.Fatalf("request effort = %q, want %q", got.requestThink, tc.want)
			}
			if got.startedThink != tc.want {
				t.Fatalf("reported effort = %q, want %q", got.startedThink, tc.want)
			}
		})
	}
}
