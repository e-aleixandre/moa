package owner

import (
	"strings"
	"testing"
)

func TestRolePromptForbidsReaskingSessionQuestions(t *testing.T) {
	p := strings.Join(strings.Fields(RolePrompt("Winerim", "master", "")), " ")
	for _, want := range []string{
		"asking it again with ask_user would ask them twice",
		"when the book or your instructions let you decide it",
		"leave it open for the user",
		"citing the session id",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "NEVER") || strings.Contains(p, "do NOT") {
		t.Error("the prompt still shouts in capitals")
	}
}

func TestBookSectionDefersDecisionsToInstructions(t *testing.T) {
	p := BookSection("Winerim", "")
	for _, want := range []string{"Decide what your instructions", "ask the rest with ask_user"} {
		if !strings.Contains(p, want) {
			t.Errorf("book section lacks %q", want)
		}
	}
	if strings.Contains(p, "If a product decision is missing") {
		t.Error("the book section still forbids deciding")
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
