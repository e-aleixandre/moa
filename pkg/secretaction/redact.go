package secretaction

import (
	"bytes"
	"encoding/json"
	"sort"
)

// Placeholder replaces every delivered value in output.
const Placeholder = "[secret]"

// Redactor replaces exact values in a stream whose chunks may split a value.
// It holds back the shortest tail that could still become a value, so a value
// cut between two chunks is redacted once its end arrives. Redaction prevents
// accidental echoes (a terminal with echo on, an error message); it is not a
// defence against a command written to exfiltrate what it reads.
type Redactor struct {
	values  [][]byte // longest first, so the longest match wins at a position
	pending []byte   // unemitted input: a possible prefix of a value
}

// NewRedactor builds a redactor over values (empty ones are ignored). It
// keeps references to values, not copies: wiping them later is enough.
func NewRedactor(values [][]byte) *Redactor {
	r := &Redactor{}
	for _, v := range values {
		if len(v) > 0 {
			r.values = append(r.values, v)
		}
	}
	sort.SliceStable(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

// Write ingests p and returns the bytes that are safe to emit now. The
// returned slice is fresh; p is not retained.
func (r *Redactor) Write(p []byte) []byte {
	if len(r.values) == 0 {
		return append([]byte(nil), p...)
	}
	buf := make([]byte, 0, len(r.pending)+len(p))
	buf = append(buf, r.pending...)
	buf = append(buf, p...)
	Wipe(r.pending)
	out, rest := r.scan(buf, false)
	r.pending = append(r.pending[:0], rest...)
	Wipe(buf)
	return out
}

// Flush returns whatever was held back, redacted, and forgets it.
func (r *Redactor) Flush() []byte {
	out, _ := r.scan(r.pending, true)
	Wipe(r.pending)
	r.pending = r.pending[:0]
	return out
}

// scan redacts buf left to right. Unless final, it stops at the first position
// whose remainder is a proper prefix of a value and returns that remainder.
func (r *Redactor) scan(buf []byte, final bool) (out, rest []byte) {
	out = make([]byte, 0, len(buf))
	i := 0
	for i < len(buf) {
		if n := r.matchAt(buf[i:]); n > 0 {
			out = append(out, Placeholder...)
			i += n
			continue
		}
		if !final && r.partialAt(buf[i:]) {
			return out, buf[i:]
		}
		out = append(out, buf[i])
		i++
	}
	return out, nil
}

func (r *Redactor) matchAt(b []byte) int {
	for _, v := range r.values {
		if bytes.HasPrefix(b, v) {
			return len(v)
		}
	}
	return 0
}

// partialAt reports whether b (the end of the input) is the beginning of a
// value that has not fully arrived yet.
func (r *Redactor) partialAt(b []byte) bool {
	for _, v := range r.values {
		if len(b) < len(v) && bytes.HasPrefix(v, b) {
			return true
		}
	}
	return false
}

// RedactString redacts a complete text in one go.
func RedactString(s string, values [][]byte) string {
	r := NewRedactor(values)
	out := r.Write([]byte(s))
	out = append(out, r.Flush()...)
	return string(out)
}

// EscapedVariants returns copies of values plus the escaped forms a browser
// tool may echo them in: as a JSON string body and as a single-quoted JS
// string body (Playwright prints `fill('<value>')`). The caller wipes them.
func EscapedVariants(values [][]byte) [][]byte {
	out := make([][]byte, 0, 3*len(values))
	for _, v := range values {
		if len(v) == 0 {
			continue
		}
		out = append(out, append([]byte(nil), v...))
		if j := jsonBody(v); !bytes.Equal(j, v) {
			out = append(out, j)
		}
		if js := singleQuotedBody(v); !bytes.Equal(js, v) {
			out = append(out, js)
		}
	}
	return out
}

// jsonBody is v encoded as a JSON string, without the quotes and without
// HTML escaping.
func jsonBody(v []byte) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(string(v))
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(b) >= 2 {
		b = b[1 : len(b)-1]
	}
	out := append([]byte(nil), b...)
	Wipe(buf.Bytes())
	return out
}

func singleQuotedBody(v []byte) []byte {
	out := make([]byte, 0, len(v)+8)
	for _, c := range v {
		switch c {
		case '\\', '\'':
			out = append(out, '\\', c)
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			out = append(out, c)
		}
	}
	return out
}
