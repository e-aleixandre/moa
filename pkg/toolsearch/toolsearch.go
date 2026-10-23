// Package toolsearch is the tool that loads deferred tools on demand: it ranks
// their definitions against a query with BM25 and answers with references the
// provider expands into the full definitions. The definitions never leave the
// request's tool list, so loading one does not change the cached prefix.
package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Name is the tool's name on the wire.
const Name = core.ToolSearchName

// Limit caps the tools one search loads.
const Limit = 8

const mcpPrefix = "mcp__"

// server returns the MCP server of a tool name (mcp__<server>__<tool>) and the
// tool's own part, or "" for a name that is not an MCP one.
func server(name string) (srv, tool string) {
	rest, ok := strings.CutPrefix(name, mcpPrefix)
	if !ok {
		return "", name
	}
	srv, tool, ok = strings.Cut(rest, "__")
	if !ok {
		return "", name
	}
	return srv, tool
}

// Spec is the tool_search definition for a set of deferred tools. Its
// description is the index: the servers and their tool names, so the model
// knows what it can load without seeing any schema. It depends only on the
// deferred definitions, which a session restores from its own snapshot, so
// it is stable for as long as they are.
func Spec(deferred []core.ToolSpec) core.ToolSpec {
	byServer := map[string][]string{}
	var other []string
	for _, s := range deferred {
		srv, tool := server(s.Name)
		if srv == "" {
			other = append(other, s.Name)
			continue
		}
		byServer[srv] = append(byServer[srv], tool)
	}
	servers := make([]string, 0, len(byServer))
	for srv := range byServer {
		servers = append(servers, srv)
	}
	sort.Strings(servers)

	var b strings.Builder
	fmt.Fprintf(&b, "Load the tools listed below before using them: they are available but not loaded. "+
		"Search by keywords or by exact tool name; the matching tools (up to %d, best first) are loaded and can be called "+
		"right after, for the rest of the conversation (search again after it is compacted). "+
		"Calling one of these tools without loading it first fails.\n\nMCP servers:", Limit)
	for _, srv := range servers {
		tools := byServer[srv]
		sort.Strings(tools)
		fmt.Fprintf(&b, "\n- %s (%d tools, named %s%s__<tool>): %s", srv, len(tools), mcpPrefix, srv, strings.Join(tools, ", "))
	}
	if len(other) > 0 {
		sort.Strings(other)
		fmt.Fprintf(&b, "\n- other: %s", strings.Join(other, ", "))
	}
	return core.ToolSpec{
		Name:        Name,
		Description: b.String(),
		Parameters: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{`+
			`"query":{"type":"string","description":"Keywords describing the action, a server name, or an exact tool name"},`+
			`"limit":{"type":"integer","description":"Most tools to load (1-%d, default %d)"}},"required":["query"]}`, Limit, Limit)),
	}
}

// Tool is the executable tool_search over deferred.
func Tool(deferred []core.ToolSpec) core.Tool {
	spec := Spec(deferred)
	return core.Tool{
		Name:        spec.Name,
		Label:       "Tool search",
		Description: spec.Description,
		Parameters:  spec.Parameters,
		Effect:      core.EffectReadOnly,
		Execute: func(_ context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			query, _ := params["query"].(string)
			if strings.TrimSpace(query) == "" {
				return core.ErrorResult("query is required"), nil
			}
			limit := Limit
			if n, ok := params["limit"].(float64); ok && n >= 1 && n < Limit {
				limit = int(n)
			}
			return Result(Search(deferred, query, limit)), nil
		},
	}
}

// Result is what a search returns: one reference per tool, which the
// provider expands into its definition, and a text line naming them for
// everything that reads only text (the UI, compaction, other codecs).
func Result(names []string) core.Result {
	if len(names) == 0 {
		return core.TextResult("No tools matched. Try other keywords, a server name, or an exact tool name from this tool's description.")
	}
	content := make([]core.Content, 0, len(names)+1)
	content = append(content, core.TextContent("Loaded tools: "+strings.Join(names, ", ")))
	for _, n := range names {
		content = append(content, core.ToolReferenceContent(n))
	}
	return core.Result{Content: content}
}

// NotLoadedText answers a call to a deferred tool the conversation has not
// loaded: it carries the schema, so the model can see what the tool takes,
// and the way to load it.
func NotLoadedText(spec core.ToolSpec) string {
	params := strings.TrimSpace(string(spec.Parameters))
	if params == "" {
		params = "{}"
	}
	return fmt.Sprintf("%s is not loaded yet. Load it with %s (query: %q) and call it again.\n"+
		"Description: %s\nInput schema: %s", spec.Name, Name, spec.Name, spec.Description, params)
}

// Referenced reports the tools that msgs have loaded: the targets of the
// tool_reference blocks in their tool results.
func Referenced(msgs []core.AgentMessage) map[string]bool {
	out := map[string]bool{}
	for _, m := range msgs {
		if m.Role != "tool_result" {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "tool_reference" {
				out[c.ToolName] = true
			}
		}
	}
	return out
}

// Search ranks deferred against query with BM25 over each tool's name,
// description and argument names and descriptions, and returns the names of
// the best limit matches. An exact name (full or without the server prefix)
// ranks first.
func Search(deferred []core.ToolSpec, query string, limit int) []string {
	if limit <= 0 || limit > Limit {
		limit = Limit
	}
	q := strings.ToLower(strings.TrimSpace(query))
	terms := tokenize(q)

	docs := make([][]string, len(deferred))
	total := 0
	for i, s := range deferred {
		docs[i] = document(s)
		total += len(docs[i])
	}
	if len(docs) == 0 {
		return nil
	}
	avg := float64(total) / float64(len(docs))
	df := map[string]int{}
	for _, d := range docs {
		seen := map[string]bool{}
		for _, t := range d {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}

	const k1, b = 1.2, 0.75
	type scored struct {
		name  string
		score float64
	}
	var hits []scored
	n := float64(len(docs))
	for i, s := range deferred {
		_, tool := server(s.Name)
		if q == strings.ToLower(s.Name) || q == strings.ToLower(tool) {
			hits = append(hits, scored{s.Name, math.Inf(1)})
			continue
		}
		tf := map[string]int{}
		for _, t := range docs[i] {
			tf[t]++
		}
		var score float64
		for _, t := range terms {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
			score += idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(len(docs[i]))/avg))
		}
		if score > 0 {
			hits = append(hits, scored{s.Name, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].name < hits[j].name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

// document is the searchable text of a tool. The name counts twice: it is
// the strongest evidence of what a tool does.
func document(s core.ToolSpec) []string {
	name := tokenize(strings.ToLower(s.Name))
	terms := append(append([]string{}, name...), name...)
	terms = append(terms, tokenize(strings.ToLower(s.Description))...)
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if json.Unmarshal(s.Parameters, &schema) == nil {
		for arg, p := range schema.Properties {
			terms = append(terms, tokenize(strings.ToLower(arg))...)
			terms = append(terms, tokenize(strings.ToLower(p.Description))...)
		}
	}
	return terms
}

func tokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
