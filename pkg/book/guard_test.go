package book

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
)

// guardFixture puts the book where a real one lives, under the home directory,
// so ~, $HOME and the absolute spelling all name the same place.
func guardFixture(t *testing.T) (home, dir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home, filepath.Join(home, ".config", "moa", "codebases", "abc123", "book")
}

func bash(cmd string) map[string]any { return map[string]any{"command": cmd} }

// These are the shapes of the writes a real owner made through bash, taken from
// its transcript, with the business text replaced.
func TestGuardRefusesTheWritesOwnersReallyMake(t *testing.T) {
	_, dir := guardFixture(t)
	tilde := "~/.config/moa/codebases/abc123/book"
	cases := map[string]string{
		"cd tilde && echo >> relative":  `cd ` + tilde + ` && echo '- **10-nov:** something decided' >> work/topic.md`,
		"cd abs && echo >> relative":    `cd ` + dir + ` && echo "- **10-nov:** x" >> areas/a/b.md`,
		"cd && cat > heredoc":           "cd " + tilde + " && cat > decisions/2026-11-x.md <<'EOF'\n# T\nbody\nEOF",
		"cd && cat >> heredoc":          "cd " + tilde + " && cat >> work/x.md <<'EOF'\nmore\nEOF",
		"cd && python heredoc":          "cd " + tilde + " && python3 - <<'PY'\nfrom pathlib import Path\np = Path('PROJECT.md')\np.write_text(p.read_text().replace('a', 'b'))\nPY",
		"cd && python open w":           "cd " + tilde + " && python3 - <<'PY'\nwith open('work/x.md', 'a') as f:\n    f.write('x')\nPY",
		"python -c abs":                 `python3 -c "open('` + dir + `/PROJECT.md','w').write('x')"`,
		"python heredoc abs path":       "python3 - <<'PY'\nopen('" + dir + "/work/a.md', 'a').write('x')\nPY",
		"cd && sed -i":                  `cd ` + tilde + ` && sed -i 's/old/new/' PROJECT.md`,
		"cd && sed -i.bak":              `cd ` + dir + ` && sed -i.bak -e 's/old/new/' work/a.md`,
		"var then sed -i":               `D=` + dir + `; sed -i 's/a/b/' $D/PROJECT.md`,
		"var then echo >>":              `D=` + dir + `; echo hi >> "$D/work/a.md"`,
		"HOME spelling":                 `echo hi >> $HOME/.config/moa/codebases/abc123/book/work/a.md`,
		"absolute redirect":             `echo hi > ` + dir + `/people.md`,
		"tee":                           `echo hi | tee -a ` + dir + `/work/a.md`,
		"cd && tee":                     `cd ` + tilde + ` && printf x | tee work/a.md`,
		"cp into":                       `cp /tmp/draft.md ` + dir + `/work/a.md`,
		"cd && cp from tmp":             `cd ` + tilde + ` && cp /tmp/draft.md work/a.md`,
		"mv into":                       `mv /tmp/draft.md ` + dir + `/work/a.md`,
		"cd && mv inside":               `cd ` + tilde + ` && mv work/a.md work/b.md`,
		"rm":                            `rm ` + dir + `/work/a.md`,
		"cd && rm":                      `cd ` + tilde + ` && rm -f work/a.md`,
		"touch":                         `cd ` + tilde + ` && mkdir -p areas/x && touch areas/x/README.md`,
		"stderr redirect does not hide": `cd ` + tilde + ` && echo x >> work/a.md 2>/dev/null`,
		"after a read":                  `cd ` + tilde + ` && grep -n foo work/a.md && echo bar >> work/a.md`,
		"bash -c":                       `bash -c "cd ` + tilde + ` && echo x >> a.md"`,
		"in a pipeline after cd":        `cd ` + tilde + ` && cat work/a.md | sed 's/a/b/' > work/b.md`,
		"perl -pi":                      `cd ` + tilde + ` && perl -pi -e 's/a/b/' work/a.md`,
		"find -delete":                  `find ` + dir + ` -name '*.bak' -delete`,
		"dd":                            `dd if=/tmp/x of=` + dir + `/a.md`,
		"name from a substitution":      `cd ` + tilde + ` && f=$(ls work | grep -i x | head -1); echo x >> work/$f`,
		"bare variable after cd":        `cd ` + tilde + ` && f=$(ls decisions | head -1); echo x >> "$f"`,
		"export then heredoc":           "export CB=~/.config/moa/codebases/abc123\nF=\"$CB/book/decisions/a.md\"\ncat > \"$F\" <<'EOF'\nx\nEOF",
		"write inside a substitution":   `echo $(echo x >> ` + dir + `/a.md)`,
		"mv into the directory":         `mv /tmp/draft.md ` + dir + `/`,
		"quoted substitution":           `echo "$(echo note >> ` + dir + `/work/a.md)"`,
		"shell heredoc":                 "bash <<'SH'\necho note >> " + dir + "/work/a.md\nSH",
		"inside if":                     `if test -f ` + dir + `/a.md; then sed -i 's/old/new/' ` + dir + `/a.md; fi`,
		"find exec":                     `find ` + dir + ` -name a.md -exec sed -i 's/old/new/' {} +`,
		"pathlib open w":                `cd ` + tilde + ` && python3 -c "from pathlib import Path; Path('work/a.md').open('w').write('x')"`,
		"python explicit mode":          `cd ` + tilde + ` && python3 -c "open('work/a.md', mode='w').write('x')"`,
		"newline separated":             "cd " + tilde + "\necho x >> work/a.md",
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			if !WritesBook(dir, "bash", bash(cmd)) {
				t.Errorf("not refused: %s", cmd)
			}
		})
	}
}

func TestGuardLeavesReadingAndOtherWorkAlone(t *testing.T) {
	_, dir := guardFixture(t)
	tilde := "~/.config/moa/codebases/abc123/book"
	cases := map[string]string{
		"cd && grep":                      `cd ` + tilde + ` && grep -rn -i "foo" work | head -20`,
		"cd && cat":                       `cd ` + tilde + ` && cat work/a.md`,
		"cd && sed -n":                    `cd ` + tilde + ` && sed -n 1,40p decisions/x.md`,
		"cd && ls wc":                     `cd ` + tilde + ` && ls work | wc -l`,
		"cd && head":                      `cd ` + tilde + ` && head -50 PROJECT.md`,
		"read into tmp":                   `cat ` + dir + `/PROJECT.md > /tmp/copy.md`,
		"cd && cat > tmp":                 `cd ` + tilde + ` && cat work/a.md > /tmp/a.md`,
		"cp out of the book":              `cp ` + dir + `/work/a.md /tmp/a.md`,
		"cd && cp out":                    `cd ` + tilde + ` && cp -a . /tmp/snapshot`,
		"stderr to null":                  `cd ` + tilde + ` && grep foo work/a.md 2>/dev/null`,
		"2>&1":                            `cd ` + tilde + ` && grep foo work/a.md 2>&1 | head`,
		"quoted arrow is text":            `cd ` + tilde + ` && grep -n 'a >> b' work/a.md`,
		"heredoc body is data":            "cat > /tmp/x.md <<'EOF'\necho x >> " + dir + "/a.md\nEOF",
		"sed -n -i-free":                  `sed -n '1,5p' ` + dir + `/PROJECT.md`,
		"python reads":                    "cd " + tilde + " && python3 - <<'PY'\nfrom pathlib import Path\nprint(len(Path('PROJECT.md').read_text()))\nPY",
		"python writes elsewhere":         "python3 - <<'PY'\nopen('/tmp/out.txt', 'w').write('x')\nPY",
		"writes in the repo":              `cd ~/dev/project/main && echo x >> notes.md`,
		"write after leaving":             `cd ` + tilde + ` && cat a.md; cd /tmp && echo x >> a.md`,
		"snapshot recipe":                 `cp -a "$CB/book" "$TMP/book" && mv "$TMP" "$DEST"`,
		"sed -i outside":                  `cd /tmp && sed -i 's/a/b/' x.md`,
		"restore swaps the book":          `mv -T ` + dir + ` /tmp/old && mv -T /tmp/new ` + dir,
		"git in the repo":                 `cd ~/dev/project/main && git status`,
		"subshell cd does not leak":       `(cd ` + tilde + ` && ls) && echo x >> a.md`,
		"temp dir beside the book":        `CB=~/.config/moa/codebases/abc123; NEW=$(mktemp -d "$CB/book.tidy.XXXXXX"); cd ` + tilde + ` && cp -a /tmp/src/. "$NEW/"`,
		"python reads a book file":        `cd ` + tilde + ` && python3 -c "print(open('work/a.md').read())"`,
		"child shell cd does not leak":    `bash -c 'cd ` + dir + ` && ls'; echo x >> notes.md`,
		"subshell variable does not leak": `D=/tmp; (D=` + dir + `; ls "$D"); echo x >> "$D/out.md"`,
		"node reads":                      `cd ` + tilde + ` && node -e "console.log(require('fs').readFileSync('work/a.md','utf8').replace('old','new'))"`,
		"ruby reads":                      `cd ` + tilde + ` && ruby -e "puts File.open('work/a.md', 'r').read"`,
		"python reads and replaces":       `cd ` + tilde + ` && python3 -c "print(open('work/a.md').read().replace('old','new'))"`,
		"find exec read":                  `find ` + dir + ` -name '*.md' -exec grep -l foo {} +`,
		"empty":                           ``,
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			if WritesBook(dir, "bash", bash(cmd)) {
				t.Errorf("refused: %s", cmd)
			}
		})
	}
}

// The shell may already be inside the book when a command starts: the call
// names the directory, or a previous call left it there.
func TestGuardStartsFromTheShellsDirectory(t *testing.T) {
	_, dir := guardFixture(t)
	if !WritesBookFrom(dir, dir, "bash", bash("echo note >> work/a.md")) {
		t.Error("a relative write from inside the book was not refused")
	}
	if WritesBookFrom(dir, "/tmp", "bash", bash("echo note >> work/a.md")) {
		t.Error("a relative write from elsewhere was refused")
	}
	if WritesBookFrom(dir, dir, "bash", bash("cat work/a.md | head")) {
		t.Error("a read from inside the book was refused")
	}
}

func TestGuardCoversTheFileToolsToo(t *testing.T) {
	_, dir := guardFixture(t)
	for tool, args := range map[string]map[string]any{
		"write":       {"path": dir + "/PROJECT.md"},
		"edit":        {"path": "~/.config/moa/codebases/abc123/book/a.md"},
		"multiedit":   {"path": dir + "/work/a.md"},
		"apply_patch": {"patch": "*** Begin Patch\n*** Update File: " + dir + "/a.md\n*** End Patch"},
	} {
		if !WritesBook(dir, tool, args) {
			t.Errorf("%s into the book not refused", tool)
		}
	}
	move := map[string]any{"patch": "*** Begin Patch\n*** Update File: /tmp/draft.md\n*** Move to: " + dir + "/a.md\n*** End Patch"}
	if !WritesBook(dir, "apply_patch", move) {
		t.Error("a patch moving a file into the book was not refused")
	}
	if WritesBook(dir, "write", map[string]any{"path": "/tmp/x.md"}) || WritesBook(dir, "read", map[string]any{"path": dir + "/PROJECT.md"}) {
		t.Error("an unrelated call was refused")
	}
}

// The hook is what the agent loop really calls: the refusal reaches the model
// with the pointer to the book tool.
func TestGuardExtensionBlocksThroughTheHost(t *testing.T) {
	_, dir := guardFixture(t)
	host := extension.NewHost(core.NewRegistry(), nil)
	if err := host.Load(GuardExtension(dir, nil)); err != nil {
		t.Fatal(err)
	}
	d := host.FireToolCall(context.Background(), "bash", bash("echo x >> "+dir+"/a.md"))
	if d == nil || !d.Block || !strings.Contains(d.Reason, "book tool") {
		t.Fatalf("decision = %+v", d)
	}
	if d := host.FireToolCall(context.Background(), "bash", bash("grep x "+dir+"/a.md")); d != nil {
		t.Fatalf("a read was blocked: %+v", d)
	}
}

// One assistant turn can carry `cd BOOK` and the write after it: both are
// checked before either runs, while the shell is still elsewhere.
func TestGuardSeesACdAndAWriteInTheSameBatch(t *testing.T) {
	_, dir := guardFixture(t)
	cwd := "/tmp"
	host := extension.NewHost(core.NewRegistry(), nil)
	if err := host.Load(GuardExtension(dir, func(context.Context, string, map[string]any) string { return cwd })); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if d := host.FireToolCall(ctx, "bash", bash("cd "+dir)); d != nil {
		t.Fatalf("a bare cd was blocked: %+v", d)
	}
	if d := host.FireToolCall(ctx, "bash", bash("echo note >> work/a.md")); d == nil || !d.Block {
		t.Fatal("the write after a cd in the same batch was allowed")
	}
	// The batch ran: the shell is in the book, then leaves it.
	cwd = dir
	if d := host.FireToolCall(ctx, "bash", bash("cd /tmp")); d != nil {
		t.Fatalf("leaving the book was blocked: %+v", d)
	}
	cwd = "/tmp"
	if d := host.FireToolCall(ctx, "bash", bash("echo note >> notes.md")); d != nil {
		t.Fatalf("a write after leaving the book was blocked: %+v", d)
	}
}

// File tools resolve against the workspace root, whatever directory the shell
// has wandered into.
func TestGuardResolvesFileToolsFromTheWorkspace(t *testing.T) {
	home, dir := guardFixture(t)
	project := filepath.Join(home, "project")
	rel, _ := filepath.Rel(project, filepath.Join(dir, "new.md"))
	if !WritesBookFrom(dir, project, "write", map[string]any{"path": rel}) {
		t.Errorf("relative path %s into the book not refused", rel)
	}
	if WritesBookFrom(dir, project, "write", map[string]any{"path": "notes.md"}) {
		t.Error("a workspace write was refused")
	}
}

func TestGuardBatchWithAReadBetweenAndExplicitCwd(t *testing.T) {
	home, dir := guardFixture(t)
	repo := filepath.Join(home, "repo")
	newHost := func() *extension.Host {
		host := extension.NewHost(core.NewRegistry(), nil)
		if err := host.Load(GuardExtension(dir, func(context.Context, string, map[string]any) string { return repo })); err != nil {
			t.Fatal(err)
		}
		return host
	}
	ctx := context.Background()

	host := newHost()
	host.FireToolCall(ctx, "bash", bash("cd "+dir))
	if d := host.FireToolCall(ctx, "bash", bash("cat work/a.md")); d != nil {
		t.Fatalf("a read was blocked: %+v", d)
	}
	if d := host.FireToolCall(ctx, "bash", bash("echo x >> work/a.md")); d == nil {
		t.Fatal("a read between the cd and the write hid the cd")
	}

	// A call that names its own directory is judged from it alone, whatever
	// earlier calls did.
	host = newHost()
	host.FireToolCall(ctx, "bash", bash("cd "+dir+" && ls"))
	if d := host.FireToolCall(ctx, "bash", map[string]any{"command": "echo x >> notes.md", "cwd": repo}); d != nil {
		t.Fatalf("a write with an explicit repo cwd was blocked: %+v", d)
	}

	// Once the shell's own directory is seen to change, earlier guesses go.
	host = newHost()
	host.FireToolCall(ctx, "bash", bash("cd "+dir))
	host2 := extension.NewHost(core.NewRegistry(), nil)
	cwd := dir
	if err := host2.Load(GuardExtension(dir, func(context.Context, string, map[string]any) string { return cwd })); err != nil {
		t.Fatal(err)
	}
	host2.FireToolCall(ctx, "bash", bash("cd /tmp"))
	cwd = repo
	if d := host2.FireToolCall(ctx, "bash", bash("echo x >> notes.md")); d != nil {
		t.Fatalf("a repo write after the shell left the book was blocked: %+v", d)
	}
}
