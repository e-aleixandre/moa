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
		"a deployment or a decision blocks work and the user has not answered in writing for a while",
		"something is broken in production",
		"an OK that cuts work in progress",
		"at the end of every turn",
		"or about what you already asked recently",
		"is work stopped or at risk until they answer",
		"urgent only for broken production or a real time window",
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
