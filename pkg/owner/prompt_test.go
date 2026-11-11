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

func TestRolePromptSaysTheBookIsWrittenOnlyWithItsTool(t *testing.T) {
	p := strings.Join(strings.Fields(RolePrompt("Winerim", "master", "")), " ")
	for _, want := range []string{"written only with the book tool", "never through bash", "book-tidy"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "the book tool warns") {
		t.Error("the prompt still talks about warnings")
	}
}
