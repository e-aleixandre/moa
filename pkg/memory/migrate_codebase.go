package memory

// Remove in 1.0.
//
// Everything below is a migration EVENT, not a compatibility layer: project
// memory used to be keyed by the canonical path of the working directory, so
// every git worktree of a repository had its own island of facts and deleting
// a worktree orphaned them for good. It is now keyed by the repository
// (core.CodebaseKey), which lives in a new directory — `codebases/<key>` — so
// this runs once per codebase: it COPIES the old stores in and never touches
// them again, and nothing here is consulted at runtime. The old directory is
// left intact on purpose, so downgrading to a previous binary finds the memory
// it expects.
//
// Memory is the one thing moa keeps that the user cannot reconstruct, so every
// decision here breaks the same way: when in doubt, do not declare the
// migration finished. A run that copies nothing and retries tomorrow costs a
// directory scan; a run that wrongly declares itself done costs facts nobody
// can get back.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

const (
	// migrationRecordName is the marker that says what has been copied into
	// this codebase. It is an explicit file rather than "does codebases/<key>/
	// memory exist?" because that directory is created by an ordinary
	// memory.write too: a failed migration followed by one write would have
	// looked exactly like a finished one, and the legacy stores would have
	// been abandoned with no way to notice.
	migrationRecordName = "memory-migration.json"
	// migrationLockName serializes the migration between moa processes. Two
	// sessions starting at once in two worktrees of the same repository is the
	// ordinary case, not a corner one.
	migrationLockName = ".memory-migration.lock"

	migrationRecordVersion = 1

	// projectHashLen is how long the store directory names are: ProjectHash
	// and CodebaseKey both emit 8 bytes of sha256 as hex.
	projectHashLen = 16
)

// orphanLedger* name the one place that remembers the path-keyed stores no
// codebase could claim. It lives beside the codebases, not inside one, because
// it belongs to none of them in particular: written per codebase it would
// either repeat the same list under every key or be lost under none.
const (
	orphanLedgerName     = "orphaned-memory.json"
	orphanLedgerLockName = ".orphaned-memory.lock"
	orphanLedgerVersion  = 1
	orphanLedgerNote     = "Project memory written by an older moa that could not be attributed to a repository. " +
		"Nothing was deleted: the facts are still under projects/<hash>/memory. " +
		"moa copies a store in by itself if the directory in last_known_cwd exists again, or if a session is started in it."
)

// migrationInsideLock runs inside the migration's critical section and returns
// the function that marks leaving it. It is nil in production and exists so a
// test can observe that two migrations never overlap: a lock is one of the few
// things whose absence changes no output at all, so without a seam the test
// covering it passes just as happily once the lock is removed.
//
// It brackets the whole section rather than pinging from inside it. A single
// call site only proves the lock is held at that instant, so a mutation that
// released it early — right after the ping, before the scan and the merge —
// would go unnoticed by the very test written to catch it.
var migrationInsideLock func() func()

// readFactFile reads one source fact. It is a variable so a test can inject
// the failure this whole file is designed around — a source that stops being
// readable part way through the copy. The obvious way to provoke it, a file
// with mode 000, is not a failure at all when the tests run as root, which is
// how the two tests covering "an interrupted migration publishes nothing"
// ended up skipping themselves exactly where the behaviour matters.
var readFactFile = os.ReadFile

// migrationRecord is the durable state of this codebase's migration.
//
// Copied stores are recorded one by one, and a store is never read twice.
// That is what makes deleting a migrated fact stick: without it, every startup
// would copy the legacy store again and resurrect what the user threw away.
//
// Complete means "everything this codebase could decide has been decided", not
// "nothing was left behind anywhere". The difference is what every later
// startup costs: a store that belongs to nobody identifiable is not this
// codebase's unfinished business, and treating it as such made every project
// rescan the whole session history on every start, forever, because of one
// deleted worktree somebody else's.
//
// So it is false only while the *evidence* was unreadable — the one case where
// repeating the same work can produce a different answer. What can be
// re-checked cheaply instead of blocking the seal goes into Pending: a store
// whose last known directory would belong to this codebase if it came back
// costs one stat to reconsider, not a scan. Everything else that was left
// behind is recorded once, for everyone, in the orphan ledger.
type migrationRecord struct {
	Version   int          `json:"version"`
	UpdatedAt time.Time    `json:"updated_at"`
	Complete  bool         `json:"complete"`
	Copied    []storeEntry `json:"copied_stores,omitempty"`
	Pending   []storeEntry `json:"pending_stores,omitempty"`
}

// validate reports whether this record can be trusted to say what has already
// been copied and whether the job is done.
//
// It is not a formality. Complete seals the migration, and a marker that seals
// it wrongly abandons the legacy stores for good — the one outcome this file
// exists to prevent. Deserializing proves nothing about that: a truncated
// write, a hand-edited file, or a record from a version whose fields mean
// something else all parse cleanly and are still an empty answer. `{"complete":
// true}` is valid JSON and, honoured, loses every fact. Rejecting a record
// costs one more additive merge, which is a directory scan.
func (r migrationRecord) validate() error {
	if r.Version != migrationRecordVersion {
		return fmt.Errorf("version %d, want %d", r.Version, migrationRecordVersion)
	}
	// Every record this code writes is stamped by writeMigrationRecord, so a
	// missing stamp means the file did not come from a write that finished.
	if r.UpdatedAt.IsZero() {
		return errors.New("no timestamp")
	}
	for _, group := range [][]storeEntry{r.Copied, r.Pending} {
		for _, e := range group {
			if err := e.validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// storeEntry is one path-keyed store, named by the hash that is its directory
// name. The cwd is recorded when it is known because the hash cannot be
// reversed and says nothing to a human reading this file.
type storeEntry struct {
	Hash  string `json:"hash"`
	CWD   string `json:"cwd,omitempty"`
	Facts int    `json:"facts"`
}

// validate rejects entries that cannot describe a store on disk. A hash is one
// directory name; anything else means the file is not what it claims to be,
// and these entries are what "already copied" is decided from.
//
// The shape is checked rather than just the separators: these hashes are always
// the 16 hex characters ProjectHash produces, so anything else is rejected
// without having to reason about which separator the running platform honours
// ("a\b" is one name on Linux and two on Windows).
func (e storeEntry) validate() error {
	if len(e.Hash) != projectHashLen {
		return fmt.Errorf("store entry hash %q is not a store directory name", e.Hash)
	}
	for _, r := range e.Hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("store entry hash %q is not hexadecimal", e.Hash)
		}
	}
	if e.Facts < 0 {
		return fmt.Errorf("store entry %s claims %d facts", e.Hash, e.Facts)
	}
	return nil
}

func (s *Store) migrationRecordPath() string {
	return filepath.Join(s.codebaseRoot, migrationRecordName)
}

// codebasesDir is ~/.config/moa/codebases, the parent every codebase shares.
func (s *Store) codebasesDir() string { return filepath.Dir(s.codebaseRoot) }

func (s *Store) orphanLedgerPath() string {
	return filepath.Join(s.codebasesDir(), orphanLedgerName)
}

// Migrate brings this store's project scope up to date. Call it once, at
// startup, before reading anything.
//
// The order is load-bearing: the codebase migration reconciles the destination
// with every legacy store first, so that the v1 wrapping below writes into a
// directory whose names are already settled and cannot displace a fact the
// merge was about to place there.
func (s *Store) Migrate() error {
	if err := s.migrateFromPathKeyedStores(); err != nil {
		return err
	}
	return s.MigrateV1IfNeeded()
}

// migrateFromPathKeyedStores copies the facts of every path-keyed store that
// belongs to this codebase into the codebase-keyed one.
//
// It is idempotent through the record described above, not through the state
// of the destination directory: a destination that exists without a record is
// merged into, never taken as proof that someone else did the work. Merging is
// additive — an existing fact keeps its name and its bytes, and anything that
// would collide is kept beside it under a suffixed name — so a run interrupted
// halfway leaves a destination that is short some facts but wrong about none,
// and the next run finishes the job.
func (s *Store) migrateFromPathKeyedStores() error {
	if rec, ok := s.readMigrationRecord(); ok && rec.Complete && !s.mustLookAgain(rec) {
		// The path every start takes once this has run: one file read, plus at
		// most one stat per thing that could still change the answer.
		return nil
	}
	// Cheap gate: with no path-keyed stores on disk there is nothing to copy,
	// and — more importantly — no reason to walk the session history looking
	// for one. This is the branch every user who never ran an older moa takes,
	// and it deliberately writes nothing: a record here would create a
	// directory per codebase to state the obvious, and re-checking an empty
	// directory costs one readdir.
	legacy := scanLegacyStores(filepath.Join(s.configDir, "projects"))
	if len(legacy) == 0 {
		return nil
	}

	if err := os.MkdirAll(s.codebaseRoot, 0o700); err != nil {
		return fmt.Errorf("memory: creating codebase dir: %w", err)
	}
	lock, err := core.AcquireFileLock(filepath.Join(s.codebaseRoot, migrationLockName))
	if err != nil {
		return fmt.Errorf("memory: locking migration: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if migrationInsideLock != nil {
		defer migrationInsideLock()()
	}
	// Another process may have finished the whole thing while we waited for
	// the lock; its record is as good as ours.
	rec, _ := s.readMigrationRecord()
	if rec.Complete && !s.mustLookAgain(rec) {
		return nil
	}

	scan := s.attributeStores(legacy)

	alreadyCopied := make(map[string]int, len(rec.Copied))
	for _, c := range rec.Copied {
		alreadyCopied[c.Hash] = c.Facts
	}
	var todo []factSource
	for _, src := range scan.sources {
		// Trust "already copied" only as far as the count it claims. A record
		// that names a store but understates what was in it — hand-edited, or
		// written before an older binary appended to that store again — would
		// otherwise strand facts forever. Re-merging is safe: bytes already
		// present are recognized, not re-filed under a new name.
		if copied, ok := alreadyCopied[src.store.hash]; ok && copied >= src.facts {
			continue
		}
		todo = append(todo, src)
	}

	files, err := collectFacts(todo)
	if err != nil {
		// Nothing has been written yet: collection reads everything before the
		// merge starts, so a store that cannot be read fully aborts the run
		// with the destination exactly as it was, and no record to seal it.
		return err
	}
	written, err := s.mergeIntoDestination(files)
	if err != nil {
		return err
	}

	for _, src := range todo {
		rec.Copied = append(rec.Copied, storeEntry{Hash: src.store.hash, CWD: src.cwd, Facts: src.facts})
	}
	sort.Slice(rec.Copied, func(i, j int) bool { return rec.Copied[i].Hash < rec.Copied[j].Hash })
	rec.Pending = scan.pending(s.codebaseKey)
	// Sealing is safe as long as everything that could reopen this codebase is
	// re-checkable with a stat: a pending store's directory coming back, or
	// this workspace's own store showing up later. What cannot be re-checked
	// that way is evidence that was unreadable — retrying is the only way to
	// find out whether it still is.
	rec.Complete = !scan.blinded()

	// Write down what was left behind before sealing anything: the ledger is
	// the only durable trace of stores no codebase claims, so a marker saying
	// "done" must never be the more durable of the two.
	if err := s.recordOrphans(scan); err != nil {
		return err
	}
	if err := s.writeMigrationRecord(rec); err != nil {
		return err
	}

	// A run that copies nothing is the common case once this has happened
	// once — it stays at debug so an incomplete migration does not print the
	// same line on every startup.
	args := []any{
		"codebase", s.codebaseKey,
		"dest", s.projectDir,
		"facts", written,
		"complete", rec.Complete,
		"copied_from", sourcePaths(todo),
	}
	if written > 0 {
		slog.Info("memory: project memory now follows the repository", args...)
	} else {
		slog.Debug("memory: nothing new to migrate for this codebase", args...)
	}
	return nil
}

// mustLookAgain reports whether a sealed migration has to be opened again,
// answering with stats instead of with the full scan.
//
// A sealed record is the common case forever after the first run, so what it
// costs is what starting moa costs. Both things that can change the answer are
// cheap to ask about: a store this workspace owns by construction that has not
// been copied yet, and a recorded directory coming back.
func (s *Store) mustLookAgain(rec migrationRecord) bool {
	ownHash := filepath.Base(s.legacyProjectRoot)
	own := filepath.Join(s.configDir, "projects", ownHash, "memory")
	i := slices.IndexFunc(rec.Copied, func(e storeEntry) bool { return e.Hash == ownHash })
	// Opening a directory whose old store nobody could name is how such a
	// store gets claimed: its hash is this workspace's by construction, no
	// guessing involved. Without this a codebase sealed from another worktree
	// would ignore it for good. The stat only happens for a workspace that has
	// not had its own store copied, so the ordinary case pays nothing.
	if i < 0 {
		if dirExists(own) {
			return true
		}
	} else if countFacts(own) > rec.Copied[i].Facts {
		// The store was copied, but an older binary still writes to the
		// path-keyed location, so it can have grown since. Counting one
		// directory the user already owns is one readdir, and it is the
		// difference between picking those facts up and stranding them.
		return true
	}
	for _, p := range rec.Pending {
		// The directory is back, so git can say which repository it belongs to
		// again and the store may be attributable now.
		if p.CWD != "" && dirExists(p.CWD) {
			return true
		}
	}
	return false
}

// readMigrationRecord loads the marker. A record that cannot be parsed is
// treated as absent: re-running an additive merge costs a directory scan,
// while trusting an unreadable file would abandon the legacy stores forever.
func (s *Store) readMigrationRecord() (migrationRecord, bool) {
	data, err := os.ReadFile(s.migrationRecordPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("memory: cannot read migration record, will migrate again", "path", s.migrationRecordPath(), "error", err)
		}
		return migrationRecord{}, false
	}
	var rec migrationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		slog.Warn("memory: corrupt migration record, will migrate again", "path", s.migrationRecordPath(), "error", err)
		return migrationRecord{}, false
	}
	if err := rec.validate(); err != nil {
		slog.Warn("memory: unusable migration record, will migrate again", "path", s.migrationRecordPath(), "error", err)
		return migrationRecord{}, false
	}
	return rec, true
}

// writeMigrationRecord persists the marker, and only once every fact it
// vouches for is on disk: it is written last on purpose, so a crash anywhere
// before this point leaves the migration pending rather than declared done.
func (s *Store) writeMigrationRecord(rec migrationRecord) error {
	rec.Version = migrationRecordVersion
	rec.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encoding migration record: %w", err)
	}
	if err := writeFileAtomic(s.migrationRecordPath(), append(data, '\n')); err != nil {
		return fmt.Errorf("memory: writing migration record: %w", err)
	}
	return nil
}

// legacyStore is one <projects>/<hash> directory of the path-keyed layout.
type legacyStore struct {
	hash      string
	root      string // <projects>/<hash>
	memoryDir string // "" when this store never held v2 facts
	v1Path    string // "" when it has no flat MEMORY.md
	facts     int    // fact files, so a warning can say what is at stake
	// knownCWD is the directory a session says this store came from, set only
	// when that directory is gone. It is the one thing that makes an orphaned
	// store recoverable by hand later, so it is worth carrying into the
	// warning and the record.
	knownCWD string
}

// factSource is a legacy store attributed to this codebase, with the working
// directory that identified it — the useful half of the log line, as the hash
// says nothing to a human.
type factSource struct {
	store legacyStore
	cwd   string
	facts int
}

// storeScan is the outcome of deciding, for every path-keyed store on disk,
// whether it belongs to this codebase, to another one, or to nothing anyone
// can name any more.
type storeScan struct {
	sources []factSource
	// unattributed holds the stores whose codebase could not be established:
	// no session ever recorded a working directory that hashes to them, or the
	// directory it recorded no longer exists. Guessing from the parent
	// directory would merge unrelated projects' memory on the strength of a
	// hunch, so they are left where they are — but never in silence.
	unattributed []legacyStore
	// unreadableSessions counts session files that could not be decoded. They
	// are why an unattributed store may be unattributed: the evidence, not the
	// store, is what is missing — and unlike a session that simply never
	// recorded a cwd, it may be there next time.
	unreadableSessions int
}

// blinded reports whether this scan was decided on incomplete evidence, which
// is the only reason to refuse to seal.
//
// A session file that could not be decoded may decode next time — it is a
// truncated write, a transient IO error — and it may be the only record of
// which directory a store came from. A session that simply never recorded a
// cwd is not evidence to recover: written by a moa old enough not to store
// one, it will answer the same forever, and waiting on it would keep every
// codebase on the expensive path for good. Nothing else about an unattributed
// store changes by looking again either: a directory that no longer exists
// does not come back because moa rescanned, and mustLookAgain covers the case
// where it does.
func (sc storeScan) blinded() bool { return sc.unreadableSessions > 0 }

// pending returns the stores this codebase should reconsider on a later start,
// and only those.
//
// The scoping is the whole point. An orphan is pending for the codebase that
// could plausibly turn out to own it — one whose last known directory, if it
// came back, would key to us — and for nobody else. Left global, a single
// deleted worktree the user will never recreate kept every codebase on the
// expensive path forever: on this machine one such store (13 facts, a deleted
// winerim-backend worktree) made moa, autowow and the rest rescan the whole
// session history on every single start.
//
// A store with no known cwd is nobody's candidate: there is no directory to
// stat and no evidence to re-read, so it goes to the ledger and stays there
// until the user starts a session in the directory it came from — at which
// point it is that workspace's own store by construction and gets copied
// without any of this.
func (sc storeScan) pending(codebaseKey string) []storeEntry {
	var out []storeEntry
	for _, st := range sc.unattributed {
		if st.facts == 0 {
			continue // an empty store has nothing to lose
		}
		if st.knownCWD == "" || !couldBelongTo(st.knownCWD, codebaseKey) {
			continue
		}
		out = append(out, storeEntry{Hash: st.hash, CWD: st.knownCWD, Facts: st.facts})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out
}

// couldBelongTo reports whether a vanished directory, were it to come back,
// would belong to this codebase.
//
// The directory itself is gone, so git cannot be asked about it — but its
// ancestors can, and an ancestor that is inside a repository is not a guess
// about identity: a path under /repo/wt/gone can only ever be recreated inside
// /repo. That is enough to decide who is entitled to look again, which is a
// far weaker claim than deciding who owns the facts (still nobody: the store
// is only copied once the directory actually exists and answers for itself).
//
// The walk stops at the first ancestor git recognizes, so it costs at most a
// handful of execs, once, while the scan is already running — and never on the
// sealed path.
func couldBelongTo(cwd, codebaseKey string) bool {
	dir := filepath.Clean(cwd)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return false // reached the root without finding a repository
		}
		dir = parent
		if !dirExists(dir) {
			continue
		}
		// RepoCodebaseKey, not CodebaseKey: outside a repository the latter
		// falls back to hashing the path, which would answer for every
		// directory on the way up and hand the orphan to whichever
		// non-repository ancestor happened to match.
		key, ok := core.RepoCodebaseKey(dir)
		if !ok {
			return false // a real directory that is in no repository: stop
		}
		return key == codebaseKey
	}
}

// orphanLedger is the shared record of path-keyed stores nobody could claim.
//
// It exists because the alternative to a durable note is a warning on every
// start, and a warning nobody can act on twice is noise that trains the user
// to ignore the log. One file, updated when the set of orphans actually
// changes, logged once when it does.
type orphanLedger struct {
	Version   int           `json:"version"`
	UpdatedAt time.Time     `json:"updated_at"`
	Note      string        `json:"note"`
	Stores    []orphanEntry `json:"stores"`
}

type orphanEntry struct {
	Hash         string `json:"hash"`
	Path         string `json:"path"`
	Facts        int    `json:"facts"`
	LastKnownCWD string `json:"last_known_cwd,omitempty"`
}

// recordOrphans folds what this scan could not place into the shared ledger.
//
// Adding rather than replacing is deliberate: this process only looked at the
// stores it could not attribute *to itself*, and a store another codebase
// already claimed and copied must not reappear as an orphan because a third
// one has no opinion about it. Entries are dropped only when the store is gone
// from disk, which is the user deleting it.
func (s *Store) recordOrphans(scan storeScan) error {
	found := make([]orphanEntry, 0, len(scan.unattributed))
	for _, st := range scan.unattributed {
		if st.facts == 0 {
			continue // an empty store has nothing to lose
		}
		found = append(found, orphanEntry{Hash: st.hash, Path: st.root, Facts: st.facts, LastKnownCWD: st.knownCWD})
	}
	if len(found) == 0 && !fileExists(s.orphanLedgerPath()) {
		return nil // nothing to say and nothing to correct
	}

	// Every codebase writes here, so the read-modify-write needs its own lock:
	// the migration lock is per codebase and would not serialize two different
	// ones migrating at the same time.
	if err := os.MkdirAll(s.codebasesDir(), 0o700); err != nil {
		return fmt.Errorf("memory: creating codebases dir: %w", err)
	}
	lock, err := core.AcquireFileLock(filepath.Join(s.codebasesDir(), orphanLedgerLockName))
	if err != nil {
		return fmt.Errorf("memory: locking orphan ledger: %w", err)
	}
	defer func() { _ = lock.Close() }()

	ledger := s.readOrphanLedger()
	byHash := make(map[string]orphanEntry, len(ledger.Stores)+len(found))
	for _, e := range ledger.Stores {
		// A store the user deleted by hand stops being anybody's problem.
		if !dirExists(e.Path) {
			continue
		}
		byHash[e.Hash] = e
	}
	// A store this run just copied is no longer an orphan, whoever filed it.
	for _, src := range scan.sources {
		delete(byHash, src.store.hash)
	}
	for _, e := range found {
		byHash[e.Hash] = e
	}

	merged := make([]orphanEntry, 0, len(byHash))
	for _, e := range byHash {
		merged = append(merged, e)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Hash < merged[j].Hash })
	if slices.Equal(merged, ledger.Stores) {
		return nil // said already, and saying it again is the noise to avoid
	}

	ledger.Version = orphanLedgerVersion
	ledger.UpdatedAt = time.Now().UTC()
	ledger.Note = orphanLedgerNote
	ledger.Stores = merged
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encoding orphan ledger: %w", err)
	}
	if err := writeFileAtomic(s.orphanLedgerPath(), append(data, '\n')); err != nil {
		return fmt.Errorf("memory: writing orphan ledger: %w", err)
	}
	// Once, when the set changes, with the path to the files: a user whose
	// worktree is gone cannot be helped automatically, but they can be told
	// where the facts are — and the ledger keeps saying it after the line has
	// scrolled away.
	if len(merged) > 0 {
		slog.Warn("memory: some legacy project memory could not be attributed to a repository",
			"stores", len(merged), "facts", totalFacts(merged), "listed_in", s.orphanLedgerPath())
	}
	return nil
}

// readOrphanLedger loads the shared ledger, treating anything unusable as
// empty: it is a note to the user, so the worst a fresh start costs is
// repeating a warning, while trusting a garbled file could drop an orphan from
// the only list that mentions it.
func (s *Store) readOrphanLedger() orphanLedger {
	data, err := os.ReadFile(s.orphanLedgerPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("memory: cannot read the orphaned-memory list", "path", s.orphanLedgerPath(), "error", err)
		}
		return orphanLedger{}
	}
	var ledger orphanLedger
	if err := json.Unmarshal(data, &ledger); err != nil || ledger.Version != orphanLedgerVersion {
		slog.Warn("memory: unusable orphaned-memory list, rewriting it", "path", s.orphanLedgerPath(), "error", err)
		return orphanLedger{}
	}
	return ledger
}

func totalFacts(entries []orphanEntry) int {
	n := 0
	for _, e := range entries {
		n += e.Facts
	}
	return n
}

// attributeStores decides which path-keyed stores belong to this codebase.
//
// The hash cannot be reversed, so the directory name alone cannot say which
// path it was made from. The session store knows: every session persists its
// cwd in metadata, so hashing the cwds it recorded rebuilds the
// hash → directory mapping from the outside. Sessions are read as headers,
// which stop decoding before the conversation history.
//
// A working directory that no longer exists is deliberately not resolved. git
// cannot say which repository a deleted worktree belonged to, and attributing
// it to whatever repository its parent happens to be in would merge unrelated
// projects' memory on a guess.
func (s *Store) attributeStores(legacy map[string]legacyStore) storeScan {
	var scan storeScan
	scanned, err := session.ScanCWDs(filepath.Join(s.configDir, "sessions"))
	if err != nil {
		// ScanCWDs returns whatever it could read alongside the error; an
		// unreadable store means one fewer candidate, and the stores it would
		// have named end up reported as unattributed below.
		slog.Debug("memory: partial session scan while migrating", "error", err)
	}
	scan.unreadableSessions = scanned.Unreadable

	// Newest session first, so the freshest recorded path wins for a hash.
	cwdByHash := make(map[string]string, len(scanned.CWDs))
	for _, cwd := range scanned.CWDs {
		h := core.ProjectHash(cwd)
		if _, seen := cwdByHash[h]; !seen {
			cwdByHash[h] = cwd
		}
	}

	ownHash := filepath.Base(s.legacyProjectRoot)
	hashes := make([]string, 0, len(legacy))
	for h := range legacy {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)

	for _, h := range hashes {
		st := legacy[h]
		// This workspace's own store belongs to this codebase by construction:
		// no session needed, and it is the one that must not be missed when
		// the session history has been pruned.
		if h == ownHash {
			scan.sources = append(scan.sources, factSource{store: st, facts: st.facts})
			continue
		}
		cwd, known := cwdByHash[h]
		if !known {
			scan.unattributed = append(scan.unattributed, st)
			continue
		}
		// The path is only evidence while it exists: git answers about a
		// directory, and CodebaseKey falls back to hashing the path when it
		// cannot, which for a deleted worktree would invent an identity.
		if !dirExists(cwd) {
			st.knownCWD = cwd
			scan.unattributed = append(scan.unattributed, st)
			continue
		}
		if core.CodebaseKey(cwd) != s.codebaseKey {
			continue // attributed, to somebody else's codebase
		}
		scan.sources = append(scan.sources, factSource{store: st, cwd: cwd, facts: st.facts})
	}
	return scan
}

// scanLegacyStores maps each <projects>/<hash> entry that holds memory to what
// it holds: v2 facts, a v1 MEMORY.md, or both. The flat file counts, because
// the v1 wrapping only ever looked at the store of the directory being opened
// and a MEMORY.md left in a sibling worktree would be invisible forever.
func scanLegacyStores(projectsDir string) map[string]legacyStore {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	out := make(map[string]legacyStore)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := filepath.Join(projectsDir, e.Name())
		st := legacyStore{hash: e.Name(), root: root}
		if dir := filepath.Join(root, "memory"); dirExists(dir) {
			st.memoryDir = dir
			st.facts = countFacts(dir)
		}
		if v1 := filepath.Join(root, "MEMORY.md"); fileExists(v1) {
			st.v1Path = v1
			st.facts++
		}
		if st.memoryDir == "" && st.v1Path == "" {
			continue // state.json and friends: not memory's business
		}
		out[st.hash] = st
	}
	return out
}

func countFacts(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if isFactFile(e) {
			n++
		}
	}
	return n
}

// isFactFile reports whether a directory entry is a fact this migration should
// copy. Symlinks are not followed: a fact store is user data that other
// programs can write into, and a link planted there would make the migration
// read — and republish under the user's memory — a file from anywhere on the
// filesystem.
func isFactFile(e fs.DirEntry) bool {
	if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".md") {
		return false
	}
	return !isReservedFile(e.Name())
}

// migratedFact is one fact file found in a source store.
type migratedFact struct {
	name string
	src  string
	data []byte
	// modTime decides which of several same-named facts keeps the plain name.
	modTime int64
}

// collectFacts reads every fact of every source, grouped by fact name.
func collectFacts(sources []factSource) (map[string][]migratedFact, error) {
	files := make(map[string][]migratedFact)
	for _, src := range sources {
		if src.store.memoryDir != "" {
			if err := collectStoreFacts(src.store.memoryDir, files); err != nil {
				return nil, err
			}
		}
		if src.store.v1Path != "" {
			if err := collectV1Fact(src.store.v1Path, files); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

func collectStoreFacts(dir string, files map[string][]migratedFact) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("memory: reading %s: %w", dir, err)
	}
	for _, e := range entries {
		if !isFactFile(e) {
			if e.Type()&fs.ModeSymlink != 0 {
				slog.Warn("memory: not migrating a symlinked fact", "path", filepath.Join(dir, e.Name()))
			}
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := readFactFile(path)
		if err != nil {
			// Copying is all-or-nothing: skipping an unreadable fact here
			// would publish a store that looks complete and quietly is not.
			return fmt.Errorf("memory: reading %s: %w", path, err)
		}
		var modTime int64
		if info, err := e.Info(); err == nil {
			modTime = info.ModTime().UnixNano()
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		files[name] = append(files[name], migratedFact{name: name, src: path, data: data, modTime: modTime})
	}
	return nil
}

// collectV1Fact turns a flat v1 MEMORY.md into the same fact MigrateV1IfNeeded
// would produce, byte for byte, so whichever of the two gets there first the
// other finds its own work already done instead of writing a second copy.
func collectV1Fact(path string, files map[string][]migratedFact) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // retired between the scan and now
		}
		return fmt.Errorf("memory: reading %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil // an empty v1 file has nothing to wrap
	}
	fact := serialize(v1Fact(string(data)))
	var modTime int64
	if info, err := os.Lstat(path); err == nil {
		modTime = info.ModTime().UnixNano()
	}
	name := v1FactName
	files[name] = append(files[name], migratedFact{name: name, src: path, data: fact, modTime: modTime})
	return nil
}

// mergeIntoDestination writes the collected facts into the codebase store,
// adding to whatever is already there and overwriting nothing.
//
// Merging rather than publishing a freshly built directory is what makes a
// destination without a record safe: it may have been left by an interrupted
// run, by an ordinary memory.write, or by another moa version, and none of
// those is evidence about which facts it should end up holding.
func (s *Store) mergeIntoDestination(files map[string][]migratedFact) (int, error) {
	existing, err := readDestinationFacts(s.projectDir)
	if err != nil {
		return 0, err
	}
	plan := planMigratedFacts(files, existing)
	if len(plan) == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(s.projectDir, 0o700); err != nil {
		return 0, fmt.Errorf("memory: creating memory dir: %w", err)
	}
	for _, p := range plan {
		if p.target != p.name {
			// The filename is authoritative for the ID, so the frontmatter has
			// to follow or every later List would warn about the mismatch this
			// migration just created.
			p.data = renameFactHeader(p.data, p.target)
			slog.Warn("memory: two versions of a fact merged into one codebase",
				"fact", p.name, "kept_as", p.target, "from", p.src)
		}
		if err := writeFileAtomic(filepath.Join(s.projectDir, p.target+".md"), p.data); err != nil {
			return 0, fmt.Errorf("memory: writing migrated fact: %w", err)
		}
	}
	// The count is a promise about the disk, so check the disk. A migration
	// that reports more facts than it left behind is the failure mode this
	// whole file is written against, and it costs one stat per fact to make it
	// impossible to claim.
	for _, p := range plan {
		if !fileExists(filepath.Join(s.projectDir, p.target+".md")) {
			return 0, fmt.Errorf("memory: migrated fact %q is missing after writing it", p.target)
		}
	}
	return len(plan), nil
}

// readDestinationFacts reads the facts already in the codebase store. They are
// live: an ID in an earlier conversation resolves to them, so they keep their
// names and their bytes no matter what the legacy stores hold.
func readDestinationFacts(dir string) (map[string][]byte, error) {
	out := make(map[string][]byte)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("memory: reading %s: %w", dir, err)
	}
	for _, e := range entries {
		if !isFactFile(e) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			// Not knowing what a destination fact holds means not knowing
			// whether the copy about to be made would displace it.
			return nil, fmt.Errorf("memory: reading %s: %w", filepath.Join(dir, e.Name()), err)
		}
		out[strings.TrimSuffix(e.Name(), ".md")] = data
	}
	return out, nil
}

// plannedFact is one file the merge is going to write. name is where it came
// from, target is where it lands.
type plannedFact struct {
	name   string
	target string
	src    string
	data   []byte
}

// planMigratedFacts assigns a destination filename to every collected fact,
// resolving the collisions that merging sibling worktrees produces.
//
// Worktrees of one repository learn the same things, so the same fact name in
// two stores is the normal case, not the exception. Byte-identical copies
// collapse into one. When the contents differ, the version already in the
// destination — or, failing that, the most recently modified one — keeps the
// name, and the others are kept beside it as "<name>-2", "<name>-3", …
//
// Suffixes are drawn from a single namespace covering every name in play:
// the destination's, every source's, and the suffixes already handed out. A
// per-fact counter is not enough, and the difference is a lost fact rather
// than an ugly name — with two versions of "foo" and an unrelated "foo-2", the
// displaced "foo" is written to foo-2.md and then "foo-2" overwrites it.
func planMigratedFacts(files map[string][]migratedFact, existing map[string][]byte) []plannedFact {
	reserved := make(map[string]bool, len(files)+len(existing))
	for name := range existing {
		reserved[name] = true
	}
	for name := range files {
		reserved[name] = true // every source name claims its own name
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var plan []plannedFact
	for _, name := range names {
		variants := files[name]
		// Newest first; the path breaks ties so the result never depends on
		// directory iteration order.
		sort.SliceStable(variants, func(i, j int) bool {
			if variants[i].modTime != variants[j].modTime {
				return variants[i].modTime > variants[j].modTime
			}
			return variants[i].src < variants[j].src
		})

		// Anything already in the destination under this name or one of its
		// suffixes has been placed before — by an earlier run, or by an
		// interrupted one. Seeding the comparison with those bytes is what
		// keeps a retry from filing the same fact again under a fresh suffix.
		kept := existingVersions(existing, name)
		nameTaken := existing[name] != nil
		for _, v := range variants {
			if containsBytes(kept, v.data) {
				continue // same fact learned in two worktrees, or already copied
			}
			target := name
			if nameTaken {
				target = nextFreeName(name, reserved)
			}
			reserved[target] = true
			nameTaken = true
			plan = append(plan, plannedFact{name: name, target: target, src: v.src, data: v.data})
			kept = append(kept, v.data)
		}
	}
	return plan
}

// existingVersions returns the bytes of every destination fact that this name
// could have been filed as: the name itself and its "-N" suffixes.
func existingVersions(existing map[string][]byte, name string) [][]byte {
	var out [][]byte
	if data, ok := existing[name]; ok {
		out = append(out, data)
	}
	prefix := name + "-"
	suffixed := make([]string, 0, 4)
	for other := range existing {
		if strings.HasPrefix(other, prefix) && isNumericSuffix(other[len(prefix):]) {
			suffixed = append(suffixed, other)
		}
	}
	sort.Strings(suffixed)
	for _, other := range suffixed {
		out = append(out, existing[other])
	}
	return out
}

func isNumericSuffix(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// nextFreeName returns the first "<name>-N" nobody has claimed.
func nextFreeName(name string, reserved map[string]bool) string {
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !reserved[candidate] {
			return candidate
		}
	}
}

// renameFactHeader rewrites the `name:` line of a fact's frontmatter, leaving
// the rest of the file byte-for-byte alone. Parsing and re-serializing would be
// shorter but would drop any frontmatter key moa does not know about, and a
// migration is the worst possible moment to normalize a file a user may have
// hand-edited.
func renameFactHeader(data []byte, newName string) []byte {
	text := string(data)
	newline := "\n"
	if strings.HasPrefix(text, "---\r\n") {
		newline = "\r\n"
	} else if !strings.HasPrefix(text, "---\n") {
		return data // no frontmatter to fix; the filename still decides the ID
	}
	lines := strings.Split(text, newline)
	// Locate the closing delimiter before touching anything: a file whose
	// frontmatter never closes is not a fact moa can parse, and rewriting a
	// line inside it would be editing a body that merely starts with "---".
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return data // unterminated frontmatter: leave the file exactly as found
	}
	for i := 1; i < closeIdx; i++ {
		if key, _, ok := splitKV(lines[i]); ok && key == "name" {
			lines[i] = "name: " + newName
			return []byte(strings.Join(lines, newline))
		}
	}
	return data // no name line: the filename is authoritative anyway
}

func containsBytes(haystack [][]byte, needle []byte) bool {
	return slices.ContainsFunc(haystack, func(b []byte) bool { return bytes.Equal(b, needle) })
}

func sourcePaths(sources []factSource) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		if s.cwd != "" {
			out = append(out, s.store.root+" ("+s.cwd+")")
			continue
		}
		out = append(out, s.store.root)
	}
	return out
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Debug("memory: cannot stat directory", "path", path, "error", err)
		}
		return false
	}
	return info.IsDir()
}
