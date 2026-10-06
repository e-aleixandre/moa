package core

// DropUnansweredToolCalls returns msgs without the assistant tool_calls that
// have no tool_result in the run of tool_result messages right after them.
// Providers reject such history for good (Anthropic: "tool_use ids were found
// without tool_result blocks immediately after"), so a session that once
// persisted an unanswered call would never recover. It only shapes a request:
// the input slice and the persisted history are left untouched, and messages
// with nothing to drop are returned as-is.
func DropUnansweredToolCalls(msgs []Message) []Message {
	var out []Message
	for i, m := range msgs {
		if m.Role != "assistant" {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		answered := map[string]bool{}
		for _, n := range msgs[i+1:] {
			if n.Role != "tool_result" {
				break
			}
			answered[n.ToolCallID] = true
		}
		kept := make([]Content, 0, len(m.Content))
		dropped := false
		for _, c := range m.Content {
			if c.Type == "tool_call" && !answered[c.ToolCallID] {
				dropped = true
				continue
			}
			kept = append(kept, c)
		}
		if !dropped {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		if out == nil {
			out = append(make([]Message, 0, len(msgs)), msgs[:i]...)
		}
		m.Content = kept
		out = append(out, m)
	}
	if out == nil {
		return msgs
	}
	return out
}
