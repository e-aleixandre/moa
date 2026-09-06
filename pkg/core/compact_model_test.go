package core

import (
	"strings"
	"testing"
)

func TestGetCompactModel(t *testing.T) {
	tests := []struct {
		name string
		cfg  MoaConfig
		want string
	}{
		{"unset means the session's model", MoaConfig{}, ""},
		{"explicit session keyword", MoaConfig{CompactModel: "session"}, ""},
		{"keyword is case-insensitive", MoaConfig{CompactModel: "Session"}, ""},
		{"whitespace only", MoaConfig{CompactModel: "   "}, ""},
		{"a spec is returned trimmed", MoaConfig{CompactModel: "  terra "}, "terra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetCompactModel(tt.cfg); got != tt.want {
				t.Errorf("GetCompactModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Compaction must never be the reason a session stops working: every path here
// returns a usable model, and an unusable choice is reported rather than
// enforced.
func TestResolveCompactModel(t *testing.T) {
	session, ok := ResolveModel("opus")
	if !ok {
		t.Fatal("opus should resolve")
	}
	all := func(string) bool { return true }
	none := func(string) bool { return false }

	t.Run("no configured model uses the session's own", func(t *testing.T) {
		got, reason := ResolveCompactModel("", session, all)
		if got.ID != session.ID || reason != CompactModelFallbackNone {
			t.Errorf("got %q/%q, want the session model and no fallback", got.ID, reason)
		}
	})

	t.Run("a configured model with credentials wins", func(t *testing.T) {
		got, reason := ResolveCompactModel("terra", session, all)
		if got.ID == session.ID {
			t.Error("configured summarizer was ignored")
		}
		if reason != CompactModelFallbackNone {
			t.Errorf("unexpected fallback %q", reason)
		}
	})

	t.Run("an unknown spec falls back and says so", func(t *testing.T) {
		got, reason := ResolveCompactModel("not-a-model-xyz", session, all)
		if got.ID != session.ID {
			t.Errorf("fallback should use the session model, got %q", got.ID)
		}
		if reason != CompactModelFallbackUnknown {
			t.Errorf("reason = %q, want unknown_model", reason)
		}
	})

	t.Run("a model whose provider has no credential falls back", func(t *testing.T) {
		got, reason := ResolveCompactModel("terra", session, none)
		if got.ID != session.ID {
			t.Errorf("fallback should use the session model, got %q", got.ID)
		}
		if reason != CompactModelFallbackNoCredential {
			t.Errorf("reason = %q, want no_credential", reason)
		}
	})

	// The session is already talking to this provider, so its credential is
	// proven by the conversation itself: a stale availability probe must not
	// push compaction onto a different model.
	t.Run("same provider as the session needs no extra probe", func(t *testing.T) {
		got, reason := ResolveCompactModel("haiku", session, none)
		if got.ID == session.ID {
			t.Error("a same-provider summarizer should be honoured")
		}
		if reason != CompactModelFallbackNone {
			t.Errorf("unexpected fallback %q", reason)
		}
	})
}

func TestCompactModelFallbackNotice(t *testing.T) {
	used, _ := ResolveModel("opus")

	// The ordinary compaction is silent by design: the compaction card is the
	// whole story, whether the summary came from the session's model or from
	// the configured one. Only an unhonoured choice earns a line.
	if got := CompactModelFallbackNotice(CompactModelFallbackNone, "terra", used); got != "" {
		t.Errorf("no fallback should render nothing, got %q", got)
	}
	for _, reason := range []CompactModelFallback{CompactModelFallbackUnknown, CompactModelFallbackNoCredential} {
		got := CompactModelFallbackNotice(reason, "terra", used)
		if got == "" {
			t.Fatalf("%q rendered no notice", reason)
		}
		if !strings.Contains(got, "terra") || !strings.Contains(got, used.Name) {
			t.Errorf("notice should name the rejected spec and the model used: %q", got)
		}
		// It leads with what happened, not with the failure: the reader's first
		// question is which model wrote the summary they are about to trust.
		if !strings.HasPrefix(got, "✂ Summarized with "+used.Name) {
			t.Errorf("notice should lead with the model that summarized: %q", got)
		}
	}

	// A model missing from the catalog must still name something rather than
	// render "Summarized with  —".
	bare := CompactModelFallbackNotice(CompactModelFallbackNoCredential, "terra", Model{ID: "some-id"})
	if !strings.Contains(bare, "some-id") {
		t.Errorf("a nameless model should fall back to its id: %q", bare)
	}
}
