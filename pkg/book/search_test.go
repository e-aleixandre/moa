package book

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func writeSheet(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The sheet that is ABOUT a thing has to beat the files that merely mention it,
// and a question typed without accents has to reach a title written with them:
// that pair is the whole reason search stopped being a substring scan.
func TestSearchRanksTheSheetAboveMentions(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "areas/erp/albaranes.md", `---
title: Importación de albaranes
aliases: albarán, delivery note
---

## Producto

Subir el albarán de un proveedor y convertirlo en pedido.
`)
	for i := 1; i <= 40; i++ {
		writeSheet(t, dir, fmt.Sprintf("areas/otros/ficha-%02d.md", i), fmt.Sprintf(`---
title: Ficha %d
---

Aquí se mencionan los albaranes una vez y nada más.
`, i))
	}
	writeSheet(t, dir, "PROJECT.md", "# Project\n\nalbaranes, pedidos, proveedores\n")

	hits, total, err := SearchBook(dir, "importacion albaran proveedor", 0)
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Fatal("no hits")
	}
	if hits[0].Path != "areas/erp/albaranes.md" {
		t.Fatalf("first hit = %s (want areas/erp/albaranes.md)\n%+v", hits[0].Path, hits)
	}
	if hits[0].Title != "Importación de albaranes" || hits[0].Area != "erp" {
		t.Fatalf("hit metadata = %+v", hits[0])
	}
	if hits[0].Snippet == "" {
		t.Fatal("hit without a snippet")
	}
	if len(hits) > defaultSearchLimit {
		t.Fatalf("limit ignored: %d hits", len(hits))
	}
}

func TestSearchLimitAndStopwords(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		writeSheet(t, dir, fmt.Sprintf("areas/a/s%02d.md", i), "---\ntitle: Pedidos\n---\n\npedidos de proveedor\n")
	}
	hits, total, err := SearchBook(dir, "¿cómo funciona el pedido de proveedor?", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 || total != 30 {
		t.Fatalf("hits=%d total=%d", len(hits), total)
	}
	// "como", "funciona", "el" and "de" are stopwords; had they survived, every
	// file would still match, but the ranking would be noise.
	terms := queryTerms("¿cómo funciona el pedido de proveedor?")
	var got []string
	for _, term := range terms {
		got = append(got, term.text)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "pedido,proveedor" {
		t.Fatalf("query terms = %v", got)
	}
}

// A short token must not match by prefix: "use" would pull in every sheet that
// says "usuario".
func TestSearchPrefixOnlyFromFourRunes(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "areas/a/usuarios.md", "---\ntitle: Usuarios\n---\n\ngestión de usuarios\n")
	writeSheet(t, dir, "areas/a/albaranes.md", "---\ntitle: Albaranes\n---\n\nimportación de albaranes\n")

	if hits, _, _ := SearchBook(dir, "usu", 5); len(hits) != 0 {
		t.Fatalf("short token matched by prefix: %+v", hits)
	}
	hits, _, _ := SearchBook(dir, "albaran", 5)
	if len(hits) == 0 || hits[0].Path != "areas/a/albaranes.md" {
		t.Fatalf("prefix search = %+v", hits)
	}
}

// Hidden files, .git and anything that is not markdown are invisible to every
// walk: the book is prose, and a book directory that was once a git repo (or
// holds an editor's swap file) must not spend the owner's context on it.
func TestWalksIgnoreHiddenAndNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "areas/a/sheet.md", "---\ntitle: Sheet\n---\n\nalbaranes\n")
	writeSheet(t, dir, ".git/config", "albaranes\n")
	writeSheet(t, dir, ".hidden.md", "albaranes\n")
	writeSheet(t, dir, "notes.txt", "albaranes\n")

	hits, total, err := SearchBook(dir, "albaranes", 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || hits[0].Path != "areas/a/sheet.md" {
		t.Fatalf("hits = %+v", hits)
	}
	files, err := Files(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "areas/a/sheet.md" {
		t.Fatalf("Files = %+v", files)
	}
}

func TestListUnderAPathShowsTitles(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, "areas/erp/albaranes.md", "---\ntitle: Importación de albaranes\n---\n\nx\n")
	writeSheet(t, dir, "areas/web/login.md", "---\ntitle: Login\n---\n\ny\n")

	res, err := list(dir, "areas/erp", nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := resultText(res)
	if !strings.Contains(listed, "areas/erp/albaranes.md — Importación de albaranes") {
		t.Fatalf("list = %q", listed)
	}
	if strings.Contains(listed, "login") {
		t.Fatalf("list leaked another area: %q", listed)
	}
}

func TestOwnerFileIsNotWritableByTheOwner(t *testing.T) {
	dir := t.TempDir()
	tool := NewTool(dir, true)
	res := run(t, tool, map[string]any{"action": "write", "path": OwnerFile, "content": "merge whenever"})
	if !res.IsError {
		t.Fatal("the owner rewrote the user's preferences file")
	}
	if _, err := os.Stat(filepath.Join(dir, OwnerFile)); err == nil {
		t.Fatal("OWNER.md was created by a refused write")
	}
}

// A book of 500 sheets is scanned per query, with no index to keep in sync. The
// budget is the point: if a scan cost more than a fraction of a turn, the design
// would need an index, and an index can disagree with the files on disk.
func TestSearchScalesToFiveHundredSheets(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing test")
	}
	dir := t.TempDir()
	body := strings.Repeat("Implementación del módulo con entidades, servicios y jobs. ", 60)
	for i := 0; i < 500; i++ {
		writeSheet(t, dir, fmt.Sprintf("areas/area%02d/ficha-%03d.md", i%15, i), fmt.Sprintf(`---
title: Ficha número %d del módulo
aliases: ficha%d, sheet%d
---

## Producto

%s
`, i, i, i, body))
	}
	writeSheet(t, dir, "areas/erp/albaranes.md", "---\ntitle: Importación de albaranes\naliases: albarán\n---\n\nSubir el albarán del proveedor.\n")

	var samples []time.Duration
	for i := 0; i < 7; i++ {
		start := time.Now()
		hits, _, err := SearchBook(dir, "importacion albaran proveedor", 10)
		samples = append(samples, time.Since(start))
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 || hits[0].Path != "areas/erp/albaranes.md" {
			t.Fatalf("ranking broke at scale: %+v", hits)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p50 := samples[len(samples)/2]
	if p50 > 200*time.Millisecond {
		t.Fatalf("p50 scan of 500 sheets = %s (budget 200ms)", p50)
	}
	t.Logf("p50 = %s over %d sheets", p50, 501)
}
