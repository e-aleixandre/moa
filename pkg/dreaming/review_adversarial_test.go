package dreaming

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/book"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/storelock"
)

func TestDreamingAdversarialReorderedDecisionQuoteMustFail(t *testing.T) {
	snap := Snapshot{Files: map[string][]byte{"book/decisions/a.md": []byte("# Regla\nNo está permitido borrar datos.\n")}}
	q := Quote{Path: "decisions/a.md", Line: 2, Text: "borrar datos ... permitido"}
	if quoteHolds(snap, q) {
		t.Fatal("accepted a fabricated quote whose fragments are in reverse order")
	}
}

func TestDreamingAdversarialBookSecretNeverSentOrPrinted(t *testing.T) {
	s := fixture(t)
	// A clearly synthetic credential, intentionally matching the existing detector.
	secret := "sk-review-DUMMY-123456789012345678901234567890"
	path := filepath.Join(s.bookDir(), "decisions/precios.md")
	data := "# Precios\nToken de prueba: " + secret + "\n"
	if !isSecret([]byte(data)) {
		t.Fatal("fixture must be recognized by the existing detector")
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	fp, cfg, news := fakeNight(t, 5)
	answer := fp.answer
	fp.answer = func(req core.Request) string {
		if req.System == pairerSystem {
			return `{"marcas": [], "preguntas": ["¿Debo retirar ` + secret + ` de decisions/precios.md?"]}`
		}
		return answer(req)
	}
	if _, err := s.Night(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, req := range fp.requests {
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				if strings.Contains(c.Text, secret) {
					t.Error("recognized secret sent to a model")
				}
			}
		}
	}
	for _, n := range *news {
		if strings.Contains(n.Body, secret) {
			t.Error("recognized secret printed in morning news")
		}
	}
}

func TestDreamingAdversarialNightCancellationWhileRunLockBusy(t *testing.T) {
	s := fixture(t)
	lock, err := storelock.Lock(s.workDir())
	if err != nil {
		t.Fatal(err)
	}
	_, cfg, _ := fakeNight(t, 0.0001)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := s.Night(ctx, cfg); done <- err }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		_ = lock.Close()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		_ = lock.Close()
		<-done // Join the worker before failing/cleaning up.
		t.Fatal("night ignores cancellation while waiting in flock; serve Close waits indefinitely")
	}
}

func TestDreamingAdversarialChangedCrossSourceDefersWholePlan(t *testing.T) {
	for _, name := range []string{"areas/b.md", "areas/facturación.md", "areas/datos fiscales.md"} {
		t.Run(name, func(t *testing.T) { crossSourceCase(t, name) })
	}
}

func crossSourceCase(t *testing.T, name string) {
	s := fixture(t)
	if name != "areas/b.md" {
		if err := os.WriteFile(filepath.Join(s.bookDir(), name), []byte("# B\nB1 viejo\nB2 vigente\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fp, cfg, _ := fakeNight(t, 5)
	answer := fp.answer
	mutated := false
	fp.answer = func(req core.Request) string {
		switch req.System {
		case crossSystem:
			return "## Por hoja\n### work/c.md\n- CASA de B: vigente B2 ("+name+":3).\n"
		case organizerSystem:
			prompt := req.Messages[0].Content[0].Text
			if strings.Contains(prompt, "Hoja: work/c.md\n") {
				if !strings.Contains(prompt, name+":3") {
					t.Fatal("cross-sheet source did not reach the organizer")
				}
				if err := book.WriteFile(s.bookDir(), name, []byte("# B\nB2 descartado por el dueño\n")); err != nil {
					t.Fatal(err)
				}
				mutated = true
				return `{"estado":"## Estado actual\n- B2 vigente (12-nov · areas/b.md:3)","conservar":[[0,3,3]],"archivar":[{"f":0,"lineas":[2,2],"motivo":"superado","fuente":"work/c.md:3"}]}`
			}
		}
		return answer(req)
	}
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !mutated {
		t.Fatal("fixture did not exercise the organizer")
	}
	if contains(res.Applied, "book/work/c.md") {
		t.Fatalf("Night applied a state based on areas/b.md after its source was edited through book.WriteFile: %+v", res)
	}
}

func TestDreamingAdversarialPartialMarkChecksOriginalDecisionParts(t *testing.T) {
	s := fixture(t)
	old := "decisions/precios.md"
	part := "decisions/precios/parte-01.md"
	newp := "decisions/2026-11-12-tarifa.md"
	if err := book.WriteFile(s.bookDir(), part, []byte("El precio anterior ya no vale.\n")); err != nil {
		t.Fatal(err)
	}
	if err := book.WriteFile(s.bookDir(), newp, []byte(newDecision)); err != nil {
		t.Fatal(err)
	}
	n := prepare(t, s, "2026-11-12", "work/c.md")
	units, due, err := s.DueDecisions(n.snap)
	if err != nil {
		t.Fatal(err)
	}
	m := Mark{Old: old, New: newp, NewDate: "2026-11-12", Scope: "parcial", Point: "precio", Change: "Manda la tarifa", OldQuote: Quote{Path: part, Line: 1, Text: "El precio anterior ya no vale"}, NewQuote: Quote{Path: newp, Line: 2, Text: "el precio vigente es el de la tarifa"}}
	gs, c, _ := n.r.markGroups(n.snap, units, due, []Mark{m}, "2026-11-12", map[string]string{})
	if len(gs) != 1 {
		t.Fatalf("fixture mark rejected: %+v", gs)
	}
	if err := book.WriteFile(s.bookDir(), part, []byte("El dueño confirma que el precio anterior sigue vigente.\n")); err != nil {
		t.Fatal(err)
	}
	rep, err := n.r.Apply(n.snap, "dreaming", gs, c, n.fin, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Applied) > 0 {
		t.Fatal("partial supersession applied after its original literal source was replaced")
	}
}

func TestDreamingAdversarialCostReturnedMatchesPersistedCost(t *testing.T) {
	s := fixture(t)
	_, cfg, _ := fakeNight(t, 5)
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.loadState()
	if err != nil {
		t.Fatal(err)
	}
	spent := st.Nights[cfg.Date].SpentUSD
	if spent <= 0 {
		t.Fatal("fixture did not spend")
	}
	if res.CostUSD != spent {
		t.Fatalf("returned CostUSD=%v, persisted real spend=%v", res.CostUSD, spent)
	}
}

func TestDreamingAdversarialNewPartMakesUnitConflict(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "work/a.md")
	if err := book.WriteFile(s.bookDir(), "work/a/parte-02.md", []byte("nueva parte escrita por el owner durante el dreaming\n")); err != nil {
		t.Fatal(err)
	}
	rep := n.apply(t)
	if len(rep.Applied) > 0 {
		t.Fatalf("unit with changed membership still applied: %+v", rep)
	}
}

func TestDreamingAdversarialFabricatedQuoteCanArchiveDecision(t *testing.T) {
	s, res, _, _ := markNight(t, "total", "fuente ... última")
	if contains(res.Applied, "book/decisions/precios.md") {
		if _, err := os.Stat(filepath.Join(s.bookDir(), "decisions/precios.md")); !os.IsNotExist(err) {
			t.Fatal(err)
		}
		t.Fatal("a reversed, nonliteral quote was accepted and the decision was removed from live search")
	}
}
func TestDreamingAdversarialPartialMarkUndoDoesNotReprocessUnchangedDecision(t *testing.T) {
	s := fixture(t)
	if err := book.WriteFile(s.bookDir(), "decisions/precios/parte-01.md", []byte("apéndice vigente\n")); err != nil {
		t.Fatal(err)
	}
	if err := book.WriteFile(s.bookDir(), "decisions/2026-11-12-tarifa.md", []byte(newDecision)); err != nil {
		t.Fatal(err)
	}
	n := prepare(t, s, "2026-11-12", "work/c.md")
	units, due, err := s.DueDecisions(n.snap)
	if err != nil {
		t.Fatal(err)
	}
	m := Mark{Old: "decisions/precios.md", New: "decisions/2026-11-12-tarifa.md", NewDate: "2026-11-12", Scope: "parcial", Point: "precio", Change: "tarifa", OldQuote: Quote{Path: "decisions/precios.md", Line: 2, Text: "precio vigente"}, NewQuote: Quote{Path: "decisions/2026-11-12-tarifa.md", Line: 2, Text: "precio vigente"}}
	gs, c, _ := n.r.markGroups(n.snap, units, due, []Mark{m}, "2026-11-12", map[string]string{})
	rep, err := n.r.Apply(n.snap, "dreaming", gs, c, n.fin, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.r.Close()
	if _, err := s.Undo(rep.Commit, nil); err != nil {
		t.Fatal(err)
	}
	r, _, err := s.Begin(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	snap, err := r.Snapshot("after undo")
	if err != nil {
		t.Fatal(err)
	}
	_, again, err := s.DueDecisions(snap)
	if err != nil {
		t.Fatal(err)
	}
	if again[m.Old] {
		t.Fatal("undo did not record the hash of the unchanged parts; same decision is due again immediately")
	}
}

// A night waiting for the book's lock (a tool writing) stops when cancelled.
func TestDreamingAdversarialNightCancellationWhileBookLockBusy(t *testing.T) {
	s := fixture(t)
	lock, err := storelock.Lock(s.bookDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	_, cfg, _ := fakeNight(t, 5)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := s.Night(ctx, cfg); done <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		_ = lock.Close()
		<-done
		t.Fatal("the night ignored cancellation while waiting for the book lock")
	}
}
