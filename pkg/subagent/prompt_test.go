package subagent

import (
	"strings"
	"testing"
	"time"

	agentcontext "github.com/e-aleixandre/moa/pkg/context"
)

// TestBuildSystemPrompt_BuilderPinsTail: siblings must reuse the parent's
// date/git snapshot. A live restamp would miss the shared GPT-5.6 prefix
// even after they share a prompt_cache_key.
func TestBuildSystemPrompt_BuilderPinsTail(t *testing.T) {
	now := time.Date(2026, 8, 30, 21, 0, 0, 0, time.UTC)
	git := "Branch: main\nLast commit: abc def"
	builder := func(opts agentcontext.SystemPromptOptions) string {
		opts.Now = now
		opts.Git = &git
		return agentcontext.BuildSystemPrompt(opts)
	}

	a := buildSystemPrompt(builder, "", nil, "/test", "", "")
	b := buildSystemPrompt(builder, "", nil, "/test", "", "")
	if a != b {
		t.Fatal("pinned builder must produce identical sibling prompts")
	}
	if !strings.Contains(a, "Current date: Sunday, August 30, 2026") {
		t.Fatalf("date line missing: %q", a[strings.LastIndex(a, "Current date:"):])
	}
	if strings.Contains(a, "21:00") || strings.Contains(a, "PM") {
		t.Fatal("time of day leaked into a sibling prompt")
	}
	if !strings.Contains(a, git) {
		t.Fatal("expected pinned git section")
	}
}
