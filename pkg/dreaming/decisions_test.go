package dreaming

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const newDecision = "# Precio de tarifa (12/11)\nSustituye a precios.md: el precio vigente es el de la tarifa, no la última fuente.\n"

func markNight(t *testing.T, scope, oldQuote string) (Store, NightResult, []News, []byte) {
	t.Helper()
	s := fixture(t)
	if err := os.WriteFile(filepath.Join(s.bookDir(), "decisions/2026-11-12-tarifa.md"), []byte(newDecision), 0o600); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(filepath.Join(s.bookDir(), "decisions/precios.md"))
	pairAnswer = `{"marcas": [{"vieja": "decisions/precios.md", "nueva": "decisions/2026-11-12-tarifa.md", "fecha_nueva": "12-nov-2026",
	  "alcance": "` + scope + `", "punto": "precio vigente", "que_cambia": "Manda la tarifa.",
	  "cita_vieja": {"ruta": "decisions/precios.md", "linea": 2, "texto": "` + oldQuote + `"},
	  "cita_nueva": {"ruta": "decisions/2026-11-12-tarifa.md", "linea": 2, "texto": "el precio vigente es el de la tarifa"}}],
	  "preguntas": ["¿Vale también para Katxina? (decisions/precios.md, decisions/2026-11-12-tarifa.md)"]}`
	t.Cleanup(func() { pairAnswer = "" })
	_, cfg, news := fakeNight(t, 5)
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, res, *news, orig
}

// A partial mark puts the header above the decision's untouched bytes and
// tells the morning both quotes and the question.
func TestPartialMarkKeepsTheDecisionBytes(t *testing.T) {
	s, res, news, orig := markNight(t, "parcial", "Decisión del dueño: el precio vigente es la *última* fuente")
	if !contains(res.Applied, "book/decisions/precios.md") {
		t.Fatalf("result = %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(s.bookDir(), "decisions/precios.md"))
	header := strings.TrimSuffix(string(got), string(orig))
	if !strings.HasSuffix(string(got), string(orig)) || !strings.HasPrefix(header, "> **Superada en parte por** `decisions/2026-11-12-tarifa.md`") {
		t.Fatalf("marked decision:\n%s", got)
	}
	// The cited line counts the header.
	n := strings.Count(header, "\n")
	if !strings.Contains(header, "decisions/precios.md:"+strconv.Itoa(2+n)) {
		t.Fatalf("old quote not shifted by %d lines:\n%s", n, header)
	}
	body := news[0].Body
	if !strings.Contains(body, "el precio vigente es el de la tarifa") || !strings.Contains(body, "¿Vale también para Katxina?") {
		t.Fatalf("news:\n%s", body)
	}
	// Undo gives the decision back as it was.
	if _, err := s.Undo(res.Commit, nil); err != nil {
		t.Fatal(err)
	}
	if back, _ := os.ReadFile(filepath.Join(s.bookDir(), "decisions/precios.md")); string(back) != string(orig) {
		t.Fatalf("after undo:\n%s", back)
	}
}

func TestTotalMarkMovesTheDecisionVerbatim(t *testing.T) {
	s, res, _, orig := markNight(t, "total", "el precio vigente es la última fuente")
	if !contains(res.Applied, "book/decisions/precios.md") {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(s.bookDir(), "decisions/precios.md")); !os.IsNotExist(err) {
		t.Fatal("a totally superseded decision stayed in decisions/")
	}
	got, _ := os.ReadFile(filepath.Join(s.bookDir(), "archivo/decisions/precios.md"))
	if !strings.HasSuffix(string(got), string(orig)) || !strings.Contains(string(got), "**Superada por**") {
		t.Fatalf("archived decision:\n%s", got)
	}
}

func TestMarkWithAnAlteredQuoteIsRefused(t *testing.T) {
	s, res, _, orig := markNight(t, "parcial", "el precio vigente es la primera fuente")
	if len(res.Applied) != 0 && contains(res.Applied, "book/decisions/precios.md") {
		t.Fatalf("applied: %+v", res)
	}
	if _, ok := res.Rejected["marca decisions/precios.md"]; !ok {
		t.Fatalf("not rejected: %+v", res.Rejected)
	}
	if got, _ := os.ReadFile(filepath.Join(s.bookDir(), "decisions/precios.md")); string(got) != string(orig) {
		t.Fatal("the decision changed")
	}
}

func TestMarkRules(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "work/c.md")
	units, _, err := s.DueDecisions(n.snap)
	if err != nil {
		t.Fatal(err)
	}
	units["decisions/2025-01-01-vieja.md"] = Unit{Sheet: "decisions/2025-01-01-vieja.md", Files: []string{"decisions/2025-01-01-vieja.md"}}
	due := map[string]bool{"decisions/precios.md": true}
	good := Mark{Old: "decisions/precios.md", New: "decisions/precios.md", NewDate: "x", Scope: "total", Point: "p", Change: "c",
		OldQuote: Quote{Path: "decisions/precios.md", Line: 2, Text: "precio vigente"}, NewQuote: Quote{Path: "decisions/precios.md", Line: 2, Text: "precio vigente"}}
	if err := checkMark(n.snap, units, due, good); err == nil {
		t.Fatal("a decision superseded itself")
	}
	m := good
	m.Old, m.OldQuote.Path = "work/c.md", "work/c.md"
	if err := checkMark(n.snap, units, due, m); err == nil {
		t.Fatal("a sheet was marked as a decision")
	}
	m = good
	m.Old = "decisions/2025-01-01-vieja.md"
	m.New = "decisions/precios.md"
	m.Scope = "a medias"
	if err := checkMark(n.snap, units, due, m); err == nil {
		t.Fatal("bad scope accepted")
	}
	// Valid but for the night: neither side changed since the last night.
	units["decisions/b.md"] = units["decisions/precios.md"]
	m = good
	m.New = "decisions/b.md"
	if err := checkMark(n.snap, units, due, m); err != nil {
		t.Fatalf("control mark refused: %v", err)
	}
	if err := checkMark(n.snap, units, map[string]bool{}, m); err == nil {
		t.Fatal("a mark between decisions that did not change was accepted")
	}
	m = good
	m.Old, m.New = "decisions/2025-01-01-vieja.md", "decisions/precios.md"
	m.Point = "dos\nlíneas"
	if err := checkMark(n.snap, units, due, m); err == nil {
		t.Fatal("multi-line point accepted")
	}
}


// A total mark never overwrites what the archive already holds.
func TestTotalMarkNeverOverwritesTheArchive(t *testing.T) {
	s := fixture(t)
	dst := filepath.Join(s.bookDir(), "archivo/decisions/precios.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("archivado antes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := prepare(t, s, "2026-11-12", "work/c.md")
	units, due, err := s.DueDecisions(n.snap)
	if err != nil {
		t.Fatal(err)
	}
	units["decisions/b.md"] = units["decisions/precios.md"]
	m := Mark{Old: "decisions/precios.md", New: "decisions/b.md", NewDate: "x", Scope: "total", Point: "p", Change: "c",
		OldQuote: Quote{Path: "decisions/precios.md", Line: 2, Text: "precio vigente"}, NewQuote: Quote{Path: "decisions/precios.md", Line: 2, Text: "precio vigente"}}
	rejected := map[string]string{}
	groups, _, _ := n.r.markGroups(n.snap, units, due, []Mark{m}, "2026-11-12", rejected)
	if len(groups) != 0 || rejected["marca decisions/precios.md"] == "" {
		t.Fatalf("groups = %+v, rejected = %v", groups, rejected)
	}
}
