package compaction

import (
	"fmt"
	"strings"
)

const summarizationSystemPrompt = `You are a conversation summarizer for a coding agent. Your job is to produce a structured summary of a conversation between a user and an AI coding assistant.

Output format (use exactly these sections):

## Goal
What the user is trying to accomplish.

## Constraints & Preferences
Coding style, architecture decisions, libraries, patterns the user prefers.

## Progress
### Done
Completed work items.
### In Progress
Currently active work.
### Blocked
Items waiting on something.

## Key Decisions
Important decisions made during the conversation (with brief rationale).

## Next Steps
What should happen next.

## Critical Context
Non-obvious facts the assistant must remember (e.g., "tests must pass with -race", "don't modify package X").

Rules:
- Be concise but complete. Don't lose information that would change behavior.
- Preserve file paths, function names, and error messages exactly.
- If there's a previous summary, merge it with new information. Don't repeat unchanged items.
- Omit empty sections.`

func buildPrompt(serialized, previousSummary, focus string) string {
	// A caller focus (from `/compact <focus>`) is placed last, closest to the
	// generation point, and framed as an emphasis on top of the format — never
	// a replacement for it, so the structured summary stays intact.
	var focusBlock string
	if f := strings.TrimSpace(focus); f != "" {
		focusBlock = fmt.Sprintf(`

The user asked you to pay special attention to the following while summarizing. Keep everything relevant to it in full detail, even minor points; still produce the complete structured summary, this only raises the priority of what matches:

<focus>
%s
</focus>`, f)
	}

	if previousSummary != "" {
		return fmt.Sprintf(`Here is the previous conversation summary:

<previous_summary>
%s
</previous_summary>

Here is the new conversation that happened after the previous summary:

<conversation>
%s
</conversation>

Merge the previous summary with the new conversation into an updated summary. Preserve all relevant information from the previous summary and add new information from the conversation. Remove items that are no longer relevant (e.g., completed tasks that don't need tracking).%s`, previousSummary, serialized, focusBlock)
	}

	return fmt.Sprintf(`Summarize the following conversation between a user and an AI coding assistant:

<conversation>
%s
</conversation>

Produce a structured summary following the format in your instructions.%s`, serialized, focusBlock)
}
