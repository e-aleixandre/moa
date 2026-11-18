package core

import (
	"strings"
	"testing"
)

func only(providers ...string) AuxiliaryModelAvailable {
	return func(p string) bool {
		for _, q := range providers {
			if p == q {
				return true
			}
		}
		return false
	}
}

func TestDreamingDefaultModelsFollowTheConnectedProviders(t *testing.T) {
	cases := []struct {
		name        string
		avail       AuxiliaryModelAvailable
		reader, org string
		ok          bool
	}{
		{"only anthropic", only("anthropic"), "claude-haiku-5-5", "sonnet", true},
		{"only openai", only("openai"), "luna", "sol", true},
		{"both", only("anthropic", "openai"), "luna", "sonnet", true},
		{"none", only(), "", "", false},
		{"no probe keeps the pilot pairing", nil, "luna", "sonnet", true},
		{"unrelated provider", only("xai"), "", "", false},
	}
	for _, c := range cases {
		r, o, ok := DreamingDefaultModels(c.avail)
		if r != c.reader || o != c.org || ok != c.ok {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", c.name, r, o, ok, c.reader, c.org, c.ok)
		}
	}
}

func TestDreamingDefaultsAreRealModelsOfTheirProvider(t *testing.T) {
	for _, avail := range []AuxiliaryModelAvailable{only("anthropic"), only("openai"), only("anthropic", "openai")} {
		r, o, _ := DreamingDefaultModels(avail)
		for _, spec := range []string{r, o} {
			m, ok := ResolveModel(spec)
			if !ok || m.Pricing == nil {
				t.Errorf("%q is not a priced catalog model", spec)
			}
			if !avail(m.Provider) {
				t.Errorf("%q needs %s, which is not connected", spec, m.Provider)
			}
		}
	}
}

func TestResolveDreamingModels(t *testing.T) {
	both := only("anthropic", "openai")

	// Nothing set: the defaults, with nothing to report.
	r, o, notes, ok := ResolveDreamingModels(MoaConfig{}, both)
	if !ok || r.ID != "gpt-6-luna" || o.Provider != "anthropic" || len(notes) != 0 {
		t.Fatalf("defaults = %s / %s, notes %v, ok %v", r.ID, o.ID, notes, ok)
	}

	// A usable choice wins over the default.
	r, o, notes, ok = ResolveDreamingModels(MoaConfig{DreamingReaderModel: "claude-haiku-5-5", DreamingOrganizerModel: "opus"}, both)
	if !ok || r.ID != "claude-haiku-5-5" || !strings.Contains(o.ID, "opus") || len(notes) != 0 {
		t.Fatalf("choice = %s / %s, notes %v", r.ID, o.ID, notes)
	}

	// A model whose provider lost its credential falls back and says so.
	r, _, notes, ok = ResolveDreamingModels(MoaConfig{DreamingReaderModel: "luna"}, only("anthropic"))
	if !ok || r.ID != "claude-haiku-5-5" || len(notes) != 1 || !strings.Contains(notes[0], "Lectura") || !strings.Contains(notes[0], "luna") {
		t.Fatalf("no credential: %s, notes %v", r.ID, notes)
	}

	// An unknown spec (hand-edited config) falls back for that role only.
	r, o, notes, ok = ResolveDreamingModels(MoaConfig{DreamingOrganizerModel: "nope-9"}, only("openai"))
	if !ok || r.ID != "gpt-6-luna" || o.Provider != "openai" || len(notes) != 1 || !strings.Contains(notes[0], "Organización") {
		t.Fatalf("unknown: %s / %s, notes %v", r.ID, o.ID, notes)
	}

	// Nothing connected: no night, however it is configured.
	if _, _, _, ok = ResolveDreamingModels(MoaConfig{DreamingReaderModel: "luna"}, only()); ok {
		t.Fatal("a night was possible with no provider")
	}
}
