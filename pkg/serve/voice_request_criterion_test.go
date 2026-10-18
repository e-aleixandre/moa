package serve

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/owner"
)

// The owner's criterion for calling (16/10) is what decides when an owner
// calls: it is in request_call's description and in the owner's own prompt.
// The server still enforces level, hours and pause.
func TestRequestCall_CriterionInDescriptionAndOwnerPrompt(t *testing.T) {
	want := []string{
		"like a PM who directs developers and keeps the person in charge informed",
		"a blocker that needs their decision",
		"a pile of news they should know",
		"something has to be deployed",
		"an important feature is finished",
		"Do not call every time a turn ends",
		"Everything else goes in writing",
		"urgent only for an urgent blocker or something broken",
		"A call never authorizes anything",
		"deployments and permissions still need the user's tap on screen",
	}
	desc := (&Manager{}).newRequestCallTool("s", nil, false).Description
	prompt := owner.RolePrompt("Infra", "main", "")
	for _, w := range want {
		if !strings.Contains(desc, w) {
			t.Errorf("request_call description lacks %q", w)
		}
		if !strings.Contains(prompt, w) {
			t.Errorf("owner prompt lacks %q", w)
		}
	}
	if strings.Contains(desc, "Never for reports") {
		t.Error("the description still forbids calling about news")
	}
}
