package dreaming

import (
	"crypto/sha1" //nolint:gosec // git's object id, not a security boundary
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/e-aleixandre/moa/pkg/storelock"
)

// Store is one codebase directory (~/.config/moa/codebases/<key>): the owner's
// book and the project's memory, the history repository beside them, and the
// dreaming's own working files.
type Store struct {
	Codebase string
}

func (s Store) bookDir() string   { return filepath.Join(s.Codebase, "book") }
func (s Store) memoryDir() string { return filepath.Join(s.Codebase, "memory") }
func (s Store) workDir() string   { return filepath.Join(s.Codebase, "dreaming") }
func (s Store) journalPath() string {
	return filepath.Join(s.workDir(), "journal.json")
}
func (s Store) statePath() string { return filepath.Join(s.workDir(), "state.json") }

// Stores the history covers, as the top directory of a repository path.
const (
	bookPrefix   = "book/"
	memoryPrefix = "memory/"
)

// ErrBusy reports that a night (or an undo) holds the run lock.
var ErrBusy = errors.New("a dreaming is in progress; try again later")

// Protected reports whether a book path is never consolidated (I2): the user's
// decisions, the user's file, the index, history kept by hand, and the archive
// itself. The archive only ever gains whole new files.
func Protected(rel string) bool {
	switch rel {
	case "PROJECT.md", "OWNER.md":
		return true
	}
	for _, p := range []string{"decisions/", "archivo/", "historico/"} {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// gitBlobID is git's object id for a blob with these bytes (SHA-1 object
// format), computed without running git so a comparison under the lock is a
// read and a hash.
func gitBlobID(data []byte) string {
	h := sha1.New() //nolint:gosec
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// Run is one exclusive session over the store: a night, an undo or a recovery.
// It holds the run lock for its whole life; the book and memory locks are taken
// only around the short sections that read a snapshot or write.
type Run struct {
	s    Store
	run  *os.File
	repo gitRepo
}

// Begin takes the run lock (waiting when wait is true, otherwise failing with
// ErrBusy), opens or creates the history repository and recovers whatever a
// previous process left half done.
func (s Store) Begin(wait bool, pub Publisher) (*Run, Report, error) {
	if !storelock.Supported {
		return nil, Report{}, errors.New("dreaming needs file locking, which this platform lacks")
	}
	if err := os.MkdirAll(s.workDir(), 0o700); err != nil {
		return nil, Report{}, err
	}
	var f *os.File
	var err error
	if wait {
		f, err = storelock.Lock(s.workDir())
	} else {
		var ok bool
		f, ok, err = storelock.TryLock(s.workDir())
		if err == nil && !ok {
			return nil, Report{}, ErrBusy
		}
	}
	if err != nil {
		return nil, Report{}, err
	}
	repo, err := openRepo(s.Codebase, f)
	if err != nil {
		_ = f.Close()
		return nil, Report{}, err
	}
	r := &Run{s: s, run: f, repo: repo}
	rep, err := r.recover(pub)
	if err != nil {
		_ = f.Close()
		return nil, rep, err
	}
	return r, rep, nil
}

// Close releases the run lock.
func (r *Run) Close() error { return r.run.Close() }

// lockStores takes book then memory, the fixed order after run.
func (r *Run) lockStores() (func(), error) {
	b, err := storelock.Lock(r.s.bookDir())
	if err != nil {
		return nil, err
	}
	m, err := storelock.Lock(r.s.memoryDir())
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	return func() { _ = m.Close(); _ = b.Close() }, nil
}

// Snapshot is S0: every regular file of the book and the memory as it was,
// committed with the exact bytes read.
type Snapshot struct {
	Commit  string
	Files   map[string][]byte // repository path → bytes
	Blobs   map[string]string // repository path → blob id
	Skipped []string          // symlinks and other non-regular entries, never touched
}

// Snapshot reads both stores under their locks and commits them as S0, or
// reuses the current commit when nothing changed.
func (r *Run) Snapshot(title string) (Snapshot, error) {
	unlock, err := r.lockStores()
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	snap := Snapshot{Files: map[string][]byte{}, Blobs: map[string]string{}}
	// Only the book for now: memory is not consolidated yet, and versioning it
	// would keep a secret a fact holds in the history after the fact is gone,
	// which the user has not decided.
	for _, store := range []struct{ dir, prefix string }{{r.s.bookDir(), bookPrefix}} {
		if err := walkStore(store.dir, func(rel string, data []byte, regular bool) error {
			if !regular {
				snap.Skipped = append(snap.Skipped, store.prefix+rel)
				return nil
			}
			id, err := r.repo.hashObject(data)
			if err != nil {
				return err
			}
			snap.Files[store.prefix+rel] = data
			snap.Blobs[store.prefix+rel] = id
			return nil
		}); err != nil {
			return Snapshot{}, err
		}
	}
	head, err := r.repo.head()
	if err != nil {
		return Snapshot{}, err
	}
	tree, err := r.repo.buildTree(snap.Blobs)
	if err != nil {
		return Snapshot{}, err
	}
	if headTree, err := r.repo.treeSHA(head); err != nil {
		return Snapshot{}, err
	} else if head != "" && headTree == tree {
		snap.Commit = head
		return snap, nil
	}
	commit, err := r.repo.commitTree(tree, head, title)
	if err != nil {
		return Snapshot{}, err
	}
	if err := r.repo.publish(commit, head); err != nil {
		return Snapshot{}, err
	}
	snap.Commit = commit
	return snap, nil
}

// walkStore visits every file under dir except dot entries (locks, temporary
// files). A symlink is reported as not regular and never followed.
func walkStore(dir string, visit func(rel string, data []byte, regular bool) error) error {
	err := filepath.WalkDir(dir, func(full string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && full == dir {
				return filepath.SkipDir
			}
			return err
		}
		if full == dir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return visit(rel, nil, false)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		return visit(rel, data, true)
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

var partPattern = regexp.MustCompile(`^(.+)/parte-[0-9]+\.md$`)

// Units groups the snapshot's book files into units: a sheet x.md and the
// x/parte-NN.md files book-tidy split it into. Protected paths are not units.
func (snap Snapshot) Units() []Unit {
	var sheets []string
	parts := map[string][]string{}
	for p := range snap.Files {
		rel, ok := strings.CutPrefix(p, bookPrefix)
		if !ok || !strings.HasSuffix(rel, ".md") || Protected(rel) {
			continue
		}
		if m := partPattern.FindStringSubmatch(rel); m != nil {
			if _, ok := snap.Files[bookPrefix+m[1]+".md"]; ok {
				parts[m[1]+".md"] = append(parts[m[1]+".md"], rel)
				continue
			}
		}
		sheets = append(sheets, rel)
	}
	sort.Strings(sheets)
	out := make([]Unit, 0, len(sheets))
	for _, s := range sheets {
		ps := parts[s]
		sort.Strings(ps)
		out = append(out, Unit{Sheet: s, Files: append([]string{s}, ps...)})
	}
	return out
}

// Hash identifies a unit's content in the snapshot: its paths and blobs.
func (snap Snapshot) UnitHash(u Unit) string {
	m := map[string]string{}
	for _, f := range u.Files {
		m[bookPrefix+f] = snap.Blobs[bookPrefix+f]
	}
	return unitHash(m)
}

func unitHash(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%s\n", p, files[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Orig returns a unit's original bytes, one slice per file.
func (snap Snapshot) Orig(u Unit) [][]byte {
	out := make([][]byte, len(u.Files))
	for i, f := range u.Files {
		out[i] = snap.Files[bookPrefix+f]
	}
	return out
}

// SourceExists reports whether path:line names a line of the snapshot's book.
func (snap Snapshot) SourceExists(path string, line int) bool {
	data, ok := snap.Files[bookPrefix+path]
	return ok && line >= 1 && line <= len(splitLines(data))
}

// Change is one path of a group: its expected content before and after, as
// blob ids ("" = the path does not exist). The bytes live in the object store,
// so a journal of ids is enough to finish or roll back.
type Change struct {
	Path string `json:"path"`
	Pre  string `json:"pre"`
	Post string `json:"post"`
}

// Group is the smallest set of paths that is applied, undone or rolled back
// together: a unit with its new archive files, or a fact with its copy.
// Sources are other paths the plan relied on; they must still be what the
// models read.
type Group struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"` // "unit" or "fact"
	Changes []Change `json:"changes"`
	Sources []Change `json:"sources,omitempty"` // Pre only
	// After is the state the group leaves its unit or fact in (see state.json).
	After string `json:"after,omitempty"`
}

// UnitGroup turns a verified output into the group that applies it.
func (r *Run) UnitGroup(snap Snapshot, u Unit, out Output, sources []string) (Group, map[string][]byte, error) {
	g := Group{ID: bookPrefix + u.Sheet, Kind: "unit"}
	content := map[string][]byte{}
	after := map[string]string{}
	paths := make([]string, 0, len(out.Files))
	for p := range out.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		data := out.Files[rel]
		if rel != u.Sheet && !strings.HasPrefix(rel, "archivo/") {
			return Group{}, nil, fmt.Errorf("unit %s writes %s", u.Sheet, rel)
		}
		id, err := r.repo.hashObject(data)
		if err != nil {
			return Group{}, nil, err
		}
		rp := bookPrefix + rel
		g.Changes = append(g.Changes, Change{Path: rp, Pre: snap.Blobs[rp], Post: id})
		content[id] = data
		if rel == u.Sheet {
			after[rp] = id
		}
	}
	for _, rel := range out.Remove {
		rp := bookPrefix + rel
		g.Changes = append(g.Changes, Change{Path: rp, Pre: snap.Blobs[rp]})
	}
	for _, src := range sources {
		rp := bookPrefix + src
		if id, ok := snap.Blobs[rp]; ok {
			g.Sources = append(g.Sources, Change{Path: rp, Pre: id})
		}
	}
	g.After = unitHash(after)
	return g, content, nil
}

// Publisher delivers the morning news. It must be idempotent on News.Origin:
// a recovery may deliver the same news again.
type Publisher func(News) error

// News is what the user reads in For you.
type News struct {
	Key    string `json:"key"`
	Origin string `json:"origin"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// Finish is what has to happen once a commit is published: the state the run
// leaves behind and the news. It is in the journal so a crash after publishing
// still ends with both.
type Finish struct {
	Run   string            `json:"run"`
	Units map[string]string `json:"units,omitempty"` // state key → hash
	News  *News             `json:"news,omitempty"`
}

type journal struct {
	Kind   string  `json:"kind"` // "apply" or "undo"
	Run    string  `json:"run"`
	Old    string  `json:"old"`
	New    string  `json:"new"`
	Groups []Group `json:"groups"`
	Finish Finish  `json:"finish"`
}

// Report is what an apply, undo or recovery did, for the news and the logs.
type Report struct {
	Commit    string
	Applied   []string
	Conflicts []string // group ids left alone because something else wrote them
	Recovered string   // "", "finished" or "rolled back"
}

// testHookAfterWrite, when set, runs after each file write of an apply, undo
// or rollback; an error simulates a crash at that point.
var testHookAfterWrite func(step int) error

// errCrash is how a test hook stops a write sequence.
var errCrash = errors.New("simulated crash")

// liveBlob is the current content id of a repository path ("" = absent). A
// symlink anywhere below the store, or a non-regular file, is an error: the
// dreaming writes only real files inside the store (I6).
func (s Store) liveBlob(path string) (string, []byte, error) {
	full, err := s.confined(path)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", nil, err
	}
	return gitBlobID(data), data, nil
}

// confined maps a repository path to disk, refusing anything outside the two
// stores and any symlink on the way.
func (s Store) confined(path string) (string, error) {
	var root, rel string
	switch {
	case strings.HasPrefix(path, bookPrefix):
		root, rel = s.bookDir(), strings.TrimPrefix(path, bookPrefix)
	case strings.HasPrefix(path, memoryPrefix):
		root, rel = s.memoryDir(), strings.TrimPrefix(path, memoryPrefix)
	default:
		return "", fmt.Errorf("%s is outside the book and the memory", path)
	}
	if rel == "" || strings.Contains(rel, "\\") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid path %q", path)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("invalid path %q", path)
		}
	}
	cur := root
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink", root)
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a plain directory", cur)
		}
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

// allowedChange enforces I2 on a change, whatever produced it: protected book
// paths are never rewritten; the archive only gains or loses whole files.
func allowedChange(c Change) error {
	rel, ok := strings.CutPrefix(c.Path, bookPrefix)
	if !ok {
		return nil
	}
	if !Protected(rel) {
		return nil
	}
	if strings.HasPrefix(rel, "archivo/") && (c.Pre == "") != (c.Post == "") {
		return nil
	}
	return fmt.Errorf("%s is protected", c.Path)
}

// commitMessage carries the manifest of a commit's groups, so an undo needs
// nothing but the history.
func commitMessage(title, kind, run string, groups []Group) (string, error) {
	manifest, err := json.Marshal(groups)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\n\nDreaming-Run: %s\nDreaming-Kind: %s\nDreaming-Groups: %s\n", title, run, kind, manifest), nil
}

func parseMessage(msg string) (kind, run string, groups []Group, err error) {
	for _, line := range strings.Split(msg, "\n") {
		switch {
		case strings.HasPrefix(line, "Dreaming-Run: "):
			run = strings.TrimPrefix(line, "Dreaming-Run: ")
		case strings.HasPrefix(line, "Dreaming-Kind: "):
			kind = strings.TrimPrefix(line, "Dreaming-Kind: ")
		case strings.HasPrefix(line, "Dreaming-Groups: "):
			err = json.Unmarshal([]byte(strings.TrimPrefix(line, "Dreaming-Groups: ")), &groups)
		}
	}
	if err == nil && (run == "" || kind == "") {
		err = errors.New("not a dreaming commit")
	}
	return kind, run, groups, err
}

// Apply writes the accepted groups of a night (see commit) on top of the
// snapshot. content maps every post blob to its bytes.
//
// news, when not nil, writes the morning news once the accepted groups and the
// commit are known; it goes into the journal with the rest of the ending.
func (r *Run) Apply(snap Snapshot, title string, groups []Group, content map[string][]byte, fin Finish, news func(Report) *News, pub Publisher) (Report, error) {
	return r.commit("apply", snap.Commit, title, groups, content, fin, news, pub)
}

// commit is the single protocol of apply and undo, under book+memory:
// check every group against the disk, keep the ones whose paths (and sources)
// are exactly what was expected, build the commit and the journal with those
// alone, then write, publish and finish.
func (r *Run) commit(kind, base, title string, groups []Group, content map[string][]byte, fin Finish, news func(Report) *News, pub Publisher) (Report, error) {
	unlock, err := r.lockStores()
	if err != nil {
		return Report{}, err
	}
	defer unlock()
	head, err := r.repo.head()
	if err != nil {
		return Report{}, err
	}
	if head != base {
		return Report{}, fmt.Errorf("%w: expected %s, found %s", ErrRefMoved, shortSHA(base), shortSHA(head))
	}
	var rep Report
	rejected := map[string]bool{}
	touched := map[string]string{} // path → group id
	for _, g := range groups {
		for _, c := range g.Changes {
			touched[c.Path] = g.ID
		}
	}
	for _, g := range groups {
		for _, c := range g.Changes {
			if err := allowedChange(c); err != nil {
				return Report{}, err
			}
			live, _, err := r.s.liveBlob(c.Path)
			if err != nil || live != c.Pre {
				rejected[g.ID] = true
			}
		}
		for _, src := range g.Sources {
			live, _, err := r.s.liveBlob(src.Path)
			if err != nil || live != src.Pre {
				rejected[g.ID] = true
			}
		}
	}
	// A plan that relied on a source another rejected group would have
	// rewritten is rejected too, until nothing changes.
	for changed := true; changed; {
		changed = false
		for _, g := range groups {
			if rejected[g.ID] {
				continue
			}
			for _, src := range g.Sources {
				if owner, ok := touched[src.Path]; ok && owner != g.ID && rejected[owner] {
					rejected[g.ID] = true
					changed = true
				}
			}
		}
	}
	var accepted []Group
	for _, g := range groups {
		if rejected[g.ID] {
			rep.Conflicts = append(rep.Conflicts, g.ID)
			continue
		}
		accepted = append(accepted, g)
		rep.Applied = append(rep.Applied, g.ID)
	}
	if len(accepted) == 0 {
		if news != nil && pub != nil {
			if n := news(rep); n != nil {
				return rep, pub(*n)
			}
		}
		return rep, nil
	}
	tree, err := r.repo.treeOf(head)
	if err != nil {
		return Report{}, err
	}
	for _, g := range accepted {
		for _, c := range g.Changes {
			if c.Post == "" {
				delete(tree, c.Path)
			} else {
				tree[c.Path] = c.Post
			}
		}
	}
	treeID, err := r.repo.buildTree(tree)
	if err != nil {
		return Report{}, err
	}
	msg, err := commitMessage(title, kind, fin.Run, accepted)
	if err != nil {
		return Report{}, err
	}
	next, err := r.repo.commitTree(treeID, head, msg)
	if err != nil {
		return Report{}, err
	}
	fin.Units = keepUnits(fin.Units, accepted)
	if news != nil {
		fin.News = news(Report{Commit: next, Applied: rep.Applied, Conflicts: rep.Conflicts})
	}
	j := journal{Kind: kind, Run: fin.Run, Old: head, New: next, Groups: accepted, Finish: fin}
	if err := writeJSONAtomic(r.s.journalPath(), j); err != nil {
		return Report{}, err
	}
	step := 0
	for _, g := range accepted {
		if err := r.writeGroup(g, content, false, &step); err != nil {
			return rep, err
		}
	}
	if err := r.repo.publish(next, head); err != nil {
		return rep, err
	}
	rep.Commit = next
	return rep, r.finish(j, pub)
}

// keepUnits drops the state entries of groups that were not accepted.
func keepUnits(units map[string]string, accepted []Group) map[string]string {
	out := map[string]string{}
	for _, g := range accepted {
		if h, ok := units[g.ID]; ok {
			out[g.ID] = h
		}
	}
	return out
}

// writeGroup moves a group's paths from Pre to Post (or, rolling back, from
// Post to Pre). Order keeps I1 at every step: forward, the archive exists
// before the sheet loses its lines and parts go last; backward, what was
// removed or replaced comes back before what was created goes.
func (r *Run) writeGroup(g Group, content map[string][]byte, backward bool, step *int) error {
	type op struct{ path, from, to string }
	var creates, replaces, removes []op
	for _, c := range g.Changes {
		o := op{c.Path, c.Pre, c.Post}
		if backward {
			o = op{c.Path, c.Post, c.Pre}
		}
		switch {
		case o.from == "" && o.to != "":
			creates = append(creates, o)
		case o.to == "":
			removes = append(removes, o)
		default:
			replaces = append(replaces, o)
		}
	}
	// The same order serves both directions once the ops are oriented: bring
	// back or create files first, rewrite next, delete last.
	seq := append(append(append([]op(nil), creates...), replaces...), removes...)
	for _, o := range seq {
		full, err := r.s.confined(o.path)
		if err != nil {
			return err
		}
		if o.to == "" {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return err
			}
			removeEmptyParents(filepath.Dir(full), r.storeRoot(o.path))
		} else {
			data, ok := content[o.to]
			if !ok {
				data, err = r.repo.blob(o.to)
				if err != nil {
					return err
				}
			}
			if gitBlobID(data) != o.to {
				return fmt.Errorf("content of %s does not match %s", o.path, o.to)
			}
			if err := writeFileAtomic(full, data); err != nil {
				return err
			}
		}
		*step++
		if testHookAfterWrite != nil {
			if err := testHookAfterWrite(*step); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Run) storeRoot(path string) string {
	if strings.HasPrefix(path, memoryPrefix) {
		return r.s.memoryDir()
	}
	return r.s.bookDir()
}

// removeEmptyParents removes now-empty directories up to (not including) root.
// os.Remove refuses a non-empty directory, so nothing with content goes.
func removeEmptyParents(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// finish is the one ending of a published commit, normal or recovered:
// state, then news, then the journal goes. Every step is idempotent.
func (r *Run) finish(j journal, pub Publisher) error {
	st, err := r.s.loadState()
	if err != nil {
		return err
	}
	for k, v := range j.Finish.Units {
		st.Units[k] = v
	}
	if err := r.s.saveState(st); err != nil {
		return err
	}
	if j.Finish.News != nil && pub != nil {
		if err := pub(*j.Finish.News); err != nil {
			return fmt.Errorf("publishing the news: %w", err)
		}
	}
	if err := os.Remove(r.s.journalPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(r.s.workDir())
}

// recover finishes or rolls back what a journal describes. Only a run that
// holds the run lock calls it, and every git child of the dead process held
// that lock too, so no git of the previous run is still writing.
func (r *Run) recover(pub Publisher) (Report, error) {
	data, err := os.ReadFile(r.s.journalPath())
	if os.IsNotExist(err) {
		return Report{}, nil
	}
	if err != nil {
		return Report{}, err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return Report{}, fmt.Errorf("the dreaming journal is unreadable; nothing was changed: %w", err)
	}
	unlock, err := r.lockStores()
	if err != nil {
		return Report{}, err
	}
	head, err := r.repo.head()
	if err != nil {
		unlock()
		return Report{}, err
	}
	switch head {
	case j.New:
		unlock()
		return Report{Commit: j.New, Recovered: "finished"}, r.finish(j, pub)
	case j.Old:
	default:
		unlock()
		return Report{}, fmt.Errorf("the dreaming journal expects %s or %s but history is at %s; nothing was changed",
			shortSHA(j.Old), shortSHA(j.New), shortSHA(head))
	}
	defer unlock()
	rep := Report{Recovered: "rolled back"}
	step := 0
	for _, g := range j.Groups {
		clean := true
		for _, c := range g.Changes {
			live, _, err := r.s.liveBlob(c.Path)
			if err != nil || (live != c.Pre && live != c.Post) {
				clean = false
			}
		}
		if !clean {
			rep.Conflicts = append(rep.Conflicts, g.ID)
			continue
		}
		if err := r.writeGroup(g, nil, true, &step); err != nil {
			return rep, err
		}
		rep.Applied = append(rep.Applied, g.ID)
	}
	if len(rep.Conflicts) > 0 {
		failed := filepath.Join(r.s.workDir(), "journal-failed-"+j.Run+".json")
		if err := os.Rename(r.s.journalPath(), failed); err != nil {
			return rep, err
		}
		return rep, syncDir(r.s.workDir())
	}
	if err := os.Remove(r.s.journalPath()); err != nil {
		return rep, err
	}
	return rep, syncDir(r.s.workDir())
}

// Undo reverts the night that produced commit: each of its groups whose paths
// still hold what that night wrote goes back to what it found; any other is
// left whole and reported. The undone units are recorded as processed, so the
// next night does not consolidate them again until someone changes them.
func (s Store) Undo(commit string, pub Publisher) (Report, error) {
	r, _, err := s.Begin(false, pub)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = r.Close() }()
	msg, err := r.repo.message(commit)
	if err != nil {
		return Report{}, fmt.Errorf("%s is not in the dreaming history", commit)
	}
	kind, run, groups, err := parseMessage(msg)
	if err != nil || kind != "apply" {
		return Report{}, fmt.Errorf("%s is not a dreaming night", commit)
	}
	head, err := r.repo.head()
	if err != nil {
		return Report{}, err
	}
	inverse := make([]Group, 0, len(groups))
	units := map[string]string{}
	for _, g := range groups {
		ig := Group{ID: g.ID, Kind: g.Kind}
		before := map[string]string{}
		for _, c := range g.Changes {
			ig.Changes = append(ig.Changes, Change{Path: c.Path, Pre: c.Post, Post: c.Pre})
			if c.Pre != "" && !strings.HasPrefix(c.Path, bookPrefix+"archivo/") {
				before[c.Path] = c.Pre
			}
		}
		units[g.ID] = unitHash(before)
		inverse = append(inverse, ig)
	}
	fin := Finish{Run: run + "-undo", Units: units, News: nil}
	return r.commit("undo", head, "Deshecho el dreaming "+run, inverse, nil, fin, nil, pub)
}

// state.json: what the dreaming already processed, by unit or fact, and which
// civil dates already ran.
type state struct {
	Units  map[string]string `json:"units"`
	Nights map[string]Night  `json:"nights"`
}

// Night is the persisted record of one civil date's run.
type Night struct {
	Started  string  `json:"started"`
	SpentUSD float64 `json:"spent_usd"`
}

func (s Store) loadState() (state, error) {
	st := state{Units: map[string]string{}, Nights: map[string]Night{}}
	data, err := os.ReadFile(s.statePath())
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("the dreaming state is unreadable: %w", err)
	}
	if st.Units == nil {
		st.Units = map[string]string{}
	}
	if st.Nights == nil {
		st.Nights = map[string]Night{}
	}
	return st, nil
}

func (s Store) saveState(st state) error { return writeJSONAtomic(s.statePath(), st) }

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// writeFileAtomic is temp + fsync + rename + fsync of the directory, so a
// crash leaves either the old or the new file, and the rename itself is
// durable before the next step relies on it.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".dreaming-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

// Due returns the units whose content changed since the dreaming last
// processed (or undid) them; on the first night, all of them.
func (s Store) Due(snap Snapshot) ([]Unit, error) {
	st, err := s.loadState()
	if err != nil {
		return nil, err
	}
	var out []Unit
	for _, u := range snap.Units() {
		if st.Units[bookPrefix+u.Sheet] != snap.UnitHash(u) {
			out = append(out, u)
		}
	}
	return out, nil
}
