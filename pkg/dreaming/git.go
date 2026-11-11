package dreaming

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The codebase repository is an object store and nothing else: moa never
// checks a tree out and never uses the repository's index. Blobs are written
// from the exact bytes moa read or approved (hash-object --no-filters), trees
// are built in a throwaway index, and a commit is published with a
// compare-and-swap on the ref. That is what makes a commit mean "these bytes",
// whatever someone wrote on disk around it, and keeps the user's global git
// configuration (autocrlf, filters, hooks, signing) out of it.

// Ref is the branch the nightly history lives on.
const Ref = "refs/heads/main"

// ErrRefMoved reports that the ref was not where the caller expected it.
var ErrRefMoved = errors.New("dreaming: the history ref moved")

type gitRepo struct {
	dir string
	// hold is passed to every git child, so a child still running after this
	// process died keeps the run lock until it exits (see recovery).
	hold *os.File
}

func (r gitRepo) run(stdin []byte, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", r.dir,
		"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
		"-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "-c", "gc.auto=0"}, args...)...)
	cmd.Env = append(gitEnv(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if r.hold != nil {
		cmd.ExtraFiles = []*os.File{r.hold}
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// gitEnv is a minimal environment: no global or system configuration, a fixed
// identity, and nothing inherited that could point git somewhere else.
func gitEnv() []string {
	env := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=moa dreaming", "GIT_AUTHOR_EMAIL=dreaming@moa.invalid",
		"GIT_COMMITTER_NAME=moa dreaming", "GIT_COMMITTER_EMAIL=dreaming@moa.invalid",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}
	return env
}

func openRepo(dir string, hold *os.File) (gitRepo, error) {
	r := gitRepo{dir: dir, hold: hold}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return r, nil
	}
	if _, err := r.run(nil, nil, "init", "-q", "--initial-branch=main"); err != nil {
		return r, err
	}
	return r, nil
}

// head returns the commit the ref points at, "" when there is none yet.
func (r gitRepo) head() (string, error) {
	out, err := r.run(nil, nil, "rev-parse", "-q", "--verify", Ref)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r gitRepo) hashObject(data []byte) (string, error) {
	out, err := r.run(data, nil, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r gitRepo) blob(sha string) ([]byte, error) {
	return r.run(nil, nil, "cat-file", "blob", sha)
}

// treeOf lists a commit's files as path → blob ("" commit: empty).
func (r gitRepo) treeOf(commit string) (map[string]string, error) {
	out := map[string]string{}
	if commit == "" {
		return out, nil
	}
	raw, err := r.run(nil, nil, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	for _, rec := range bytes.Split(raw, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		fields := strings.Fields(string(rec[:tab]))
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		out[string(rec[tab+1:])] = fields[2]
	}
	return out, nil
}

func (r gitRepo) treeSHA(commit string) (string, error) {
	if commit == "" {
		return "", nil
	}
	out, err := r.run(nil, nil, "rev-parse", commit+"^{tree}")
	return strings.TrimSpace(string(out)), err
}

// buildTree writes a tree with exactly files (path → blob), using a private
// index file so the repository's own index is never involved.
func (r gitRepo) buildTree(files map[string]string) (string, error) {
	idx, err := os.CreateTemp(filepath.Join(r.dir, ".git"), "dreaming-index-*")
	if err != nil {
		return "", err
	}
	name := idx.Name()
	_ = idx.Close()
	_ = os.Remove(name) // git wants to create it
	defer func() { _ = os.Remove(name) }()
	env := []string{"GIT_INDEX_FILE=" + name}
	var info bytes.Buffer
	for path, sha := range files {
		fmt.Fprintf(&info, "100644 %s\t%s\x00", sha, path)
	}
	if _, err := r.run(info.Bytes(), env, "update-index", "-z", "--add", "--index-info"); err != nil {
		return "", err
	}
	out, err := r.run(nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r gitRepo) commitTree(tree, parent, msg string) (string, error) {
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	out, err := r.run([]byte(msg), nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// publish moves the ref from old to next, atomically and only if it is still
// at old ("" = must not exist yet).
func (r gitRepo) publish(next, old string) error {
	oldArg := old
	if oldArg == "" {
		oldArg = strings.Repeat("0", 40)
	}
	if _, err := r.run(nil, nil, "update-ref", Ref, next, oldArg); err != nil {
		return fmt.Errorf("%w: %v", ErrRefMoved, err)
	}
	return nil
}

func (r gitRepo) message(commit string) (string, error) {
	out, err := r.run(nil, nil, "cat-file", "commit", commit)
	if err != nil {
		return "", err
	}
	if i := bytes.Index(out, []byte("\n\n")); i >= 0 {
		return string(out[i+2:]), nil
	}
	return "", nil
}
