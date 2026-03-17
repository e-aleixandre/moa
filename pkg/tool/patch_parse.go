package tool

import (
	"fmt"
	"strings"
	"unicode"
)

// HunkType classifies a patch hunk operation.
type HunkType int

const (
	HunkAdd    HunkType = iota // create a new file
	HunkDelete                 // delete an existing file
	HunkUpdate                 // modify an existing file
)

// PatchHunk represents an operation on a single file within a patch.
type PatchHunk struct {
	Type     HunkType
	Path     string
	MovePath string       // only for update with rename (*** Move to:)
	Content  string       // only for add: full file content
	Chunks   []PatchChunk // only for update: diff chunks
}

// PatchChunk represents a contiguous block of changes within an update hunk.
type PatchChunk struct {
	Context string    // @@ anchor text (empty if none)
	Ops     []PatchOp // operations in order
}

// PatchOpType classifies a line operation within a chunk.
type PatchOpType int

const (
	OpContext PatchOpType = iota // ' ' line — present in both old and new
	OpAdd                       // '+' line — only in new
	OpRemove                    // '-' line — only in old
)

// PatchOp is a single line operation within a chunk.
type PatchOp struct {
	Type PatchOpType
	Line string // line content without the prefix character
}

// ParsePatch parses the Codex-style *** Begin Patch format.
func ParsePatch(text string) ([]PatchHunk, error) {
	lines := strings.Split(text, "\n")

	// Find *** Begin Patch
	startIdx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "*** Begin Patch" {
			startIdx = i + 1
			break
		}
	}
	if startIdx < 0 {
		return nil, fmt.Errorf("patch: missing *** Begin Patch marker")
	}

	// Find *** End Patch
	endIdx := -1
	for i := startIdx; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "*** End Patch" {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return nil, fmt.Errorf("patch: missing *** End Patch marker")
	}

	patchLines := lines[startIdx:endIdx]
	if len(patchLines) == 0 {
		return nil, fmt.Errorf("patch: empty patch (no hunks between markers)")
	}

	return parseHunks(patchLines)
}

// parseHunks parses the body between Begin/End markers into hunks.
func parseHunks(lines []string) ([]PatchHunk, error) {
	var hunks []PatchHunk
	i := 0

	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			i++
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "*** Add File:"):
			path := strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Add File:"))
			if path == "" {
				return nil, fmt.Errorf("patch line %d: *** Add File: missing path", i)
			}
			i++
			// Collect content: all lines must be '+' prefixed or empty
			var contentLines []string
			for i < len(lines) {
				if strings.HasPrefix(lines[i], "***") {
					break
				}
				if strings.HasPrefix(lines[i], "+") {
					contentLines = append(contentLines, lines[i][1:])
				} else if strings.TrimSpace(lines[i]) == "" {
					contentLines = append(contentLines, "")
				} else {
					return nil, fmt.Errorf("patch line %d: unexpected line in Add File block (expected '+' prefix): %q", i, lines[i])
				}
				i++
			}
			hunks = append(hunks, PatchHunk{
				Type:    HunkAdd,
				Path:    path,
				Content: strings.Join(contentLines, "\n") + "\n",
			})

		case strings.HasPrefix(trimmed, "*** Delete File:"):
			path := strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Delete File:"))
			if path == "" {
				return nil, fmt.Errorf("patch line %d: *** Delete File: missing path", i)
			}
			hunks = append(hunks, PatchHunk{
				Type: HunkDelete,
				Path: path,
			})
			i++

		case strings.HasPrefix(trimmed, "*** Update File:"):
			path := strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Update File:"))
			if path == "" {
				return nil, fmt.Errorf("patch line %d: *** Update File: missing path", i)
			}
			i++
			hunk := PatchHunk{Type: HunkUpdate, Path: path}

			// Optional *** Move to:
			if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "*** Move to:") {
				hunk.MovePath = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "*** Move to:"))
				i++
			}

			// Parse chunks (@@, +, -, space lines)
			chunks, newI, err := parseChunks(lines, i)
			if err != nil {
				return nil, err
			}
			if len(chunks) == 0 {
				return nil, fmt.Errorf("patch line %d: Update File %s has no chunks", i, path)
			}
			hunk.Chunks = chunks
			i = newI
			hunks = append(hunks, hunk)

		default:
			return nil, fmt.Errorf("patch line %d: unexpected line (expected *** file header): %q", i, trimmed)
		}
	}

	if len(hunks) == 0 {
		return nil, fmt.Errorf("patch: no valid hunks found")
	}
	return hunks, nil
}

// parseChunks parses update chunks starting at position i.
// Returns the parsed chunks and the new line index.
func parseChunks(lines []string, i int) ([]PatchChunk, int, error) {
	var chunks []PatchChunk
	var current *PatchChunk

	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Stop at next file header or end
		if strings.HasPrefix(trimmed, "*** Add File:") ||
			strings.HasPrefix(trimmed, "*** Delete File:") ||
			strings.HasPrefix(trimmed, "*** Update File:") ||
			trimmed == "*** End Patch" {
			break
		}

		// End of file marker (Codex uses this)
		if trimmed == "*** End of File" {
			i++
			continue
		}

		// @@ starts a new chunk
		if strings.HasPrefix(line, "@@") {
			if current != nil {
				chunks = append(chunks, *current)
			}
			ctx := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
			current = &PatchChunk{Context: ctx}
			i++
			continue
		}

		// Op lines: +, -, or space prefix
		if len(line) > 0 {
			switch line[0] {
			case '+':
				if current == nil {
					current = &PatchChunk{}
				}
				current.Ops = append(current.Ops, PatchOp{Type: OpAdd, Line: line[1:]})
				i++
				continue
			case '-':
				if current == nil {
					current = &PatchChunk{}
				}
				current.Ops = append(current.Ops, PatchOp{Type: OpRemove, Line: line[1:]})
				i++
				continue
			case ' ':
				if current == nil {
					current = &PatchChunk{}
				}
				current.Ops = append(current.Ops, PatchOp{Type: OpContext, Line: line[1:]})
				i++
				continue
			}
		}

		i++
	}

	if current != nil {
		chunks = append(chunks, *current)
	}
	return chunks, i, nil
}

// seekSequence searches for pattern in lines starting at startIdx.
// Uses 4 progressively tolerant passes. Returns the index of the first matching
// line, or -1 if not found.
func seekSequence(lines, pattern []string, startIdx int) int {
	if len(pattern) == 0 {
		return startIdx
	}

	type comparator func(a, b string) bool

	passes := []comparator{
		// Pass 1: exact
		func(a, b string) bool { return a == b },
		// Pass 2: rstrip
		func(a, b string) bool {
			return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t")
		},
		// Pass 3: trim
		func(a, b string) bool {
			return strings.TrimSpace(a) == strings.TrimSpace(b)
		},
		// Pass 4: unicode-normalized + trim
		func(a, b string) bool {
			return normalizeUnicode(strings.TrimSpace(a)) == normalizeUnicode(strings.TrimSpace(b))
		},
	}

	for _, cmp := range passes {
		idx := seekWith(lines, pattern, startIdx, cmp)
		if idx >= 0 {
			return idx
		}
	}
	return -1
}

// seekWith searches for pattern in lines using the given comparator.
func seekWith(lines, pattern []string, startIdx int, cmp func(string, string) bool) int {
	n := len(pattern)
	for i := startIdx; i <= len(lines)-n; i++ {
		match := true
		for j := 0; j < n; j++ {
			if !cmp(lines[i+j], pattern[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// normalizeUnicode replaces common Unicode variants with ASCII equivalents.
func normalizeUnicode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\u00a0': // NBSP
			b.WriteByte(' ')
		case r == '\u2018' || r == '\u2019': // smart single quotes
			b.WriteByte('\'')
		case r == '\u201c' || r == '\u201d': // smart double quotes
			b.WriteByte('"')
		case r == '\u2013': // en dash
			b.WriteByte('-')
		case r == '\u2014': // em dash
			b.WriteString("--")
		case r == '\u2026': // ellipsis
			b.WriteString("...")
		case r > unicode.MaxASCII:
			b.WriteRune(r) // keep other unicode as-is
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// chunkOldLines extracts the "old" lines from a chunk (Context + Remove).
func chunkOldLines(chunk PatchChunk) []string {
	var lines []string
	for _, op := range chunk.Ops {
		if op.Type == OpContext || op.Type == OpRemove {
			lines = append(lines, op.Line)
		}
	}
	return lines
}

// chunkNewLines extracts the "new" lines from a chunk (Context + Add).
func chunkNewLines(chunk PatchChunk) []string {
	var lines []string
	for _, op := range chunk.Ops {
		if op.Type == OpContext || op.Type == OpAdd {
			lines = append(lines, op.Line)
		}
	}
	return lines
}

// applyPatchChunks applies update chunks to file content.
// Chunks are applied with a forward-only cursor.
func applyPatchChunks(originalContent string, chunks []PatchChunk) (string, error) {
	lines := strings.Split(originalContent, "\n")
	// Remove trailing empty element from final newline
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Collect all replacements first, then apply in reverse order
	type replacement struct {
		startIdx int
		oldLen   int
		newLines []string
	}
	var replacements []replacement
	cursor := 0

	for ci, chunk := range chunks {
		old := chunkOldLines(chunk)
		new := chunkNewLines(chunk)

		if len(old) == 0 {
			// Pure additions: find context anchor position
			if chunk.Context != "" {
				// Find the context anchor line
				anchorIdx := seekSequence(lines, []string{chunk.Context}, cursor)
				if anchorIdx < 0 {
					return "", fmt.Errorf("chunk %d: context anchor %q not found", ci+1, chunk.Context)
				}
				cursor = anchorIdx
			}
			// Insert at cursor position
			replacements = append(replacements, replacement{
				startIdx: cursor,
				oldLen:   0,
				newLines: new,
			})
			continue
		}

		// Use context anchor to advance cursor if present
		searchFrom := cursor
		if chunk.Context != "" {
			anchorIdx := seekSequence(lines, []string{chunk.Context}, cursor)
			if anchorIdx >= 0 {
				searchFrom = anchorIdx
			}
		}

		idx := seekSequence(lines, old, searchFrom)
		if idx < 0 {
			// Build a helpful error with the first few lines
			preview := old[0]
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}
			return "", fmt.Errorf("chunk %d: could not find old lines starting with %q (searched from line %d)", ci+1, preview, searchFrom+1)
		}

		replacements = append(replacements, replacement{
			startIdx: idx,
			oldLen:   len(old),
			newLines: new,
		})
		cursor = idx + len(old)
	}

	// Apply replacements in reverse order to preserve indices
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		// Build new slice: before + new lines + after
		after := make([]string, len(lines[r.startIdx+r.oldLen:]))
		copy(after, lines[r.startIdx+r.oldLen:])
		lines = append(lines[:r.startIdx], append(r.newLines, after...)...)
	}

	result := strings.Join(lines, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result, nil
}
