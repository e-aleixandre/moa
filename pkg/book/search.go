package book

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Search is lexical, ranked, and computed by scanning the book on every call.
//
// No persistent index: a book of 500 sheets is a few megabytes and scans in
// well under the time a model spends reading the answer, while an index is a
// second source of truth that can disagree with the files the user edits by
// hand. The ranking is BM25 over weighted fields, which is what makes the
// difference between "the sheet about delivery notes" and the forty files that
// mention them once.

const (
	// defaultSearchLimit / maxSearchLimit bound how many sheets come back. The
	// owner reads the hit, not the list.
	defaultSearchLimit = 10
	maxSearchLimit     = 25
	// snippetBytes bounds the excerpt per hit, like the memory tool's.
	snippetBytes = 240
	// minPrefixRunes is the shortest token allowed to match by prefix, in both
	// directions. Shorter ones match too much ("use" would hit "user",
	// "usual", "used").
	minPrefixRunes = 4
	// minSingularRunes / minPluralRunes bound the one morphological rule the
	// ranker has: dropping a Spanish/English plural ending. They keep "notes"
	// from becoming "not" while letting "albaranes" reach "albarán".
	minSingularRunes = 3
	minPluralRunes   = 4
)

// field weights: where a word appears says more than how often it does.
const (
	weightTitle    = 4.0
	weightAliases  = 4.0
	weightFilename = 3.0
	weightHeading  = 2.0
	weightBody     = 1.0
)

// BM25 parameters. k1 saturates term frequency (a sheet naming a word twenty
// times is not twenty times more about it); b normalizes by length so a long
// sheet does not win on volume.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// stopwords are the words that appear in every question and rank nothing. Both
// languages, because the book is written in Spanish and the tooling in English,
// and a question mixes them ("cómo funciona el import").
//
// Negations (sin, no, not, without) are deliberately NOT here: "pedidos sin
// factura" and "pedidos con factura" are different questions, and dropping the
// word makes the ranker answer the opposite one.
var stopwords = map[string]bool{
	// Spanish
	"a": true, "al": true, "algo": true, "como": true, "con": true, "cual": true,
	"cuando": true, "de": true, "del": true, "donde": true, "dos": true, "el": true,
	"ella": true, "en": true, "es": true, "esta": true, "este": true, "esto": true,
	"funciona": true, "hace": true, "hay": true, "la": true, "las": true, "lo": true,
	"los": true, "mas": true, "me": true, "mi": true, "muy": true,
	"nos": true, "o": true, "para": true, "pero": true, "por": true, "porque": true,
	"que": true, "se": true, "si": true, "sobre": true, "su": true,
	"sus": true, "te": true, "tiene": true, "un": true, "una": true, "uno": true,
	"y": true, "ya": true,
	// English
	"an": true, "and": true, "are": true, "be": true, "but": true, "by": true,
	"can": true, "did": true, "do": true, "does": true, "for": true, "from": true,
	"has": true, "have": true, "how": true, "i": true, "in": true, "is": true,
	"it": true, "of": true, "on": true, "or": true, "our": true, "the": true,
	"their": true, "there": true, "this": true, "to": true, "was": true, "we": true,
	"what": true, "when": true, "where": true, "which": true, "why": true,
	"with": true, "you": true, "your": true,
}

// Hit is one ranked sheet: enough to decide whether to read it, never enough to
// answer from the list alone.
type Hit struct {
	Path    string
	Title   string
	Area    string
	Snippet string
	Score   float64
	// allTerms and kind order the results before Score does (see SearchBook).
	allTerms bool
	kind     int
}

// document is one book file as the ranker sees it.
//
// length is the length of the BODY only. The field boosts (a title is worth
// four body mentions) live in terms, outside the normalization: counting them
// as length too made a sheet whose title is the exact query look "long" and
// lose to a short sheet that mentions the word once in passing.
type document struct {
	path     string
	title    string
	area     string
	body     string
	kind     int
	length   float64
	terms    map[string]float64 // weighted frequency, fields included
	distinct map[string]bool
}

// SearchBook ranks the book's files against a free-text query. It is exported
// for callers outside the tool (tests, future HTTP search) and is the single
// implementation: the tool formats what this returns.
func SearchBook(dir, query string, limit int) ([]Hit, int, error) {
	return searchBook(dir, query, limit, nil)
}

// searchBook is SearchBook plus the visibility filter the children's variant
// of the tool applies (OWNER.md is the user's file and children never see it).
func searchBook(dir, query string, limit int, hidden func(rel string) bool) ([]Hit, int, error) {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil, 0, nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	docs, err := scan(dir, hidden)
	if err != nil {
		return nil, 0, err
	}
	if len(docs) == 0 {
		return nil, 0, nil
	}

	df := map[string]int{}
	// Document frequency counts the query's terms only: everything else is
	// tokenized but never scored, so counting it would be work for nobody.
	matched := make([]map[string]bool, len(docs))
	for i, doc := range docs {
		hitTerms := map[string]bool{}
		for _, term := range terms {
			if doc.matches(term) {
				hitTerms[term.text] = true
			}
		}
		matched[i] = hitTerms
		for text := range hitTerms {
			df[text]++
		}
	}

	var avgLen float64
	for _, doc := range docs {
		avgLen += doc.length
	}
	avgLen /= float64(len(docs))
	if avgLen <= 0 {
		avgLen = 1
	}

	total := float64(len(docs))
	var hits []Hit
	for i, doc := range docs {
		if len(matched[i]) == 0 {
			continue
		}
		var score float64
		for _, term := range terms {
			if !matched[i][term.text] {
				continue
			}
			tf := doc.weightedFreq(term)
			idf := math.Log(1 + (total-float64(df[term.text])+0.5)/(float64(df[term.text])+0.5))
			score += idf * (tf * (bm25K1 + 1)) / (tf + bm25K1*(1-bm25B+bm25B*doc.length/avgLen))
		}
		hits = append(hits, Hit{
			Path:    doc.path,
			Title:   doc.title,
			Area:    doc.area,
			Snippet: snippetFor(doc.body, terms),
			Score:   score,
			// Carrying every term is a stronger signal than any score: a sheet
			// about both words beats one that is very much about one of them.
			allTerms: len(matched[i]) == len(terms),
			kind:     doc.kind,
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.allTerms != b.allTerms {
			return a.allTerms
		}
		// Score decides. What the file is only breaks a tie: a work file whose
		// title is the query is the answer, and ranking it under a sheet that
		// mentions the word once hides exactly the work in progress the owner
		// is asking about.
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		return a.Path < b.Path
	})
	found := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, found, nil
}

// kind ranks, smallest first.
const (
	kindSheet = iota
	kindIndex
	kindWork
	kindDecision
	kindOther
)

func kindOf(rel string) int {
	base := baseName(rel)
	switch {
	case rel == "PROJECT.md" || base == "README.md":
		return kindIndex
	case strings.HasPrefix(rel, "areas/"):
		return kindSheet
	case strings.HasPrefix(rel, "work/"):
		return kindWork
	case strings.HasPrefix(rel, "decisions/"):
		return kindDecision
	}
	return kindOther
}

func baseName(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

// areaOf names the area a file belongs to, "" for the book's own roots.
func areaOf(rel string) string {
	if !strings.HasPrefix(rel, "areas/") {
		return ""
	}
	rest := rel[len("areas/"):]
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i]
	}
	return ""
}

// scan reads every markdown file of the book and tokenizes it once.
func scan(dir string, hidden func(rel string) bool) ([]document, error) {
	var docs []document
	err := walkBook(dir, func(rel, full string) error {
		if hidden != nil && hidden(rel) {
			return nil
		}
		data, err := os.ReadFile(full)
		if err != nil || len(data) > maxFileBytes {
			return nil
		}
		docs = append(docs, newDocument(rel, string(data)))
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return docs, nil
}

// walkBook is the single walk of the book: markdown only, no hidden files or
// directories (which keeps .git out of every listing, search and future lint),
// paths reported book-relative with forward slashes.
func walkBook(dir string, visit func(rel, full string) error) error {
	return filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner of the book is skipped, not fatal
		}
		name := d.Name()
		if d.IsDir() {
			if full != dir && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(name, ".") {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(name), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(dir, full)
		if relErr != nil {
			return nil
		}
		return visit(filepath.ToSlash(rel), full)
	})
}

func newDocument(rel, text string) document {
	fm, body := parseFrontmatter(text)
	doc := document{
		path:     rel,
		title:    fm.Title,
		area:     areaOf(rel),
		body:     body,
		kind:     kindOf(rel),
		terms:    map[string]float64{},
		distinct: map[string]bool{},
	}
	// counted is what normalizes the score (the body), boosted is what raises
	// it (where the word appears). Only the first feeds length.
	add := func(text string, weight float64, counted bool) {
		for _, tok := range tokenize(text) {
			doc.terms[tok] += weight
			doc.distinct[tok] = true
			if counted {
				doc.length++
			}
		}
	}
	add(fm.Title, weightTitle, false)
	add(strings.Join(fm.Aliases, " "), weightAliases, false)
	add(strings.TrimSuffix(baseName(rel), ".md"), weightFilename, false)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			add(strings.TrimLeft(line, "# "), weightHeading, true)
			continue
		}
		add(line, weightBody, true)
	}
	return doc
}

// term is one query word, with what it is allowed to match: by prefix (in
// either direction) and by its singular/plural variants.
type term struct {
	text     string
	prefix   bool
	variants []string
}

// matchesToken is the single matching rule, used both to decide whether a
// document matches and to weigh how much.
//
// Prefix is symmetric on purpose: "albaran" has to find "albaranes" AND
// "albaranes" has to find the alias "albarán", which one-way prefixing missed.
// Variants add the minimum morphology Spanish and English share (-s, -es), so
// "api" reaches "APIs" without lowering the prefix threshold for everybody.
func (t term) matchesToken(token string) bool {
	if token == t.text {
		return true
	}
	if t.prefix && strings.HasPrefix(token, t.text) {
		return true
	}
	if utf8.RuneCountInString(token) >= minPrefixRunes && strings.HasPrefix(t.text, token) {
		return true
	}
	for _, variant := range t.variants {
		if variant == token {
			return true
		}
		for _, other := range wordVariants(token) {
			if other == variant {
				return true
			}
		}
	}
	return false
}

// wordVariants returns a token and its singular, when stripping the plural
// leaves a word rather than a stump ("notes" gives "note", never "not").
func wordVariants(tok string) []string {
	out := []string{tok}
	if rest, ok := strings.CutSuffix(tok, "es"); ok && utf8.RuneCountInString(rest) >= minPluralRunes {
		out = append(out, rest)
	}
	if rest, ok := strings.CutSuffix(tok, "s"); ok && utf8.RuneCountInString(rest) >= minSingularRunes {
		out = append(out, rest)
	}
	return out
}

func (d document) matches(t term) bool {
	if d.distinct[t.text] {
		return true
	}
	for token := range d.distinct {
		if t.matchesToken(token) {
			return true
		}
	}
	return false
}

func (d document) weightedFreq(t term) float64 {
	var freq float64
	for token, w := range d.terms {
		if t.matchesToken(token) {
			freq += w
		}
	}
	return freq
}

// queryTerms normalizes and filters the query. If every word is a stopword the
// stopwords are kept: a query of "how does it" is a bad query, but answering it
// with nothing found is worse than answering it literally.
func queryTerms(query string) []term {
	tokens := tokenize(query)
	kept := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if !stopwords[tok] {
			kept = append(kept, tok)
		}
	}
	if len(kept) == 0 {
		kept = tokens
	}
	seen := map[string]bool{}
	out := make([]term, 0, len(kept))
	for _, tok := range kept {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, term{
			text:     tok,
			prefix:   utf8.RuneCountInString(tok) >= minPrefixRunes,
			variants: wordVariants(tok),
		})
	}
	return out
}

// tokenize lowercases, strips accents and splits on anything that is not a
// letter or a digit. Accents are folded rather than respected because the book
// is written with them and queries are typed without them ("albaran" must find
// "albarán"); folding is a fixed table, not a dependency.
func tokenize(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		r = foldRune(r)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// foldRune maps the accented letters Spanish (and the odd loanword) actually
// uses onto their base letter. ñ folds to n on purpose: "ano"/"año" is the only
// pair it damages, and nobody searches the book for either.
func foldRune(r rune) rune {
	switch r {
	case 'á', 'à', 'ä', 'â', 'ã', 'å':
		return 'a'
	case 'é', 'è', 'ë', 'ê':
		return 'e'
	case 'í', 'ì', 'ï', 'î':
		return 'i'
	case 'ó', 'ò', 'ö', 'ô', 'õ':
		return 'o'
	case 'ú', 'ù', 'ü', 'û':
		return 'u'
	case 'ñ':
		return 'n'
	case 'ç':
		return 'c'
	}
	return r
}

// snippetFor returns a bounded excerpt around the first line of the body that
// carries one of the query's terms.
func snippetFor(body string, terms []term) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "---") {
			continue
		}
		tokens := map[string]bool{}
		for _, tok := range tokenize(trimmed) {
			tokens[tok] = true
		}
		for _, t := range terms {
			for tok := range tokens {
				if t.matchesToken(tok) {
					return clip(trimmed, snippetBytes)
				}
			}
		}
	}
	// No line carries the term (it matched the title or the filename): the
	// first prose line still says what the sheet is about.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return clip(trimmed, snippetBytes)
		}
	}
	return ""
}

func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return truncateUTF8(s, limit-3) + "…"
}

// formatSearch renders ranked hits for the model: the path to read next, what
// the sheet is, and one line of evidence that it is the right one.
func formatSearch(hits []Hit, total int, query string) string {
	if len(hits) == 0 {
		return fmt.Sprintf("Nothing in the book matches %q.", query)
	}
	var sb strings.Builder
	if total > len(hits) {
		fmt.Fprintf(&sb, "%d files match (showing %d):\n", total, len(hits))
	} else {
		fmt.Fprintf(&sb, "%d file(s) match:\n", total)
	}
	for _, hit := range hits {
		sb.WriteString("- " + hit.Path)
		if hit.Title != "" {
			sb.WriteString(" — " + hit.Title)
		}
		if hit.Area != "" {
			sb.WriteString(" (" + hit.Area + ")")
		}
		sb.WriteString("\n")
		if hit.Snippet != "" {
			sb.WriteString("    " + hit.Snippet + "\n")
		}
		if sb.Len() >= maxSearchBytes {
			break
		}
	}
	sb.WriteString("Read the file with the book tool's read action.\n")
	return sb.String()
}
