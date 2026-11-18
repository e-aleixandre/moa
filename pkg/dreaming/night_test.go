package dreaming

import (
	"context"
	"errors"
	"slices"
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
	stop     string // StopReason of every answer
	answer   func(req core.Request) string
}

func (f *fakeProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	ch := make(chan core.AssistantEvent, 2)
	text := f.answer(req)
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: text}
	msg := &core.Message{Role: "assistant", Usage: f.usage, StopReason: f.stop}
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

// pairAnswer, when set, is what the fake pairer answers.
var pairAnswer string

// fakeNight answers like the pilot's models: analyses as text, and a plan
// that consolidates work/c.md while every other sheet is already clean.
func fakeNight(t *testing.T, budget float64) (*fakeProvider, NightConfig, *[]News) {
	luna, sonnet := models(t)
	fp := &fakeProvider{usage: &core.Usage{Input: 1000, Output: 500}, answer: func(req core.Request) string {
		prompt := req.Messages[0].Content[0].Text
		switch req.System {
		case memorySystem:
			return keepAllFacts(prompt)
		case pairerSystem:
			if pairAnswer != "" {
				return pairAnswer
			}
			return `{"marcas": [], "preguntas": []}`
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

// What the night could not honour of its owner's model choice is said in the
// morning news, not only in a log.
func TestMorningNewsSaysWhyTheChosenModelWasNotUsed(t *testing.T) {
	cfg := NightConfig{Date: "2026-11-12", Notes: []string{"Lectura: «luna» no tiene credenciales ahora; se usó el de por defecto."}}
	n := morningNews(cfg, Report{}, NightResult{}, nil, factOutcome{}, &Budget{Limit: 3})
	if !strings.Contains(n.Body, "Lectura: «luna» no tiene credenciales") {
		t.Fatalf("news body = %q", n.Body)
	}
	quiet := morningNews(NightConfig{Date: "2026-11-12"}, Report{}, NightResult{}, nil, factOutcome{}, &Budget{Limit: 3})
	if strings.Contains(quiet.Body, "Lectura") {
		t.Fatalf("a night with no fallback mentioned one: %q", quiet.Body)
	}
}

// An analysis that stopped on the output cap is refused, not handed on whole:
// the organizer would treat the unread rest of the sheet as unchanged.
func TestTruncatedAnalysisIsRejectedNotPassedOn(t *testing.T) {
	s := fixture(t)
	fp, cfg, _ := fakeNight(t, 3)
	inner := fp.answer
	fp.answer = func(req core.Request) string {
		if req.System != organizerSystem && req.System != memorySystem && req.System != pairerSystem {
			fp.stop = "max_tokens"
		} else {
			fp.stop = ""
		}
		return inner(req)
	}
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("a sheet with a cut-off analysis was applied: %+v", res)
	}
	if why := res.Rejected["work/c.md"]; !strings.Contains(why, "sin espacio de respuesta") {
		t.Fatalf("rejected = %v", res.Rejected)
	}
	for _, req := range fp.requests {
		if req.System == organizerSystem {
			t.Fatal("the organizer was called on a truncated analysis")
		}
	}
}

// Each default pair must be able to run a night inside the owner's budget:
// the up-front reservation of one organizer call has to fit it, and a night
// has to organize at least one sheet. With only OpenAI the pair once reserved
// the model's whole 128k output at the fast-mode rate, more than the budget.
func TestEveryDefaultPairFitsTheNightBudget(t *testing.T) {
	has := func(ps ...string) core.AuxiliaryModelAvailable {
		return func(p string) bool { return slices.Contains(ps, p) }
	}
	for name, avail := range map[string]core.AuxiliaryModelAvailable{
		"only anthropic": has("anthropic"),
		"only openai":    has("openai"),
		"both":           has("anthropic", "openai"),
	} {
		t.Run(name, func(t *testing.T) {
			reader, organizer, notes, ok := core.ResolveDreamingModels(core.MoaConfig{}, avail)
			if !ok || len(notes) != 0 {
				t.Fatalf("defaults: ok=%v notes=%v", ok, notes)
			}
			for _, m := range []core.Model{reader, organizer} {
				res, err := Reservation(m, organizerSystem, strings.Repeat("x", 60*1024), organizerMaxOut)
				if err != nil || res > 3 {
					t.Fatalf("%s reserves $%.2f (err %v) against a $3 night", m.ID, res, err)
				}
			}
			s := fixture(t)
			_, cfg, _ := fakeNight(t, 3)
			cfg.Analyzer, cfg.Organizer = reader, organizer
			res, err := s.Night(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(res.Applied, "book/work/c.md") {
				t.Fatalf("no sheet was organized: applied=%v deferred=%v rejected=%v", res.Applied, res.Deferred, res.Rejected)
			}
		})
	}
}

// The reservation counts what the call can really cost: the output it asks
// for, not the model's maximum, no fast-mode premium the dreaming never asks
// for, and the long-context tier only when the prompt reaches it.
func TestReservationFollowsTheCallNotTheModel(t *testing.T) {
	sol, ok := core.ResolveModel("sol")
	if !ok {
		t.Fatal("sol missing")
	}
	short, err := Reservation(sol, "s", "p", 32_000)
	if err != nil {
		t.Fatal(err)
	}
	if want := 32_000*sol.Pricing.Output/1e6 + float64(2+transportOverheadBytes)*sol.Pricing.CacheWrite/1e6; short > want+1e-9 {
		t.Fatalf("short prompt reserves $%.4f, want at most $%.4f (requested output, base tier, no fast)", short, want)
	}
	long, err := Reservation(sol, "s", strings.Repeat("x", 300_000), 32_000)
	if err != nil {
		t.Fatal(err)
	}
	if long <= short*2 {
		t.Fatalf("a prompt past the long-context threshold reserves $%.4f, barely above $%.4f", long, short)
	}
}

// A request that never got an answer (rejected, or failed before a single
// token) was not billed: it gives its reservation back. One that failed
// mid-answer may have been, so it keeps it.
func TestFailedCallReleasesItsReservationOnlyWhenNothingWasBilled(t *testing.T) {
	_, sonnet := models(t)
	run := func(p core.Provider) (*Budget, error) {
		b := &Budget{Limit: 3}
		_, err := b.call(t.Context(), func(core.Model) (core.Provider, error) { return p, nil }, sonnet, "s", "p", 1000)
		return b, err
	}
	b, err := run(streamErrProvider{reject: true})
	if err == nil || b.Spent != 0 {
		t.Fatalf("rejected request: err=%v spent=%v, want an error and 0", err, b.Spent)
	}
	b, err = run(streamErrProvider{})
	if err == nil || b.Spent != 0 {
		t.Fatalf("error before any output: err=%v spent=%v, want an error and 0", err, b.Spent)
	}
	b, err = run(streamErrProvider{partial: true})
	if err == nil || b.Spent <= 0 {
		t.Fatalf("error after output began: err=%v spent=%v, want the reservation kept", err, b.Spent)
	}
}

type streamErrProvider struct{ reject, partial bool }

func (p streamErrProvider) Stream(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	if p.reject {
		return nil, errors.New("429")
	}
	ch := make(chan core.AssistantEvent, 2)
	if p.partial {
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "abc"}
	}
	ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: errors.New("quota")}
	close(ch)
	return ch, nil
}
