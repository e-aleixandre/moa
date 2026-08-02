package moadocs

import (
	"strings"
	"testing"
)

// The whole point of embedding is that a user who installed the binary has no
// repository to read from: if the embed ever stops matching docs/, the agent
// silently goes back to answering about moa from memory.
func TestPages_AreEmbeddedInTheBinary(t *testing.T) {
	pages := Pages()
	if len(pages) < 10 {
		t.Fatalf("expected the documentation set to be embedded, got %d pages", len(pages))
	}

	want := map[string]bool{
		"cli": false, "configuration": false, "tools": false,
		"automation": false, "serve": false, "recipes/linear": false,
	}
	for _, p := range pages {
		if _, ok := want[p.Name]; ok {
			want[p.Name] = true
		}
		if p.Title == "" {
			t.Errorf("page %q has no title, so the index would read badly", p.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("page %q is missing from the embedded docs", name)
		}
	}
}

func TestRead_ReturnsTheRealPage(t *testing.T) {
	content, ok := Read("cli")
	if !ok {
		t.Fatal("cli page should be readable")
	}
	if !strings.Contains(content, "# CLI Reference") {
		t.Errorf("content does not look like the CLI page: %.60q", content)
	}
}

// The model writes the page name from its own head, so near misses are
// expected; each one it gets wrong costs a wasted round trip.
func TestRead_AcceptsTheNamesAModelWouldWrite(t *testing.T) {
	for _, name := range []string{
		"cli", "cli.md", "CLI", " cli ", "docs/cli", "docs/cli.md", "/cli",
	} {
		if _, ok := Read(name); !ok {
			t.Errorf("Read(%q) should resolve to the cli page", name)
		}
	}
	if _, ok := Read("recipes/linear"); !ok {
		t.Error("nested recipe pages should resolve")
	}
}

// The page name reaches this from model output, so it is untrusted input: it
// must never be usable to read files outside the embedded docs.
func TestRead_RejectsAnythingOutsideTheDocs(t *testing.T) {
	for _, name := range []string{
		"", "   ", "nope", "../go.mod", "../../etc/passwd",
		"docs/../go.mod", "/etc/passwd", "recipes", "assets/moa.png",
	} {
		if content, ok := Read(name); ok {
			t.Errorf("Read(%q) should have failed, returned %d bytes", name, len(content))
		}
	}
}

// The description is the only thing this feature puts in the system prompt, so
// a regression here is a silent, permanent context cost in every session.
func TestDescription_StaysCheapAndListsThePages(t *testing.T) {
	d := Description()
	if len(d) > 600 {
		t.Errorf("description grew to %d chars; it is paid on every request", len(d))
	}
	for _, name := range []string{"cli", "automation", "configuration"} {
		if !strings.Contains(d, name) {
			t.Errorf("description should name the %q page so the model knows to look it up", name)
		}
	}
	if strings.Contains(d, "\n") {
		t.Error("description must stay a single prompt line")
	}
}

// Ordering is what makes the listing read as a table of contents; alphabetical
// order would open with "architecture", the page a user asks about last.
func TestPages_LeadWithWhatAUserAsksFirst(t *testing.T) {
	pages := Pages()
	if pages[0].Name != "overview" || pages[1].Name != "quickstart" {
		t.Errorf("expected overview and quickstart first, got %q, %q", pages[0].Name, pages[1].Name)
	}
	var last string
	for _, p := range pages {
		last = p.Name
	}
	if !strings.HasPrefix(last, "recipes/") {
		t.Errorf("unranked pages should sort last, got %q", last)
	}
}
