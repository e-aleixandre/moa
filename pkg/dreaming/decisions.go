package dreaming

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The decisions pass follows the pilot (tmp/dreaming/INFORME.md): the cheap
// model extracts each decision's points, the organizer pairs the ones a later
// decision supersedes, and code checks both quotes and writes a "Superada
// por" header above the decision's untouched bytes. A total supersession
// moves the decision, as it is, to archivo/decisions/.

// Mark is one supersession the pairer proposes.
type Mark struct {
	Old      string `json:"vieja"`
	New      string `json:"nueva"`
	NewDate  string `json:"fecha_nueva"`
	Scope    string `json:"alcance"` // total | parcial
	Point    string `json:"punto"`
	Change   string `json:"que_cambia"`
	OldQuote Quote  `json:"cita_vieja"`
	NewQuote Quote  `json:"cita_nueva"`
}

// Quote is a literal line the mark rests on.
type Quote struct {
	Path string `json:"ruta"`
	Line int    `json:"linea"`
	Text string `json:"texto"`
}

type pairing struct {
	Marks     []Mark   `json:"marcas"`
	Questions []string `json:"preguntas"`
}

// decisionOutcome is what the pass leaves for the commit and the news.
type decisionOutcome struct {
	groups    []Group
	content   map[string][]byte
	clean     map[string]string
	marks     []Mark
	questions []string
}

var (
	quoteNoise = regexp.MustCompile("[\\s*_`«»\"“”]+")
	ellipsis   = regexp.MustCompile(`\s*\.\.\.\s*`)
	fileDate   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	oneLine    = regexp.MustCompile("^[^\\r\\n`]{1,300}$")
)

func normQuote(s string) string {
	return strings.ToLower(strings.TrimSpace(quoteNoise.ReplaceAllString(s, " ")))
}

// quoteHolds reports whether q is literal text of its line (one line of
// margin, the pilot's normalization of spacing, emphasis and quotes; "..."
// may cut the middle) in the snapshot.
func quoteHolds(snap Snapshot, q Quote) bool {
	data, ok := snap.Files[bookPrefix+q.Path]
	if !ok || q.Line < 1 {
		return false
	}
	lines := strings.Split(string(data), "\n")
	if q.Line > len(lines) {
		return false
	}
	window := normQuote(strings.Join(lines[max(0, q.Line-2):min(len(lines), q.Line+1)], " "))
	text := normQuote(strings.NewReplacer("…", " ... ", "[...]", " ... ").Replace(q.Text))
	parts := 0
	for _, part := range ellipsis.Split(text, -1) {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		parts++
		if !strings.Contains(window, part) {
			return false
		}
	}
	return parts > 0
}

func unitOf(units map[string]Unit, path string) (Unit, bool) {
	u, ok := units[path]
	return u, ok
}

// checkMark is everything code can prove about a mark.
func checkMark(snap Snapshot, units map[string]Unit, due map[string]bool, m Mark) error {
	old, ok1 := unitOf(units, m.Old)
	nu, ok2 := unitOf(units, m.New)
	switch {
	case !ok1 || !ok2:
		return fmt.Errorf("%s → %s: both must be decisions of the snapshot", m.Old, m.New)
	case m.Old == m.New:
		return fmt.Errorf("%s supersedes itself", m.Old)
	case !due[m.Old] && !due[m.New]:
		return fmt.Errorf("%s → %s: neither changed since the last night", m.Old, m.New)
	case m.Scope != "total" && m.Scope != "parcial":
		return fmt.Errorf("%s: alcance %q", m.Old, m.Scope)
	}
	if dOld, dNew := fileDate.FindString(filepath.Base(m.Old)), fileDate.FindString(filepath.Base(m.New)); dOld != "" && dNew != "" && dNew < dOld {
		return fmt.Errorf("%s (%s) is older than %s", m.New, dNew, m.Old)
	}
	for _, f := range []string{m.NewDate, m.Point, m.Change} {
		if !oneLine.MatchString(f) {
			return fmt.Errorf("%s: fecha, punto and que_cambia must be short single lines", m.Old)
		}
	}
	if !contains(old.Files, m.OldQuote.Path) || !contains(nu.Files, m.NewQuote.Path) {
		return fmt.Errorf("%s: each quote must come from its own decision", m.Old)
	}
	if !quoteHolds(snap, m.OldQuote) || !quoteHolds(snap, m.NewQuote) {
		return fmt.Errorf("%s → %s: a quote is not literal text of its line", m.Old, m.New)
	}
	for _, q := range []Quote{m.OldQuote, m.NewQuote} {
		if strings.ContainsAny(q.Text, "\r\n") || len(q.Text) > 600 {
			return fmt.Errorf("%s: a quote must be one line", m.Old)
		}
	}
	return nil
}

// markHeader is the text above a marked decision. Line numbers that point
// into the marked file itself are shifted by the header's own lines, so they
// stay right after the header is written.
func markHeader(marks []Mark, date string, shift map[string]int) string {
	var parts []string
	for _, m := range marks {
		head := "Superada en parte por"
		if m.Scope == "total" {
			head = "Superada por"
		}
		parts = append(parts, fmt.Sprintf("> **%s** `%s` (%s) — %s: %s  \n> Decía (`%s:%d`): «%s»  \n> Ahora (`%s:%d`): «%s»",
			head, m.New, m.NewDate, m.Point, m.Change,
			m.OldQuote.Path, m.OldQuote.Line+shift[m.OldQuote.Path], m.OldQuote.Text,
			m.NewQuote.Path, m.NewQuote.Line+shift[m.NewQuote.Path], m.NewQuote.Text))
	}
	return strings.Join(parts, "\n>\n") + fmt.Sprintf("\n>\n> _Marca del dreaming, %s. El texto de la decisión sigue abajo, sin cambios; las líneas citadas ya cuentan esta cabecera._\n\n", date)
}

// markGroups validates the marks and turns them into one group per marked
// decision; the decisions they rest on are its sources.
func (r *Run) markGroups(snap Snapshot, units map[string]Unit, due map[string]bool, marks []Mark, date string, rejected map[string]string) ([]Group, map[string][]byte, []Mark) {
	byOld := map[string][]Mark{}
	var order []string
	for _, m := range marks {
		if err := checkMark(snap, units, due, m); err != nil {
			rejected["marca "+m.Old] = err.Error()
			continue
		}
		if byOld[m.Old] == nil {
			order = append(order, m.Old)
		}
		byOld[m.Old] = append(byOld[m.Old], m)
	}
	shift := map[string]int{}
	for old, ms := range byOld {
		shift[old] = strings.Count(markHeader(ms, date, nil), "\n")
	}
	content := map[string][]byte{}
	var groups []Group
	var applied []Mark
	for _, old := range order {
		ms := byOld[old]
		u := units[old]
		header := markHeader(ms, date, shift)
		orig := snap.Files[bookPrefix+old]
		marked := append([]byte(header), orig...)
		total := false
		for _, m := range ms {
			total = total || m.Scope == "total"
		}
		g := Group{ID: bookPrefix + old, Kind: "unit"}
		after := map[string]string{}
		bad := ""
		for _, f := range u.Files {
			rp := bookPrefix + f
			data := snap.Files[rp]
			if f == old {
				data = marked
			}
			id, err := r.repo.hashObject(data)
			if err != nil {
				bad = err.Error()
				break
			}
			content[id] = data
			switch {
			case total:
				dst := bookPrefix + "archivo/" + f
				if _, taken := snap.Files[dst]; taken {
					bad = dst + " already exists"
				}
				g.Changes = append(g.Changes, Change{Path: rp, Pre: snap.Blobs[rp]}, Change{Path: dst, Post: id})
			case f == old:
				g.Changes = append(g.Changes, Change{Path: rp, Pre: snap.Blobs[rp], Post: id})
				after[rp] = id
			default:
				after[rp] = snap.Blobs[rp]
			}
		}
		if bad != "" {
			rejected["marca "+old] = bad
			continue
		}
		sources := map[string]bool{}
		for _, m := range ms {
			for _, f := range units[m.New].Files {
				if !sources[f] {
					sources[f] = true
					g.Sources = append(g.Sources, Change{Path: bookPrefix + f, Pre: snap.Blobs[bookPrefix+f]})
				}
			}
		}
		g.After = unitHash(after)
		groups = append(groups, g)
		applied = append(applied, ms...)
	}
	return groups, content, applied
}

// DueDecisions are the decisions that changed since they were last read.
func (s Store) DueDecisions(snap Snapshot) (map[string]Unit, map[string]bool, error) {
	st, err := s.loadState()
	if err != nil {
		return nil, nil, err
	}
	all := map[string]Unit{}
	due := map[string]bool{}
	for _, u := range snap.Units() {
		if !isDecision(u) {
			continue
		}
		all[u.Sheet] = u
		if st.Units[bookPrefix+u.Sheet] != snap.UnitHash(u) {
			due[u.Sheet] = true
		}
	}
	return all, due, nil
}

// markDecisions runs the pass when a decision changed. Extractions are kept
// by content in the dreaming directory, so a night pays only for decisions
// it has not read before.
func (r *Run) markDecisions(ctx context.Context, b *Budget, cfg NightConfig, snap Snapshot, units map[string]Unit, due map[string]bool, rejected map[string]string, deferred *[]string) (decisionOutcome, error) {
	out := decisionOutcome{content: map[string][]byte{}, clean: map[string]string{}}
	if len(due) == 0 {
		return out, nil
	}
	names := make([]string, 0, len(units))
	for n := range units {
		names = append(names, n)
	}
	sort.Strings(names)
	cache := filepath.Join(r.s.workDir(), "decisiones")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return out, err
	}
	var extractions strings.Builder
	for _, n := range names {
		u := units[n]
		path := filepath.Join(cache, snap.UnitHash(u)+".md")
		text, err := os.ReadFile(path)
		if err != nil {
			t, err := r.s.read(ctx, b, cfg, snap, u, decisionsAnalyzerSystem, "Extrae los puntos de esta decisión.")
			if err != nil {
				*deferred = append(*deferred, "decisions/ (lectura)")
				return out, nil
			}
			text = []byte(t)
			_ = writeFileAtomic(path, text)
		}
		extractions.Write(text)
		extractions.WriteString("\n\n")
	}
	var p strings.Builder
	p.WriteString("Extracciones de todas las decisiones:\n\n")
	p.WriteString(extractions.String())
	p.WriteString("\nDecisiones que han cambiado esta noche, numeradas:\n\n")
	for _, n := range names {
		if due[n] {
			p.WriteString(strings.Join(numbered(units[n], snap.Orig(units[n])), "\n"))
		}
	}
	res, err := b.call(ctx, cfg.Factory, cfg.Organizer, pairerSystem, p.String(), organizerMaxOut)
	if err != nil {
		*deferred = append(*deferred, "decisions/ (emparejado)")
		return out, nil
	}
	var pr pairing
	if err := json.Unmarshal([]byte(extractJSON(res.Text)), &pr); err != nil {
		rejected["decisions/"] = "the pairing is not valid JSON: " + err.Error()
		return out, nil
	}
	out.questions = pr.Questions
	out.groups, out.content, out.marks = r.markGroups(snap, units, due, pr.Marks, cfg.Date, rejected)
	marked := map[string]bool{}
	for _, g := range out.groups {
		marked[strings.TrimPrefix(g.ID, bookPrefix)] = true
	}
	for n := range due {
		if !marked[n] {
			out.clean[bookPrefix+n] = snap.UnitHash(units[n])
		}
	}
	return out, nil
}
