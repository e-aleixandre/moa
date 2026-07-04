package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func sampleTranscript(jobID string) SubagentTranscript {
	return SubagentTranscript{
		JobID:   jobID,
		Task:    "do a thing",
		Model:   "haiku",
		Status:  "completed",
		Async:   true,
		CostUSD: 0.0042,
		Usage:   &core.Usage{Input: 100, Output: 40},
		Messages: []core.AgentMessage{
			{Message: core.Message{Role: "assistant", Content: []core.Content{{Type: "text", Text: "hi"}}}},
		},
	}
}

func TestSubagentStore_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewSubagentStore(dir, "sess1")

	in := sampleTranscript("sa-abc")
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load("sa-abc")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.JobID != in.JobID || got.Task != in.Task || got.Model != in.Model {
		t.Fatalf("metadata mismatch: %+v", got)
	}
	if got.Status != "completed" || !got.Async || got.CostUSD != 0.0042 {
		t.Fatalf("fields mismatch: %+v", got)
	}
	if got.Usage == nil || got.Usage.Input != 100 || got.Usage.Output != 40 {
		t.Fatalf("usage mismatch: %+v", got.Usage)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content[0].Text != "hi" {
		t.Fatalf("messages mismatch: %+v", got.Messages)
	}
}

func TestSubagentStore_LoadMissing(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")
	if _, err := s.Load("nope"); err == nil {
		t.Fatal("expected error for missing transcript")
	}
}

func TestSubagentStore_List(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")

	// Empty (no directory yet) → nil, no error.
	if got, err := s.List(); err != nil || got != nil {
		t.Fatalf("List on empty: got %v, err %v", got, err)
	}

	_ = s.Save(sampleTranscript("sa-1"))
	_ = s.Save(sampleTranscript("sa-2"))
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 transcripts, got %d", len(got))
	}
}

func TestSubagentStore_Remove(t *testing.T) {
	dir := t.TempDir()
	s := NewSubagentStore(dir, "sess1")
	_ = s.Save(sampleTranscript("sa-1"))

	if err := s.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Directory gone → List returns nil.
	if got, _ := s.List(); got != nil {
		t.Fatalf("expected nil after Remove, got %v", got)
	}
	// Remove again is a no-op.
	if err := s.Remove(); err != nil {
		t.Fatalf("Remove (idempotent): %v", err)
	}
}

func TestSubagentStore_DirLayout(t *testing.T) {
	s := NewSubagentStore("/base/proj", "sessABC")
	want := filepath.Join("/base/proj", "sessABC.subagents")
	if s.Dir() != want {
		t.Fatalf("Dir() = %q, want %q", s.Dir(), want)
	}
}

func TestSubagentStore_RejectsUnsafeJobID(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")
	bad := []string{"", "../evil", "a/b", `a\b`, "..", "foo/../bar"}
	for _, id := range bad {
		if err := s.Save(sampleTranscript(id)); err == nil {
			t.Errorf("Save(%q) should have failed", id)
		}
		if _, err := s.Load(id); err == nil {
			t.Errorf("Load(%q) should have failed", id)
		}
	}
}

func TestSubagentStore_ListSortedNewestFirst(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")
	older := sampleTranscript("sa-old")
	older.FinishedAt = time.Unix(1000, 0)
	newer := sampleTranscript("sa-new")
	newer.FinishedAt = time.Unix(2000, 0)
	_ = s.Save(older)
	_ = s.Save(newer)
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].JobID != "sa-new" {
		t.Fatalf("expected newest-first, got %+v", list)
	}
}
