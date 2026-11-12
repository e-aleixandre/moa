package dreaming

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/book"
	"github.com/e-aleixandre/moa/pkg/storelock"
)

// fixture is a codebase directory with a small real book.
func fixture(t *testing.T) Store {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"book/PROJECT.md":            "# Proyecto\n- índice\n",
		"book/OWNER.md":              "lo que pide el dueño\n",
		"book/decisions/precios.md":  "# Precios\nDecisión del dueño: el precio vigente es la última fuente.\n",
		"book/historico/2025.md":     "viejo\n",
		"book/work/a.md":             "---\naliases: [katxina]\n---\n# A\nA1 viejo\nA2 viejo\nA3 vigente\n",
		"book/work/a/parte-01.md":    "<!-- fin-cabecera -->\nA4 viejo\r\nA5 vigente",
		"book/areas/b.md":            "# B\nB1 viejo\nB2 vigente\n",
		"memory/hecho.md":            "---\nname: hecho\n---\ncuerpo\n",
		"book/archivo/work/z/old.md": "archivo previo\n",
		"book/work/c.md":             "# C\nC1 viejo\nC2\n",
	}
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Store{Codebase: dir}
}

// planAll keeps the last line of every file and archives the rest as
// superseded by the sheet's first line.
func planAll(snap Snapshot, u Unit) Plan {
	orig := snap.Orig(u)
	p := Plan{State: "## Estado actual\n- consolidado (12-nov · detalle abajo)"}
	for f, o := range orig {
		lines := splitLines(o)
		start := 1
		if f == 0 {
			start = headerLen(lines) + 1
		}
		if start > len(lines) {
			continue
		}
		if start < len(lines) {
			p.Archive = append(p.Archive, Archived{File: f, Lines: [2]int{start, len(lines) - 1}, Reason: ReasonSuperseded, Source: u.Sheet + ":1"})
		}
		p.Keep = append(p.Keep, [3]int{f, len(lines), len(lines)})
	}
	return p
}

type night struct {
	r       *Run
	snap    Snapshot
	groups  []Group
	content map[string][]byte
	fin     Finish
}

// prepare runs a night up to the point of applying: snapshot, plan every unit
// (or only the named ones), assemble and build the groups.
func prepare(t *testing.T, s Store, date string, only ...string) *night {
	t.Helper()
	r, _, err := s.Begin(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	snap, err := r.Snapshot("antes del dreaming " + date)
	if err != nil {
		t.Fatal(err)
	}
	n := &night{r: r, snap: snap, content: map[string][]byte{}, fin: Finish{Run: "run-" + date, Units: map[string]string{}}}
	for _, u := range snap.Units() {
		if len(only) > 0 && !contains(only, u.Sheet) {
			continue
		}
		if len(only) == 0 && isDecision(u) {
			continue // decisions have their own rules; see decision_test.go
		}
		out, err := Assemble(AssembleInput{Unit: u, Orig: snap.Orig(u), Plan: planAll(snap, u), Date: date, Nonce: "nonce" + strings.ReplaceAll(date, "-", ""), S0: snap.Commit, SourceExists: snap.SourceExists})
		if err != nil {
			t.Fatalf("%s: %v", u.Sheet, err)
		}
		g, content, err := r.UnitGroup(snap, u, out, nil)
		if err != nil {
			t.Fatal(err)
		}
		n.groups = append(n.groups, g)
		for k, v := range content {
			n.content[k] = v
		}
		n.fin.Units[g.ID] = g.After
	}
	return n
}

func (n *night) apply(t *testing.T) Report {
	t.Helper()
	rep, err := n.r.Apply(n.snap, "dreaming", n.groups, n.content, n.fin, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// tree reads both stores as path → content (dot files excluded).
func tree(t *testing.T, s Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, st := range []struct{ dir, prefix string }{{s.bookDir(), bookPrefix}, {s.memoryDir(), memoryPrefix}} {
		if err := walkStore(st.dir, func(rel string, data []byte, regular bool) error {
			out[st.prefix+rel] = string(data)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func gitOut(t *testing.T, s Store, args ...string) string {
	t.Helper()
	out, err := gitRepo{dir: s.Codebase}.run(nil, nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// allLinesPresent checks I1 on the live disk: every line of every original
// unit file is somewhere in the live book (as many times as it occurred).
func allLinesPresent(t *testing.T, s Store, snap Snapshot) {
	t.Helper()
	var live bytes.Buffer
	for p, c := range tree(t, s) {
		if strings.HasPrefix(p, bookPrefix) {
			live.WriteString(c)
			live.WriteString("\n")
		}
	}
	for _, u := range snap.Units() {
		for i, o := range snap.Orig(u) {
			for _, l := range splitLines(o) {
				if !bytes.Contains(live.Bytes(), bytes.TrimRight(l, "\n")) {
					t.Fatalf("line %q of %s is gone from the live book", l, u.Files[i])
				}
			}
		}
	}
}

func TestNightAppliesApprovedBytesAndKeepsProtectedIntact(t *testing.T) {
	s := fixture(t)
	before := tree(t, s)
	n := prepare(t, s, "2026-11-12")
	rep := n.apply(t)
	if len(rep.Conflicts) != 0 || len(rep.Applied) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	after := tree(t, s)
	for p, c := range before {
		rel := strings.TrimPrefix(p, bookPrefix)
		if strings.HasPrefix(p, memoryPrefix) || Protected(rel) {
			if after[p] != c {
				t.Fatalf("protected %s changed", p)
			}
		}
	}
	if _, ok := after["book/work/a/parte-01.md"]; ok {
		t.Fatal("the part was not removed")
	}
	if !strings.HasPrefix(after["book/work/a.md"], "---\naliases: [katxina]\n---\n# A\n") {
		t.Fatalf("header lost:\n%s", after["book/work/a.md"])
	}
	allLinesPresent(t, s, n.snap)
	// The commit is exactly the approved bytes: its tree equals the disk.
	for p, c := range after {
		got := gitOut(t, s, "cat-file", "blob", rep.Commit+":"+p)
		if got != strings.TrimSpace(c) {
			t.Fatalf("%s in the commit differs from disk", p)
		}
	}
	if parent := gitOut(t, s, "rev-parse", rep.Commit+"^"); parent != n.snap.Commit {
		t.Fatalf("D's parent = %s, want S0 %s", parent, n.snap.Commit)
	}
	if _, err := os.Stat(s.journalPath()); !os.IsNotExist(err) {
		t.Fatal("journal left behind")
	}
}

// I3: a cooperative write during the model phase leaves its group alone.
func TestCooperativeWriteDuringTheNightIsNotOverwritten(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12")
	// The owner writes A through the API, appends to B through the tool, and
	// someone creates the archive path C's group would create.
	if err := book.WriteFile(s.bookDir(), "work/a.md", []byte("nota nueva del owner\n")); err != nil {
		t.Fatal(err)
	}
	tool := book.NewTool(s.bookDir(), true)
	if res, err := tool.Execute(t.Context(), map[string]any{"action": "append", "path": "areas/b.md", "content": "B3 nuevo\n"}, nil); err != nil || res.IsError {
		t.Fatalf("append: %v %+v", err, res)
	}
	if err := os.MkdirAll(filepath.Join(s.bookDir(), "archivo/work/c"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.bookDir(), "archivo/work/c/2026-11-12.md"), []byte("ajeno\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := n.apply(t)
	if len(rep.Applied) != 0 || len(rep.Conflicts) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	got := tree(t, s)
	if got["book/work/a.md"] != "nota nueva del owner\n" || !strings.HasSuffix(got["book/areas/b.md"], "B3 nuevo\n") ||
		got["book/archivo/work/c/2026-11-12.md"] != "ajeno\n" || got["book/work/a/parte-01.md"] == "" {
		t.Fatalf("a cooperative write was overwritten:\n%v", got)
	}
	st, err := s.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Units) != 0 {
		t.Fatalf("conflicting units recorded as processed: %v", st.Units)
	}
}

// I3: while the night writes, a book write waits for it and lands after.
func TestBookWriteWaitsForTheApply(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "work/c.md")
	inApply := make(chan struct{})
	release := make(chan struct{})
	testHookAfterWrite = func(step int) error {
		if step == 1 {
			close(inApply)
			<-release
		}
		return nil
	}
	t.Cleanup(func() { testHookAfterWrite = nil })
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); n.apply(t) }()
	<-inApply
	wrote := make(chan error, 1)
	go func() { wrote <- book.WriteFile(s.bookDir(), "work/c.md", []byte("owner después\n")) }()
	select {
	case <-wrote:
		t.Fatal("a book write ran while the night was applying")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if got := tree(t, s)["book/work/c.md"]; got != "owner después\n" {
		t.Fatalf("c = %q", got)
	}
}

// The declared exception: a writer without the lock that lands right after
// the night renamed a file is not adopted by the commit, and undo does not
// erase it.
func TestUncooperativeWriteIsNeitherCommittedNorUndone(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "areas/b.md")
	testHookAfterWrite = func(step int) error {
		// Step 1 creates the archive, step 2 renames the sheet.
		if step == 2 {
			return os.WriteFile(filepath.Join(s.bookDir(), "areas/b.md"), []byte("bash del owner\n"), 0o600)
		}
		return nil
	}
	t.Cleanup(func() { testHookAfterWrite = nil })
	rep := n.apply(t)
	testHookAfterWrite = nil
	if got := gitOut(t, s, "cat-file", "blob", rep.Commit+":book/areas/b.md"); strings.Contains(got, "bash del owner") {
		t.Fatal("the commit adopted bytes the night never approved")
	}
	_ = n.r.Close()
	urep, err := s.Undo(rep.Commit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(urep.Conflicts) != 1 {
		t.Fatalf("undo report = %+v", urep)
	}
	got := tree(t, s)
	if got["book/areas/b.md"] != "bash del owner\n" {
		t.Fatalf("undo erased a foreign write: %q", got["book/areas/b.md"])
	}
	if !strings.Contains(got["book/archivo/areas/b/2026-11-12.md"], "B1 viejo") {
		t.Fatal("the conflicting group lost its archive")
	}
}

// I4: a crash after any write is rolled back to S0 by the next run, with no
// line missing at the crash point.
func TestCrashMidApplyRollsBackToTheSnapshot(t *testing.T) {
	for k := 1; ; k++ {
		s := fixture(t)
		n := prepare(t, s, "2026-11-12")
		s0 := tree(t, s)
		total := 0
		testHookAfterWrite = func(step int) error {
			total = step
			if step == k {
				return errCrash
			}
			return nil
		}
		_, err := n.r.Apply(n.snap, "dreaming", n.groups, n.content, n.fin, nil, nil)
		testHookAfterWrite = nil
		if err == nil {
			if k == 1 {
				t.Fatal("no write happened")
			}
			return // k passed the last write: every step was covered
		}
		if !errors.Is(err, errCrash) {
			t.Fatal(err)
		}
		allLinesPresent(t, s, n.snap)
		_ = n.r.Close()
		r, rep, err := s.Begin(true, nil)
		if err != nil {
			t.Fatalf("k=%d: %v", k, err)
		}
		_ = r.Close()
		if rep.Recovered != "rolled back" {
			t.Fatalf("k=%d (of %d): recovery = %+v", k, total, rep)
		}
		if got := tree(t, s); !equalTrees(got, s0) {
			t.Fatalf("k=%d: not back to S0:\n%v\nwant\n%v", k, got, s0)
		}
		if head := gitOut(t, s, "rev-parse", Ref); head != n.snap.Commit {
			t.Fatalf("k=%d: head moved", k)
		}
		if _, err := os.Stat(s.journalPath()); !os.IsNotExist(err) {
			t.Fatalf("k=%d: journal left", k)
		}
		// Recovering again is a no-op.
		r, rep, err = s.Begin(true, nil)
		if err != nil || rep.Recovered != "" {
			t.Fatalf("second recovery: %+v %v", rep, err)
		}
		_ = r.Close()
	}
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// A crash after the history moved finishes the night instead of undoing it,
// and publishes its news exactly once even when publishing failed before.
func TestCrashAfterPublishFinishes(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12")
	n.fin.News = &News{Key: "dreaming/2026-11-12", Origin: "dreaming/run", Title: "t"}
	failing := func(News) error { return errors.New("db down") }
	if _, err := n.r.Apply(n.snap, "dreaming", n.groups, n.content, n.fin, nil, failing); err == nil {
		t.Fatal("expected the publish failure")
	}
	_ = n.r.Close()
	consolidated := tree(t, s)
	var got []News
	r, rep, err := s.Begin(true, func(nw News) error { got = append(got, nw); return nil })
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if rep.Recovered != "finished" || len(got) != 1 || got[0].Origin != "dreaming/run" {
		t.Fatalf("recovery = %+v, news = %v", rep, got)
	}
	if !equalTrees(tree(t, s), consolidated) {
		t.Fatal("a published night was rolled back")
	}
	st, _ := s.loadState()
	if len(st.Units) != 3 {
		t.Fatalf("state not finished: %v", st.Units)
	}
}

// A path written by someone else between the crash and the recovery is left
// alone with its whole group, and the journal is kept for the user.
func TestRecoveryLeavesForeignWritesAlone(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "work/a.md")
	testHookAfterWrite = func(step int) error {
		if step == 2 { // archive created, sheet replaced; the part still there
			return errCrash
		}
		return nil
	}
	_, err := n.r.Apply(n.snap, "dreaming", n.groups, n.content, n.fin, nil, nil)
	testHookAfterWrite = nil
	if !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	_ = n.r.Close()
	if err := book.WriteFile(s.bookDir(), "work/a.md", []byte("escrito tras la caída\n")); err != nil {
		t.Fatal(err)
	}
	r, rep, err := s.Begin(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if len(rep.Conflicts) != 1 {
		t.Fatalf("recovery = %+v", rep)
	}
	got := tree(t, s)
	if got["book/work/a.md"] != "escrito tras la caída\n" || !strings.Contains(got["book/archivo/work/a/2026-11-12.md"], "A1 viejo") {
		t.Fatal("recovery touched a group someone else wrote")
	}
	if _, err := os.Stat(filepath.Join(s.workDir(), "journal-failed-run-2026-11-12.json")); err != nil {
		t.Fatal("the journal of the conflicting recovery was not kept")
	}
}

// I5.
func TestUndoRestoresTheSnapshotAndRespectsLaterEdits(t *testing.T) {
	s := fixture(t)
	s0 := tree(t, s)
	n := prepare(t, s, "2026-11-12")
	rep := n.apply(t)
	_ = n.r.Close()
	if err := book.WriteFile(s.bookDir(), "areas/b.md", []byte("editado después\n")); err != nil {
		t.Fatal(err)
	}
	urep, err := s.Undo(rep.Commit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(urep.Conflicts) != 1 || urep.Conflicts[0] != "book/areas/b.md" {
		t.Fatalf("undo = %+v", urep)
	}
	got := tree(t, s)
	for _, p := range []string{"book/work/a.md", "book/work/a/parte-01.md", "book/work/c.md"} {
		if got[p] != s0[p] {
			t.Fatalf("%s not restored", p)
		}
	}
	if _, ok := got["book/archivo/work/a/2026-11-12.md"]; ok {
		t.Fatal("the undone archive file is still there")
	}
	if got["book/areas/b.md"] != "editado después\n" || got["book/archivo/areas/b/2026-11-12.md"] == "" {
		t.Fatal("the conflicting group was touched")
	}
	// The next night does not consolidate what was undone.
	r, _, err := s.Begin(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	snap, err := r.Snapshot("noche 2")
	if err != nil {
		t.Fatal(err)
	}
	due, err := s.Due(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range due {
		if u.Sheet == "work/a.md" || u.Sheet == "work/c.md" {
			t.Fatalf("%s is due again after undo", u.Sheet)
		}
	}
}

func TestUndoAnOlderNightAfterAnother(t *testing.T) {
	s := fixture(t)
	s0 := tree(t, s)
	n1 := prepare(t, s, "2026-11-12", "work/a.md")
	rep1 := n1.apply(t)
	_ = n1.r.Close()
	n2 := prepare(t, s, "2026-11-13", "areas/b.md")
	n2.apply(t)
	_ = n2.r.Close()
	b := tree(t, s)["book/areas/b.md"]
	if _, err := s.Undo(rep1.Commit, nil); err != nil {
		t.Fatal(err)
	}
	got := tree(t, s)
	if got["book/work/a.md"] != s0["book/work/a.md"] || got["book/areas/b.md"] != b {
		t.Fatal("undoing night 1 did not keep night 2")
	}
}

func TestUndoRefusesWhileANightRuns(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12")
	rep := n.apply(t)
	if _, err := s.Undo(rep.Commit, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("undo during a night = %v, want ErrBusy", err)
	}
}

// I2 and I6: a group that targets a protected path, or reaches one through a
// symlink, never writes.
func TestProtectedPathsCannotBeWritten(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "work/c.md")
	before := tree(t, s)
	x, err := n.r.repo.hashObject([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"book/OWNER.md", "book/historico/2025.md", "book/archivo/work/z/old.md"} {
		bad := Group{ID: "x", Changes: []Change{{Path: p, Pre: n.snap.Blobs[p], Post: x}}}
		if _, err := n.r.Apply(n.snap, "d", []Group{bad}, map[string][]byte{x: []byte("x")}, Finish{Run: "r"}, nil, nil); err == nil {
			t.Fatalf("a change to %s was accepted", p)
		}
	}
	// archivo/work/c is a symlink into decisions/: the archive write must not
	// follow it.
	if err := os.MkdirAll(filepath.Join(s.bookDir(), "archivo/work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.bookDir(), "decisions"), filepath.Join(s.bookDir(), "archivo/work/c")); err != nil {
		t.Fatal(err)
	}
	rep := n.apply(t)
	if len(rep.Applied) != 0 {
		t.Fatalf("applied through a symlink: %+v", rep)
	}
	after := tree(t, s)
	for p, c := range before {
		if strings.HasPrefix(p, "book/decisions/") && after[p] != c {
			t.Fatalf("%s changed", p)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(s.bookDir(), "decisions")); len(entries) != 1 {
		t.Fatalf("decisions/ gained files: %v", entries)
	}
}

// The user's git configuration cannot change what is stored.
func TestHistoryIgnoresTheUserGitConfig(t *testing.T) {
	home := t.TempDir()
	cfg := "[core]\n\tautocrlf = true\n\tattributesFile = " + filepath.Join(home, "attrs") + "\n[filter \"x\"]\n\tclean = tr a-z A-Z\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "attrs"), []byte("*.md filter=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	s := fixture(t)
	r, _, err := s.Begin(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	snap, err := r.Snapshot("s0")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.repo.blob(snap.Blobs["book/work/a/parte-01.md"])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<!-- fin-cabecera -->\nA4 viejo\r\nA5 vigente" {
		t.Fatalf("stored bytes changed: %q", got)
	}
	again, err := r.Snapshot("s0 again")
	if err != nil {
		t.Fatal(err)
	}
	if again.Commit != snap.Commit {
		t.Fatal("an unchanged snapshot made a new commit")
	}
}

// Every git child holds the run lock: a git that outlives a crashed server
// keeps the next run out until it exits.
func TestGitChildrenHoldTheRunLock(t *testing.T) {
	s := fixture(t)
	r, _, err := s.Begin(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "1")
	cmd.ExtraFiles = []*os.File{r.run}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.Close() // the parent lets go; the child still holds the description
	if _, ok, _ := storelock.TryLock(s.workDir()); ok {
		t.Fatal("the run lock was free while a child held it")
	}
	_ = cmd.Wait()
	f, ok, err := storelock.TryLock(s.workDir())
	if err != nil || !ok {
		t.Fatalf("lock not released after the child: %v", err)
	}
	_ = f.Close()
}

// R6: a plan whose source changed during the night, or whose source belongs
// to a group that was not accepted, is deferred.
func TestPlansDependingOnAChangedSourceAreDeferred(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "work/c.md", "areas/b.md", "work/a.md")
	for i := range n.groups {
		switch n.groups[i].ID {
		case "book/work/c.md": // relied on a decision
			n.groups[i].Sources = []Change{{Path: "book/decisions/precios.md", Pre: n.snap.Blobs["book/decisions/precios.md"]}}
		case "book/areas/b.md": // relied on A, whose group will conflict
			n.groups[i].Sources = []Change{{Path: "book/work/a.md", Pre: n.snap.Blobs["book/work/a.md"]}}
		}
	}
	if err := os.WriteFile(filepath.Join(s.bookDir(), "decisions/precios.md"), []byte("decisión cambiada\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A conflicts through its part, so its sheet is unchanged on disk: B must
	// still be deferred because A's group is not in the accepted set.
	if err := os.WriteFile(filepath.Join(s.bookDir(), "work/a/parte-01.md"), []byte("parte editada\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := n.apply(t)
	if len(rep.Applied) != 0 || len(rep.Conflicts) != 3 {
		t.Fatalf("report = %+v", rep)
	}
}

// Two groups that would create the same path are both refused: neither may
// silently replace the other.
func TestTwoGroupsNeverWriteTheSamePath(t *testing.T) {
	s := fixture(t)
	n := prepare(t, s, "2026-11-12", "work/c.md")
	var groups []Group
	content := map[string][]byte{}
	for i, text := range []string{"primera\n", "segunda\n"} {
		id, err := n.r.repo.hashObject([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		content[id] = []byte(text)
		groups = append(groups, Group{ID: fmt.Sprintf("g%d", i), Changes: []Change{{Path: "book/archivo/work/x/2026-11-12.md", Post: id}}})
	}
	rep, err := n.r.Apply(n.snap, "d", groups, content, Finish{Run: "r"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Applied) != 0 || len(rep.Conflicts) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(s.bookDir(), "archivo/work/x/2026-11-12.md")); !os.IsNotExist(err) {
		t.Fatal("a contested path was written")
	}
}
