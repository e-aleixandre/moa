package memory

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeFact(t *testing.T, s *Store, m Memory) {
	t.Helper()
	m.Lifecycle = LifecycleDurable
	if _, err := s.Write(m); err != nil {
		t.Fatal(err)
	}
}

func TestSearchMatchesAllFieldsInBothScopes(t *testing.T) {
	s := newStore(t)
	writeFact(t, s, Memory{Name: "docker-setup", Description: "d", Scope: ScopeProject, Body: "b"})
	writeFact(t, s, Memory{Name: "ci", Description: "the docker pipeline", Scope: ScopeProject, Body: "b"})
	writeFact(t, s, Memory{Name: "editor", Description: "d", Scope: ScopeGlobal, Body: "runs inside DOCKER"})
	writeFact(t, s, Memory{Name: "unrelated", Description: "d", Scope: ScopeProject, Body: "b"})

	res, err := s.Search(SearchOptions{Query: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 {
		t.Fatalf("Total = %d, want 3", res.Total)
	}
	var got []string
	for _, h := range res.Hits {
		got = append(got, h.Memory.ID()+":"+h.Field)
	}
	// Name hits first, then description, then body; alphabetical within each.
	want := []string{"project/docker-setup:name", "project/ci:description", "global/editor:body"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order: got %v want %v", got, want)
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	s := newStore(t)
	if _, err := s.Search(SearchOptions{Query: "  "}); err == nil {
		t.Error("empty query should error")
	}
}

func TestSearchRegex(t *testing.T) {
	s := newStore(t)
	writeFact(t, s, Memory{Name: "port-3306", Description: "d", Scope: ScopeProject, Body: "b"})
	writeFact(t, s, Memory{Name: "port-abc", Description: "d", Scope: ScopeProject, Body: "b"})

	res, err := s.Search(SearchOptions{Query: `port-\d+`, Regex: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Hits[0].Memory.Name != "port-3306" {
		t.Errorf("regex search returned %+v", res.Hits)
	}
	if _, err := s.Search(SearchOptions{Query: "port-(", Regex: true}); err == nil {
		t.Error("an uncompilable pattern must be reported, not silently empty")
	}
	// Without regex the same pattern is a literal and matches nothing.
	res, err = s.Search(SearchOptions{Query: `port-\d+`})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 {
		t.Errorf("substring mode should not interpret the pattern, got %d hits", res.Total)
	}
}

func TestSearchPaging(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 12; i++ {
		writeFact(t, s, Memory{Name: fmt.Sprintf("hit-%02d", i), Description: "d", Scope: ScopeProject, Body: "b"})
	}
	res, err := s.Search(SearchOptions{Query: "hit-"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 12 || len(res.Hits) != DefaultSearchLimit {
		t.Fatalf("default page: total=%d hits=%d", res.Total, len(res.Hits))
	}
	rest, err := s.Search(SearchOptions{Query: "hit-", Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest.Hits) != 2 || rest.Hits[0].Memory.Name != "hit-10" {
		t.Errorf("offset page: %+v", rest.Hits)
	}
	// An over-large limit is clamped, an out-of-range offset is empty.
	big, err := s.Search(SearchOptions{Query: "hit-", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if big.Limit != MaxSearchLimit {
		t.Errorf("limit not clamped: %d", big.Limit)
	}
	past, err := s.Search(SearchOptions{Query: "hit-", Offset: 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(past.Hits) != 0 {
		t.Errorf("offset past the end should be empty, got %d", len(past.Hits))
	}
}

func TestSearchSnippetIsBoundedAndValidUTF8(t *testing.T) {
	s := newStore(t)
	// A long multibyte body with the match in the middle: the window must be
	// cut on rune boundaries at both ends.
	filler := strings.Repeat("ñ", 500)
	writeFact(t, s, Memory{
		Name: "unicode", Description: "d", Scope: ScopeProject,
		Body: filler + " AGUJA " + filler,
	})
	res, err := s.Search(SearchOptions{Query: "aguja"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("expected one hit, got %d", len(res.Hits))
	}
	sn := res.Hits[0].Snippet
	if len(sn) > SnippetBytes {
		t.Errorf("snippet is %d bytes, over the %d limit", len(sn), SnippetBytes)
	}
	if !utf8.ValidString(sn) {
		t.Errorf("snippet is not valid UTF-8: %q", sn)
	}
	if !strings.Contains(sn, "AGUJA") {
		t.Errorf("snippet lost the match: %q", sn)
	}
	if strings.Contains(sn, filler) {
		t.Error("snippet returned the whole body")
	}
}

func TestSearchSnippetOfShortBodyIsWhole(t *testing.T) {
	s := newStore(t)
	writeFact(t, s, Memory{Name: "short", Description: "d", Scope: ScopeProject, Body: "just\nthese words"})
	res, err := s.Search(SearchOptions{Query: "these"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Hits[0].Snippet != "just these words" {
		t.Errorf("snippet = %q", res.Hits[0].Snippet)
	}
}

func TestLiteralMatcherMapsFoldedMatchEndToOriginal(t *testing.T) {
	find, err := matcherFor(SearchOptions{Query: "kkkk"})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("prefix ", 30) + "KKKK" + strings.Repeat(" suffix", 30)
	start, end, ok := find(text)
	if !ok {
		t.Fatal("expected Kelvin-sign region to match")
	}
	wantStart := strings.Index(text, "KKKK")
	wantEnd := wantStart + len("KKKK")
	if start != wantStart || end != wantEnd {
		t.Errorf("match = [%d,%d), want [%d,%d)", start, end, wantStart, wantEnd)
	}
	if text[start:end] != "KKKK" {
		t.Errorf("matched original text = %q, want Kelvin-sign region", text[start:end])
	}
}
