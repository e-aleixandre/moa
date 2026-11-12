package dreaming

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/memory"
)

var factHeader = regexp.MustCompile(`(?m)^=== hecho [0-9]+ · (\S+) ===$`)

// keepAllFacts answers the memory pass with "conservar" for every fact shown.
func keepAllFacts(prompt string) string {
	var a factAnswer
	for _, m := range factHeader.FindAllStringSubmatch(prompt, -1) {
		a.Facts = append(a.Facts, factVerdict{Fact: m[1], Verdict: VerdictKeep})
	}
	data, _ := json.Marshal(a)
	return string(data)
}

// memFixture is a codebase whose memory is written through the real memory
// store, with one decision in the book.
func memFixture(t *testing.T) (Store, *memory.Store) {
	t.Helper()
	ms := memory.New(t.TempDir(), t.TempDir())
	s := Store{Codebase: filepath.Dir(ms.ProjectDir())}
	full := filepath.Join(s.bookDir(), "decisions", "precios.md")
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("# Precios\nDecisión del dueño: el precio vigente es la última fuente.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, ms
}

func writeFact(t *testing.T, ms *memory.Store, name, desc, body string) {
	t.Helper()
	if _, err := ms.Write(memory.Memory{Name: name, Description: desc, Body: body, Lifecycle: memory.LifecycleDurable, Scope: memory.ScopeProject}); err != nil {
		t.Fatal(err)
	}
}

func writeRawFact(t *testing.T, ms *memory.Store, name, content string) {
	t.Helper()
	full := filepath.Join(ms.ProjectDir(), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// memNight is fakeNight with the memory pass answered by verdicts. during, if
// set, runs while the model "thinks", outside every store lock.
func memNight(t *testing.T, verdicts func(prompt string) string, during func()) (*fakeProvider, NightConfig, *[]News) {
	fp, cfg, news := fakeNight(t, 3)
	book := fp.answer
	fp.answer = func(req core.Request) string {
		if req.System == memorySystem {
			if during != nil {
				during()
			}
			return verdicts(req.Messages[0].Content[0].Text)
		}
		return book(req)
	}
	return fp, cfg, news
}

func answer(vs ...factVerdict) func(string) string {
	return func(string) string {
		data, _ := json.Marshal(factAnswer{Facts: vs})
		return "```json\n" + string(data) + "\n```"
	}
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func readMem(t *testing.T, ms *memory.Store, name string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ms.ProjectDir(), name))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

const crlfFact = "---\r\nname: viejo\r\ndescription: el servidor es foo\r\ndurable: true\r\n---\r\nEl servidor es foo.\r\nSin salto final"

func TestMemoryArchiveMovesExactBytesAndUndoes(t *testing.T) {
	s, ms := memFixture(t)
	writeRawFact(t, ms, "viejo.md", crlfFact)
	writeFact(t, ms, "nuevo", "el servidor es bar", "El servidor es bar desde el 10-nov.")
	_, cfg, news := memNight(t, answer(
		factVerdict{Fact: "viejo.md", Verdict: VerdictArchive, Reason: ReasonSuperseded, Source: "memory/nuevo.md:9"},
		factVerdict{Fact: "nuevo.md", Verdict: VerdictKeep},
	), nil)
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0] != "memory/viejo.md" {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := readMem(t, ms, "viejo.md"); ok {
		t.Fatal("the archived fact is still in the memory")
	}
	archived, ok := readMem(t, ms, "archivo/2026-11-12/viejo.md")
	if !ok || archived != crlfFact {
		t.Fatalf("archive = %q, want the exact original bytes", archived)
	}
	for _, m := range ms.List() {
		if m.Name == "viejo" || strings.Contains(m.Path, "archivo") {
			t.Fatalf("the memory index still lists %s", m.Path)
		}
	}
	if idx := ms.FormatIndex(ms.List()); strings.Contains(idx, "viejo") || !strings.Contains(idx, "nuevo") {
		t.Fatalf("index = %q", idx)
	}
	if got := gitOut(t, s, "cat-file", "blob", res.Commit+":memory/archivo/2026-11-12/viejo.md"); got != strings.TrimSpace(crlfFact) {
		t.Fatalf("committed archive = %q", got)
	}
	if !strings.Contains((*news)[0].Body, "1 hechos archivados") {
		t.Fatalf("news = %s", (*news)[0].Body)
	}

	if _, err := s.Undo(res.Commit, nil); err != nil {
		t.Fatal(err)
	}
	if got, ok := readMem(t, ms, "viejo.md"); !ok || got != crlfFact {
		t.Fatalf("undo restored %q", got)
	}
	if _, ok := readMem(t, ms, "archivo/2026-11-12/viejo.md"); ok {
		t.Fatal("the undone archive copy is still there")
	}
	if m, ok, err := ms.Read("project/viejo"); err != nil || !ok || m.Description != "el servidor es foo" {
		t.Fatalf("restored fact unreadable: %+v %v %v", m, ok, err)
	}
	// What was undone is not consolidated again until it changes.
	r, _, err := s.Begin(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	snap, err := r.Snapshot("después")
	if err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueFacts(snap); err != nil || len(due) != 0 {
		t.Fatalf("due after undo = %v %v", due, err)
	}
}

func TestMemoryCorrectionKeepsTheExactPrefix(t *testing.T) {
	s, ms := memFixture(t)
	writeRawFact(t, ms, "viejo.md", crlfFact)
	plain := "---\nname: puerto\ndescription: el puerto del servicio\ndurable: true\n---\nEl puerto es 8080.\n"
	writeRawFact(t, ms, "puerto.md", plain)
	writeFact(t, ms, "fuente", "cambios de infraestructura", "El 10-nov el puerto pasó a 9090 y el servidor a bar.")
	writeFact(t, ms, "malanota", "algo", "dato")
	_, cfg, news := memNight(t, answer(
		factVerdict{Fact: "puerto.md", Verdict: VerdictCorrect, Source: "memory/fuente.md:9", Note: "el puerto es 9090 desde el 10-nov", ValidDesc: yes()},
		factVerdict{Fact: "viejo.md", Verdict: VerdictCorrect, Source: "memory/fuente.md:9", Note: "el servidor es bar", ValidDesc: no()},
		factVerdict{Fact: "malanota.md", Verdict: VerdictCorrect, Source: "memory/fuente.md:9", Note: "dos\nlíneas", ValidDesc: yes()},
		factVerdict{Fact: "fuente.md", Verdict: VerdictKeep},
	), nil)
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := readMem(t, ms, "puerto.md")
	want := plain + "\n> Corregido por el dreaming el 2026-11-12: el puerto es 9090 desde el 10-nov (memory/fuente.md:9)\n"
	if got != want {
		t.Fatalf("corrected = %q\nwant %q", got, want)
	}
	m, ok, err := ms.Read("project/puerto")
	if err != nil || !ok || m.Description != "el puerto del servicio" || !strings.Contains(m.Body, "9090") {
		t.Fatalf("corrected fact through memory: %+v %v %v", m, ok, err)
	}
	// descripcion_valida false: not corrected, flagged for review.
	if got, _ := readMem(t, ms, "viejo.md"); got != crlfFact {
		t.Fatalf("a correction that invalidates the description was applied: %q", got)
	}
	if !strings.Contains((*news)[0].Body, "a revisar") || !strings.Contains((*news)[0].Body, "memory/viejo.md") {
		t.Fatalf("news = %s", (*news)[0].Body)
	}
	if _, ok := res.Rejected["memory/malanota.md"]; !ok {
		t.Fatalf("a multi-line note was accepted: %+v", res)
	}
	if got, _ := readMem(t, ms, "malanota.md"); strings.Contains(got, "Corregido") {
		t.Fatal("a rejected correction was written")
	}
}

// The CRLF fact without a final newline keeps its bytes as the prefix.
func TestCorrectedFactPrefix(t *testing.T) {
	got := string(correctedFact([]byte(crlfFact), "2026-11-12", "nota", "memory/x.md:1"))
	if !strings.HasPrefix(got, crlfFact) || got != crlfFact+"\n\n> Corregido por el dreaming el 2026-11-12: nota (memory/x.md:1)\n" {
		t.Fatalf("got %q", got)
	}
	ms := memory.New(t.TempDir(), t.TempDir())
	writeRawFact(t, ms, "x.md", got)
	if m, ok, err := ms.Read("project/x"); err != nil || !ok || m.Description != "el servidor es foo" {
		t.Fatalf("unreadable: %+v %v %v", m, ok, err)
	}
}

func TestMemorySecretsAreNeitherSentNorTouched(t *testing.T) {
	s, ms := memFixture(t)
	const pattern = "sk-abcdef1234567890ABCDEF"
	const marked = "valor-marcado-que-no-parece-clave"
	writeFact(t, ms, "clave", "clave de la API", "La clave es "+pattern+".")
	writeRawFact(t, ms, "marcado.md", "---\nname: marcado\ndescription: acceso\nsecret: true\ndurable: true\n---\n"+marked+"\n")
	writeFact(t, ms, "normal", "un hecho", "nada secreto")
	before := tree(t, s)
	// The model tries to archive everything, secrets by name included.
	fp, cfg, news := memNight(t, answer(
		factVerdict{Fact: "clave.md", Verdict: VerdictArchive, Reason: ReasonClosed, Source: "memory/normal.md:1"},
		factVerdict{Fact: "marcado.md", Verdict: VerdictArchive, Reason: ReasonClosed, Source: "memory/normal.md:1"},
		factVerdict{Fact: "normal.md", Verdict: VerdictKeep},
	), nil)
	if _, err := s.Night(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	sawMemory := false
	for _, req := range fp.requests {
		text := req.System
		for _, m := range req.Messages {
			for _, c := range m.Content {
				text += c.Text
			}
		}
		if req.System == memorySystem {
			sawMemory = true
		}
		if strings.Contains(text, pattern) || strings.Contains(text, marked) {
			t.Fatal("a secret reached the model")
		}
	}
	if !sawMemory {
		t.Fatal("the memory pass did not run")
	}
	after := tree(t, s)
	for _, p := range []string{"memory/clave.md", "memory/marcado.md"} {
		if after[p] != before[p] {
			t.Fatalf("secret %s changed", p)
		}
	}
	body := (*news)[0].Body
	if !strings.Contains(body, "memory/clave.md") || !strings.Contains(body, "memory/marcado.md") {
		t.Fatalf("secrets not named: %s", body)
	}
	if strings.Contains(body, pattern) || strings.Contains(body, marked) {
		t.Fatal("a secret value is in the news")
	}
}

func TestSecretDetector(t *testing.T) {
	for _, s := range []string{
		"x ghp_abcdefghijklmnopqrstuvwxyz0123", "AKIAABCDEFGHIJKLMNOP", "-----BEGIN OPENSSH PRIVATE KEY-----",
		"xoxb-123-abc", "Password: hunter2", "token=abc", "api_key: 0123456789abcdef0123456789abcdef",
		"---\nsecret: \"true\"\n---\nnada",
	} {
		if !isSecret([]byte(s)) {
			t.Errorf("%q not detected", s)
		}
	}
	for _, s := range []string{"---\nname: x\n---\nel puerto es 8080", "una clave de diseño"} {
		if isSecret([]byte(s)) {
			t.Errorf("%q flagged", s)
		}
	}
}

func TestMemoryNeedsAValidSourceAndYieldsToConcurrentWrites(t *testing.T) {
	s, ms := memFixture(t)
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		writeFact(t, ms, n, "hecho "+n, "cuerpo de "+n)
	}
	orig := tree(t, s)
	_, cfg, _ := memNight(t, answer(
		factVerdict{Fact: "a.md", Verdict: VerdictArchive, Reason: ReasonSuperseded, Source: "memory/nope.md:1"},
		factVerdict{Fact: "b.md", Verdict: VerdictArchive, Reason: ReasonSuperseded, Source: "decisions/precios.md:99"},
		factVerdict{Fact: "c.md", Verdict: VerdictCorrect, Source: "decisions/precios.md:2", Note: "otra cosa", ValidDesc: yes()},
		factVerdict{Fact: "d.md", Verdict: VerdictArchive, Reason: ReasonRepeated, Source: "memory/e.md:9"},
		factVerdict{Fact: "e.md", Verdict: VerdictKeep},
		factVerdict{Fact: "f.md", Verdict: VerdictArchive, Reason: ReasonSuperseded, Source: "decisions/precios.md:2"},
		// g cites h, which leaves the index the same night: g stays.
		factVerdict{Fact: "g.md", Verdict: VerdictArchive, Reason: ReasonRepeated, Source: "memory/h.md:9"},
		factVerdict{Fact: "h.md", Verdict: VerdictArchive, Reason: ReasonClosed, Source: "decisions/precios.md:2"},
	), func() {
		// While the model thinks, sessions write c (the corrected fact) and
		// e (the source d relies on) through the memory store.
		writeFact(t, ms, "c", "hecho c", "c reescrito por una sesión")
		writeFact(t, ms, "e", "hecho e", "e reescrito por una sesión")
	})
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"memory/a.md", "memory/b.md", "memory/g.md"} {
		if _, ok := res.Rejected[f]; !ok {
			t.Fatalf("%s without a valid source was not rejected: %+v", f, res)
		}
	}
	if !contains(res.Conflicts, "memory/c.md") || !contains(res.Conflicts, "memory/d.md") {
		t.Fatalf("conflicts = %v", res.Conflicts)
	}
	if len(res.Applied) != 2 || !contains(res.Applied, "memory/f.md") || !contains(res.Applied, "memory/h.md") {
		t.Fatalf("applied = %v", res.Applied)
	}
	got := tree(t, s)
	for _, p := range []string{"memory/a.md", "memory/b.md", "memory/d.md", "memory/g.md"} {
		if got[p] != orig[p] {
			t.Fatalf("%s changed", p)
		}
	}
	if !strings.Contains(got["memory/c.md"], "c reescrito por una sesión") || strings.Contains(got["memory/c.md"], "Corregido") {
		t.Fatalf("the concurrent write was overwritten: %q", got["memory/c.md"])
	}
	// A conflict is not recorded: it is due again.
	r, _, err := s.Begin(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	snap, err := r.Snapshot("después")
	if err != nil {
		t.Fatal(err)
	}
	due, _ := s.DueFacts(snap)
	for _, f := range []string{"memory/a.md", "memory/c.md", "memory/d.md"} {
		if !contains(due, f) {
			t.Fatalf("%s is not due again: %v", f, due)
		}
	}
}

func TestMemoryArchiveIsImmutable(t *testing.T) {
	for _, c := range []Change{
		{Path: "memory/archivo/2026-11-01/x.md", Pre: "a", Post: "b"},
		{Path: "memory/archivo/2026-11-01/x.md", Pre: "a", Post: "a"},
	} {
		if allowedChange(c) == nil {
			t.Fatalf("%+v allowed", c)
		}
	}
	if allowedChange(Change{Path: "memory/archivo/2026-11-12/x.md", Post: "b"}) != nil {
		t.Fatal("creating an archive file refused")
	}

	s, ms := memFixture(t)
	writeFact(t, ms, "viejo", "viejo", "viejo")
	writeFact(t, ms, "nuevo", "nuevo", "nuevo")
	writeRawFact(t, ms, "archivo/2026-11-12/viejo.md", "archivo previo\n")
	writeRawFact(t, ms, "archivo/2026-11-01/otro.md", "otro previo\n")
	fp, cfg, _ := memNight(t, answer(
		factVerdict{Fact: "viejo.md", Verdict: VerdictArchive, Reason: ReasonSuperseded, Source: "memory/nuevo.md:1"},
		factVerdict{Fact: "nuevo.md", Verdict: VerdictKeep},
	), nil)
	res, err := s.Night(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Rejected["memory/viejo.md"]; !ok {
		t.Fatalf("an archive collision was not rejected: %+v", res)
	}
	if got, _ := readMem(t, ms, "archivo/2026-11-12/viejo.md"); got != "archivo previo\n" {
		t.Fatalf("the previous archive file changed: %q", got)
	}
	for _, req := range fp.requests {
		if req.System == memorySystem && strings.Contains(req.Messages[0].Content[0].Text, "otro previo") {
			t.Fatal("an archive file was sent as a fact")
		}
	}
}

// Changes only in the memory start a night; with nothing pending there is no
// call and no commit.
func TestOnlyMemoryChangesTriggerTheNight(t *testing.T) {
	s, ms := memFixture(t)
	writeFact(t, ms, "uno", "uno", "uno")
	fp, cfg, _ := memNight(t, keepAllFacts, nil)
	if _, err := s.Night(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	head := gitOut(t, s, "rev-parse", Ref)
	calls := len(fp.requests)

	cfg.Date = "2026-11-13"
	if res, err := s.Night(t.Context(), cfg); err != nil || !res.Ran || len(fp.requests) != calls {
		t.Fatalf("an unchanged night called a model: %d → %d (%+v %v)", calls, len(fp.requests), res, err)
	}
	if gitOut(t, s, "rev-parse", Ref) != head {
		t.Fatal("an unchanged night committed")
	}

	writeFact(t, ms, "dos", "dos", "dos")
	cfg.Date = "2026-11-14"
	if _, err := s.Night(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	var last core.Request
	n := 0
	for _, req := range fp.requests[calls:] {
		n++
		last = req
	}
	if n != 1 || last.System != memorySystem || !strings.Contains(last.Messages[0].Content[0].Text, "dos.md") || strings.Contains(last.Messages[0].Content[0].Text, "uno.md") {
		t.Fatalf("a memory change made %d calls (want one memory call with only the new fact)", n)
	}
	if got := gitOut(t, s, "show", Ref+":memory/dos.md"); !strings.Contains(got, "dos") {
		t.Fatal("the new fact is not versioned")
	}
	head = gitOut(t, s, "rev-parse", Ref)
	calls = len(fp.requests)
	cfg.Date = "2026-11-15"
	if _, err := s.Night(t.Context(), cfg); err != nil || len(fp.requests) != calls || gitOut(t, s, "rev-parse", Ref) != head {
		t.Fatal("a night with nothing pending called or committed")
	}
}
