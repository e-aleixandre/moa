package memory

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// SnippetBytes bounds the excerpt returned per hit. Search exists so the
	// agent can find a fact without paying for its body: it returns where the
	// match is, never the fact itself.
	SnippetBytes = 240
	// DefaultSearchLimit / MaxSearchLimit bound how many hits come back.
	DefaultSearchLimit = 10
	MaxSearchLimit     = 25
)

// matchField says where a query matched, and orders the results: a hit in the
// name is a stronger signal than one buried in a body.
type matchField int

const (
	matchName matchField = iota
	matchDescription
	matchBody
)

func (f matchField) String() string {
	switch f {
	case matchName:
		return "name"
	case matchDescription:
		return "description"
	}
	return "body"
}

// SearchOptions parameterizes Search.
type SearchOptions struct {
	Query  string
	Regex  bool // treat Query as an RE2 pattern instead of a literal substring
	Limit  int
	Offset int
}

// SearchHit is one matching fact with a bounded excerpt around the match.
type SearchHit struct {
	Memory  Memory
	Field   string
	Snippet string
}

// SearchResult is a page of hits plus the total, so the caller can tell the
// agent how to ask for the rest.
type SearchResult struct {
	Hits   []SearchHit
	Total  int
	Offset int
	Limit  int
}

// Search does a lexical search over name, description and body in both scopes.
// Matching is case-insensitive substring by default, or RE2 when opts.Regex is
// set; an uncompilable pattern is an error rather than a silent zero-result.
func (s *Store) Search(opts SearchOptions) (SearchResult, error) {
	if strings.TrimSpace(opts.Query) == "" {
		return SearchResult{}, fmt.Errorf("query is required for search")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	find, err := matcherFor(opts)
	if err != nil {
		return SearchResult{}, err
	}

	var hits []SearchHit
	for _, m := range s.List() {
		for _, cand := range []struct {
			field matchField
			text  string
		}{
			{matchName, m.Name},
			{matchDescription, m.Description},
			{matchBody, m.Body},
		} {
			start, end, ok := find(cand.text)
			if !ok {
				continue
			}
			hits = append(hits, SearchHit{
				Memory:  m,
				Field:   cand.field.String(),
				Snippet: snippetAround(cand.text, start, end),
			})
			break // one hit per fact: the strongest field wins
		}
	}

	// Deterministic order: strongest field first, then alphabetical by ID, so
	// paging with offset is stable across calls.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Field != hits[j].Field {
			return fieldRank(hits[i].Field) < fieldRank(hits[j].Field)
		}
		return hits[i].Memory.ID() < hits[j].Memory.ID()
	})

	total := len(hits)
	if offset > total {
		offset = total
	}
	page := hits[offset:]
	if len(page) > limit {
		page = page[:limit]
	}
	return SearchResult{Hits: page, Total: total, Offset: offset, Limit: limit}, nil
}

func fieldRank(field string) int {
	switch field {
	case "name":
		return 0
	case "description":
		return 1
	}
	return 2
}

// matcherFor returns a function locating the first match in a text.
func matcherFor(opts SearchOptions) (func(string) (int, int, bool), error) {
	if opts.Regex {
		re, err := regexp.Compile(opts.Query)
		if err != nil {
			return nil, fmt.Errorf("invalid regex %q: %v", opts.Query, err)
		}
		return func(text string) (int, int, bool) {
			loc := re.FindStringIndex(text)
			if loc == nil {
				return 0, 0, false
			}
			return loc[0], loc[1], true
		}, nil
	}
	needle := strings.ToLower(opts.Query)
	return func(text string) (int, int, bool) {
		// Lowercasing can change byte lengths for some runes, so index into the
		// folded copy only to detect the match, then map back by scanning the
		// original with the same fold applied prefix-wise.
		i := strings.Index(strings.ToLower(text), needle)
		if i < 0 {
			return 0, 0, false
		}
		start := originalOffset(text, i)
		end := originalOffset(text, i+len(needle))
		return start, end, true
	}, nil
}

// originalOffset maps a byte offset in strings.ToLower(text) back to text.
// Folding is per-rune, so walk both in step until the folded offset is reached.
func originalOffset(text string, foldedOffset int) int {
	folded := 0
	for i, r := range text {
		if folded >= foldedOffset {
			return i
		}
		folded += len(strings.ToLower(string(r)))
	}
	return len(text)
}

// snippetAround returns at most SnippetBytes of text centered on [start,end),
// cut on rune boundaries and marked with ellipses when it is not the whole
// text. The ellipses count against the budget: the result is never longer than
// SnippetBytes.
func snippetAround(text string, start, end int) string {
	if len(text) <= SnippetBytes {
		return collapseWhitespace(text)
	}
	// Reserve room for both ellipses up front; a snippet in the middle of a
	// long body almost always needs them, and over-reserving costs one rune.
	const ellipsis = "…"
	window := SnippetBytes - 2*len(ellipsis)
	matchLen := end - start
	pad := (window - matchLen) / 2
	if pad < 0 {
		pad = 0
	}
	from := start - pad
	if from < 0 {
		from = 0
	}
	to := from + window
	if to > len(text) {
		to = len(text)
		from = to - window
		if from < 0 {
			from = 0
		}
	}
	// Never split a rune: half a character is invalid UTF-8 in the tool result.
	for from > 0 && !utf8.RuneStart(text[from]) {
		from++
	}
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to--
	}
	snippet := collapseWhitespace(text[from:to])
	if from > 0 {
		snippet = ellipsis + snippet
	}
	if to < len(text) {
		snippet += ellipsis
	}
	return snippet
}

// collapseWhitespace keeps a snippet on one line: bodies are markdown, and a
// multi-line excerpt would blur the boundary between results.
func collapseWhitespace(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}
