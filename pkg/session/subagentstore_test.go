package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
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
	wantJSON, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	gotJSON, err := os.ReadFile(filepath.Join(s.Dir(), "sa-abc.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("sidecar differs from compact Marshal\n got: %s\nwant: %s", gotJSON, wantJSON)
	}

	got, err := s.Load(in.JobID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, &in) {
		t.Fatalf("loaded sidecar differs from saved sidecar\n got: %#v\nwant: %#v", got, in)
	}
}

func TestSubagentStore_LoadIndentedSidecarCompatibility(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")
	if err := os.MkdirAll(s.Dir(), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const oldIndentedSidecar = `{
  "job_id": "sa-old",
  "task": "saved before compact JSON",
  "model": "haiku",
  "status": "completed",
  "async": true,
  "messages": [
    {
      "role": "assistant",
      "content": [{"type": "text", "text": "done"}]
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(s.Dir(), "sa-old.json"), []byte(oldIndentedSidecar), 0600); err != nil {
		t.Fatalf("WriteFile old indented sidecar: %v", err)
	}

	got, err := s.Load("sa-old")
	if err != nil {
		t.Fatalf("Load old indented sidecar: %v", err)
	}
	want := &SubagentTranscript{
		JobID: "sa-old", Task: "saved before compact JSON", Model: "haiku", Status: "completed", Async: true,
		Messages: []core.AgentMessage{{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded indented sidecar differs\n got: %#v\nwant: %#v", got, want)
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

func TestSubagentStore_ListSummariesMatchesListHeadersAndOrder(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")
	older := sampleTranscript("sa-old")
	older.Title = "Older"
	older.Thinking = "medium"
	older.Result = "finished <success>"
	older.Error = ""
	olderPercent := 42
	older.ContextPercent = &olderPercent
	older.StartedAt = time.Unix(100, 0)
	older.FinishedAt = time.Unix(200, 0)
	newer := sampleTranscript("sa-new")
	newer.Title = "Newer"
	newer.Thinking = "high"
	newer.Result = "finished & verified"
	newer.Error = ""
	newerPercent := 84
	newer.ContextPercent = &newerPercent
	newer.StartedAt = time.Unix(300, 0)
	newer.FinishedAt = time.Unix(400, 0)
	if err := s.Save(older); err != nil {
		t.Fatalf("Save older: %v", err)
	}
	if err := s.Save(newer); err != nil {
		t.Fatalf("Save newer: %v", err)
	}

	full, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	summaries, err := s.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(summaries) != len(full) {
		t.Fatalf("ListSummaries returned %d sidecars, want %d", len(summaries), len(full))
	}
	for i := range full {
		want := full[i]
		want.Messages = nil
		if !reflect.DeepEqual(summaries[i], want) {
			t.Errorf("summary %d = %#v, want %#v", i, summaries[i], want)
		}
	}
}

func TestSubagentStore_ListSummariesStopsBeforeMessages(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")
	if err := os.MkdirAll(s.Dir(), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// The messages value is deliberately incomplete. A successful summary can
	// only mean the decoder stopped at its key instead of loading the tail.
	data := []byte(`{"job_id":"sa-large","task":"header only","model":"haiku","status":"completed","async":true,"cost_usd":0.5,"messages":[`)
	if err := os.WriteFile(filepath.Join(s.Dir(), "sa-large.json"), data, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	summaries, err := s.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].JobID != "sa-large" || summaries[0].Messages != nil {
		t.Fatalf("ListSummaries = %#v, want header without messages", summaries)
	}
	full, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(full) != 0 {
		t.Fatalf("List parsed malformed messages: %#v", full)
	}
}

func TestSubagentStore_ListSummariesSkipsCorruptHeader(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")
	good := sampleTranscript("sa-good")
	good.FinishedAt = time.Unix(100, 0)
	if err := s.Save(good); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "sa-bad.json"), []byte(`{"job_id":`), 0600); err != nil {
		t.Fatalf("WriteFile corrupt sidecar: %v", err)
	}

	summaries, err := s.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].JobID != "sa-good" {
		t.Fatalf("ListSummaries = %#v, want only valid sidecar", summaries)
	}
}

func TestSubagentStore_ListSummariesReusesUnchangedSidecars(t *testing.T) {
	s := NewSubagentStore(t.TempDir(), "sess1")
	in := sampleTranscript("sa-cache")
	in.FinishedAt = time.Unix(100, 0)
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var reads int
	summaryReadHook = func(string) { reads++ }
	t.Cleanup(func() { summaryReadHook = nil })

	first, err := s.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if reads != 1 {
		t.Fatalf("first ListSummaries decoded %d files, want 1", reads)
	}

	second, err := s.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries cache hit: %v", err)
	}
	if reads != 1 {
		t.Fatalf("unchanged ListSummaries decoded %d files, want 1", reads)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached summaries differ\n got: %#v\nwant: %#v", second, first)
	}

	updated := in
	updated.Title = "changed"
	if err := s.Save(updated); err != nil {
		t.Fatalf("Save update: %v", err)
	}
	third, err := s.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries after Save: %v", err)
	}
	if reads != 2 {
		t.Fatalf("ListSummaries after Save decoded %d files, want 2", reads)
	}
	if len(third) != 1 || third[0].Title != "changed" {
		t.Fatalf("ListSummaries after Save = %#v, want updated title", third)
	}
}
