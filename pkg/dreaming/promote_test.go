package dreaming

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A decision that only lives in a sheet gets its own file, with the cited
// lines copied byte for byte from the snapshot, applied with its unit.
func TestPromotionCopiesTheDecisionVerbatim(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "work/a.md")
	u := Unit{Sheet: "work/a.md", Files: []string{"work/a.md", "work/a/parte-01.md"}}
	plan := Plan{Promote: []Promotion{{Path: "decisions/a4.md", Title: "A4", Source: "work/a/parte-01.md:2-3"}}}
	if err := n.r.addPromotions(n.snap, u, plan, "2026-11-12", &n.groups[0], n.content); err != nil {
		t.Fatal(err)
	}
	if rep := n.apply(t); len(rep.Applied) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	got, err := os.ReadFile(filepath.Join(s.bookDir(), "decisions/a4.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The part ends without a newline and uses CRLF: the quote keeps both.
	if !strings.HasSuffix(string(got), "\n\nA4 viejo\r\nA5 vigente\n") || !strings.HasPrefix(string(got), "# A4\n") {
		t.Fatalf("promoted file:\n%q", got)
	}
}

func TestPromotionRules(t *testing.T) {
	u := Unit{Sheet: "work/a.md", Files: []string{"work/a.md"}}
	orig := [][]byte{[]byte("# A\nEl dueño decide X (10/11).\n")}
	exists := func(rel string) bool { return rel == "decisions/precios.md" }
	ok := Promotion{Path: "decisions/x.md", Title: "X", Source: "work/a.md:2-2"}
	if _, err := promotionFile(u, orig, ok, "2026-11-12", "abcdef1", exists); err != nil {
		t.Fatal(err)
	}
	bad := map[string]Promotion{
		"existing file":       {Path: "decisions/precios.md", Title: "X", Source: "work/a.md:2-2"},
		"outside decisions":   {Path: "work/x.md", Title: "X", Source: "work/a.md:2-2"},
		"nested or traversal": {Path: "decisions/../OWNER.md", Title: "X", Source: "work/a.md:2-2"},
		"another unit's file": {Path: "decisions/x.md", Title: "X", Source: "work/b.md:2-2"},
		"range past the end":  {Path: "decisions/x.md", Title: "X", Source: "work/a.md:2-3"},
		"multi-line title":    {Path: "decisions/x.md", Title: "X\nY", Source: "work/a.md:2-2"},
		"no range":            {Path: "decisions/x.md", Title: "X", Source: "work/a.md:2"},
	}
	for name, p := range bad {
		if _, err := promotionFile(u, orig, p, "2026-11-12", "abcdef1", exists); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
