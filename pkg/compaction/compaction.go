// Package compaction summarizes old conversation turns to reduce context size.
//
// The core loop calls Compact when context approaches the model's input limit.
// Old turns are serialized, sent to the LLM for summarization, and replaced
// with a single compaction_summary message. Recent turns are kept verbatim.
package compaction

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// Result holds the outcome of a compaction.
type Result struct {
	Summary       string
	TokensBefore  int
	TokensAfter   int
	ReadFiles     []string
	ModifiedFiles []string
	Usage         *core.Usage // LLM usage for the summarization call
}

// estimatedSummaryTokens is a conservative estimate for the summary message.
const estimatedSummaryTokens = 2000

// FindCutPoint returns the index of the first message to KEEP (everything
// before it gets summarized). Returns 0 if nothing needs cutting.
//
// The cut targets contextWindow - reserveTokens - summary overhead, ensuring
// the result actually fits. Snaps to a valid boundary (user, assistant, or
// compaction_summary — never mid-tool-result).
func FindCutPoint(msgs []core.AgentMessage, contextTokens, contextWindow int, settings core.CompactionSettings) int {
	if len(msgs) == 0 {
		return 0
	}

	// How many tokens we want to keep.
	targetKeep := settings.KeepRecent + estimatedSummaryTokens
	// But ensure we actually drop below the threshold.
	maxKeep := contextWindow - settings.ReserveTokens - estimatedSummaryTokens
	if maxKeep < targetKeep {
		targetKeep = maxKeep
	}
	if targetKeep <= 0 {
		targetKeep = settings.KeepRecent
	}

	// Walk backwards, accumulating token cost of kept messages.
	accumulated := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		accumulated += core.EstimateTokens(msgs[i].Message)
		if accumulated >= targetKeep {
			// Snap forward to a valid cut boundary.
			for j := i; j < len(msgs); j++ {
				r := msgs[j].Role
				if r == "user" || r == "assistant" || r == "compaction_summary" {
					return j
				}
			}
			return i
		}
	}
	return 0
}

// maxSerializationChars caps the serialization to prevent the summarization
// request itself from exceeding model limits.
const maxSerializationChars = 400_000

// SerializeForSummary converts messages to a human-readable transcript for
// the summarization prompt. Truncates at maxSerializationChars.
func SerializeForSummary(msgs []core.AgentMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "user":
			b.WriteString("[User]: ")
			b.WriteString(extractText(m.Message))
			b.WriteByte('\n')
		case "assistant":
			b.WriteString("[Assistant]: ")
			b.WriteString(extractText(m.Message))
			b.WriteByte('\n')
			for _, c := range m.Content {
				if c.Type == "tool_call" {
					fmt.Fprintf(&b, "  [Tool call: %s]\n", c.ToolName)
				}
			}
		case "tool_result":
			text := extractText(m.Message)
			// Truncate long tool results.
			if len(text) > 500 {
				text = text[:500] + "..."
			}
			fmt.Fprintf(&b, "[Tool result: %s]: %s\n", m.ToolName, text)
		case "compaction_summary":
			b.WriteString("[Previous summary]: ")
			b.WriteString(extractText(m.Message))
			b.WriteByte('\n')
		}
		// Check AFTER writing so even a single huge message doesn't blow past the cap.
		if b.Len() > maxSerializationChars {
			b.WriteString("\n[...truncated]\n")
			break
		}
	}
	return b.String()
}

func extractText(m core.Message) string {
	var parts []string
	for _, c := range m.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "thinking":
			// Omit thinking from serialization.
		}
	}
	return strings.Join(parts, " ")
}

// FileOps tracks file operations found in tool calls.
type FileOps struct {
	Read    map[string]bool
	Written map[string]bool
	Edited  map[string]bool
}

// ExtractFileOps scans messages for tool calls that reference files.
func ExtractFileOps(msgs []core.AgentMessage) FileOps {
	ops := FileOps{
		Read:    make(map[string]bool),
		Written: make(map[string]bool),
		Edited:  make(map[string]bool),
	}
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type != "tool_call" {
				continue
			}
			path, _ := c.Arguments["path"].(string)
			if path == "" {
				continue
			}
			switch c.ToolName {
			case "read", "Read":
				ops.Read[path] = true
			case "write", "Write":
				ops.Written[path] = true
			case "edit", "Edit":
				ops.Edited[path] = true
			}
		}
	}
	return ops
}

// ReadOnly returns files that were read but not modified, sorted.
func (f FileOps) ReadOnly() []string {
	var result []string
	for p := range f.Read {
		if !f.Written[p] && !f.Edited[p] {
			result = append(result, p)
		}
	}
	sort.Strings(result)
	return result
}

// Modified returns files that were written or edited, sorted.
func (f FileOps) Modified() []string {
	seen := make(map[string]bool)
	for p := range f.Written {
		seen[p] = true
	}
	for p := range f.Edited {
		seen[p] = true
	}
	var result []string
	for p := range seen {
		result = append(result, p)
	}
	sort.Strings(result)
	return result
}

// GenerateSummary makes an LLM call to summarize conversation messages.
// Returns the summary text, provider-reported usage (may be nil), or an error.
func GenerateSummary(ctx context.Context, provider core.Provider, model core.Model, opts core.StreamOptions, msgs []core.AgentMessage, previousSummary string) (string, *core.Usage, error) {
	serialized := SerializeForSummary(msgs)
	prompt := buildPrompt(serialized, previousSummary)

	req := core.Request{
		Model:  model,
		System: summarizationSystemPrompt,
		Messages: []core.Message{
			core.NewUserMessage(prompt),
		},
		Options: opts,
	}

	ch, err := provider.Stream(ctx, req)
	if err != nil {
		return "", nil, fmt.Errorf("summarization request: %w", err)
	}

	var text strings.Builder
	var finalMsg *core.Message
	for event := range ch {
		switch event.Type {
		case core.ProviderEventTextDelta:
			text.WriteString(event.Delta)
		case core.ProviderEventDone:
			finalMsg = event.Message
		case core.ProviderEventError:
			return "", nil, fmt.Errorf("summarization: %w", event.Error)
		}
	}

	// Fallback: if streaming produced nothing, extract from final message.
	result := text.String()
	if result == "" && finalMsg != nil {
		for _, c := range finalMsg.Content {
			if c.Type == "text" {
				result += c.Text
			}
		}
	}

	if strings.TrimSpace(result) == "" {
		return "", nil, fmt.Errorf("summarization produced empty output")
	}

	var usage *core.Usage
	if finalMsg != nil {
		usage = finalMsg.Usage
	}
	return result, usage, nil
}

// Compact orchestrates context compaction. Returns nil Result if nothing
// needs compacting. On LLM failure, returns the error with the original
// messages unchanged (non-fatal).
func Compact(ctx context.Context, provider core.Provider, model core.Model, opts core.StreamOptions, msgs []core.AgentMessage, contextTokens, contextWindow int, settings core.CompactionSettings) (*Result, []core.AgentMessage, error) {
	cutIndex := FindCutPoint(msgs, contextTokens, contextWindow, settings)
	if cutIndex <= 0 {
		return nil, msgs, nil
	}

	toSummarize := msgs[:cutIndex]
	toKeep := msgs[cutIndex:]

	// Extract previous summary from first message if it's a compaction_summary.
	var previousSummary string
	if len(toSummarize) > 0 && toSummarize[0].Role == "compaction_summary" {
		previousSummary = extractText(toSummarize[0].Message)
		toSummarize = toSummarize[1:]
	}

	fileOps := ExtractFileOps(toSummarize)

	summary, usage, err := GenerateSummary(ctx, provider, model, opts, toSummarize, previousSummary)
	if err != nil {
		return nil, msgs, fmt.Errorf("compaction: %w", err)
	}

	summary += formatFileOps(fileOps)

	summaryMsg := core.AgentMessage{
		Message: core.Message{
			Role:      "compaction_summary",
			Content:   []core.Content{core.TextContent(summary)},
			Timestamp: time.Now().Unix(),
		},
	}

	compacted := make([]core.AgentMessage, 0, len(toKeep)+1)
	compacted = append(compacted, summaryMsg)
	compacted = append(compacted, toKeep...)

	tokensAfter := 0
	for _, m := range compacted {
		tokensAfter += core.EstimateTokens(m.Message)
	}

	return &Result{
		Summary:       summary,
		TokensBefore:  contextTokens,
		TokensAfter:   tokensAfter,
		ReadFiles:     fileOps.ReadOnly(),
		ModifiedFiles: fileOps.Modified(),
		Usage:         usage,
	}, compacted, nil
}

func formatFileOps(ops FileOps) string {
	readOnly := ops.ReadOnly()
	modified := ops.Modified()
	if len(readOnly) == 0 && len(modified) == 0 {
		return ""
	}

	var b strings.Builder
	if len(readOnly) > 0 {
		b.WriteString("\n\n## Files Read\n")
		for _, f := range readOnly {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	if len(modified) > 0 {
		b.WriteString("\n## Files Modified\n")
		for _, f := range modified {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	return b.String()
}
