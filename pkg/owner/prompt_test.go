package owner

import (
	"strings"
	"testing"
)

func TestRolePromptForbidsReaskingSessionQuestions(t *testing.T) {
	p := strings.Join(strings.Fields(RolePrompt("Winerim", "master", "")), " ")
	for _, want := range []string{
		"you NEVER ask it again with ask_user",
		"leave it open for the user",
		"citing the session id",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
