package dreaming

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

type fakeProvider struct {
	mu       sync.Mutex
	requests []core.Request
	usage    *core.Usage
	answer   func(req core.Request) string
}

func (f *fakeProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	ch := make(chan core.AssistantEvent, 2)
	text := f.answer(req)
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: text}
	msg := &core.Message{Role: "assistant", Usage: f.usage}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: msg}
	close(ch)
	return ch, nil
}

func models(t *testing.T) (core.Model, core.Model) {
	t.Helper()
	luna, ok1 := core.ResolveModel("luna")
	sonnet, ok2 := core.ResolveModel("sonnet")
	if !ok1 || !ok2 {
		t.Fatal("models not in the catalog")
	}
	return luna, sonnet
}

// fakeNight answers like the pilot's models: analyses as text, and a plan
// that consolidates work/c.md while every other sheet is already clean.
func fakeNight(t *testing.T, budget float64) (*fakeProvider, NightConfig, *[]News) {
	luna, sonnet := models(t)
	fp := &fakeProvider{usage: &core.Usage{Input: 1000, Output: 500}, answer: func(req core.Request) string {
		prompt := req.Messages[0].Content[0].Text
		switch req.System {
		case memorySystem:
			return keepAllFacts(prompt)
		case organizerSystem:
			if strings.Contains(prompt, "Hoja: work/c.md\n") {
				return "```json\n" + `{"estado": "## Estado actual\n- C2 vigente (12-nov · detalle abajo)\n\n## Por resolver\n- PREGUNTA AL DUEÑO: ¿sigue C2?", "conservar": [[0,3,3]], "archivar": [{"f":0,"lineas":[2,2],"motivo":"superado","fuente":"work/c.md:3"}]}` + "\n```"
			}
			return `{"sin_cambios": true}`
		default:
			return "E | C | algo | 12-nov | VIGENTE | work/c.md:3"
		}
	}}
	var news []News
	cfg := NightConfig{Date: "2026-11-12", BudgetUSD: budget, Analyzer: luna, Organizer: sonnet,
		Factory: func(core.Model) (core.Provider, error) { return fp, nil },
		Publish: func(n News) error { news = append(news, n); return nil },
		Now:     func() time.Time { return time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC) }}
	return fp, cfg, &news
}

func TestNightConsolidatesWithToollessCallsAndPublishesOnce(t *testing.T) {
	s := fixture(t)
	fp, cfg, news := fakeNight(t, 3)
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ran || len(res.Applied) != 1 || res.Applied[0] != "book/work/c.md" {
		t.Fatalf("result = %+v", res)
	}
	for _, req := range fp.requests {
		if len(req.Tools) != 0 {
			t.Fatal("a model was given tools")
		}
		if req.Options.CacheRetention != core.CacheOff {
			t.Fatal("cache writes not suppressed")
		}
	}
	if len(*news) != 1 || !strings.Contains((*news)[0].Body, "PREGUNTA AL DUEÑO") || !strings.Contains((*news)[0].Body, "deshaz el dreaming") {
		t.Fatalf("news = %+v", *news)
	}
	got := tree(t, s)
	if !strings.Contains(got["book/work/c.md"], "## Estado actual") || !strings.Contains(got["book/archivo/work/c/2026-11-12.md"], "C1 viejo") {
		t.Fatalf("c.md not consolidated:\n%s", got["book/work/c.md"])
	}
	// Same date again: nothing runs, nothing is called.
	before := len(fp.requests)
	res, err = s.Night(t.Context(), cfg)
	if err != nil || res.Ran || len(fp.requests) != before {
		t.Fatalf("second run on the same date: %+v %v", res, err)
	}
	// Next date with nothing changed: no model call at all.
	cfg.Date = "2026-11-13"
	res, err = s.Night(t.Context(), cfg)
	if err != nil || len(fp.requests) != before || len(res.Applied) != 0 {
		t.Fatalf("unchanged book cost calls: %d → %d (%+v, %v)", before, len(fp.requests), res, err)
	}
}

// I10: every call is paid for before it is sent, a call without usage keeps
// its reservation, and a model without a price is never used.
func TestBudgetIsAHardCap(t *testing.T) {
	_, sonnet := models(t)
	res, err := Reservation(sonnet, "s", strings.Repeat("x", 100_000), organizerMaxOut)
	if err != nil {
		t.Fatal(err)
	}
	// 100 KB + system + transport margin at $2/M plus 32K out at $10/M.
	if want := float64(100_000+1+transportOverheadBytes)*2/1e6 + 32_000*10/1e6; res < want-1e-9 {
		t.Fatalf("reservation %v under the bound %v", res, want)
	}
	fp := &fakeProvider{answer: func(core.Request) string { return "ok" }} // no usage
	b := &Budget{Limit: res * 1.5}
	if _, err := b.call(t.Context(), func(core.Model) (core.Provider, error) { return fp, nil }, sonnet, "s", strings.Repeat("x", 100_000), organizerMaxOut); err != nil {
		t.Fatal(err)
	}
	if b.Spent < res-1e-9 {
		t.Fatalf("a call without usage cost %v, want its reservation %v", b.Spent, res)
	}
	if _, err := b.call(t.Context(), func(core.Model) (core.Provider, error) { return fp, nil }, sonnet, "s", strings.Repeat("x", 100_000), organizerMaxOut); !errors.Is(err, ErrBudget) {
		t.Fatalf("second call = %v, want ErrBudget", err)
	}
	if len(fp.requests) != 1 {
		t.Fatalf("%d requests sent, want 1", len(fp.requests))
	}
	free := sonnet
	free.Pricing = nil
	if _, err := Reservation(free, "", "x", 10); err == nil {
		t.Fatal("a model without a price was accepted")
	}
}

// A night whose budget cannot pay for the first call writes nothing, calls
// nothing, and does not run again that day.
func TestNightWithoutBudgetCallsNothing(t *testing.T) {
	s := fixture(t)
	fp, cfg, _ := fakeNight(t, 0.0001)
	before := tree(t, s)
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.requests) != 0 || len(res.Applied) != 0 {
		t.Fatalf("calls = %d, result = %+v", len(fp.requests), res)
	}
	if !equalTrees(tree(t, s), before) {
		t.Fatal("the book changed")
	}
}
