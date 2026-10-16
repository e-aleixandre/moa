package subagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

type daybreakLaunch struct {
	mu       sync.Mutex
	models   []string
	programs []string
}

func (d *daybreakLaunch) factory(model core.Model) (core.Provider, error) {
	return newMockProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		d.mu.Lock()
		d.models = append(d.models, req.Model.ID)
		d.programs = append(d.programs, req.Options.AccessProgram)
		d.mu.Unlock()
		return textResponse("OK")(ctx, req)
	}), nil
}

func (d *daybreakLaunch) last() (string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.models) == 0 {
		return "", ""
	}
	return d.models[len(d.models)-1], d.programs[len(d.programs)-1]
}

func daybreakConfig(d *daybreakLaunch, parentOn bool, catalog func() core.DaybreakAvailability) Config {
	sol, _ := core.ResolveModel("gpt-6-sol")
	return Config{
		DefaultModel:    sol,
		CurrentModel:    func() core.Model { return sol },
		ProviderFactory: d.factory,
		CurrentDaybreak: func() bool { return parentOn },
		DaybreakCatalog: catalog,
	}
}

func subagentKnownCatalog() core.DaybreakAvailability {
	return core.DaybreakAvailability{Transport: true, Known: true, Models: map[string]bool{"gpt-6-sol": true, "gpt-6.1-sol": false}}
}

const notApplied = "Daybreak not applied"

func TestSubagentInheritsDaybreak(t *testing.T) {
	d := &daybreakLaunch{}
	sub, _, _ := newSubagentTools(t, daybreakConfig(d, true, subagentKnownCatalog))
	res, err := sub.Execute(context.Background(), map[string]any{"task": "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if model, program := d.last(); model != "gpt-6-sol" || program != core.DaybreakAccessProgram {
		t.Fatalf("child = %s program=%q, want gpt-6-sol with Daybreak", model, program)
	}
	if strings.Contains(textOf(res), notApplied) {
		t.Fatalf("compatible child got a note: %s", textOf(res))
	}
}

// An incompatible OpenAI model runs standard, as asked, and the result says so.
func TestSubagentIncompatibleOpenAIRunsStandard(t *testing.T) {
	d := &daybreakLaunch{}
	sub, _, _ := newSubagentTools(t, daybreakConfig(d, true, subagentKnownCatalog))
	res, err := sub.Execute(context.Background(), map[string]any{"task": "hello", "model": "sol"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if model, program := d.last(); model != "gpt-6.1-sol" || program != "" {
		t.Fatalf("child = %s program=%q, want standard gpt-6.1-sol", model, program)
	}
	if want := "Daybreak not applied: GPT-6.1 Sol doesn't support it"; !strings.Contains(textOf(res), want) {
		t.Fatalf("result %q lacks %q", textOf(res), want)
	}
}

// Another provider: Daybreak does not apply, no note, no error.
func TestSubagentOtherProviderNoNote(t *testing.T) {
	d := &daybreakLaunch{}
	sub, _, _ := newSubagentTools(t, daybreakConfig(d, true, subagentKnownCatalog))
	res, err := sub.Execute(context.Background(), map[string]any{"task": "hello", "model": "haiku"}, nil)
	if err != nil || res.IsError {
		t.Fatalf("launch = %v %s", err, textOf(res))
	}
	if _, program := d.last(); program != "" {
		t.Fatalf("Anthropic child program=%q", program)
	}
	if strings.Contains(textOf(res), notApplied) {
		t.Fatalf("other provider got a note: %s", textOf(res))
	}
}

// Without a known catalog the program is sent and the provider answers.
func TestSubagentUnknownCatalogInherits(t *testing.T) {
	d := &daybreakLaunch{}
	sub, _, _ := newSubagentTools(t, daybreakConfig(d, true, nil))
	res, err := sub.Execute(context.Background(), map[string]any{"task": "hello", "model": "sol"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, program := d.last(); program != core.DaybreakAccessProgram {
		t.Fatalf("unknown catalog child program=%q", program)
	}
	if strings.Contains(textOf(res), notApplied) {
		t.Fatalf("unknown catalog produced a note: %s", textOf(res))
	}
}

// "daybreak" as a subagent model is GPT-6 Sol with the program, even when the
// parent runs standard; the allowlist accepts it.
func TestSubagentDaybreakSpec(t *testing.T) {
	for _, allow := range [][]string{nil, {"daybreak"}, {core.LegacyDaybreakModelID}} {
		d := &daybreakLaunch{}
		cfg := daybreakConfig(d, false, subagentKnownCatalog)
		cfg.AllowedModels = allow
		sub, _, _ := newSubagentTools(t, cfg)
		for _, spec := range []string{"daybreak", core.LegacyDaybreakModelID} {
			res, err := sub.Execute(context.Background(), map[string]any{"task": "hello", "model": spec}, nil)
			if err != nil || res.IsError {
				t.Fatalf("allow=%v spec=%s: %v %s", allow, spec, err, textOf(res))
			}
			if model, program := d.last(); model != "gpt-6-sol" || program != core.DaybreakAccessProgram {
				t.Fatalf("allow=%v spec=%s: child = %s program=%q", allow, spec, model, program)
			}
		}
	}
	// Parent standard, no spec: the child is standard too.
	d := &daybreakLaunch{}
	sub, _, _ := newSubagentTools(t, daybreakConfig(d, false, subagentKnownCatalog))
	if _, err := sub.Execute(context.Background(), map[string]any{"task": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, program := d.last(); program != "" {
		t.Fatalf("standard parent's child program=%q", program)
	}
}

// The async launch says it too, and the job records the mode for its card.
func TestSubagentAsyncDaybreakNoteAndJob(t *testing.T) {
	d := &daybreakLaunch{}
	sub, _, _, store := newSubagentToolsWithStore(t, daybreakConfig(d, true, subagentKnownCatalog))
	res, err := sub.Execute(context.Background(), map[string]any{"task": "hello", "model": "sol", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(res), "Daybreak not applied: GPT-6.1 Sol doesn't support it") {
		t.Fatalf("async start %q lacks the note", textOf(res))
	}
	res2, err := sub.Execute(context.Background(), map[string]any{"task": "hello", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var infos []JobInfo
	for time.Now().Before(deadline) {
		infos = (&Jobs{store: store}).Snapshot()
		if len(infos) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	byModel := map[string]bool{}
	for _, info := range infos {
		byModel[info.Model] = info.Daybreak
	}
	if byModel["gpt-6.1-sol"] || !byModel["gpt-6-sol"] {
		t.Fatalf("job daybreak flags = %v (%s)", byModel, textOf(res2))
	}
}
