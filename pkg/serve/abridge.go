package serve

import "fmt"

// abridge keeps the first head and last tail characters of text and says, in
// the place of what it cut, how much is missing and how to get it — the same
// shape a truncated bash output has. An owner handed a cut it cannot see reads
// the fragment as the whole; one told "12,000 characters are missing, read
// them with X" decides for itself whether it needs them. how completes the
// sentence "... truncated — <how>".
func abridge(text string, head, tail int, how string) string {
	runes := []rune(text)
	if len(runes) <= head+tail {
		return text
	}
	omitted := len(runes) - head - tail
	return string(runes[:head]) +
		fmt.Sprintf("\n\n[... %d of %d characters truncated — %s ...]\n\n", omitted, len(runes), how) +
		string(runes[len(runes)-tail:])
}

// textChunk returns the characters [offset, offset+size) of text. When more
// remains it ends with the notice the read tool uses for a partial file, and
// next is how to ask for the following chunk given its offset.
func textChunk(text string, offset, size int, next func(offset int) string) (string, error) {
	runes := []rune(text)
	if offset < 0 {
		offset = 0
	}
	if offset > 0 && offset >= len(runes) {
		return "", fmt.Errorf("offset %d is past the end of the message, which has %d characters", offset, len(runes))
	}
	end := min(len(runes), offset+size)
	out := string(runes[offset:end])
	if end < len(runes) {
		out += fmt.Sprintf("\n\n[truncated — showing characters %d-%d of %d, %d more. %s]",
			offset, end, len(runes), len(runes)-end, next(end))
	}
	return out, nil
}
