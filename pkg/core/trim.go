package core

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Context trimming elides the CONTENT of old tool results while keeping the
// messages themselves. It runs before compaction, deterministically and without
// a model call: an old `read` or `bash` output is reproducible by running the
// tool again, whereas a summary is not reversible at all.
//
// The message, its MsgID and its ToolCallID survive the elision on purpose.
// Dropping the message would leave the tool_call that produced it unanswered,
// which providers reject and which the tree repairs on reload by injecting
// synthetic error results (pkg/session/tree.go).

const (
	// TrimProjectionVersion is the version of the elision algorithm below. It
	// is persisted with every trim so a later change to the placeholder format
	// or the eligibility rule cannot retroactively rewrite regions elided by an
	// older build: each region is replayed with the rules that produced it,
	// which is what keeps a restored context identical to the one the provider
	// saw — and its prefix cache warm.
	TrimProjectionVersion = 1

	// trimMinTokens is the smallest result worth eliding. The placeholder
	// itself costs ~60 tokens, so below this the saving does not pay for the
	// noise of a placeholder the model has to read past.
	trimMinTokens = 300

	// trimTailChars is how much of the original tail the placeholder keeps.
	// Outcomes live at the end of an output — "Exit code: 1", a test summary
	// line, the verdict of a report — and bash reports a failing command as a
	// plain text result, not as IsError (pkg/tool/bash.go), so the tail is the
	// only thing that tells a failed command from a successful one.
	trimTailChars = 160
)

// TrimSpan is one applied trim: everything eligible from the previous span's
// watermark up to (not including) WatermarkMsgID was elided under Version.
type TrimSpan struct {
	WatermarkMsgID string
	Version        int
}

// TrimPlan is the outcome of PlanTrim: the projected conversation plus what it
// would cost and save. Messages is nil when nothing was eligible.
type TrimPlan struct {
	WatermarkMsgID string
	Version        int
	Results        int
	TokensRemoved  int
	Messages       []AgentMessage
}

// PlanTrim computes the next trim over msgs: protect a recent tail of about
// keepRecent estimated tokens, then elide every eligible message between
// prevWatermarkMsgID (exclusive of nothing — it is where the last trim stopped)
// and the start of that tail.
//
// The watermark is snapped BACKWARD to the assistant that owns a group of tool
// results, never forward: forward is what compaction's FindCutPoint does,
// because there the boundary decides what gets summarized, and pushing a whole
// tool group to the summarized side is the safe direction. Here the boundary
// decides what is PROTECTED, so the safe direction is the opposite one —
// snapping forward would elide a large, very recent result that the promise of
// a protected tail says it must keep.
//
// Returns ok=false when nothing could be elided (including a watermark that
// would not advance past the previous one: trims must move forward, or an older
// region would be re-elided under newer rules).
func PlanTrim(msgs []AgentMessage, keepRecent int, prevWatermarkMsgID string) (TrimPlan, bool) {
	if len(msgs) == 0 {
		return TrimPlan{}, false
	}

	start := 0
	if prevWatermarkMsgID != "" {
		if idx := indexOfMsgID(msgs, prevWatermarkMsgID); idx >= 0 {
			start = idx
		}
	}

	wm := protectedTailStart(msgs, keepRecent)
	if wm <= start {
		return TrimPlan{}, false
	}

	out := make([]AgentMessage, len(msgs))
	copy(out, msgs)
	removed, count := applyTrimSpan(out, start, wm, TrimProjectionVersion)
	if count == 0 {
		return TrimPlan{}, false
	}
	return TrimPlan{
		WatermarkMsgID: msgs[wm].MsgID,
		Version:        TrimProjectionVersion,
		Results:        count,
		TokensRemoved:  removed,
		Messages:       out,
	}, true
}

// ApplyTrims replays persisted trims over a reconstructed conversation. Spans
// are applied in chronological order, each over its own interval, so a region
// elided by an older build keeps that build's placeholders.
//
// A span whose watermark is no longer present was dropped by a later
// compaction: everything the compaction retained is newer than that watermark,
// so the span has nothing left to elide. A span that would move the boundary
// backwards is ignored rather than trusted — persisted state cannot be assumed
// well-ordered, and re-eliding an older region would silently diverge from the
// context the provider actually saw.
func ApplyTrims(msgs []AgentMessage, spans []TrimSpan) []AgentMessage {
	if len(msgs) == 0 || len(spans) == 0 {
		return msgs
	}
	out := make([]AgentMessage, len(msgs))
	copy(out, msgs)

	prev := 0
	for _, span := range spans {
		if span.WatermarkMsgID == "" {
			continue
		}
		wm := indexOfMsgID(out, span.WatermarkMsgID)
		if wm < 0 || wm < prev {
			continue
		}
		applyTrimSpan(out, prev, wm, span.Version)
		prev = wm
	}
	return out
}

// applyTrimSpan is the single implementation of the elision, shared by the live
// decision (PlanTrim) and the reconstruction from the tree (ApplyTrims). Two
// implementations would drift, and a restored context that differs from the one
// already sent rewrites the provider's prefix cache for the whole session.
func applyTrimSpan(msgs []AgentMessage, start, end, version int) (removed, count int) {
	for i := start; i < end && i < len(msgs); i++ {
		m := msgs[i]
		if !trimEligible(m) {
			continue
		}
		before := EstimateTokens(m.Message)
		placeholder := trimPlaceholder(m, version)
		if placeholder == "" {
			continue
		}
		trimmed := m
		trimmed.Content = []Content{TextContent(placeholder)}
		after := EstimateTokens(trimmed.Message)
		if after >= before {
			continue
		}
		msgs[i] = trimmed
		removed += before - after
		count++
	}
	return removed, count
}

// trimEligible reports whether a message's content can be replaced by a
// placeholder.
//
// Non-textual blocks are excluded in v1: their token cost is a constant in
// EstimateTokens rather than a measurement, so eliding them would report a
// saving nobody verified, and an image is not reproducible by re-running a
// tool. Keeping them also keeps the AttachmentID in history, which is what the
// blob store's reachability depends on.
//
// Results marked as errors are kept whole — they are usually short and they are
// exactly what the model is still working from. Note this is "not marked as an
// error", not "successful": a bash command with a non-zero exit status returns
// an ordinary text result, which is why the placeholder keeps the tail.
func trimEligible(m AgentMessage) bool {
	switch {
	case m.Role == "tool_result":
		if m.IsError {
			return false
		}
	case m.Role == "user" && subagentNotificationJobID(m) != "":
		// An async subagent's completion reaches the parent as a user message,
		// not as a tool result. It is the same thing semantically — a delegated
		// job's report — and it carries the same weight in context.
	default:
		return false
	}
	if !onlyTextContent(m.Content) {
		return false
	}
	if EstimateTokens(m.Message) < trimMinTokens {
		return false
	}
	return messageText(m) != ""
}

func onlyTextContent(content []Content) bool {
	for _, c := range content {
		if c.Type != "text" {
			return false
		}
	}
	return len(content) > 0
}

func messageText(m AgentMessage) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// trimPlaceholder renders what replaces an elided message. The text is a pure
// function of the message and the version: anything varying (a timestamp, a
// counter) would change bytes already sent and invalidate the prefix cache the
// monotonic watermark exists to preserve.
//
// The job ID of a subagent report travels in the TEXT, not in Custom: the
// provider request is built by defaultConvertToLLM, which drops Custom
// entirely, so a placeholder that referred to "the subagent" without an ID
// would leave the model no way to get the verdict back.
func trimPlaceholder(m AgentMessage, _ int) string {
	text := messageText(m)
	if text == "" {
		return ""
	}
	tokens := humanizeTokenCount(EstimateTokens(m.Message))

	var header string
	switch {
	case m.Role == "user":
		jobID := subagentNotificationJobID(m)
		task := firstLineSnippet(customString(m.Custom, "subagent_task"), 80)
		if task != "" {
			header = fmt.Sprintf("[Subagent report elided to save context: ~%s tokens. Job %s — %s. Use subagent_status with that job ID for the full result.]", tokens, jobID, task)
		} else {
			header = fmt.Sprintf("[Subagent report elided to save context: ~%s tokens. Job %s. Use subagent_status with that job ID for the full result.]", tokens, jobID)
		}
	case customString(m.Custom, "subagent_job_id") != "":
		header = fmt.Sprintf("[Subagent output elided to save context: ~%s tokens. Job %s — use subagent_status with that job ID for the full result.]",
			tokens, customString(m.Custom, "subagent_job_id"))
	default:
		header = fmt.Sprintf("[Output elided to save context: ~%s tokens, %d lines. Re-run the tool if you need it.]",
			tokens, strings.Count(text, "\n")+1)
	}

	tail := trimTail(text)
	if tail == "" {
		return header
	}
	return header + "\n…\n" + tail
}

// trimTail returns the last trimTailChars of text, cut at a rune boundary and
// preferring a line start so the kept fragment reads as lines rather than as a
// severed one.
func trimTail(text string) string {
	if len(text) <= trimTailChars {
		return strings.TrimRight(text, "\n")
	}
	tail := text[len(text)-trimTailChars:]
	for !utf8.ValidString(tail) && len(tail) > 0 {
		tail = tail[1:]
	}
	if nl := strings.IndexByte(tail, '\n'); nl >= 0 && nl < len(tail)-1 {
		tail = tail[nl+1:]
	}
	return strings.TrimRight(tail, "\n")
}

// subagentNotificationJobID returns the job an async subagent notification
// reports on, or "" for any other user message. Without an ID the placeholder
// could not point anywhere, so such a message stays whole.
func subagentNotificationJobID(m AgentMessage) string {
	if customString(m.Custom, "source") != "subagent" {
		return ""
	}
	return customString(m.Custom, "subagent_job_id")
}

func customString(custom map[string]any, key string) string {
	if custom == nil {
		return ""
	}
	s, _ := custom[key].(string)
	return s
}

func firstLineSnippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		cut := s[:max]
		for !utf8.ValidString(cut) && len(cut) > 0 {
			cut = cut[:len(cut)-1]
		}
		s = strings.TrimSpace(cut) + "…"
	}
	return s
}

// protectedTailStart returns the index of the first message of the recent tail
// that a trim must not touch, walking back until keepRecent estimated tokens
// are covered and then snapping back to the assistant that owns any tool
// results the boundary landed inside.
func protectedTailStart(msgs []AgentMessage, keepRecent int) int {
	if keepRecent <= 0 {
		return len(msgs)
	}
	accumulated := 0
	i := len(msgs) - 1
	for ; i >= 0; i-- {
		accumulated += EstimateTokens(msgs[i].Message)
		if accumulated >= keepRecent {
			break
		}
	}
	if i < 0 {
		// The whole conversation fits in the protected tail.
		return 0
	}
	for i > 0 && msgs[i].Role == "tool_result" {
		i--
	}
	return i
}

func indexOfMsgID(msgs []AgentMessage, msgID string) int {
	for i := range msgs {
		if msgs[i].MsgID == msgID {
			return i
		}
	}
	return -1
}

// humanizeTokenCount renders a token count as a magnitude, the way the
// compaction notice does: the model needs to know "big", not the digits.
func humanizeTokenCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// TrimPayload is the typed result of a trim event.
type TrimPayload struct {
	WatermarkMsgID string `json:"watermark_msg_id"`
	Version        int    `json:"version"`
	TokensBefore   int    `json:"tokens_before"`
	TokensAfter    int    `json:"tokens_after"`
	Results        int    `json:"results"`
}
