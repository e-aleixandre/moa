package dreaming

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Promotion asks for a new decision file for a decision of the user that
// only lives in a sheet (decisions/dreaming.md, 11/11). The model names the
// file, a title and the lines; the body is copied by code, byte for byte,
// from the snapshot, so the decision itself is never paraphrased.
type Promotion struct {
	Path   string `json:"ruta"`   // decisions/<slug>.md, must not exist
	Title  string `json:"titulo"` // one line
	Source string `json:"fuente"` // <file of this unit>:<a>-<b>
}

var (
	promotionPath  = regexp.MustCompile(`^decisions/[a-z0-9][a-z0-9-]{0,80}\.md$`)
	promotionRange = regexp.MustCompile(`^(.+):([0-9]+)-([0-9]+)$`)
)

// promotionFile renders the new decision: a title, where it was copied from,
// and the cited lines exactly as they are in the snapshot.
func promotionFile(u Unit, orig [][]byte, p Promotion, date, s0 string, exists func(string) bool) ([]byte, error) {
	if !promotionPath.MatchString(p.Path) || exists(p.Path) {
		return nil, fmt.Errorf("promotion %q: needs a new decisions/<slug>.md", p.Path)
	}
	title := strings.TrimSpace(p.Title)
	if title == "" || len(title) > 120 || strings.ContainsAny(title, "\r\n`") {
		return nil, fmt.Errorf("promotion %s: the title must be one short line", p.Path)
	}
	m := promotionRange.FindStringSubmatch(p.Source)
	if m == nil {
		return nil, fmt.Errorf("promotion %s: source %q is not <file>:<a>-<b>", p.Path, p.Source)
	}
	f := -1
	for i, name := range u.Files {
		if name == m[1] {
			f = i
		}
	}
	a, _ := strconv.Atoi(m[2])
	b, _ := strconv.Atoi(m[3])
	if f < 0 {
		return nil, fmt.Errorf("promotion %s: %s is not a file of %s", p.Path, m[1], u.Sheet)
	}
	lines := splitLines(orig[f])
	if a < 1 || b < a || b > len(lines) {
		return nil, fmt.Errorf("promotion %s: lines %d-%d out of range", p.Path, a, b)
	}
	quote := bytes.Join(lines[a-1:b], nil)
	var out bytes.Buffer
	fmt.Fprintf(&out, "# %s\n\n> Decisión del dueño copiada literal por el dreaming el %s desde `%s:%d-%d` (S0 %s).\n\n", title, date, m[1], a, b, shortSHA(s0))
	out.Write(quote)
	if !bytes.HasSuffix(quote, []byte("\n")) {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
