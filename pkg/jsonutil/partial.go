// Package jsonutil provides utilities for working with JSON streams.
package jsonutil

import (
	"encoding/json"
	"strings"
)

// PartialParser parses potentially incomplete JSON objects, returning the best
// result so far. It guarantees monotonic non-regression: once a key is parsed,
// subsequent calls never drop it even if the raw JSON temporarily becomes
// harder to repair.
type PartialParser struct {
	last map[string]any
}

// Parse attempts to parse the (potentially incomplete) JSON string.
// Returns the best parse achieved so far — never regresses to fewer top-level keys.
// Returns nil only if no valid parse has ever been achieved.
func (p *PartialParser) Parse(fullJSON string) map[string]any {
	if len(fullJSON) == 0 {
		return p.last
	}

	// Fast path: try parsing as-is (handles complete JSON).
	var result map[string]any
	if err := json.Unmarshal([]byte(fullJSON), &result); err == nil {
		p.last = mergeMaps(p.last, result)
		return p.last
	}

	// Slow path: attempt repair of incomplete JSON.
	if repaired := tryRepair(fullJSON); repaired != "" {
		if err := json.Unmarshal([]byte(repaired), &result); err == nil {
			p.last = mergeMaps(p.last, result)
		}
	}

	return p.last
}

// Reset clears accumulated state.
func (p *PartialParser) Reset() {
	p.last = nil
}

// mergeMaps merges new into old, preserving all keys from both.
// Keys in new overwrite keys in old (newer values win).
// Keys in old that are absent from new are preserved (monotonic non-regression).
func mergeMaps(old, new map[string]any) map[string]any {
	if old == nil {
		return new
	}
	if new == nil {
		return old
	}
	merged := make(map[string]any, len(old)+len(new))
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range new {
		merged[k] = v
	}
	return merged
}

// tryRepair attempts to repair truncated JSON by closing open constructs.
// It tries multiple strategies and returns the first one that produces valid JSON.
func tryRepair(s string) string {
	// Strategy 1: Close strings/brackets from current position.
	if r := repairFromEnd(s); r != "" {
		var m map[string]any
		if json.Unmarshal([]byte(r), &m) == nil {
			return r
		}
	}

	// Strategy 2: Truncate at last comma, then close.
	if idx := findLastComma(s); idx > 0 {
		truncated := s[:idx]
		if r := repairFromEnd(truncated); r != "" {
			var m map[string]any
			if json.Unmarshal([]byte(r), &m) == nil {
				return r
			}
		}
	}

	// Strategy 3: Just close the outermost brace if the string starts with {.
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		return "{}"
	}

	return ""
}

// repairFromEnd walks the JSON string, tracking nesting, then appends
// the necessary closing characters.
func repairFromEnd(s string) string {
	var stack []byte // '{', '['
	inString := false
	i := 0

	for i < len(s) {
		ch := s[i]

		if inString {
			if ch == '\\' {
				i += 2
				if i > len(s) {
					// Truncated escape — trim it and close the string.
					s = s[:i-2]
					return s + "\"" + closeBrackets(stack)
				}
				continue
			}
			if ch == '"' {
				inString = false
			}
			i++
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '{')
		case '[':
			stack = append(stack, '[')
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				stack = stack[:len(stack)-1]
			}
		case ']':
			if len(stack) > 0 && stack[len(stack)-1] == '[' {
				stack = stack[:len(stack)-1]
			}
		}
		i++
	}

	if inString {
		// Ended inside a string. Close it, then close brackets.
		s = trimPartialEscape(s)
		return s + "\"" + closeBrackets(stack)
	}

	// Remove trailing junk (comma, colon, whitespace after last valid token).
	s = trimTrailing(s)
	return s + closeBrackets(stack)
}

// closeBrackets returns closing characters for the remaining stack.
func closeBrackets(stack []byte) string {
	if len(stack) == 0 {
		return ""
	}
	buf := make([]byte, len(stack))
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			buf[len(stack)-1-i] = '}'
		} else {
			buf[len(stack)-1-i] = ']'
		}
	}
	return string(buf)
}

// findLastComma finds the position of the last comma that's not inside a string.
func findLastComma(s string) int {
	lastComma := -1
	inString := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inString {
			if ch == '\\' {
				i++
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case ',':
			lastComma = i
		}
	}
	return lastComma
}

// trimTrailing removes trailing whitespace, commas, and colons.
// For colons, also removes the preceding key.
func trimTrailing(s string) string {
	s = strings.TrimRight(s, " \t\n\r")
	if len(s) == 0 {
		return s
	}

	last := s[len(s)-1]
	switch last {
	case ',':
		return s[:len(s)-1]
	case ':':
		// Remove key + colon. Find the key's opening quote.
		prefix := strings.TrimRight(s[:len(s)-1], " \t\n\r")
		if len(prefix) > 0 && prefix[len(prefix)-1] == '"' {
			// Find matching opening quote (skip escaped quotes).
			j := len(prefix) - 2
			for j >= 0 {
				if prefix[j] == '"' && (j == 0 || prefix[j-1] != '\\') {
					break
				}
				j--
			}
			if j >= 0 {
				before := strings.TrimRight(prefix[:j], " \t\n\r")
				if len(before) > 0 && before[len(before)-1] == ',' {
					before = before[:len(before)-1]
				}
				return before
			}
		}
		return s[:len(s)-1]
	}
	return s
}

// trimPartialEscape removes trailing partial escape sequences from a string
// that's about to be closed with a quote.
func trimPartialEscape(s string) string {
	// Odd trailing backslashes → remove the last one.
	i := len(s) - 1
	n := 0
	for i >= 0 && s[i] == '\\' {
		n++
		i--
	}
	if n%2 == 1 {
		return s[:len(s)-1]
	}

	// Truncated \uXXXX → remove partial escape.
	for j := len(s) - 1; j >= 1; j-- {
		if s[j] == 'u' && j > 0 && s[j-1] == '\\' {
			remaining := s[j+1:]
			if len(remaining) < 4 {
				return s[:j-1]
			}
			break
		}
		if !isHexDigit(s[j]) {
			break
		}
	}

	return s
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
