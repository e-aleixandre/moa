package bus

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func knownCatalog() core.DaybreakAvailability {
	return core.DaybreakAvailability{Transport: true, Known: true, Models: map[string]bool{"gpt-6-sol": true, "gpt-6-luna": true}}
}

func daybreakSessionContext(t *testing.T, fa *fakeAgent, catalog func() core.DaybreakAvailability) (*SessionContext, chan ConfigChanged, EventBus) {
	t.Helper()
	b := NewLocalBus()
	t.Cleanup(b.Close)
	sctx := newTestSessionContext(b, fa)
	sctx.ProviderFactory = func(m core.Model) (core.Provider, error) { return errProvider{}, nil }
	sctx.DaybreakCatalog = catalog
	RegisterHandlers(sctx)
	changed := make(chan ConfigChanged, 4)
	b.Subscribe(func(e ConfigChanged) { changed <- e })
	return sctx, changed, b
}

// The "daybreak" spec is GPT-6 Sol with the program on, keeping the thinking.
func TestHandler_SwitchModel_DaybreakSpec(t *testing.T) {
	fa := &fakeAgent{model: core.Model{ID: "claude-opus-5", Provider: "anthropic"}, thinkingLevel: "high"}
	_, changed, b := daybreakSessionContext(t, fa, knownCatalog)
	if err := b.Execute(SwitchModel{ModelSpec: "daybreak"}); err != nil {
		t.Fatal(err)
	}
	if fa.Model().ID != core.DaybreakModelID || !fa.Daybreak() {
		t.Fatalf("after daybreak spec: model=%s daybreak=%v", fa.Model().ID, fa.Daybreak())
	}
	e := drainChan(changed, b, t)
	if e.Daybreak == nil || !*e.Daybreak || e.DaybreakSupported == nil || !*e.DaybreakSupported {
		t.Fatalf("config_change = %+v, want explicit daybreak fields", e)
	}
}

// With Daybreak on and a known catalog, an incompatible OpenAI model is refused
// and nothing changes: Daybreak is never dropped silently.
func TestHandler_SwitchModel_RejectsIncompatibleUnderDaybreak(t *testing.T) {
	sol, _ := core.ResolveModel("gpt-6-sol")
	fa := &fakeAgent{model: sol, thinkingLevel: "medium", daybreak: true}
	_, _, b := daybreakSessionContext(t, fa, knownCatalog)
	err := b.Execute(SwitchModel{ModelSpec: "sol"})
	if err == nil || !strings.Contains(err.Error(), "GPT-6.1 Sol doesn't support Daybreak") {
		t.Fatalf("switch to gpt-6.1-sol = %v, want a clear refusal", err)
	}
	if fa.Model().ID != "gpt-6-sol" || !fa.Daybreak() {
		t.Fatalf("refused switch changed state: model=%s daybreak=%v", fa.Model().ID, fa.Daybreak())
	}
	// A compatible one goes through.
	if err := b.Execute(SwitchModel{ModelSpec: "luna"}); err != nil {
		t.Fatalf("switch to gpt-6-luna: %v", err)
	}
}

// Without a known catalog nothing is refused: the provider answers.
func TestHandler_SwitchModel_UnknownCatalogDoesNotReject(t *testing.T) {
	sol, _ := core.ResolveModel("gpt-6-sol")
	fa := &fakeAgent{model: sol, thinkingLevel: "medium", daybreak: true}
	_, changed, b := daybreakSessionContext(t, fa, nil)
	if err := b.Execute(SwitchModel{ModelSpec: "sol"}); err != nil {
		t.Fatalf("unknown catalog refused a switch: %v", err)
	}
	if !fa.Daybreak() {
		t.Fatal("Daybreak dropped silently")
	}
	if e := drainChan(changed, b, t); e.Daybreak == nil || !*e.Daybreak {
		t.Fatalf("config_change = %+v", e)
	}
}

// Another provider is not refused; Daybreak stays asked for but does not apply.
func TestHandler_SwitchModel_OtherProviderUnderDaybreak(t *testing.T) {
	sol, _ := core.ResolveModel("gpt-6-sol")
	fa := &fakeAgent{model: sol, thinkingLevel: "medium", daybreak: true}
	_, changed, b := daybreakSessionContext(t, fa, knownCatalog)
	if err := b.Execute(SwitchModel{ModelSpec: "opus"}); err != nil {
		t.Fatal(err)
	}
	e := drainChan(changed, b, t)
	if e.Daybreak == nil || !*e.Daybreak || e.DaybreakSupported == nil || *e.DaybreakSupported {
		t.Fatalf("config_change = %+v, want daybreak on but unsupported", e)
	}
}

func TestCollectMetadata_Daybreak(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{model: core.Model{ID: "gpt-6-sol", Provider: "openai"}, daybreak: true}
	meta := collectMetadata(newTestSessionContext(b, fa))
	if on, _ := meta[session.MetaDaybreak].(bool); !on {
		t.Fatalf("daybreak metadata = %#v, want true", meta[session.MetaDaybreak])
	}
	s := &session.Session{Metadata: meta}
	if !s.DaybreakMeta() {
		t.Fatal("DaybreakMeta() did not read it back")
	}
}
