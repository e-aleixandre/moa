package dreaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The project memory is consolidated fact by fact (decisions/dreaming.md,
// point 2): one organizer call judges every pending fact, and code turns each
// verdict into a group of its own. A fact is never rewritten: it is kept,
// moved verbatim to memory/archivo/, or corrected by appending a dated note
// after its exact original bytes.

// Verdicts and limits of the memory pass.
const (
	VerdictKeep    = "conservar"
	VerdictArchive = "archivar"
	VerdictCorrect = "corregir"
	// maxFactBatchBytes is the most fact text one call takes; the rest waits
	// for another night.
	maxFactBatchBytes = 200 * 1024
	maxNoteBytes      = 300
)

// factVerdict is the organizer's answer for one fact.
type factVerdict struct {
	Fact      string `json:"hecho"`
	Verdict   string `json:"veredicto"`
	Reason    string `json:"motivo"`
	Source    string `json:"fuente"`
	Note      string `json:"nota"`
	ValidDesc *bool  `json:"descripcion_valida"`
}

type factAnswer struct {
	Facts []factVerdict `json:"hechos"`
}

// factOutcome is what the memory pass decided, for the state and the news.
type factOutcome struct {
	groups  []Group
	content map[string][]byte
	kept    map[string]string // fact → S0 blob, recorded as processed
	verdict map[string]string // group id → verdict, for the news
	review  []string          // corrections refused because the description would lie
	secrets []string          // facts never sent nor touched
}

// isFactPath reports whether a repository path is a fact: a top-level .md of
// the memory, other than the reserved MEMORY.md the memory store ignores.
func isFactPath(p string) bool {
	rel, ok := strings.CutPrefix(p, memoryPrefix)
	return ok && !strings.Contains(rel, "/") && strings.HasSuffix(rel, ".md") && rel != "MEMORY.md"
}

// DueFacts returns the facts whose bytes changed since the dreaming last
// processed (or undid) them, most recently modified first.
func (s Store) DueFacts(snap Snapshot) ([]string, error) {
	st, err := s.loadState()
	if err != nil {
		return nil, err
	}
	var out []string
	for p, id := range snap.Blobs {
		if isFactPath(p) && st.Units[p] != id {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	mod := map[string]int64{}
	for _, p := range out {
		if info, err := os.Stat(filepath.Join(s.memoryDir(), strings.TrimPrefix(p, memoryPrefix))); err == nil {
			mod[p] = info.ModTime().UnixNano()
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return mod[out[i]] > mod[out[j]] })
	return out, nil
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`xox[bp]-[A-Za-z0-9-]+`),
	regexp.MustCompile(`(?i)password\s*[:=]`),
	regexp.MustCompile(`(?i)token\s*[:=]`),
	regexp.MustCompile(`(?i)(key|secret)[A-Za-z0-9_-]*["']?\s*[:=]?\s*["']?[A-Za-z0-9+/_-]{32,}`),
}

// isSecret reports whether a fact is marked secret in its front matter or
// looks like it holds a credential (I9). It errs on the side of a secret: a
// false positive only leaves a fact as it is.
func isSecret(data []byte) bool {
	lines := splitLines(data)
	if len(lines) > 0 && trimEOL(lines[0]) == "---" {
		for _, l := range lines[1:] {
			t := trimEOL(l)
			if t == "---" {
				break
			}
			key, val, ok := strings.Cut(t, ":")
			if !ok || strings.TrimSpace(key) != "secret" {
				continue
			}
			if strings.Trim(strings.TrimSpace(val), `"'`) == "true" {
				return true
			}
		}
	}
	for _, re := range secretPatterns {
		if re.Match(data) {
			return true
		}
	}
	return false
}

// sourceInSnapshot checks a "path:line" citation of the memory pass: a book
// path (book-relative) or a memory/… path, with the line inside the file in S0.
// It returns the repository path.
func sourceInSnapshot(snap Snapshot, src string) (string, error) {
	i := strings.LastIndexByte(src, ':')
	if i <= 0 {
		return "", fmt.Errorf("source %q is not <path>:<line>", src)
	}
	line, err := strconv.Atoi(src[i+1:])
	if err != nil {
		return "", fmt.Errorf("source %q is not <path>:<line>", src)
	}
	p := src[:i]
	if !strings.HasPrefix(p, memoryPrefix) {
		p = bookPrefix + p
	}
	data, ok := snap.Files[p]
	if !ok || line < 1 || line > len(splitLines(data)) {
		return "", fmt.Errorf("source %q is not in the snapshot", src)
	}
	return p, nil
}

// consolidateFacts runs the memory pass over the pending facts: secrets are
// set aside, the rest go to one organizer call (up to maxFactBatchBytes), and
// every valid verdict becomes a group. Invalid answers land in rejected.
func (r *Run) consolidateFacts(ctx context.Context, b *Budget, cfg NightConfig, snap Snapshot, due []string, rejected map[string]string, deferred *[]string) factOutcome {
	out := factOutcome{content: map[string][]byte{}, kept: map[string]string{}, verdict: map[string]string{}}
	var batch []string
	var prompt strings.Builder
	size := 0
	for _, p := range due {
		data := snap.Files[p]
		if isSecret(data) {
			out.secrets = append(out.secrets, p)
			out.kept[p] = snap.Blobs[p]
			continue
		}
		var f strings.Builder
		fmt.Fprintf(&f, "=== hecho %d · %s ===\n", len(batch)+1, strings.TrimPrefix(p, memoryPrefix))
		for i, l := range splitLines(data) {
			fmt.Fprintf(&f, "%d| %s\n", i+1, trimEOL(l))
		}
		if size+f.Len() > maxFactBatchBytes {
			*deferred = append(*deferred, p)
			continue
		}
		size += f.Len()
		batch = append(batch, p)
		prompt.WriteString(f.String())
		prompt.WriteString("\n")
	}
	sort.Strings(out.secrets)
	if len(batch) == 0 {
		return out
	}
	var p strings.Builder
	fmt.Fprintf(&p, "Fecha de hoy: %s.\n\nDecisiones del dueño (índice):\n%s\nHechos de la memoria del proyecto:\n\n", cfg.Date, decisionsIndex(snap))
	p.WriteString(prompt.String())
	res, err := b.call(ctx, cfg.Factory, cfg.Organizer, memorySystem, p.String(), organizerMaxOut)
	if errors.Is(err, ErrBudget) {
		*deferred = append(*deferred, batch...)
		return out
	}
	if err != nil {
		for _, f := range batch {
			rejected[f] = "veredicto: " + err.Error()
		}
		return out
	}
	answer, perr := parseFactAnswer(res.Text)
	if perr != nil {
		for _, f := range batch {
			rejected[f] = "veredicto: " + perr.Error()
		}
		return out
	}
	inBatch := map[string]bool{}
	for _, f := range batch {
		inBatch[f] = true
	}
	byFact := map[string]factVerdict{}
	dup := map[string]bool{}
	for _, v := range answer.Facts {
		f := memoryPrefix + strings.TrimPrefix(v.Fact, memoryPrefix)
		if !inBatch[f] {
			continue // not a fact this call was shown; it changes nothing
		}
		if _, seen := byFact[f]; seen {
			dup[f] = true
		}
		byFact[f] = v
	}
	for _, f := range batch {
		v, ok := byFact[f]
		switch {
		case !ok:
			rejected[f] = "veredicto: sin respuesta"
			continue
		case dup[f]:
			rejected[f] = "veredicto: repetido"
			continue
		}
		if err := r.factGroup(snap, cfg.Date, f, v, byFact, &out); err != nil {
			rejected[f] = "veredicto: " + err.Error()
		}
	}
	return out
}

// factGroup validates one verdict and records what it does.
func (r *Run) factGroup(snap Snapshot, date, f string, v factVerdict, all map[string]factVerdict, out *factOutcome) error {
	pre := snap.Blobs[f]
	orig := snap.Files[f]
	switch v.Verdict {
	case VerdictKeep:
		out.kept[f] = pre
		return nil
	case VerdictArchive, VerdictCorrect:
	default:
		return fmt.Errorf("unknown verdict %q", v.Verdict)
	}
	src, err := sourceInSnapshot(snap, v.Source)
	if err != nil {
		return err
	}
	if src == f {
		return fmt.Errorf("a fact cannot be its own source")
	}
	g := Group{ID: f, Kind: "fact", Sources: []Change{{Path: src, Pre: snap.Blobs[src]}}}
	if v.Verdict == VerdictArchive {
		switch v.Reason {
		case ReasonSuperseded, ReasonClosed, ReasonRepeated:
		default:
			return fmt.Errorf("archiving needs motivo superado, cerrado or repetido, not %q", v.Reason)
		}
		// Two facts archived on each other's word would both leave the index.
		if o, ok := all[src]; ok && o.Verdict == VerdictArchive {
			return fmt.Errorf("its source %s is archived the same night", src)
		}
		dest := memoryPrefix + "archivo/" + date + "/" + strings.TrimPrefix(f, memoryPrefix)
		if _, ok := snap.Files[dest]; ok {
			return fmt.Errorf("%s already exists", dest)
		}
		out.content[pre] = orig
		g.Changes = []Change{{Path: dest, Post: pre}, {Path: f, Pre: pre}}
		out.groups = append(out.groups, g)
		out.verdict[f] = VerdictArchive
		return nil
	}
	if v.ValidDesc == nil {
		return fmt.Errorf("descripcion_valida is missing")
	}
	if !*v.ValidDesc {
		// The index would keep showing a description the correction
		// contradicts: keep the fact as it is and ask the user.
		out.kept[f] = pre
		out.review = append(out.review, f)
		return nil
	}
	note := strings.TrimSpace(v.Note)
	if note == "" || len(note) > maxNoteBytes || strings.ContainsAny(note, "\r\n`") {
		return fmt.Errorf("the note must be one short line without backticks")
	}
	data := correctedFact(orig, date, note, v.Source)
	id, err := r.repo.hashObject(data)
	if err != nil {
		return err
	}
	out.content[id] = data
	g.Changes = []Change{{Path: f, Pre: pre, Post: id}}
	g.After = id
	out.groups = append(out.groups, g)
	out.verdict[f] = VerdictCorrect
	return nil
}

// correctedFact is the original bytes, untouched, followed by a dated note.
func correctedFact(orig []byte, date, note, source string) []byte {
	var b bytes.Buffer
	b.Write(orig)
	if len(orig) > 0 && !bytes.HasSuffix(orig, []byte("\n")) {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n> Corregido por el dreaming el %s: %s (%s)\n", date, note, source)
	return b.Bytes()
}

func parseFactAnswer(text string) (factAnswer, error) {
	var a factAnswer
	dec := json.NewDecoder(bytes.NewReader([]byte(extractJSON(text))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return factAnswer{}, fmt.Errorf("the answer is not valid JSON: %w", err)
	}
	return a, nil
}
