package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/owner"
)

// The delta is lifted from the FULL message. A session that ends with a long
// summary pushes its own delta past the 2 KiB tail the report carries, and that
// is exactly the case the extraction exists for.
func TestBookDeltaSurvivesALongFinalMessage(t *testing.T) {
	long := strings.Repeat("A line of an unusually chatty final message.\n", 300)
	text := long + "\n## Book delta\n- areas/erp/albaranes.md: el estado \"enviado\" gana canal\n- work/whatsapp.md: nueva ficha\n"

	delta := extractBookDelta(text)
	if !strings.Contains(delta, "areas/erp/albaranes.md") || !strings.Contains(delta, "work/whatsapp.md") {
		t.Fatalf("delta = %q", delta)
	}
	if extractBookDelta(long) != "" {
		t.Fatal("a message without the section reported a delta")
	}
}

// The delta is before more than 2 KiB of tail: it is provably NOT in what the
// report carries as `said`, so nothing about this passing is luck.
func TestBookDeltaIsReadBeyondTheReportTail(t *testing.T) {
	tail := strings.Repeat("Un párrafo final que el modelo escribió después del delta.\n", 200)
	text := "Trabajo hecho.\n\n## Book delta\n- areas/erp/albaranes.md: cambia el estado\n\n## Notas\n" + tail
	if len(tail) <= maxReportFinalTextBytes {
		t.Fatalf("the fixture is too short to prove anything: %d bytes", len(tail))
	}
	delta := extractBookDelta(text)
	if delta != "- areas/erp/albaranes.md: cambia el estado" {
		t.Fatalf("delta = %q", delta)
	}
	if strings.Contains(reportTail(text), "## Book delta") {
		t.Fatal("the tail still carries the delta; the fixture does not test the mechanism")
	}
}

// Two sections mean the last one: a draft followed by a final answer.
func TestBookDeltaKeepsTheLastBlock(t *testing.T) {
	text := "## Book delta\n- areas/a.md: borrador\n\n## Trabajo\nmás cosas\n\n## Book delta\n- areas/b.md: definitivo\n"
	delta := extractBookDelta(text)
	if delta != "- areas/b.md: definitivo" {
		t.Fatalf("delta = %q", delta)
	}
}

// The section runs to the next heading of the same level or higher. A deeper
// heading inside it is part of the delta; an H2 ends it.
func TestBookDeltaStopsAtTheNextHeadingOfItsLevel(t *testing.T) {
	text := "## Book delta\n- areas/a.md: x\n\n### detalle\n- areas/b.md: y\n\n## Notes\n- not part of the delta\n"
	delta := extractBookDelta(text)
	if strings.Contains(delta, "not part of the delta") {
		t.Fatalf("delta swallowed the next section: %q", delta)
	}
	if !strings.Contains(delta, "areas/b.md") {
		t.Fatalf("delta was cut at a deeper heading: %q", delta)
	}
	// A heading inside a fence is text: a session pasting a diff of a markdown
	// file must not truncate its own delta.
	fenced := "## Book delta\n- areas/a.md: x\n\n```md\n## Uso\n```\n- areas/c.md: z\n\n## Notes\nnope\n"
	if got := extractBookDelta(fenced); !strings.Contains(got, "areas/c.md") || strings.Contains(got, "nope") {
		t.Fatalf("fenced delta = %q", got)
	}
}

// Over the cap the delta is cut on a rune boundary and says so: a delta that
// ends mid-path still reads like a path.
func TestBookDeltaTruncationIsUTF8SafeAndMarked(t *testing.T) {
	body := strings.Repeat("- areas/erp/ñoño-áéíóú.md: cambia el estado del envío\n", 200)
	text := "## Book delta\n" + body
	if len(body) <= maxBookDeltaBytes {
		t.Fatalf("the fixture does not exceed the cap: %d bytes", len(body))
	}
	delta := extractBookDelta(text)
	if !strings.HasSuffix(delta, "\n[truncated]") {
		t.Fatalf("a truncated delta did not say so: %q", delta[max(0, len(delta)-60):])
	}
	if !utf8Valid(delta) {
		t.Fatal("the truncation split a rune")
	}
	if len(delta) > maxBookDeltaBytes+len("\n[truncated]") {
		t.Fatalf("truncated delta is %d bytes", len(delta))
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// The delta as the LAST section of the message: nothing follows it, so the
// extraction has to run to the end of the text.
func TestBookDeltaAsTheFinalSection(t *testing.T) {
	text := "Resumen del trabajo.\n\n## Book delta\n- areas/a.md: x\n- work/b.md: y\n"
	delta := extractBookDelta(text)
	if !strings.Contains(delta, "areas/a.md") || !strings.Contains(delta, "work/b.md") {
		t.Fatalf("a final delta was not read whole: %q", delta)
	}
}

func TestBookDeltaNoneIsDistinguishableFromMissing(t *testing.T) {
	if got := extractBookDelta("done\n\n## Book delta\n- (none)\n"); got != owner.BookDeltaNone {
		t.Fatalf("none delta = %q", got)
	}
	if got := extractBookDelta("done\n\n## Book delta\n"); got != "" {
		t.Fatalf("empty section = %q", got)
	}
	if got := extractBookDelta("done, nothing else"); got != "" {
		t.Fatalf("absent section = %q", got)
	}
}

// The report states the position against the project's canonical ref: "feat/x"
// alone does not say whether the delta goes to areas/ or to work/.
func TestReportMessageStatesTheDeltaAndThePosition(t *testing.T) {
	own := owner.Owner{CanonicalRef: "master"}
	text := reportsMessage(own, []owner.Report{
		{SessionID: "s1", Title: "with delta", Status: callbackStatusDone,
			BookDelta: "- areas/a.md: x", GitAvailable: true, Branch: "feat/x", Head: "abc1234", Dirty: true},
		{SessionID: "s2", Title: "no delta", Status: callbackStatusDone},
		{SessionID: "s3", Title: "none", Status: callbackStatusDone, BookDelta: owner.BookDeltaNone},
	})
	for _, want := range []string{
		"canonical: master · branch: feat/x @ abc1234 (uncommitted changes)",
		"book delta: - areas/a.md: x",
		"book delta: missing",
		"book delta: none",
		"git: unavailable",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report message missing %q:\n%s", want, text)
		}
	}
	// An owner whose canonical ref could not be detected says so rather than
	// letting the owner assume a default.
	unknown := reportsMessage(owner.Owner{}, []owner.Report{
		{SessionID: "s1", Status: callbackStatusDone, GitAvailable: true, Branch: "feat/x"},
	})
	if !strings.Contains(unknown, "canonical: unknown · branch: feat/x") {
		t.Fatalf("unknown canonical ref = %s", unknown)
	}
}

// git facts are all-or-none. A partial answer — a branch with no head, or a
// status that timed out — reads exactly like verified, committed work.
func TestGitPositionIsAllOrNone(t *testing.T) {
	if got := gitPosition(t.TempDir()); got.Available {
		t.Fatalf("a directory that is not a repository reported a position: %+v", got)
	}

	// A working directory that disappeared between the outcome and the report.
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if got := gitPosition(gone); got.Available {
		t.Fatalf("a deleted worktree reported a position: %+v", got)
	}

	// A git that never answers: every call has to be bounded, and a timeout on
	// any of the three makes the whole answer unavailable rather than a clean
	// tree. `status` is the one that used to be ignored.
	stub := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  *status*) sleep 30 ;;\n  *abbrev-ref*) echo feat/x ;;\n  *short*) echo abc1234 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(stub, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	previous := gitPositionTimeout
	gitPositionTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitPositionTimeout = previous })

	start := time.Now()
	got := gitPosition(t.TempDir())
	if got.Available || got.Branch != "" || got.Dirty {
		t.Fatalf("a git that hung reported %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("gitPosition waited %s on a hung git", elapsed)
	}
}
