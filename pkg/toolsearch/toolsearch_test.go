package toolsearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func specs() []core.ToolSpec {
	return []core.ToolSpec{
		{Name: "mcp__playwright__browser_click", Description: "Perform click on a web page",
			Parameters: json.RawMessage(`{"type":"object","properties":{"ref":{"type":"string","description":"Exact target element reference"}}}`)},
		{Name: "mcp__playwright__browser_navigate", Description: "Navigate to a URL"},
		{Name: "mcp__playwright__browser_take_screenshot", Description: "Take a screenshot of the current page"},
		{Name: "mcp__lovable__deploy_project", Description: "Deploy (publish) a project to production hosting"},
		{Name: "mcp__lovable__list_projects", Description: "Search and list projects in a workspace"},
	}
}

func TestSearchRanksByRelevance(t *testing.T) {
	got := Search(specs(), "take a screenshot of the page", 3)
	if len(got) == 0 || got[0] != "mcp__playwright__browser_take_screenshot" {
		t.Fatalf("got %v", got)
	}
	if got := Search(specs(), "publish my project", 8); len(got) == 0 || got[0] != "mcp__lovable__deploy_project" {
		t.Fatalf("got %v", got)
	}
	// Argument descriptions count.
	if got := Search(specs(), "element reference", 8); len(got) == 0 || got[0] != "mcp__playwright__browser_click" {
		t.Fatalf("got %v", got)
	}
	if got := Search(specs(), "kubernetes", 8); len(got) != 0 {
		t.Fatalf("unrelated query matched %v", got)
	}
}

func TestSearchExactNameFirstAndLimit(t *testing.T) {
	for _, q := range []string{"mcp__lovable__list_projects", "list_projects"} {
		if got := Search(specs(), q, 8); len(got) == 0 || got[0] != "mcp__lovable__list_projects" {
			t.Fatalf("%q: got %v", q, got)
		}
	}
	if got := Search(specs(), "playwright", 2); len(got) != 2 {
		t.Fatalf("limit ignored: %v", got)
	}
	if got := Search(specs(), "playwright", 100); len(got) != 3 {
		t.Fatalf("server query = %v, want its three tools", got)
	}
}

func TestSpecIndexIsStableAndListsServers(t *testing.T) {
	a := Spec(specs())
	rev := specs()
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if b := Spec(rev); a.Description != b.Description || string(a.Parameters) != string(b.Parameters) {
		t.Fatal("index depends on registry order")
	}
	if !strings.Contains(a.Description, "- lovable (2 tools, named mcp__lovable__<tool>): deploy_project, list_projects") ||
		!strings.Contains(a.Description, "- playwright (3 tools") {
		t.Fatalf("index:\n%s", a.Description)
	}
	if !json.Valid(a.Parameters) {
		t.Fatal("parameters are not JSON")
	}
}

func TestResultCarriesReferencesAndText(t *testing.T) {
	r := Result([]string{"a", "b"})
	if r.IsError || len(r.Content) != 3 || r.Content[0].Text != "Loaded tools: a, b" ||
		r.Content[1].Type != "tool_reference" || r.Content[2].ToolName != "b" {
		t.Fatalf("result = %+v", r)
	}
	if r := Result(nil); len(r.Content) != 1 || r.Content[0].Type != "text" {
		t.Fatalf("empty result = %+v", r)
	}
}
