package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/memory"
)

// maxSearchResultBytes caps a whole search response. Search must stay cheap
// enough to be worth calling instead of reading facts one by one.
const maxSearchResultBytes = 6 * 1024

// NewMemory creates the memory tool for managing cross-session memory as
// single-fact files (list/search/read/write/delete).
func NewMemory(cfg ToolConfig) core.Tool {
	store := cfg.MemoryStore
	lockKey := "memory:" + store.ProjectDir()

	return core.Tool{
		Name:  "memory",
		Label: "Memory",
		Description: "Manage narrowly scoped cross-session memory as small, single-fact notes. Use it only for " +
			"non-secret facts that are costly to reconstruct and have no discoverable canonical source; do not " +
			"store rules, preferences, procedures, task state, credentials, or copies of project knowledge. " +
			"Only the index (one line per fact) is in your context; search or read a fact's full text on demand. " +
			"Each fact declares a scope: \"global\" facts apply to every project, \"project\" facts only to this one. " +
			"Every write requires a checkable expiry condition or an explicit durable declaration, but lifecycle " +
			"does not make an otherwise ineligible fact appropriate. When reading a fact with an expiry condition, " +
			"delete it if you can verify that condition has happened. Update the existing fact instead of duplicating, " +
			"and delete facts that become wrong. Refer to a fact by its " +
			"canonical id from the index (e.g. \"project/external-benchmark\" or \"global/vendor-limit\").",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["list", "search", "read", "write", "delete"],
					"description": "list: show the index of all facts. search: find facts by text, returning ids and short snippets. read: return one fact's full text. write: create/overwrite one fact. delete: remove one fact."
				},
				"id": {
					"type": "string",
					"description": "Canonical id for read/delete: \"project/<name>\", \"global/<name>\", or a bare \"<name>\" if unambiguous."
				},
				"query": {
					"type": "string",
					"description": "For search: text to look for in names, descriptions and bodies of both scopes. Required for search."
				},
				"regex": {
					"type": "boolean",
					"description": "For search: treat query as a Go (RE2) regular expression instead of a case-insensitive substring. Default false."
				},
				"limit": {
					"type": "integer",
					"description": "For search: maximum number of results (default 10, maximum 25)."
				},
				"offset": {
					"type": "integer",
					"description": "For search: skip this many results to page through a large match set (default 0)."
				},
				"name": {
					"type": "string",
					"description": "Fact name for write: kebab-case ascii (e.g. \"uses-docker\"). The file is named after it."
				},
				"description": {
					"type": "string",
					"description": "One-line hook shown in the index (for write). Required, single line, at most 180 bytes — the detail goes in content."
				},
				"scope": {
					"type": "string",
					"enum": ["global", "project"],
					"description": "Where the fact lives (for write): \"global\" for facts useful in every project, \"project\" for facts about this repository. Required."
				},
				"content": {
					"type": "string",
					"description": "The full fact body in markdown (for write)."
				},
				"invalidate_when": {
					"type": "string",
					"description": "For write, the natural-language condition under which this fact stops being true. It must be checkable now by another agent against a concrete source, without interpreting relevance or asking the user. Valid: \"when issue #84 is closed\", \"when git log shows branch X is merged\", \"when port 3306 on that host responds again\". Invalid: \"when it is no longer relevant\". Mutually exclusive with durable."
				},
				"durable": {
					"type": "boolean",
					"description": "For write, explicitly mark an eligible fact with no known expiry condition as permanent. This declares lifecycle only; it does not make rules, procedures, task state, secrets, or copied project knowledge eligible. If an identifiable event could make the fact false, use invalidate_when instead. Mutually exclusive with invalidate_when."
				}
			},
			"required": ["action"]
		}`),
		Effect: core.EffectWritePath,
		LockKey: func(args map[string]any) string {
			return lockKey
		},
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			switch getString(params, "action", "") {
			case "list":
				mems := store.List()
				if len(mems) == 0 {
					return core.TextResult("No memories saved yet."), nil
				}
				var sb strings.Builder
				for _, m := range mems {
					sb.WriteString("- ")
					sb.WriteString(m.ID())
					sb.WriteString(" — ")
					sb.WriteString(m.Description)
					sb.WriteString("\n")
				}
				return core.TextResult(sb.String()), nil

			case "search":
				res, err := store.Search(memory.SearchOptions{
					Query:  getString(params, "query", ""),
					Regex:  getBool(params, "regex", false),
					Limit:  getInt(params, "limit", 0),
					Offset: getInt(params, "offset", 0),
				})
				if err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				return core.TextResult(formatSearch(res)), nil

			case "read":
				id := getString(params, "id", "")
				if id == "" {
					return core.ErrorResult("id is required for read (e.g. \"project/uses-docker\")"), nil
				}
				m, ok, err := store.Read(id)
				if err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				if !ok {
					return core.ErrorResult(fmt.Sprintf("memory %q not found", id)), nil
				}
				if m.InvalidateWhen != "" {
					return core.TextResult("Invalidate when: " + m.InvalidateWhen + "\n\n" + m.Body), nil
				}
				return core.TextResult(m.Body), nil

			case "write":
				scopeStr := getString(params, "scope", "")
				scope, ok := memory.ParseScope(scopeStr)
				if !ok {
					return core.ErrorResult(fmt.Sprintf("invalid scope %q: use \"global\" (every project) or \"project\" (this repository)", scopeStr)), nil
				}
				m := memory.Memory{
					Name:           getString(params, "name", ""),
					Description:    getString(params, "description", ""),
					Scope:          scope,
					InvalidateWhen: getString(params, "invalidate_when", ""),
					Body:           getString(params, "content", ""),
				}
				if getBool(params, "durable", false) {
					m.Lifecycle = memory.LifecycleDurable
				}
				note, err := store.Write(m)
				if err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				out := fmt.Sprintf("Saved memory %q. Read it later with: read %s", m.Name, m.ID())
				if note != "" {
					out += "\n" + note
				}
				if budget := formatIndexBudget(store.List()); budget != "" {
					out += "\n" + budget
				}
				return core.TextResult(out), nil

			case "delete":
				id := getString(params, "id", "")
				if id == "" {
					return core.ErrorResult("id is required for delete (e.g. \"project/uses-docker\")"), nil
				}
				if err := store.Delete(id); err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				return core.TextResult(fmt.Sprintf("Deleted memory %q.", id)), nil

			default:
				return core.ErrorResult(fmt.Sprintf("unknown action %q — use \"list\", \"search\", \"read\", \"write\" or \"delete\"", getString(params, "action", ""))), nil
			}
		},
	}
}

// formatSearch renders a page of hits: ids and snippets only, never bodies,
// and bounded so a broad query cannot flood the context.
func formatSearch(res memory.SearchResult) string {
	if res.Total == 0 {
		return "No memories matched."
	}
	var sb strings.Builder
	shown := res.Offset + len(res.Hits)
	fmt.Fprintf(&sb, "%d matching memories (showing %d–%d).", res.Total, res.Offset+1, shown)
	if shown < res.Total {
		fmt.Fprintf(&sb, " For the rest, search again with offset=%d.", shown)
	}
	sb.WriteString("\n\n")
	marker := fmt.Sprintf("[results truncated at %dKB — narrow the query or lower limit]\n", maxSearchResultBytes/1024)
	for _, h := range res.Hits {
		line := fmt.Sprintf("- %s (%s) — %s\n", h.Memory.ID(), h.Field, h.Snippet)
		if sb.Len()+len(line)+len(marker) > maxSearchResultBytes {
			sb.WriteString(marker)
			break
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// formatIndexBudget tells the agent whether what it just saved can actually
// reach the prompt: an overflowing index drops facts silently otherwise.
func formatIndexBudget(mems []memory.Memory) string {
	st := memory.IndexStatusOf(mems)
	if st.Dropped == 0 {
		return fmt.Sprintf("Index: %d/%d bytes used by %d facts.", st.UsedBytes, st.BudgetBytes, st.Facts)
	}
	return fmt.Sprintf("Index: %d/%d bytes used by %d facts — %d do not fit and never reach the prompt. Consolidate or delete facts.",
		st.UsedBytes, st.BudgetBytes, st.Facts, st.Dropped)
}
