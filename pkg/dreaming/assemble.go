package dreaming

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A unit is one sheet of the book as the dreaming consolidates it: x.md plus
// the x/parte-NN.md files book-tidy split it into. File 0 is always the sheet
// itself; it is the only file that survives a consolidation.
type Unit struct {
	Sheet string   // book-relative path of x.md
	Files []string // Files[0] == Sheet, then the parts in order
}

// Plan is what the organizer model returns for a unit. It never carries text
// that ends up as a kept or archived line: only line numbers, plus the new
// state block, which is the single piece of new prose.
type Plan struct {
	NoChanges bool        `json:"sin_cambios"`
	State     string      `json:"estado"`
	Keep      [][3]int    `json:"conservar"` // [file, first, last], 1-based inclusive
	Archive   []Archived  `json:"archivar"`
	Promote   []Promotion `json:"nuevas_decisiones,omitempty"`
}

// Archived is one range the organizer retires, with why and on what evidence.
type Archived struct {
	File   int    `json:"f"`
	Lines  [2]int `json:"lineas"`
	Reason string `json:"motivo"`
	Source string `json:"fuente"`
}

// isDecision reports whether a unit is one of the user's decisions: those
// are never organized; they only get "Superada por" marks (decisions.go).
func isDecision(u Unit) bool { return strings.HasPrefix(u.Sheet, "decisions/") }

// Reasons an archived range may carry. ReasonUnconfirmed is the only one that
// needs no source, and it is paid for with a visible bullet in the state.
const (
	ReasonSuperseded  = "superado"
	ReasonClosed      = "cerrado"
	ReasonRepeated    = "repetido"
	ReasonUnconfirmed = "por_confirmar"
)

// Limits of what an assembled unit may produce.
const (
	MaxSheetBytes   = 48 * 1024
	MaxArchiveBytes = 24 * 1024
	maxStateBytes   = 16 * 1024
)

// Output is everything one unit writes: whole files (the new sheet and the new
// archive files) and the paths it removes (the parts).
type Output struct {
	Files  map[string][]byte
	Remove []string
}

// AssembleInput is a unit, its original bytes and the context the guard needs.
type AssembleInput struct {
	Unit  Unit
	Orig  [][]byte // one per Unit.Files
	Plan  Plan
	Date  string // YYYY-MM-DD, the night's civil date
	Nonce string // per night; must not occur in any original nor in the state
	S0    string // snapshot commit the sources are anchored to
	// SourceExists reports whether "path:line" names a real line of the
	// snapshot. It is how a reason other than por_confirmar proves it points at
	// something.
	SourceExists func(path string, line int) bool
}

// splitLines cuts data into lines that keep their terminators, so joining them
// gives back the exact bytes (CRLF and a missing final newline included).
func splitLines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			out = append(out, data)
			break
		}
		out = append(out, data[:i+1])
		data = data[i+1:]
	}
	return out
}

func trimEOL(line []byte) string {
	return strings.TrimRight(string(line), "\r\n")
}

// headerLen is how many leading lines of the sheet are its header: the front
// matter (aliases, verified_at, the title search weighs most) and the "# title"
// line when only blank lines separate it. The header is kept verbatim at the
// top and is never archived. It is a pure function of the original, which is
// what lets the guard recompute it independently.
func headerLen(lines [][]byte) int {
	n := 0
	if len(lines) > 0 && trimEOL(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if trimEOL(lines[i]) == "---" {
				n = i + 1
				break
			}
		}
	}
	for i := n; i < len(lines); i++ {
		t := strings.TrimSpace(trimEOL(lines[i]))
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "# ") {
			return i + 1
		}
		break
	}
	return n
}

func markerPrefix(nonce string) string { return "<!-- dreaming:" + nonce + " " }

// block renders one verbatim range: a marker line naming where the bytes come
// from and how many there are, then exactly those bytes. A payload without a
// final newline gets one separator newline, which the parser knows to skip.
func block(nonce string, file, a, b int, payload []byte, extra string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%sf=%d l=%d-%d len=%d%s -->\n", markerPrefix(nonce), file, a, b, len(payload), extra)
	buf.Write(payload)
	if len(payload) > 0 && payload[len(payload)-1] != '\n' {
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

var safeSource = regexp.MustCompile(`^[^\s:]+:[0-9]+$`)

// Assemble builds a unit's output from its plan. It refuses anything the
// deterministic rules cannot prove (see Verify, which it runs on its own
// output before returning it).
func Assemble(in AssembleInput) (Output, error) {
	u, plan := in.Unit, in.Plan
	if len(u.Files) == 0 || u.Files[0] != u.Sheet || len(in.Orig) != len(u.Files) {
		return Output{}, fmt.Errorf("malformed unit %q", u.Sheet)
	}
	if plan.NoChanges {
		return Output{}, nil
	}
	if in.Nonce == "" || strings.ContainsAny(in.Nonce, " \n-") {
		return Output{}, fmt.Errorf("invalid nonce")
	}
	for _, o := range in.Orig {
		if bytes.Contains(o, []byte(in.Nonce)) {
			return Output{}, fmt.Errorf("nonce occurs in the original")
		}
	}
	state := strings.Trim(plan.State, "\n")
	if strings.Contains(state, in.Nonce) {
		return Output{}, fmt.Errorf("nonce occurs in the state")
	}
	if isDecision(u) {
		return Output{}, fmt.Errorf("%s is a decision: it is never organized", u.Sheet)
	}
	if !strings.Contains(state, "## Estado actual") {
		return Output{}, fmt.Errorf("the state needs a \"## Estado actual\" section")
	}
	if len(state) > maxStateBytes {
		return Output{}, fmt.Errorf("the state is %d bytes (max %d)", len(state), maxStateBytes)
	}

	lines := make([][][]byte, len(in.Orig))
	for f, o := range in.Orig {
		lines[f] = splitLines(o)
	}
	head := headerLen(lines[0])

	// Every line outside the header must be claimed exactly once, by a kept or
	// an archived range.
	owner := make([][]int, len(lines)) // 0 unclaimed, 1 kept, 2 archived
	for f := range lines {
		owner[f] = make([]int, len(lines[f]))
	}
	claim := func(f, a, b, who int) error {
		if f < 0 || f >= len(lines) {
			return fmt.Errorf("file %d is not in the unit", f)
		}
		if a < 1 || b < a || b > len(lines[f]) {
			return fmt.Errorf("range %d:%d-%d is outside the file (%d lines)", f, a, b, len(lines[f]))
		}
		if f == 0 && a <= head {
			return fmt.Errorf("range %d:%d-%d touches the header (lines 1-%d stay on their own)", f, a, b, head)
		}
		for i := a - 1; i < b; i++ {
			if owner[f][i] != 0 {
				return fmt.Errorf("line %d:%d is claimed twice", f, i+1)
			}
			owner[f][i] = who
		}
		return nil
	}
	for _, k := range plan.Keep {
		if err := claim(k[0], k[1], k[2], 1); err != nil {
			return Output{}, err
		}
	}
	for _, ar := range plan.Archive {
		if err := claim(ar.File, ar.Lines[0], ar.Lines[1], 2); err != nil {
			return Output{}, err
		}
		if err := checkReason(in, state, ar); err != nil {
			return Output{}, err
		}

	}
	for f := range lines {
		start := 0
		if f == 0 {
			start = head
		}
		for i := start; i < len(lines[f]); i++ {
			if owner[f][i] == 0 {
				return Output{}, fmt.Errorf("line %d:%d is neither kept nor archived", f, i+1)
			}
		}
	}

	slice := func(f, a, b int) []byte {
		return bytes.Join(lines[f][a-1:b], nil)
	}
	// Kept ranges stay in the order the organizer gave them; it may move a part
	// next to the sheet it belongs with.
	var sheet bytes.Buffer
	for _, l := range lines[0][:head] {
		sheet.Write(l)
	}
	if head > 0 && !bytes.HasSuffix(lines[0][head-1], []byte("\n")) {
		sheet.WriteByte('\n')
	}
	stem := strings.TrimSuffix(u.Sheet, ".md")
	fmt.Fprintf(&sheet, "\n> Consolidada por el dreaming el %s: arriba, el estado actual; debajo, literal, el detalle que sigue vigente. Lo retirado está, literal, en `archivo/%s/`.\n\n", in.Date, stem)
	sheet.WriteString(state)
	sheet.WriteString("\n")
	if len(plan.Keep) > 0 {
		sheet.WriteString("\n## Detalle vigente (literal)\n\n")
		for _, k := range plan.Keep {
			sheet.Write(block(in.Nonce, k[0], k[1], k[2], slice(k[0], k[1], k[2]), ""))
		}
	}
	if sheet.Len() > MaxSheetBytes {
		return Output{}, fmt.Errorf("the sheet would be %d bytes (max %d); keep less", sheet.Len(), MaxSheetBytes)
	}

	// Archive: ranges in file and line order, cut into files of bounded size.
	arch := append([]Archived(nil), plan.Archive...)
	sort.Slice(arch, func(i, j int) bool {
		if arch[i].File != arch[j].File {
			return arch[i].File < arch[j].File
		}
		return arch[i].Lines[0] < arch[j].Lines[0]
	})
	var blocks [][]byte
	for _, ar := range arch {
		extra := " motivo=" + ar.Reason
		if ar.Source != "" {
			extra += " fuente=S0:" + shortSHA(in.S0) + ":" + ar.Source
		}

		// A range bigger than an archive file is cut at line boundaries.
		a := ar.Lines[0]
		for a <= ar.Lines[1] {
			b, size := a, len(lines[ar.File][a-1])
			for b < ar.Lines[1] && size+len(lines[ar.File][b]) <= MaxArchiveBytes-1024 {
				size += len(lines[ar.File][b])
				b++
			}
			blocks = append(blocks, block(in.Nonce, ar.File, a, b, slice(ar.File, a, b), extra))
			a = b + 1
		}
	}
	files := map[string][]byte{u.Sheet: sheet.Bytes()}
	if len(blocks) > 0 {
		var chunks [][]byte
		var cur bytes.Buffer
		for _, bl := range blocks {
			if cur.Len() > 0 && cur.Len()+len(bl) > MaxArchiveBytes-512 {
				chunks = append(chunks, append([]byte(nil), cur.Bytes()...))
				cur.Reset()
			}
			cur.Write(bl)
		}
		chunks = append(chunks, append([]byte(nil), cur.Bytes()...))
		for i, c := range chunks {
			name := fmt.Sprintf("archivo/%s/%s.md", stem, in.Date)
			if len(chunks) > 1 {
				name = fmt.Sprintf("archivo/%s/%s-%02d.md", stem, in.Date, i+1)
			}
			head := fmt.Sprintf("> Archivo del dreaming (%s): texto literal retirado de `%s` por superado, cerrado o repetido. Lo vigente está en `%s`.\n\n", in.Date, u.Sheet, u.Sheet)
			files[name] = append([]byte(head), c...)
		}
	}
	out := Output{Files: files, Remove: append([]string(nil), u.Files[1:]...)}
	if err := Verify(u, in.Orig, out, in.Nonce); err != nil {
		return Output{}, fmt.Errorf("guard: %w", err)
	}
	return out, nil
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// checkReason enforces I8: a reason with a source that exists in the snapshot,
// or por_confirmar with a bullet in the state that names the range.
func checkReason(in AssembleInput, state string, ar Archived) error {
	switch ar.Reason {
	case ReasonSuperseded, ReasonClosed, ReasonRepeated:
		if !safeSource.MatchString(ar.Source) {
			return fmt.Errorf("archived %d:%d-%d: source %q is not path:line", ar.File, ar.Lines[0], ar.Lines[1], ar.Source)
		}
		i := strings.LastIndexByte(ar.Source, ':')
		line, _ := strconv.Atoi(ar.Source[i+1:])
		if in.SourceExists == nil || !in.SourceExists(ar.Source[:i], line) {
			return fmt.Errorf("archived %d:%d-%d: source %s does not exist in the snapshot", ar.File, ar.Lines[0], ar.Lines[1], ar.Source)
		}
		return nil
	case ReasonUnconfirmed:
		if ar.Source != "" && !safeSource.MatchString(ar.Source) {
			return fmt.Errorf("archived %d:%d-%d: source %q is not path:line", ar.File, ar.Lines[0], ar.Lines[1], ar.Source)
		}
		want := fmt.Sprintf("%s:%d-%d", in.Unit.Files[ar.File], ar.Lines[0], ar.Lines[1])
		for _, l := range strings.Split(state, "\n") {
			if strings.Contains(strings.ToLower(l), "por confirmar") && strings.Contains(l, want) {
				return nil
			}
		}
		return fmt.Errorf("archived %s as por_confirmar without a \"por confirmar\" bullet naming %s", want, want)
	default:
		return fmt.Errorf("archived %d:%d-%d: unknown reason %q", ar.File, ar.Lines[0], ar.Lines[1], ar.Reason)
	}
}

// Verify is the independent guard (I1): it rebuilds every original file of the
// unit from the bytes about to be written — the header the rule puts first,
// then each marked block, read as opaque payloads of their declared length —
// and requires the result to equal the original byte for byte. It shares no
// state with Assemble beyond the header rule and the marker format.
func Verify(u Unit, orig [][]byte, out Output, nonce string) error {
	if len(orig) != len(u.Files) {
		return fmt.Errorf("unit has %d files, %d originals", len(u.Files), len(orig))
	}
	sheet, ok := out.Files[u.Sheet]
	if !ok {
		return fmt.Errorf("the sheet %s is not in the output", u.Sheet)
	}
	for _, r := range out.Remove {
		found := false
		for _, f := range u.Files[1:] {
			found = found || f == r
		}
		if !found {
			return fmt.Errorf("removes %s, which is not a part of the unit", r)
		}
	}
	if len(out.Remove) != len(u.Files)-1 {
		return fmt.Errorf("removes %d paths, the unit has %d parts", len(out.Remove), len(u.Files)-1)
	}
	lines0 := splitLines(orig[0])
	head := bytes.Join(lines0[:headerLen(lines0)], nil)
	if !bytes.HasPrefix(sheet, head) {
		return fmt.Errorf("the sheet does not start with the original header")
	}
	type piece struct {
		a, b    int
		payload []byte
	}
	got := make([][]piece, len(orig))
	parse := func(name string, data []byte) error {
		prefix := []byte(markerPrefix(nonce))
		pos := 0
		for pos < len(data) {
			nl := bytes.IndexByte(data[pos:], '\n')
			if nl < 0 {
				nl = len(data) - pos
			}
			line := data[pos : pos+nl]
			next := pos + nl + 1
			if !bytes.HasPrefix(line, prefix) {
				if bytes.Contains(line, []byte(nonce)) {
					return fmt.Errorf("%s: stray nonce outside a marker", name)
				}
				pos = next
				continue
			}
			var f, a, b, n int
			rest := string(line[len(prefix):])
			if _, err := fmt.Sscanf(rest, "f=%d l=%d-%d len=%d", &f, &a, &b, &n); err != nil {
				return fmt.Errorf("%s: malformed marker %q", name, line)
			}
			if !strings.HasSuffix(rest, " -->") {
				return fmt.Errorf("%s: malformed marker %q", name, line)
			}
			if f < 0 || f >= len(orig) || n < 0 || next+n > len(data) {
				return fmt.Errorf("%s: marker out of bounds %q", name, line)
			}
			payload := data[next : next+n]
			got[f] = append(got[f], piece{a, b, payload})
			pos = next + n
			if n > 0 && payload[n-1] != '\n' {
				if pos >= len(data) || data[pos] != '\n' {
					return fmt.Errorf("%s: missing separator after a block", name)
				}
				pos++
			}
		}
		return nil
	}
	if err := parse(u.Sheet, sheet[len(head):]); err != nil {
		return err
	}
	names := make([]string, 0, len(out.Files))
	for name := range out.Files {
		if name != u.Sheet {
			if !strings.HasPrefix(name, "archivo/") {
				return fmt.Errorf("writes %s, outside the sheet and the archive", name)
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := parse(name, out.Files[name]); err != nil {
			return err
		}
	}
	for f := range orig {
		lines := splitLines(orig[f])
		next := 1
		var rebuilt bytes.Buffer
		if f == 0 {
			next = headerLen(lines) + 1
			rebuilt.Write(head)
		}
		ps := got[f]
		sort.Slice(ps, func(i, j int) bool { return ps[i].a < ps[j].a })
		for _, p := range ps {
			if p.a != next || p.b < p.a || p.b > len(lines) {
				return fmt.Errorf("file %d: block %d-%d does not continue at line %d", f, p.a, p.b, next)
			}
			if !bytes.Equal(p.payload, bytes.Join(lines[p.a-1:p.b], nil)) {
				return fmt.Errorf("file %d: block %d-%d differs from the original", f, p.a, p.b)
			}
			rebuilt.Write(p.payload)
			next = p.b + 1
		}
		if next != len(lines)+1 {
			return fmt.Errorf("file %d: lines %d-%d are missing", f, next, len(lines))
		}
		if !bytes.Equal(rebuilt.Bytes(), orig[f]) {
			return fmt.Errorf("file %d does not rebuild byte for byte", f)
		}
	}
	return nil
}
