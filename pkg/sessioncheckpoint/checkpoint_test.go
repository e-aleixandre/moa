package sessioncheckpoint

import "testing"

func TestSlotWriteLimitAndCAS(t *testing.T) {
	s := New()
	if err := s.Write("handoff"); err != nil {
		t.Fatal(err)
	}
	_, gen := s.Read()
	if err := s.Write("new handoff"); err != nil {
		t.Fatal(err)
	}
	if s.ClearIfGeneration(gen) {
		t.Fatal("stale generation cleared newer write")
	}
	text, _ := s.Read()
	if text != "new handoff" {
		t.Fatalf("text = %q", text)
	}
	if err := s.Write(string(make([]byte, MaxBytes+1))); err == nil {
		t.Fatal("accepted oversized checkpoint")
	}
}

func TestSlotMetadata(t *testing.T) {
	s := New()
	_ = s.Write("keep this")
	r := New()
	r.Restore(s.SaveToMetadata())
	got, _ := r.Read()
	if got != "keep this" {
		t.Fatalf("restored %q", got)
	}
}
