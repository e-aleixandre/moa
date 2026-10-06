package core

// DropUnansweredToolCalls returns msgs without the assistant tool_calls that
// have no tool_result in the run of tool_result messages right after them,
// and without any later tool_result for a call it removed (a restored session
// appends such a late synthetic result at the end). Providers reject both
// halves for good (Anthropic: "tool_use ids were found without tool_result
// blocks immediately after", "unexpected tool_use_id"), so a session that once
// persisted an unanswered call would never recover. It only shapes a request:
// the input slice and the persisted history are left untouched, and when
// there is nothing to drop msgs is returned as-is.
func DropUnansweredToolCalls(msgs []Message) []Message {
	dropped := map[string]bool{}
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		var answered map[string]bool
		for _, c := range m.Content {
			if c.Type != "tool_call" {
				continue
			}
			if answered == nil {
				answered = map[string]bool{}
				for _, n := range msgs[i+1:] {
					if n.Role != "tool_result" {
						break
					}
					answered[n.ToolCallID] = true
				}
			}
			if !answered[c.ToolCallID] {
				dropped[c.ToolCallID] = true
			}
		}
	}
	if len(dropped) == 0 {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "tool_result":
			if dropped[m.ToolCallID] {
				continue
			}
		case "assistant":
			kept := make([]Content, 0, len(m.Content))
			for _, c := range m.Content {
				if c.Type == "tool_call" && dropped[c.ToolCallID] {
					continue
				}
				kept = append(kept, c)
			}
			if len(kept) != len(m.Content) {
				m.Content = kept
			}
		}
		out = append(out, m)
	}
	return out
}
