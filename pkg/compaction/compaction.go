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
	"unicode/utf8"

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

// checkpointBegin and checkpointEnd delimit the ephemeral session checkpoint
// appended to a summary. The checkpoint bypasses the summarizer entirely, so
// it cannot be dropped or paraphrased by the model.
const (
	checkpointBegin = "--- BEGIN SESSION CHECKPOINT ---"
	checkpointEnd   = "--- END SESSION CHECKPOINT ---"
)

// AppendCheckpoint mechanically appends a checkpoint to a summary and rewrites
// the summary message in place. Both the manual and automatic compaction paths
// must call this: previously only the manual path did, so an automatic
// compaction silently discarded a checkpoint the user had already paid for.
func AppendCheckpoint(result *Result, compacted []core.AgentMessage, checkpoint string) {
	if result == nil || strings.TrimSpace(checkpoint) == "" {
		return
	}
	appended := "\n\n" + checkpointBegin + "\n" + checkpoint + "\n" + checkpointEnd
	result.Summary += appended
	if len(compacted) > 0 {
		compacted[0].Content = []core.Content{core.TextContent(result.Summary)}
	}
	// Keep the reported post-compaction size honest: the checkpoint can add
	// several thousand tokens that the caller would otherwise never see.
	result.TokensAfter += core.EstimateTokens(core.Message{
		Role:    "compaction_summary",
		Content: []core.Content{core.TextContent(appended)},
	})
}

// summaryMaxTokens is the hard output budget for a generated summary.
//
// This used to be unset: the call inherited the provider default and the
// observed size settled around 3.5k tokens regardless of how much was being
// summarized — an emergent ceiling, not a designed one. Measured survival of
// irrecoverable claims improves when the summarizer is allowed more room, so
// the budget is now explicit and generous, while still bounded so the
// post-compaction floor cannot drift upward without limit.
const summaryMaxTokens = 8000

// summaryTokenBudget is the summary allowance for a given context window.
// A flat 8k reserve starves small-window models: it is subtracted from the
// space available to keep recent turns, and below ~24k it made the cut point
// degenerate entirely. Scaling with the window keeps the reserve meaningful
// on large models and harmless on small ones.
//
// The same function feeds both the cut-point reserve and the real output cap,
// so the two can never diverge.
func summaryTokenBudget(contextWindow int) int {
	if contextWindow <= 0 {
		return summaryMaxTokens
	}
	if b := contextWindow / 8; b < summaryMaxTokens {
		return b
	}
	return summaryMaxTokens
}

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
	summaryReserve := summaryTokenBudget(contextWindow)
	targetKeep := settings.KeepRecent + summaryReserve
	// But ensure we actually drop below the threshold.
	maxKeep := contextWindow - settings.ReserveTokens - summaryReserve
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
			// Snap forward to a valid cut boundary (start of a user/assistant
			// turn or a summary). Never cut on a tool_result: that would keep
			// it while summarizing the tool_use that produced it, leaving an
			// orphan the provider rejects.
			for j := i; j < len(msgs); j++ {
				r := msgs[j].Role
				if r == "user" || r == "assistant" || r == "compaction_summary" {
					return j
				}
			}
			// Only trailing tool_results ahead — snap backward to the
			// assistant that owns them so the pair stays on the kept side.
			for j := i; j >= 0; j-- {
				if msgs[j].Role == "assistant" {
					return j
				}
			}
			return 0
		}
	}
	return 0
}

// defaultMaxSerializationChars is used when the model's context window is
// unknown (MaxInput == 0). Fits comfortably in a 200k-token model.
const defaultMaxSerializationChars = 400_000

// maxSerializationChars derives a serialization cap from the model's context
// window. The serialized transcript goes into a user message for the
// summarization LLM call, so it must fit in the model's input with room for
// the system prompt (~500 tokens) and the output summary.
//
// Heuristic: MaxInput * 2 chars (≈ half the context in tokens, since ~4
// chars ≈ 1 token). Clamped to defaultMaxSerializationChars minimum so
// small-context models don't produce useless micro-summaries.
func maxSerializationChars(maxInput int) int {
	if maxInput <= 0 {
		return defaultMaxSerializationChars
	}
	limit := maxInput * 2 // tokens → chars, using half the context
	if limit < 40_000 {
		limit = 40_000 // floor: ~10k tokens minimum for a useful summary
	}
	return limit
}

// Tool result budgeting.
//
// The previous flat 500-char head-truncation hid the end of every result,
// which is where outcomes live: the assertion that failed, the final error,
// the summary line of a test run.
//
// Two policies, both borrowed from gemini-cli's compression pass
// (packages/core: COMPRESSION_FUNCTION_RESPONSE_TOKEN_BUDGET):
//   - a global budget walked newest-first, so recent results keep full
//     fidelity and only older ones get squeezed;
//   - head+tail retention for anything that must be squeezed, so a truncated
//     result still shows what it was and how it ended.
const (
	// toolResultBudget is the per-result floor: even the oldest result keeps
	// this much once the global budget is exhausted.
	toolResultBudget = 1000
	// toolResultGlobalCap is the absolute ceiling for tool-result characters
	// in one serialized transcript, spent newest-first. The effective budget
	// is also bounded by a share of the serialization limit (see
	// toolResultBudgets) so tool output can never crowd out the dialogue.
	toolResultGlobalCap = 300_000
	// toolResultShareNum/Den bound the tool-result budget as a fraction of the
	// whole transcript budget. User and assistant turns carry the intent that
	// cannot be recovered by re-reading the repo, so they keep the majority.
	toolResultShareNum = 1
	toolResultShareDen = 2
	// toolResultFullDivisor sizes the per-result cap relative to the global
	// budget, so a single huge result cannot starve everything older.
	toolResultFullDivisor = 15
)

// toolResultBudgets derives the global tool-result budget and the per-result
// cap from the transcript limit. A fixed 300k budget exceeded the whole
// serialization limit on small-context models (128k model → 256k limit),
// letting a handful of recent tool results evict every user turn.
func toolResultBudgets(limit int) (global, perResult int) {
	global = limit * toolResultShareNum / toolResultShareDen
	if global > toolResultGlobalCap {
		global = toolResultGlobalCap
	}
	perResult = global / toolResultFullDivisor
	if perResult < toolResultBudget {
		perResult = toolResultBudget
	}
	return global, perResult
}

// toolResultTailShare is the fraction of the budget reserved for the tail.
const toolResultTailShare = 0.4

// elideMiddle trims text to budget, keeping the head and the tail. Returns the
// input unchanged when it already fits. Cuts are snapped to rune boundaries so
// the transcript never contains a broken multi-byte character.
func elideMiddle(text string, budget int) string {
	if budget <= 0 || len(text) <= budget {
		return text
	}
	// Reserve room for the marker so the elided result never exceeds budget.
	const markerReserve = 32
	usable := budget - markerReserve
	if usable < 2 {
		return text[:runeSafeEnd(text, budget)]
	}
	tail := int(float64(usable) * toolResultTailShare)
	head := usable - tail
	omitted := len(text) - head - tail
	if head <= 0 || tail <= 0 || omitted <= 0 {
		return text[:runeSafeEnd(text, budget)]
	}
	headEnd := runeSafeEnd(text, head)
	tailStart := runeSafeStart(text, len(text)-tail)
	return text[:headEnd] +
		fmt.Sprintf("\n… [%d chars elided] …\n", omitted) +
		text[tailStart:]
}

// runeSafeEnd moves i back to the nearest rune boundary at or before i.
func runeSafeEnd(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// runeSafeStart moves i forward to the nearest rune boundary at or after i.
func runeSafeStart(s string, i int) int {
	if i <= 0 {
		return 0
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// SerializeForSummary converts messages to a human-readable transcript for
// the summarization prompt. Truncates at a limit derived from the model's
// context window (maxInput tokens). Pass 0 for the default (400k chars).
//
// When the transcript exceeds the limit, the OLDEST messages are dropped
// rather than the newest. The previous implementation appended forward and
// broke on overflow, discarding the most recent turns — precisely the ones
// closest to the kept tail and most likely to describe live work.
func SerializeForSummary(msgs []core.AgentMessage, maxInput int) string {
	limit := maxSerializationChars(maxInput)

	// Walk newest-first so recent tool results claim the budget before older
	// ones, then restore chronological order for the transcript.
	rendered := make([]string, len(msgs))
	total := 0
	toolBudget, perResult := toolResultBudgets(limit)
	for i := len(msgs) - 1; i >= 0; i-- {
		s := renderMessage(msgs[i], &toolBudget, perResult)
		rendered[i] = s
		total += len(s)
	}

	// Drop from the front until the remainder fits, but never drop the last
	// message: emptying the transcript would make the summarizer replace real
	// history with a summary of nothing.
	start := 0
	for start < len(rendered)-1 && total > limit {
		total -= len(rendered[start])
		start++
	}
	if total > limit && start < len(rendered) {
		rendered[start] = elideMiddle(rendered[start], limit)
	}

	var b strings.Builder
	if start > 0 {
		fmt.Fprintf(&b, "[...%d earlier messages omitted (transcript exceeded serialization limit)...]\n", start)
	}
	for _, s := range rendered[start:] {
		b.WriteString(s)
	}
	return b.String()
}

// renderMessage formats one message for the summarization transcript.
// toolBudget is the remaining global allowance for tool results and is
// decremented as results consume it; pass nil to apply only the per-result
// floor.
func renderMessage(m core.AgentMessage, toolBudget *int, perResult int) string {
	var b strings.Builder
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
		text := elideMiddle(extractText(m.Message), toolResultAllowance(toolBudget, perResult))
		fmt.Fprintf(&b, "[Tool result: %s]: %s\n", m.ToolName, text)
	case "compaction_summary":
		b.WriteString("[Previous summary]: ")
		b.WriteString(extractText(m.Message))
		b.WriteByte('\n')
	}
	return b.String()
}

// toolResultAllowance returns the character budget for the next tool result,
// drawing from the global allowance while it lasts and falling back to the
// per-result floor once it is spent.
func toolResultAllowance(remaining *int, perResult int) int {
	if remaining == nil || *remaining <= toolResultBudget {
		return toolResultBudget
	}
	allow := *remaining
	if allow > perResult {
		allow = perResult
	}
	*remaining -= allow
	return allow
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
			switch c.ToolName {
			case "read", "Read":
				if p, _ := c.Arguments["path"].(string); p != "" {
					ops.Read[p] = true
				}
			case "write", "Write":
				if p, _ := c.Arguments["path"].(string); p != "" {
					ops.Written[p] = true
				}
			case "edit", "Edit", "multiedit":
				if p, _ := c.Arguments["path"].(string); p != "" {
					ops.Edited[p] = true
				}
			case "apply_patch":
				patch, _ := c.Arguments["patch"].(string)
				for _, f := range extractPatchFiles(patch) {
					if f.added {
						ops.Written[f.path] = true
					} else {
						ops.Edited[f.path] = true
					}
				}
			}
		}
	}
	return ops
}

// patchFile is a single file touched by an apply_patch body.
type patchFile struct {
	path  string
	added bool // *** Add File / *** Move to → created; update/delete → modified
}

// extractPatchFiles does a best-effort scan of an apply_patch body for the
// files it touches, reading only the *** ...File: / *** Move to: headers.
// It deliberately avoids importing the full patch parser so this package keeps
// depending only on core; a malformed patch still yields whatever headers parse.
func extractPatchFiles(patch string) []patchFile {
	if patch == "" {
		return nil
	}
	var files []patchFile
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			if p := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:")); p != "" {
				files = append(files, patchFile{path: p, added: true})
			}
		case strings.HasPrefix(line, "*** Update File:"):
			if p := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:")); p != "" {
				files = append(files, patchFile{path: p})
			}
		case strings.HasPrefix(line, "*** Delete File:"):
			if p := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:")); p != "" {
				files = append(files, patchFile{path: p})
			}
		case strings.HasPrefix(line, "*** Move to:"):
			if p := strings.TrimSpace(strings.TrimPrefix(line, "*** Move to:")); p != "" {
				files = append(files, patchFile{path: p, added: true})
			}
		}
	}
	return files
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
// focus, when non-empty, is a caller instruction (from `/compact <focus>`) that
// tells the summarizer what to keep in the foreground; it is advisory and never
// replaces the structured format.
func GenerateSummary(ctx context.Context, provider core.Provider, model core.Model, opts core.StreamOptions, msgs []core.AgentMessage, previousSummary, focus string) (string, *core.Usage, error) {
	serialized := SerializeForSummary(msgs, model.MaxInput)
	prompt := buildPrompt(serialized, previousSummary, focus)

	// Give the summarizer an explicit output budget. Without one the size of
	// the summary is whatever the provider defaults to, which is how the
	// ~3.5k-token ceiling arose without anyone choosing it.
	opts.MaxTokens = summaryBudget(model)
	// Summarization is extraction, not reasoning, and thinking tokens are
	// charged against the same output budget: leaving the session's thinking
	// level on would spend most of MaxTokens before a single line of summary
	// is written, making the summary smaller than the ceiling this budget is
	// meant to lift.
	opts.ThinkingLevel = ""

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
// messages unchanged (non-fatal). focus is an optional caller instruction
// (from `/compact <focus>`) telling the summarizer what to keep in the
// foreground; empty for automatic compaction.
func Compact(ctx context.Context, provider core.Provider, model core.Model, opts core.StreamOptions, msgs []core.AgentMessage, contextTokens, contextWindow int, settings core.CompactionSettings, focus string) (*Result, []core.AgentMessage, error) {
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

	summary, usage, err := GenerateSummary(ctx, provider, model, opts, toSummarize, previousSummary, focus)
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

// summaryBudget returns the output token budget for a summarization call,
// clamped so a small-context model is never asked for a summary that cannot
// coexist with the tail it must accompany.
func summaryBudget(model core.Model) *int {
	budget := summaryTokenBudget(model.MaxInput)
	if model.MaxOutput > 0 && budget > model.MaxOutput {
		budget = model.MaxOutput
	}
	return &budget
}
