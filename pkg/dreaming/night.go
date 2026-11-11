package dreaming

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// NightConfig is one owner's night.
type NightConfig struct {
	Date      string // the civil date in the owner's zone, YYYY-MM-DD
	BudgetUSD float64
	Factory   ProviderFactory
	Analyzer  core.Model // cheap reader (Luna)
	Organizer core.Model // writes the plans and the cross-sheet view (Sonnet)
	Publish   Publisher
	Now       func() time.Time
}

// NightResult is what a night did.
type NightResult struct {
	Ran       bool
	Commit    string
	Applied   []string
	Conflicts []string
	Rejected  map[string]string // unit → why its plan was refused
	Deferred  []string          // left for another night (budget)
	Marks     []Mark            // decisions marked as superseded
	Secrets   []string          // sheets and decisions left alone: they look like they hold a credential
	Questions []string          // for the user, from the decisions pass
	CostUSD   float64
}

// Output sizes asked of each model. They bound the reservation as well.
const (
	analyzerMaxOut  = 16_000
	crossMaxOut     = 16_000
	organizerMaxOut = 32_000
	// chunkBytes is the most an analyzer reads in one call: the pilot's cheap
	// reader skimmed or gave up on 300 KB batches and was reliable at 90 KB.
	chunkBytes = 90 * 1024
	// maxDecisionBytes caps the decisions quoted to one organizer.
	maxDecisionBytes = 60 * 1024
	// maxUnitBytes is the largest unit one organizer call takes.
	maxUnitBytes = 400 * 1024
	// runsKeep is how long a night's artifacts stay for review.
	runsKeep = 30 * 24 * time.Hour
)

// Night runs one night for the store if the date has not run yet: it never
// runs twice on a date, even after a failure.
//
// It waits for another run (an undo) without blocking in the kernel, so a
// cancelled context always ends it: the server's shutdown must not hang on a
// lock.
func (s Store) Night(ctx context.Context, cfg NightConfig) (res NightResult, err error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	res = NightResult{Rejected: map[string]string{}}
	var r *Run
	for {
		r, _, err = s.Begin(false, cfg.Publish)
		if !errors.Is(err, ErrBusy) {
			break
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		return res, err
	}
	defer func() { _ = r.Close() }()
	st, err := s.loadState()
	if err != nil {
		return res, err
	}
	if _, done := st.Nights[cfg.Date]; done {
		return res, nil
	}
	st.Nights[cfg.Date] = Night{Started: cfg.Now().UTC().Format(time.RFC3339)}
	if err := s.saveState(st); err != nil {
		return res, err
	}
	res.Ran = true
	budget := &Budget{Limit: cfg.BudgetUSD}
	defer func() {
		res.CostUSD = budget.Spent
		if st, err := s.loadState(); err == nil {
			n := st.Nights[cfg.Date]
			n.SpentUSD = budget.Spent
			st.Nights[cfg.Date] = n
			_ = s.saveState(st)
		}
	}()
	s.pruneRuns(cfg.Now())
	runDir := filepath.Join(s.workDir(), "runs", cfg.Date)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return res, err
	}
	save := func(name, content string) { _ = writeFileAtomic(filepath.Join(runDir, name), []byte(content)) }

	snap, err := r.Snapshot("Antes del dreaming del " + cfg.Date)
	if err != nil {
		return res, err
	}
	due, err := s.Due(snap)
	if err != nil {
		return res, err
	}
	dueFacts, err := s.DueFacts(snap)
	if err != nil {
		return res, err
	}
	decisionUnits, dueDecisions, err := s.DueDecisions(snap)
	if err != nil || len(due)+len(dueFacts)+len(dueDecisions) == 0 {
		return res, err
	}
	// A sheet that looks like it holds a credential is never sent to a model
	// nor changed; it is recorded as read so it is named once per change.
	secretClean := map[string]string{}
	due = slices.DeleteFunc(due, func(u Unit) bool {
		if snap.HasSecret(u) {
			res.Secrets = append(res.Secrets, u.Sheet)
			secretClean[bookPrefix+u.Sheet] = snap.UnitHash(u)
			return true
		}
		return false
	})
	s.byRecency(due)
	batch, deferred := selectBatch(snap, due, cfg)
	res.Deferred = append(res.Deferred, deferred...)

	// Read: one cheap analysis per unit.
	analyses := map[string]string{}
	var analyzed []Unit
	for i, u := range batch {
		text, err := s.analyze(ctx, budget, cfg, snap, u)
		if errors.Is(err, ErrBudget) {
			for _, rest := range batch[i:] {
				res.Deferred = append(res.Deferred, rest.Sheet)
			}
			break
		}
		if err != nil {
			res.Rejected[u.Sheet] = "análisis: " + err.Error()
			continue
		}
		analyses[u.Sheet] = text
		analyzed = append(analyzed, u)
		save("analisis-"+slug(u.Sheet)+".md", text)
	}
	cross := ""
	if len(analyzed) > 1 {
		cross, err = s.cross(ctx, budget, cfg, snap, analyzed, analyses)
		if err != nil && !errors.Is(err, ErrBudget) {
			return res, err
		}
		save("cruces.md", cross)
	}

	// Organize: one plan per unit, assembled and proven by code.
	var groups []Group
	content := map[string][]byte{}
	fin := Finish{Run: cfg.Date + "-" + randomHex(4), Units: map[string]string{}}
	plans := map[string]Plan{}
	clean := secretClean // units judged clean, facts kept and secrets: nothing to write
	for i, u := range analyzed {
		plan, decisions, err := s.organize(ctx, budget, cfg, snap, u, analyses[u.Sheet], cross)
		if errors.Is(err, ErrBudget) {
			for _, rest := range analyzed[i:] {
				res.Deferred = append(res.Deferred, rest.Sheet)
			}
			break
		}
		if err != nil {
			res.Rejected[u.Sheet] = "plan: " + err.Error()
			continue
		}
		if raw, err := json.MarshalIndent(plan, "", " "); err == nil {
			save("plan-"+slug(u.Sheet)+".json", string(raw))
		}
		if plan.NoChanges {
			clean[bookPrefix+u.Sheet] = snap.UnitHash(u)
			continue
		}
		out, err := assembleWithNonce(snap, u, plan, cfg.Date)
		if err != nil {
			res.Rejected[u.Sheet] = err.Error()
			continue
		}
		g, c, err := r.UnitGroup(snap, u, out, planSources(u, plan, decisions))
		if err != nil {
			return res, err
		}
		if err := r.addPromotions(snap, u, plan, cfg.Date, &g, c); err != nil {
			res.Rejected[u.Sheet] = err.Error()
			continue
		}
		groups = append(groups, g)
		for k, v := range c {
			content[k] = v
		}
		fin.Units[g.ID] = g.After
		plans[g.ID] = plan
	}
	facts := r.consolidateFacts(ctx, budget, cfg, snap, dueFacts, res.Rejected, &res.Deferred)
	for _, g := range facts.groups {
		groups = append(groups, g)
		fin.Units[g.ID] = g.After
	}
	for k, v := range facts.content {
		content[k] = v
	}
	for k, v := range facts.kept {
		clean[k] = v
	}
	decided, err := r.markDecisions(ctx, budget, cfg, snap, decisionUnits, dueDecisions, res.Rejected, &res.Deferred)
	if err != nil {
		return res, err
	}
	for _, g := range decided.groups {
		groups = append(groups, g)
		fin.Units[g.ID] = g.After
	}
	for k, v := range decided.content {
		content[k] = v
	}
	for k, v := range decided.clean {
		clean[k] = v
	}
	res.Marks, res.Questions = decided.marks, decided.questions
	news := func(rep Report) *News {
		n := morningNews(cfg, rep, res, plans, facts, budget)
		return &n
	}
	rep, err := r.Apply(snap, "Dreaming del "+cfg.Date, groups, content, fin, news, cfg.Publish)
	res.Commit, res.Applied, res.Conflicts = rep.Commit, rep.Applied, rep.Conflicts
	// Clean units and kept facts are recorded with their snapshot hash: if
	// someone changed one during the night, its live hash differs and it is
	// due again.
	if err == nil && len(clean) > 0 {
		st, lerr := s.loadState()
		if lerr != nil {
			return res, lerr
		}
		for k, v := range clean {
			st.Units[k] = v
		}
		err = s.saveState(st)
	}
	return res, err
}

// byRecency puts the units touched most recently first.
func (s Store) byRecency(units []Unit) {
	mod := map[string]time.Time{}
	for _, u := range units {
		for _, f := range u.Files {
			if info, err := os.Stat(filepath.Join(s.bookDir(), filepath.FromSlash(f))); err == nil && info.ModTime().After(mod[u.Sheet]) {
				mod[u.Sheet] = info.ModTime()
			}
		}
	}
	sort.SliceStable(units, func(i, j int) bool { return mod[units[i].Sheet].After(mod[units[j].Sheet]) })
}

// selectBatch keeps the units an average night can pay for (about four bytes
// per token, a 1.5× prompt for the organizer and a few thousand tokens out);
// the hard cap is still enforced call by call.
func selectBatch(snap Snapshot, due []Unit, cfg NightConfig) (batch []Unit, deferred []string) {
	estimate := func(u Unit) float64 {
		size := 0
		for _, o := range snap.Orig(u) {
			size += len(o)
		}
		tokens := float64(size) / 4
		cost := 0.0
		if p := cfg.Analyzer.Pricing; p != nil {
			cost += tokens*p.Input/1e6 + 4000*p.Output/1e6
		}
		if p := cfg.Organizer.Pricing; p != nil {
			cost += 1.5*tokens*p.Input/1e6 + 6000*p.Output/1e6
		}
		return cost
	}
	room := cfg.BudgetUSD * 0.8
	for _, u := range due {
		size := 0
		for _, o := range snap.Orig(u) {
			size += len(o)
		}
		if size > maxUnitBytes {
			deferred = append(deferred, u.Sheet+" (demasiado grande)")
			continue
		}
		c := estimate(u)
		if c > room {
			deferred = append(deferred, u.Sheet)
			continue
		}
		room -= c
		batch = append(batch, u)
	}
	return batch, deferred
}

// numbered renders a unit's files with their index, path and line numbers.
func numbered(u Unit, orig [][]byte) []string {
	var parts []string
	for f, o := range orig {
		var b strings.Builder
		fmt.Fprintf(&b, "=== fichero f=%d · %s ===\n", f, u.Files[f])
		for i, l := range splitLines(o) {
			fmt.Fprintf(&b, "%d| %s\n", i+1, strings.TrimRight(string(l), "\r\n"))
		}
		parts = append(parts, b.String())
	}
	return parts
}

func (s Store) analyze(ctx context.Context, b *Budget, cfg NightConfig, snap Snapshot, u Unit) (string, error) {
	return s.read(ctx, b, cfg, snap, u, analyzerSystem, "Extrae E/D/C/X de este tramo.")
}

// read has the cheap model read a unit in chunks of at most chunkBytes.
func (s Store) read(ctx context.Context, b *Budget, cfg NightConfig, snap Snapshot, u Unit, system, task string) (string, error) {
	var chunks []string
	var cur strings.Builder
	for _, part := range numbered(u, snap.Orig(u)) {
		for _, line := range strings.SplitAfter(part, "\n") {
			if cur.Len() > 0 && cur.Len()+len(line) > chunkBytes {
				chunks = append(chunks, cur.String())
				cur.Reset()
			}
			cur.WriteString(line)
		}
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	var out []string
	for i, c := range chunks {
		prompt := fmt.Sprintf("Hoja: %s (tramo %d de %d). %s\n\n%s", u.Sheet, i+1, len(chunks), task, c)
		res, err := b.call(ctx, cfg.Factory, cfg.Analyzer, system, prompt, analyzerMaxOut)
		if err != nil {
			return "", err
		}
		out = append(out, res.Text)
	}
	return strings.Join(out, "\n\n"), nil
}

func (s Store) cross(ctx context.Context, b *Budget, cfg NightConfig, snap Snapshot, units []Unit, analyses map[string]string) (string, error) {
	var p strings.Builder
	p.WriteString("Decisiones del dueño (índice):\n")
	p.WriteString(decisionsIndex(snap))
	p.WriteString("\nAnálisis de las hojas de esta noche:\n\n")
	for _, u := range units {
		p.WriteString(analyses[u.Sheet])
		p.WriteString("\n\n")
	}
	res, err := b.call(ctx, cfg.Factory, cfg.Organizer, crossSystem, p.String(), crossMaxOut)
	return res.Text, err
}

func decisionsIndex(snap Snapshot) string {
	var paths []string
	for p := range snap.Files {
		if strings.HasPrefix(p, bookPrefix+"decisions/") && strings.HasSuffix(p, ".md") && !isSecret(snap.Files[p]) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		title := ""
		for _, l := range splitLines(snap.Files[p]) {
			if t := strings.TrimSpace(string(l)); strings.HasPrefix(t, "# ") {
				title = strings.TrimPrefix(t, "# ")
				break
			}
		}
		fmt.Fprintf(&b, "- %s — %s\n", strings.TrimPrefix(p, bookPrefix), title)
	}
	return b.String()
}

var decisionRef = regexp.MustCompile(`decisions/[A-Za-z0-9_./-]+\.md`)

// citedDecisions returns the decisions a unit or its analysis names, whole and
// numbered, up to maxDecisionBytes: the organizer needs their text, not their
// titles, to tell whether a passage contradicts one.
func citedDecisions(snap Snapshot, texts ...string) (paths []string, rendered string) {
	seen := map[string]bool{}
	var b strings.Builder
	for _, t := range texts {
		for _, ref := range decisionRef.FindAllString(t, -1) {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			data, ok := snap.Files[bookPrefix+ref]
			if !ok || isSecret(data) {
				continue
			}
			var d strings.Builder
			fmt.Fprintf(&d, "=== %s ===\n", ref)
			for i, l := range splitLines(data) {
				fmt.Fprintf(&d, "%d| %s\n", i+1, strings.TrimRight(string(l), "\r\n"))
			}
			if b.Len()+d.Len() > maxDecisionBytes {
				continue
			}
			b.WriteString(d.String())
			paths = append(paths, ref)
		}
	}
	return paths, b.String()
}

func (s Store) organize(ctx context.Context, b *Budget, cfg NightConfig, snap Snapshot, u Unit, analysis, cross string) (Plan, []string, error) {
	files := numbered(u, snap.Orig(u))
	decisions, quoted := citedDecisions(snap, strings.Join(files, ""), analysis, crossFor(cross, u.Sheet))
	var p strings.Builder
	fmt.Fprintf(&p, "Fecha de hoy: %s.\nHoja: %s\n\n", cfg.Date, u.Sheet)
	p.WriteString(strings.Join(files, "\n"))
	p.WriteString("\n=== análisis barato ===\n")
	p.WriteString(analysis)
	if c := crossFor(cross, u.Sheet); c != "" {
		p.WriteString("\n=== cruces entre hojas ===\n")
		p.WriteString(c)
	}
	if quoted != "" {
		p.WriteString("\n=== decisiones del dueño citadas ===\n")
		p.WriteString(quoted)
	}
	res, err := b.call(ctx, cfg.Factory, cfg.Organizer, organizerSystem, p.String(), organizerMaxOut)
	if err != nil {
		return Plan{}, nil, err
	}
	plan, err := parsePlan(res.Text)
	return plan, decisions, err
}

// crossFor is the part of the cross-sheet view about one sheet, plus the
// questions for the user.
func crossFor(cross, sheet string) string {
	if cross == "" {
		return ""
	}
	var out []string
	for _, sec := range strings.Split(cross, "\n### ") {
		if strings.HasPrefix(sec, sheet) || strings.HasPrefix(strings.TrimSpace(sec), "## Preguntas") {
			out = append(out, sec)
		}
	}
	if i := strings.Index(cross, "## Preguntas al dueño"); i >= 0 {
		out = append(out, cross[i:])
	}
	return strings.Join(out, "\n")
}

var jsonBlock = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")

// extractJSON is the object of a model answer: a fenced block, or the text
// between the first "{" and the last "}".
func extractJSON(text string) string {
	raw := strings.TrimSpace(text)
	if m := jsonBlock.FindStringSubmatch(raw); m != nil {
		return m[1]
	} else if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j > i {
		return raw[i : j+1]
	}
	return raw
}

func parsePlan(text string) (Plan, error) {
	var p Plan
	dec := json.NewDecoder(bytes.NewReader([]byte(extractJSON(text))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Plan{}, fmt.Errorf("the plan is not valid JSON: %w", err)
	}
	return p, nil
}

// planSources is what a plan relied on outside its own unit (R6): the sheets
// its archived ranges cite and the decisions it was shown.
func planSources(u Unit, plan Plan, decisions []string) []string {
	own := map[string]bool{}
	for _, f := range u.Files {
		own[f] = true
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !own[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, a := range plan.Archive {
		if i := strings.LastIndexByte(a.Source, ':'); i > 0 {
			add(a.Source[:i])
		}
	}
	for _, d := range decisions {
		add(d)
	}
	sort.Strings(out)
	return out
}

func assembleWithNonce(snap Snapshot, u Unit, plan Plan, date string) (Output, error) {
	var err error
	for range 3 {
		var out Output
		out, err = Assemble(AssembleInput{Unit: u, Orig: snap.Orig(u), Plan: plan, Date: date, Nonce: randomHex(8),
			S0: snap.Commit, SourceExists: snap.SourceExists})
		if err == nil || !strings.Contains(err.Error(), "nonce occurs") {
			return out, err
		}
	}
	return Output{}, err
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func slug(path string) string { return strings.NewReplacer("/", "__", ".md", "").Replace(path) }

// morningNews is the For you item: what changed, what was skipped and why,
// the questions for the user, the cost and how to undo. It names secrets by
// file only, never by their content (I9).
func morningNews(cfg NightConfig, rep Report, res NightResult, plans map[string]Plan, facts factOutcome, b *Budget) News {
	archived := 0
	sheets, factsArchived, factsCorrected := 0, 0, 0
	var questions []string
	for _, id := range rep.Applied {
		switch facts.verdict[id] {
		case VerdictArchive:
			factsArchived++
			continue
		case VerdictCorrect:
			factsCorrected++
			continue
		}
		sheets++
		p := plans[id]
		for _, a := range p.Archive {
			archived += a.Lines[1] - a.Lines[0] + 1
		}
		for _, l := range strings.Split(p.State, "\n") {
			if strings.Contains(l, "PREGUNTA AL DUEÑO") && len(questions) < 5 {
				questions = append(questions, strings.TrimPrefix(id, bookPrefix)+": "+strings.TrimSpace(strings.TrimLeft(l, "-* ")))
			}
		}
	}
	title := fmt.Sprintf("Dreaming del %s: %d hojas consolidadas", cfg.Date, sheets)
	if n := factsArchived + factsCorrected; n > 0 {
		title += fmt.Sprintf(" y %d hechos de memoria", n)
	}
	var body strings.Builder
	if sheets > 0 {
		fmt.Fprintf(&body, "- %d hojas consolidadas; %d líneas, literales, a `archivo/`.\n", sheets, archived)
	}
	if factsArchived+factsCorrected > 0 {
		fmt.Fprintf(&body, "- Memoria: %d hechos archivados, literales, en `memory/archivo/`; %d corregidos con una nota al final.\n", factsArchived, factsCorrected)
	}
	if len(facts.review) > 0 {
		fmt.Fprintf(&body, "- Memoria a revisar (la corrección dejaría mal su descripción): %s.\n", strings.Join(facts.review, ", "))
	}
	if len(res.Secrets) > 0 {
		fmt.Fprintf(&body, "- Hojas con posibles secretos, sin tocar: %s.\n", strings.Join(res.Secrets, ", "))
	}
	if len(facts.secrets) > 0 {
		fmt.Fprintf(&body, "- Memoria con secretos, sin tocar: %s.\n", strings.Join(facts.secrets, ", "))
	}
	if len(rep.Conflicts) > 0 {
		fmt.Fprintf(&body, "- Sin tocar porque cambiaron durante la noche: %s.\n", trimIDs(rep.Conflicts))
	}
	if len(res.Rejected) > 0 {
		var names []string
		for k := range res.Rejected {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Fprintf(&body, "- Plan rechazado por las comprobaciones: %s.\n", strings.Join(names, ", "))
	}
	if len(res.Deferred) > 0 {
		fmt.Fprintf(&body, "- Para otra noche: %d hojas o hechos.\n", len(res.Deferred))
	}
	applied := map[string]bool{}
	for _, id := range rep.Applied {
		applied[id] = true
	}
	for _, m := range res.Marks {
		if !applied[bookPrefix+m.Old] {
			continue
		}
		scope := "en parte"
		if m.Scope == "total" {
			scope = "del todo (archivada)"
		}
		fmt.Fprintf(&body, "- `%s` superada %s por `%s`: %s Decía (`%s:%d`) «%s»; ahora (`%s:%d`) «%s».\n",
			m.Old, scope, m.New, m.Change, m.OldQuote.Path, m.OldQuote.Line, m.OldQuote.Text, m.NewQuote.Path, m.NewQuote.Line, m.NewQuote.Text)
	}
	questions = append(questions, res.Questions...)
	for _, q := range questions {
		fmt.Fprintf(&body, "- Pregunta: %s\n", q)
	}
	fmt.Fprintf(&body, "- Coste: $%.2f de $%.2f.\n", b.Spent, b.Limit)
	if rep.Commit != "" {
		fmt.Fprintf(&body, "- Para deshacerlo, pídeselo al owner: «deshaz el dreaming %s».\n", shortSHA(rep.Commit)[:7])
	}
	return News{Key: "dreaming/" + cfg.Date, Origin: "dreaming/" + cfg.Date, Title: title, Body: withoutSecrets(strings.TrimSpace(body.String()))}
}

func trimIDs(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strings.TrimPrefix(id, bookPrefix)
	}
	return strings.Join(out, ", ")
}

var dateDir = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// pruneRuns deletes the artifacts of nights older than runsKeep. A night's
// directory is flat; only its plain files are removed, then the directory if
// it is empty.
func (s Store) pruneRuns(now time.Time) {
	root := filepath.Join(s.workDir(), "runs")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !dateDir.MatchString(e.Name()) {
			continue
		}
		day, err := time.Parse("2006-01-02", e.Name())
		if err != nil || now.Sub(day) < runsKeep {
			continue
		}
		dir := filepath.Join(root, e.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if f.Type().IsRegular() {
				_ = os.Remove(filepath.Join(dir, f.Name()))
			}
		}
		_ = os.Remove(dir)
	}
}

// withoutSecrets drops every line of generated text that looks like it holds
// a credential: a model may have copied one from a quote.
func withoutSecrets(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if isSecret([]byte(l)) {
			lines[i] = "- (línea omitida: parecía contener un secreto)"
		}
	}
	return strings.Join(lines, "\n")
}
