package session

import "testing"

func TestRename_MarksManual(t *testing.T) {
	s := &Session{}
	s.SetAutoTitle("auto title", 80)
	if s.TitleIsManual() {
		t.Fatal("SetAutoTitle should not mark manual")
	}
	if s.Title != "auto title" {
		t.Fatalf("title = %q", s.Title)
	}

	s.Rename("manual title", 80)
	if !s.TitleIsManual() {
		t.Fatal("Rename should mark manual")
	}
	if s.Title != "manual title" {
		t.Fatalf("title = %q", s.Title)
	}
}

func TestSetAutoTitle_NeverOverwritesManual(t *testing.T) {
	s := &Session{}
	s.Rename("kept", 80)
	s.SetAutoTitle("ignored", 80)
	if s.Title != "kept" {
		t.Fatalf("manual title overwritten: %q", s.Title)
	}
}

func TestSetAutoTitle_EmptyIsNoop(t *testing.T) {
	s := &Session{Title: "existing"}
	s.SetAutoTitle("   ", 80)
	if s.Title != "existing" {
		t.Fatalf("empty auto title changed title to %q", s.Title)
	}
}

func TestRename_Truncates(t *testing.T) {
	s := &Session{}
	s.Rename("abcdefghij", 5)
	if s.Title != "abcde…" {
		t.Fatalf("truncated title = %q", s.Title)
	}
}

func TestTitleIsManual_LegacyDefaultsAuto(t *testing.T) {
	s := &Session{Title: "old session", TitleSource: ""}
	if s.TitleIsManual() {
		t.Fatal("legacy empty source should be treated as auto")
	}
}
