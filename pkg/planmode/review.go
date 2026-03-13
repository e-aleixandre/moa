package planmode

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
)

// ReviewConfig configures the plan reviewer subagent.
type ReviewConfig struct {
	ProviderFactory func(core.Model) (core.Provider, error)
	Model           core.Model
	ThinkingLevel   string
	ParentTools     *core.Registry // read-only tools extracted from here
}

// ReviewResult holds the structured output from a plan review.
type ReviewResult struct {
	Approved bool
	Feedback string
	Round    int // review round number
}

// ReviewStreamFunc is called with text deltas during review.
type ReviewStreamFunc func(delta string)

// Review runs an in-process subagent to review the plan file.
// The reviewer gets read-only tools (read, grep, find, ls) and a focused
// system prompt. Returns the verdict and full feedback text.
// If onStream is non-nil, it receives text deltas during review.
func Review(ctx context.Context, cfg ReviewConfig, planPath string, onStream ReviewStreamFunc) (ReviewResult, error) {
	if cfg.ProviderFactory == nil {
		return ReviewResult{}, errNoProvider
	}
	provider, err := cfg.ProviderFactory(cfg.Model)
	if err != nil {
		return ReviewResult{}, err
	}

	// Build read-only tool registry from parent tools.
	reviewReg := core.NewRegistry()
	for _, name := range []string{"read", "grep", "find", "ls"} {
		if t, ok := cfg.ParentTools.Get(name); ok {
			reviewReg.Register(t)
		}
	}

	child, err := agent.New(agent.AgentConfig{
		Provider:            provider,
		Model:               cfg.Model,
		SystemPrompt:        reviewerPrompt,
		ThinkingLevel:       cfg.ThinkingLevel,
		Tools:               reviewReg,
		MaxTurns:            15,
		MaxToolCallsPerTurn: 20,
		MaxRunDuration:      5 * time.Minute,
		Compaction:          &core.CompactionSettings{Enabled: false},
	})
	if err != nil {
		return ReviewResult{}, err
	}

	// Subscribe to streaming events for live output.
	if onStream != nil {
		child.Subscribe(func(e core.AgentEvent) {
			if e.Type == core.AgentEventMessageUpdate && e.AssistantEvent != nil {
				if e.AssistantEvent.Type == core.ProviderEventTextDelta {
					onStream(e.AssistantEvent.Delta)
				}
			}
		})
	}

	msgs, err := child.Run(ctx, "Review the implementation plan at: "+planPath)
	if err != nil {
		return ReviewResult{}, err
	}

	text := extractFinalText(msgs)
	return parseVerdict(text), nil
}

// extractFinalText returns the text content of the last assistant message.
func extractFinalText(msgs []core.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		var parts []string
		for _, c := range msgs[i].Content {
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

// verdictRegex matches the strict "VERDICT: APPROVED" or "VERDICT: CHANGES_REQUESTED" format.
var verdictRegex = regexp.MustCompile(`(?m)^VERDICT:\s*(APPROVED|CHANGES_REQUESTED)\s*$`)

// verdictFallbackApproved matches a line that is exactly "APPROVED" (with optional surrounding punctuation).
var verdictFallbackApproved = regexp.MustCompile(`(?im)^\**\s*(?:VERDICT:?\s*)?APPROVED\s*\**$`)

// verdictFallbackChanges matches "CHANGES REQUESTED" or "CHANGES_REQUESTED" as a full line.
var verdictFallbackChanges = regexp.MustCompile(`(?im)^\**\s*(?:VERDICT:?\s*)?CHANGES[_ ]REQUESTED\s*\**$`)

// parseVerdict extracts the reviewer's verdict from review text.
// Tries strict "VERDICT: ..." format first, falls back to full-line regex matching.
func parseVerdict(text string) ReviewResult {
	result := ReviewResult{Feedback: text}

	// Strict format: "VERDICT: APPROVED" or "VERDICT: CHANGES_REQUESTED" on its own line.
	if m := verdictRegex.FindStringSubmatch(text); len(m) == 2 {
		result.Approved = m[1] == "APPROVED"
		return result
	}

	// Fallback: full-line matching in the last 10 lines.
	lines := strings.Split(text, "\n")
	start := len(lines) - 10
	if start < 0 {
		start = 0
	}
	tail := strings.Join(lines[start:], "\n")
	if verdictFallbackChanges.MatchString(tail) {
		result.Approved = false
	} else if verdictFallbackApproved.MatchString(tail) {
		result.Approved = true
	}
	// Ambiguous: defaults to Approved=false (conservative).
	return result
}

// ReviewCode runs an in-process subagent to review code changes.
// The reviewer gets read-only tools and a focused system prompt.
// Blocks until the review is complete.
func ReviewCode(ctx context.Context, cfg ReviewConfig, summary string, filesChanged []string) (ReviewResult, error) {
	if cfg.ProviderFactory == nil {
		return ReviewResult{}, errNoProvider
	}
	provider, err := cfg.ProviderFactory(cfg.Model)
	if err != nil {
		return ReviewResult{}, err
	}

	reviewReg := core.NewRegistry()
	for _, name := range []string{"read", "grep", "find", "ls"} {
		if t, ok := cfg.ParentTools.Get(name); ok {
			reviewReg.Register(t)
		}
	}

	child, err := agent.New(agent.AgentConfig{
		Provider:            provider,
		Model:               cfg.Model,
		SystemPrompt:        codeReviewerPrompt,
		ThinkingLevel:       cfg.ThinkingLevel,
		Tools:               reviewReg,
		MaxTurns:            15,
		MaxToolCallsPerTurn: 20,
		MaxRunDuration:      5 * time.Minute,
		Compaction:          &core.CompactionSettings{Enabled: false},
	})
	if err != nil {
		return ReviewResult{}, err
	}

	var fileList strings.Builder
	for _, f := range filesChanged {
		fmt.Fprintf(&fileList, "- %s\n", f)
	}

	userMsg := fmt.Sprintf("Review these code changes.\n\nSummary: %s\n\nFiles changed:\n%s\nRead the files listed above and review the changes.", summary, fileList.String())

	msgs, err := child.Run(ctx, userMsg)
	if err != nil {
		return ReviewResult{}, err
	}

	text := extractFinalText(msgs)
	return parseVerdict(text), nil
}

var errNoProvider = &reviewError{"review: provider factory not configured"}

type reviewError struct{ msg string }

func (e *reviewError) Error() string { return e.msg }
