// Package moadocs serves moa's own documentation to the agent.
//
// A user who installed the binary has no copy of the moa repository, so
// without this the agent can only answer questions about moa from whatever it
// remembers — which is how confident, wrong answers about flags and config
// files get produced. The docs travel inside the binary, so they always
// describe the exact version being run.
package moadocs

import (
	"io/fs"
	"path"
	"sort"
	"strings"

	moa "github.com/e-aleixandre/moa"
)

// Page is one documentation page, addressed by the name the agent uses.
type Page struct {
	Name  string // "cli", "recipes/linear"
	Title string // first markdown heading
	file  string // path inside the embedded FS
}

// pageOrder ranks pages so the tool description reads as a table of contents
// rather than an alphabetical dump: what moa is, then how to drive it, then
// how to integrate it. Anything unlisted keeps alphabetical order at the end,
// so a new page shows up without touching this list.
var pageOrder = []string{
	"overview",
	"quickstart",
	"cli",
	"tools",
	"configuration",
	"serve",
	"automation",
	"architecture",
	"pulse",
	"releases",
}

// Pages returns the embedded documentation, ordered for presentation.
func Pages() []Page {
	var pages []Page
	_ = fs.WalkDir(moa.Docs, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil //nolint:nilerr // a broken entry just means no page
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, "docs/"), ".md")
		pages = append(pages, Page{Name: name, Title: title(p), file: p})
		return nil
	})

	rank := make(map[string]int, len(pageOrder))
	for i, n := range pageOrder {
		rank[n] = i
	}
	sort.Slice(pages, func(i, j int) bool {
		ri, oki := rank[pages[i].Name]
		rj, okj := rank[pages[j].Name]
		if oki != okj {
			return oki // ranked pages come first
		}
		if oki && okj {
			return ri < rj
		}
		return pages[i].Name < pages[j].Name
	})
	return pages
}

// Read returns the markdown of a page. The name is matched as the agent would
// write it, with a few forgiving variants ("cli.md", "docs/cli") so a near
// miss costs a retry instead of a failed answer.
func Read(name string) (string, bool) {
	want := normalize(name)
	if want == "" {
		return "", false
	}
	for _, p := range Pages() {
		if normalize(p.Name) == want {
			b, err := fs.ReadFile(moa.Docs, p.file)
			if err != nil {
				return "", false
			}
			return string(b), true
		}
	}
	return "", false
}

func normalize(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(path.Clean("/"+strings.Trim(n, "/")), "/")
	n = strings.TrimPrefix(n, "docs/")
	return strings.TrimSuffix(n, ".md")
}

// title reads the first markdown heading, falling back to the file name.
func title(file string) string {
	b, err := fs.ReadFile(moa.Docs, file)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if s, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
