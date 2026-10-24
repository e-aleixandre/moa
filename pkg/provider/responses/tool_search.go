package responses

import (
	"slices"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// toolSearchCallIDPrefix starts the item id of a native tool_search_call.
const toolSearchCallIDPrefix = "tsc_"

// toolSearch is how one request loads deferred tools: the native
// client-executed tool_search, verified on /codex/responses. The model emits
// a tool_search_call, which the agent runs as its tool_search tool; its
// result goes back as a tool_search_output carrying the definitions it
// loaded. Those sit in the input where the search happened, so the tools and
// every earlier item stay a byte-identical prefix.
type toolSearch struct {
	deferred map[string]core.ToolSpec // this request's deferred definitions, by name
	calls    map[string]bool          // call ids of the history's tool_search calls
}

// newToolSearch returns the request's tool search, or nil when it sends every
// definition up front: the transport does not declare it, or nothing is
// deferred. Without it, tool_search is an ordinary function and its results
// go as text, as for any other provider.
func newToolSearch(req core.Request, dialect Dialect) *toolSearch {
	if !dialect.SupportsToolSearch {
		return nil
	}
	ts := &toolSearch{deferred: map[string]core.ToolSpec{}, calls: map[string]bool{}}
	offered := false
	for _, s := range req.Tools {
		if s.Deferred {
			ts.deferred[s.Name] = s
		}
		offered = offered || s.Name == core.ToolSearchName
	}
	if len(ts.deferred) == 0 || !offered {
		return nil
	}
	for _, m := range req.Messages {
		if m.Role != "assistant" {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "tool_call" && c.ToolName == core.ToolSearchName {
				ts.calls[c.ToolCallID] = true
			}
		}
	}
	return ts
}

// answers reports whether callID is a tool_search call this request replays
// natively.
func (ts *toolSearch) answers(callID string) bool {
	return ts != nil && callID != "" && ts.calls[callID]
}

// call is the native item for a recorded tool_search call. Its id is kept
// only when it is the model's own tool_search_call id: a search recorded as a
// function call, or by another model, goes without one.
func (ts *toolSearch) call(c core.Content, args map[string]any, foreignModel bool) map[string]any {
	item := map[string]any{
		"type":      "tool_search_call",
		"call_id":   c.ToolCallID,
		"execution": "client",
		"arguments": args,
		"status":    "completed",
	}
	if !foreignModel && strings.HasPrefix(c.ToolCallItemID, toolSearchCallIDPrefix) {
		item["id"] = c.ToolCallItemID
	}
	return item
}

// output answers a tool_search call with the definitions its result
// references, in its order, as this request defers them. A reference to a
// tool no longer offered is dropped. The output has no room for text: a
// search that loaded nothing, or failed, answers with an empty list, and the
// model searches again.
func (ts *toolSearch) output(msg core.Message) map[string]any {
	tools := []map[string]any{}
	var seen []string
	for _, c := range msg.Content {
		spec, ok := ts.deferred[c.ToolName]
		if c.Type != "tool_reference" || !ok || slices.Contains(seen, c.ToolName) {
			continue
		}
		seen = append(seen, c.ToolName)
		fn := functionTool(spec)
		fn["defer_loading"] = true
		tools = append(tools, fn)
	}
	return map[string]any{
		"type":      "tool_search_output",
		"call_id":   msg.ToolCallID,
		"execution": "client",
		"status":    "completed",
		"tools":     tools,
	}
}
