package core

import (
	"slices"
	"testing"
)

func TestDaybreakSpec(t *testing.T) {
	for _, spec := range []string{"daybreak", "Daybreak", " daybreak ", "openai/daybreak", LegacyDaybreakModelID, "openai/" + LegacyDaybreakModelID} {
		if !DaybreakSpec(spec) {
			t.Errorf("DaybreakSpec(%q) = false, want true", spec)
		}
	}
	for _, spec := range []string{"", "sol", "gpt-6-sol", "openai/gpt-6-sol", "anthropic/daybreak", "daybreak-x"} {
		if DaybreakSpec(spec) {
			t.Errorf("DaybreakSpec(%q) = true, want false", spec)
		}
	}
}

// "daybreak" now means GPT-6 Sol with the Daybreak program; the retired alias
// asks for the same thing.
func TestResolveModelDaybreak(t *testing.T) {
	for _, spec := range []string{"daybreak", LegacyDaybreakModelID, "openai/daybreak"} {
		m, daybreak, ok := ResolveModelDaybreak(spec)
		if !ok || !daybreak || m.ID != DaybreakModelID || m.Provider != "openai" {
			t.Errorf("ResolveModelDaybreak(%q) = (%s/%s, %v, %v), want gpt-6-sol with Daybreak", spec, m.Provider, m.ID, daybreak, ok)
		}
	}
	m, daybreak, ok := ResolveModelDaybreak("sol")
	if !ok || daybreak || m.ID != "gpt-6.1-sol" {
		t.Errorf("ResolveModelDaybreak(sol) = (%s, %v, %v)", m.ID, daybreak, ok)
	}
	if m, ok := ResolveModel("daybreak"); !ok || m.ID != DaybreakModelID {
		t.Errorf("ResolveModel(daybreak) = %q, want %q", m.ID, DaybreakModelID)
	}
	// The retired alias keeps its own registry entry: history is priced and
	// identified by it.
	if m, ok := ResolveModel(LegacyDaybreakModelID); !ok || m.ID != LegacyDaybreakModelID {
		t.Errorf("ResolveModel(legacy) = %q, %v", m.ID, ok)
	}
}

// A migrated request is billed as gpt-6-sol, not as the alias.
func TestDaybreakBillsAsGPT6Sol(t *testing.T) {
	db, _, _ := ResolveModelDaybreak(LegacyDaybreakModelID)
	sol := knownModels[DaybreakModelID]
	if db.Pricing != sol.Pricing {
		t.Fatalf("daybreak pricing %+v, want gpt-6-sol's %+v", db.Pricing, sol.Pricing)
	}
}

func TestLegacyDaybreakLeavesTheSelector(t *testing.T) {
	for _, e := range ListModels() {
		if e.Model.ID == LegacyDaybreakModelID {
			t.Fatal("ListModels still offers the retired Daybreak alias")
		}
		if e.Model.ID == DaybreakModelID && e.Alias == "daybreak" {
			t.Fatal("gpt-6-sol is labelled daybreak in the selector")
		}
	}
	if !slices.Contains(ModelAliases(), "daybreak") {
		t.Errorf("ModelAliases() = %v, want daybreak offered as a spec", ModelAliases())
	}
}

func TestDaybreakAllowlist(t *testing.T) {
	sol, _, _ := ResolveModelDaybreak("daybreak")
	for _, list := range [][]string{{"daybreak"}, {LegacyDaybreakModelID}, {DaybreakModelID}} {
		if !IsModelAllowedFor(sol, true, list) {
			t.Errorf("allowlist %v rejects gpt-6-sol with Daybreak", list)
		}
		if !slices.Contains(AllowedModelAliases(list), "daybreak") {
			t.Errorf("AllowedModelAliases(%v) = %v, want daybreak", list, AllowedModelAliases(list))
		}
	}
	// The Daybreak entries authorize the mode, not plain Sol.
	if IsModelAllowedFor(sol, false, []string{LegacyDaybreakModelID}) {
		t.Error("legacy alias entry authorized standard gpt-6-sol")
	}
	other, _ := ResolveModel("sol")
	if IsModelAllowedFor(other, true, []string{"daybreak"}) {
		t.Error("daybreak entry authorized another model")
	}
	if !IsModelAllowedFor(other, false, nil) {
		t.Error("empty allowlist must stay unrestricted")
	}
}

func TestDaybreakAvailability(t *testing.T) {
	sol, _ := ResolveModel("gpt-6-sol")
	sol61, _ := ResolveModel("gpt-6.1-sol")
	opus, _ := ResolveModel("opus")
	known := DaybreakAvailability{Transport: true, Known: true, Models: map[string]bool{"gpt-6-sol": true, "gpt-6.1-sol": false}}
	if !known.Sendable(sol) || known.Sendable(sol61) || known.Sendable(opus) {
		t.Errorf("known catalog Sendable wrong")
	}
	if known.Rejects(sol) || !known.Rejects(sol61) || known.Rejects(opus) {
		t.Errorf("known catalog Rejects wrong: only an OpenAI model outside the catalog is rejected")
	}
	if !known.Any() {
		t.Error("known catalog with gpt-6-sol: Any() = false")
	}
	// Never loaded: nothing is rejected and the provider gets to answer.
	unknown := DaybreakAvailability{Transport: true}
	if !unknown.Sendable(sol61) || unknown.Rejects(sol61) || unknown.Any() {
		t.Errorf("unknown catalog must send, never reject, and not offer the switch")
	}
	// An API key cannot carry the program at all.
	apiKey := DaybreakAvailability{Known: true, Models: map[string]bool{"gpt-6-sol": true}}
	if apiKey.Sendable(sol) || apiKey.Any() {
		t.Errorf("without the plan transport Daybreak is neither sendable nor offered")
	}
	if !(DaybreakAvailability{Transport: true, Known: true, Models: map[string]bool{"gpt-6-sol": false}}).Known || (DaybreakAvailability{Transport: true, Known: true, Models: map[string]bool{"gpt-6-sol": false}}).Any() {
		t.Error("a catalog without any Daybreak model must not offer the switch")
	}
}

func TestDaybreakPromptCacheKey(t *testing.T) {
	base := PromptCacheKey("s1")
	db := DaybreakPromptCacheKey(base, DaybreakAccessProgram)
	if db == base || db == "" {
		t.Fatalf("Daybreak key %q must differ from standard %q", db, base)
	}
	if again := DaybreakPromptCacheKey(base, DaybreakAccessProgram); again != db {
		t.Fatalf("Daybreak key not stable: %q vs %q", again, db)
	}
	if got := DaybreakPromptCacheKey(base, ""); got != base {
		t.Fatalf("standard key changed: %q", got)
	}
	if got := DaybreakPromptCacheKey("", DaybreakAccessProgram); got != "" {
		t.Fatalf("empty key must stay empty, got %q", got)
	}
	sub := SubagentPromptCacheKey(base)
	if DaybreakPromptCacheKey(sub, DaybreakAccessProgram) == sub {
		t.Fatal("subagent Daybreak key equals its standard key")
	}
}

// Sol standard and Daybreak are not one identity, and the retired alias's
// history does not become gpt-6-sol's.
func TestDaybreakIdentityStaysSeparate(t *testing.T) {
	if SameResponseOrigin(DaybreakModelID, "gpt-5.6-sol") || SameResponseOrigin(DaybreakModelID, LegacyDaybreakModelID) {
		t.Fatal("gpt-6-sol adopted the retired alias's state")
	}
	if !SameResponseOrigin(LegacyDaybreakModelID, "gpt-5.6-sol") {
		t.Fatal("the alias relation itself must stay intact")
	}
}

// Turning Daybreak on or off is a configuration change, not an expiry.
func TestCacheMiss_AccessProgramChange(t *testing.T) {
	a := missTurn("openai", "gpt-6-sol", 1000, 100_000, 0, 0, 0, nil)
	b := missTurn("openai", "gpt-6-sol", 1005, 100_500, 0, 0, 0, nil)
	b.AccessProgram = DaybreakAccessProgram
	got := SummarizeCacheUsage([]AgentMessage{a, b})
	if got.LastMiss == nil || got.LastMiss.Cause != CacheMissModelChanged {
		t.Fatalf("program change classified as %+v, want model_changed", got.LastMiss)
	}
	// The migration itself is a model change too.
	c := missTurn("openai", "gpt-5.6-sol", 1000, 100_000, 0, 0, 0, nil)
	c.RequestedModel = LegacyDaybreakModelID
	got = SummarizeCacheUsage([]AgentMessage{c, b})
	if got.LastMiss == nil || got.LastMiss.Cause != CacheMissModelChanged {
		t.Fatalf("migration classified as %+v, want model_changed", got.LastMiss)
	}
}
