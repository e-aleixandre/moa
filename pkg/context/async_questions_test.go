package agentcontext

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestSystemPromptOpenQuestionsSectionOnlyForAsyncSessions(t *testing.T) {
	tools := []core.ToolSpec{{Name: "ask_user", Description: "ask"}, {Name: "read", Description: "read"}}
	const heading = "# Open questions"

	got := BuildSystemPrompt(SystemPromptOptions{Tools: tools, AsyncQuestions: true})
	if !strings.Contains(got, heading) {
		t.Fatal("an async session's prompt has no rule for open questions")
	}
	for _, want := range []string{"(1)", "(5)", "If you cannot show all five", "end your turn", "never authorizes starting anything important"} {
		if !strings.Contains(got, want) {
			t.Errorf("the open-questions rule lost %q", want)
		}
	}

	if got := BuildSystemPrompt(SystemPromptOptions{Tools: tools}); strings.Contains(got, heading) {
		t.Error("a session whose ask_user blocks got the open-questions rule")
	}
	// Without ask_user there is nothing to wait for, whatever the flag says.
	if got := BuildSystemPrompt(SystemPromptOptions{Tools: tools[1:], AsyncQuestions: true}); strings.Contains(got, heading) {
		t.Error("a prompt without ask_user got the open-questions rule")
	}
}
