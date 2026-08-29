package core

import "testing"

// The cascade the owner asked for: a session's own threshold wins, otherwise
// the global default, otherwise the model window. All three steps go through
// EffectiveWindow, so this is where the whole rule is pinned down.
func TestEffectiveWindowCascade(t *testing.T) {
	base := DefaultCompactionSettings
	const window = 200_000

	t.Run("session threshold wins over global", func(t *testing.T) {
		s := base
		s.CompactAt = 120_000
		s.DefaultCompactAt = 60_000
		if got := s.EffectiveWindow(window); got != 120_000 {
			t.Fatalf("session threshold should win: EffectiveWindow = %d, want 120000", got)
		}
	})

	t.Run("global applies when the session has none", func(t *testing.T) {
		s := base
		s.DefaultCompactAt = 60_000
		if got := s.EffectiveWindow(window); got != 60_000 {
			t.Fatalf("global default should apply: EffectiveWindow = %d, want 60000", got)
		}
	})

	t.Run("neither set falls back to the model window", func(t *testing.T) {
		if got := base.EffectiveWindow(window); got != window {
			t.Fatalf("no threshold should use the model window: EffectiveWindow = %d, want %d", got, window)
		}
	})

	t.Run("global below the floor is raised, not honored", func(t *testing.T) {
		s := base
		s.DefaultCompactAt = 1000
		if got := s.EffectiveWindow(window); got != s.MinCompactAt() {
			t.Fatalf("global under the floor should raise to %d, got %d", s.MinCompactAt(), got)
		}
	})

	t.Run("global above the window degrades to the window", func(t *testing.T) {
		s := base
		s.DefaultCompactAt = window + 1
		if got := s.EffectiveWindow(window); got != window {
			t.Fatalf("global over the window should clamp to %d, got %d", window, got)
		}
	})
}

func TestCompactionWithDefault(t *testing.T) {
	settings := CompactionWithDefault(90_000)
	if settings.DefaultCompactAt != 90_000 {
		t.Fatalf("DefaultCompactAt = %d, want 90000", settings.DefaultCompactAt)
	}
	if settings.CompactAt != 0 {
		t.Fatalf("CompactAt = %d, want 0: the global value is a default, not the session's own choice", settings.CompactAt)
	}
	if settings.ReserveTokens != DefaultCompactionSettings.ReserveTokens || settings.KeepRecent != DefaultCompactionSettings.KeepRecent {
		t.Fatalf("reserve/keep must stay at the package defaults, got %+v", *settings)
	}
	// Must not alias the package-level defaults, or one session's threshold
	// would leak into every agent built afterwards.
	if DefaultCompactionSettings.DefaultCompactAt != 0 {
		t.Fatalf("CompactionWithDefault mutated DefaultCompactionSettings: %+v", DefaultCompactionSettings)
	}
}

func TestGetCompactAt(t *testing.T) {
	if got := GetCompactAt(MoaConfig{}); got != 0 {
		t.Fatalf("unset compact_at = %d, want 0 (automatic)", got)
	}
	if got := GetCompactAt(MoaConfig{CompactAt: 80_000}); got != 80_000 {
		t.Fatalf("compact_at = %d, want 80000", got)
	}
	// A hand-edited negative reads as unset rather than travelling into the
	// engine as a threshold it would have to defend against.
	if got := GetCompactAt(MoaConfig{CompactAt: -5}); got != 0 {
		t.Fatalf("negative compact_at = %d, want 0", got)
	}
}
