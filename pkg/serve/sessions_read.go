package serve

import (
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

type sessionReadOptions struct {
	limit     int
	messageID string
	offset    int
	tools     bool
}

// ownerReadSession saves tokens by default and hides nothing: every message is
// abridged to its beginning and end, and every cut says how much is missing
// and how to read it whole (message_id). Tool calls are left out unless asked
// for — an owner reads what was said first — but can be listed, with the same
// notice on their arguments and results.
func (m *Manager) ownerReadSession(own owner.Owner, id string, opts sessionReadOptions) core.Result {
	if _, err := m.ownerSession(own, id); err != nil {
		return core.ErrorResult(err.Error())
	}
	snapshot, err := m.conversationSnapshot(id)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("cannot read session %s: %v", id, err))
	}
	if opts.messageID != "" {
		return readSessionMessage(snapshot, opts.messageID, opts.offset)
	}
	limit := opts.limit
	if limit <= 0 {
		limit = defaultSessionReadLimit
	}
	if limit > maxSessionReadLimit {
		limit = maxSessionReadLimit
	}
	var items []ConversationMessage
	for _, msg := range snapshot.messages {
		if msg.Role == "user" || msg.Role == "assistant" || (opts.tools && msg.Role == "tool") {
			items = append(items, msg)
		}
	}
	if len(items) == 0 {
		return core.TextResult("That session has said nothing yet.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s — %s\n\n", snapshot.id, snapshot.title)
	if total := len(items); total > limit {
		items = items[total-limit:]
		fmt.Fprintf(&sb, "[showing the last %d of %d messages; %d earlier ones are not shown — raise limit (max %d) to see more]\n\n",
			limit, total, total-limit, maxSessionReadLimit)
	}
	for _, msg := range items {
		how := fmt.Sprintf("read it whole with action=read, session_id=%s, message_id=%s", snapshot.id, msg.ID)
		if msg.Role == "tool" {
			fmt.Fprintf(&sb, "[%s] tool %s (%s): %s\n", msg.ID, msg.Tool, msg.Status, abridge(msg.fullText(), readToolHead, readToolTail, how))
			if output := snapshot.toolDetails[msg.ID].output; strings.TrimSpace(output) != "" {
				fmt.Fprintf(&sb, "  result: %s\n", abridge(output, readToolHead, readToolTail, how))
			}
			sb.WriteString("\n")
			continue
		}
		fmt.Fprintf(&sb, "[%s] %s: %s\n\n", msg.ID, msg.Role, abridge(msg.fullText(), readMessageHead, readMessageTail, how))
	}
	return core.TextResult(sb.String())
}

// readSessionMessage returns one message whole, or one chunk of it when it is
// longer than a read returns. A tool call reads as its arguments followed by
// its result.
func readSessionMessage(snapshot conversationSnapshot, messageID string, offset int) core.Result {
	var found *ConversationMessage
	for i := len(snapshot.messages) - 1; i >= 0; i-- {
		msg := snapshot.messages[i]
		if msg.ID == messageID || (messageID == lastMessageID && msg.Role == "assistant") {
			found = &snapshot.messages[i]
			break
		}
	}
	if found == nil {
		if messageID == lastMessageID {
			return core.TextResult("That session has no assistant message yet.")
		}
		return core.ErrorResult(fmt.Sprintf("message %s not found in session %s", messageID, snapshot.id))
	}
	text := found.fullText()
	label := found.Role
	if found.Role == "tool" {
		label = "tool " + found.Tool
		text = "arguments: " + text + "\n\nresult (" + found.Status + "):\n" + snapshot.toolDetails[found.ID].output
	}
	chunk, err := textChunk(text, offset, readChunkChars, func(next int) string {
		return fmt.Sprintf("Read on with action=read, session_id=%s, message_id=%s, offset=%d.", snapshot.id, found.ID, next)
	})
	if err != nil {
		return core.ErrorResult(err.Error())
	}
	return core.TextResult(fmt.Sprintf("%s — [%s] %s:\n\n%s", snapshot.id, found.ID, label, chunk))
}
