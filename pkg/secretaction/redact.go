package secretaction

import (
	"bytes"
	"encoding/json"
	"sort"
)

// Placeholder replaces every delivered value in output.
const Placeholder = "[secret]"

// Redactor replaces exact values in a stream whose chunks may split a value.
// Every byte inside any occurrence of any value is covered, overlapping
// occurrences included, and each run of overlapping occurrences becomes one
// placeholder: a value that starts inside another cannot leave a tail behind.
// It holds back the shortest tail that could still become a value, so a value
// cut between two chunks is redacted once its end arrives. Redaction prevents
// accidental echoes (a terminal with echo on, an error message); it is not a
// defence against a command written to exfiltrate what it reads.
type Redactor struct {
	values  [][]byte
	pending []byte // unemitted input: a possible prefix of a value
	// covered is how many leading bytes of pending belong to a run whose
	// placeholder was already emitted.
	covered int
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
	r.covered = 0
	return out
}

type span struct{ start, end int }

// scan redacts buf. Unless final, it emits only up to the first position
// whose remainder is a proper prefix of a value (no later byte can be
// covered by a match starting earlier) and returns the rest.
func (r *Redactor) scan(buf []byte, final bool) (out, rest []byte) {
	hold := len(buf)
	if !final {
		for i := range buf {
			if r.partialAt(buf[i:]) {
				hold = i
				break
			}
		}
	}
	carried := r.covered
	r.covered = 0
	out = make([]byte, 0, hold)
	i := 0
	for _, run := range r.runs(buf, carried) {
		continued := carried > 0 && run.start == 0
		if run.start >= hold && !continued {
			break
		}
		// A run that crosses the hold has its placeholder now: the held
		// bytes it covers must not produce another one later.
		if run.end > hold {
			r.covered = run.end - hold
		}
		if run.start >= hold {
			break
		}
		out = append(out, buf[i:run.start]...)
		if !continued {
			out = append(out, Placeholder...)
		}
		i = run.end
	}
	if i < hold {
		out = append(out, buf[i:hold]...)
	}
	return out, buf[hold:]
}

// runs returns the maximal groups of overlapping occurrences of any value in
// buf, plus its first carried bytes (already placeheld), sorted. Occurrences
// that only touch stay separate.
func (r *Redactor) runs(buf []byte, carried int) []span {
	var spans []span
	if carried > 0 {
		spans = append(spans, span{0, carried})
	}
	for _, v := range r.values {
		for from := 0; from < len(buf); {
			j := bytes.Index(buf[from:], v)
			if j < 0 {
				break
			}
			spans = append(spans, span{from + j, from + j + len(v)})
			from += j + 1
		}
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	var runs []span
	for _, s := range spans {
		if n := len(runs); n > 0 && s.start < runs[n-1].end {
			runs[n-1].end = max(runs[n-1].end, s.end)
			continue
		}
		runs = append(runs, s)
	}
	return runs
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
