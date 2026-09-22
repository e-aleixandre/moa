package owner

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Report is one child session's outcome on its way to the owner. It is the
// persisted shape of the outbox: the serve layer builds it from a run outcome
// and renders it into the message the owner reads.
//
// It lives here, next to the store, because the outbox file has to be readable
// at startup before any session exists — recovery happens before the manager
// has runtimes to ask.
type Report struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Title     string         `json:"title,omitempty"`
	CWD       string         `json:"cwd,omitempty"`
	Origin    string         `json:"origin"`
	Status    string         `json:"status"`
	FinalText string         `json:"final_text,omitempty"`
	Pending   *ReportPending `json:"pending,omitempty"`
	// BookDelta is what the session says its work changes about the book,
	// lifted from the FULL final message before FinalText was abridged:
	// a delta that only exists in the part that was cut is a delta the
	// owner never applies. Empty means the session did not say (the owner has
	// to ask); "none" is the session saying nothing changes, which is an answer.
	BookDelta string `json:"book_delta,omitempty"`
	// GitAvailable reports that the three fields below were ALL answered. It
	// exists because a partial answer is indistinguishable from good news: a
	// branch with no head and dirty=false reads as verified, committed work
	// when it may only mean `git status` timed out. False means the position
	// is unknown and the owner is told so.
	GitAvailable bool `json:"git_available,omitempty"`
	// Branch / Head / Dirty place the work in the repository, which is what
	// decides where the delta goes: the canonical branch (see Owner.
	// CanonicalRef) updates areas/, any other branch updates work/. Only
	// meaningful when GitAvailable.
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// BackgroundCount is how much autonomous work was still running when the
	// turn was reported: async subagents, background bash jobs, verifiers. A
	// report describes a completed semantic turn, not a quiet session — a
	// child that leaves a dev server running is done with what it was asked,
	// and the owner has to be told the difference. Zero (and absent from the
	// outbox) means the session had fully quiesced.
	BackgroundCount int    `json:"background_count,omitempty"`
	At              string `json:"at,omitempty"` // RFC3339
}

// BookDeltaNone is the value a session uses to say its work changes nothing in
// the book. It is deliberately distinguishable from an absent delta.
const BookDeltaNone = "none"

// ReportPending is what a blocked session is waiting for, carried literally so
// the owner reads the question (or the permission) as it was asked rather than
// a paraphrase of it.
type ReportPending struct {
	Kind string `json:"kind"` // question | permission
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
}

// reportsFile is the per-codebase outbox: reports that were accepted but not
// yet confirmed inside the owner's transcript. Delivery is at-least-once —
// re-reading one report is recoverable, losing it is not.
const reportsFile = "reports.json"

const reportsRecoveryFile = "reports.recovery.json"

// ReportsOutbox is the set of readable report lanes a coordinator owns.  The
// canonical lane is deliberately not included when it cannot be decoded: its
// bytes belong to the person repairing it, not to the next atomic save.
type ReportsOutbox struct {
	Reports             []Report
	Lanes               []string
	CanonicalPath       string
	ActivePath          string
	Unreadable          []ReportsUnreadableLane
	CanonicalUnreadable bool
	// ActiveRecoveryOwned is an incomplete-load recovery lane this coordinator
	// created exclusively. It is safe to update, but never to use as evidence
	// that the original lanes may be removed.
	ActiveRecoveryOwned bool
	// Incomplete means enumeration or target selection failed after some lanes
	// were read. Saves must use a new recovery lane and must not remove sources.
	Incomplete    bool
	IncompleteErr error
}

type ReportsUnreadableLane struct {
	Path string
	Err  error
}

func (s *Store) reportsPath(key string) string {
	return filepath.Join(s.CodebaseDir(key), reportsFile)
}

// LoadReports returns the pending outbox of a codebase. A missing file is an
// empty outbox; a malformed one is an error, so a coordinator never silently
// drops reports it cannot read.
func (s *Store) LoadReports(key string) ([]Report, error) {
	data, err := os.ReadFile(s.reportsPath(key))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read reports %s: %w", key, err)
	}
	var out []Report
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse reports %s: %w", key, err)
	}
	return out, nil
}

// LoadReportsOutbox reads every readable report lane. Recovery lanes are kept
// separate from an unreadable canonical file, so accepting another report can
// never replace evidence a person needs to repair.
func (s *Store) LoadReportsOutbox(key string) (ReportsOutbox, error) {
	canonical := s.reportsPath(key)
	out := ReportsOutbox{CanonicalPath: canonical}
	canonicalReports, exists, err := readReportLane(canonical)
	if err != nil {
		out.CanonicalUnreadable = true
		out.Unreadable = append(out.Unreadable, ReportsUnreadableLane{Path: canonical, Err: err})
	} else if exists {
		canonicalReports = recoveredReportIDs(canonical, canonicalReports)
		out.Lanes = append(out.Lanes, canonical)
		out.Reports = appendUniqueReports(out.Reports, canonicalReports)
	}

	recoveries, err := s.recoveryPaths(key)
	if err != nil {
		out.Incomplete = true
		if recovery, recoveryErr := s.nextRecoveryPath(key); recoveryErr == nil {
			out.ActivePath = recovery
		} else {
			err = fmt.Errorf("%w; select recovery lane: %v", err, recoveryErr)
		}
		out.IncompleteErr = err
		return out, err
	}
	var firstValid string
	for _, path := range recoveries {
		reports, exists, laneErr := readReportLane(path)
		if laneErr != nil || !exists {
			if laneErr != nil {
				out.Unreadable = append(out.Unreadable, ReportsUnreadableLane{Path: path, Err: laneErr})
			}
			continue // unreadable lanes are preserved, never claimed
		}
		reports = recoveredReportIDs(path, reports)
		if firstValid == "" {
			firstValid = path
		}
		out.Lanes = append(out.Lanes, path)
		out.Reports = appendUniqueReports(out.Reports, reports)
	}
	if out.CanonicalUnreadable {
		out.ActivePath = firstValid
		if out.ActivePath == "" {
			out.ActivePath, err = s.nextRecoveryPath(key)
			if err != nil {
				out.Incomplete = true
				out.IncompleteErr = err
				return out, err
			}
		}
	} else {
		out.ActivePath = canonical
	}
	return out, nil
}

// SaveReportsOutbox durably replaces the readable outbox lanes. It only
// removes old readable lanes after the replacement is on disk. An unreadable
// canonical or recovery lane is never a source and is therefore untouched.
func (s *Store) SaveReportsOutbox(key string, out ReportsOutbox, reports []Report) (ReportsOutbox, error) {
	if key == "" {
		return ReportsOutbox{}, errors.New("reports need a codebase key")
	}
	if len(reports) == 0 {
		if out.Incomplete {
			return out, errors.New("reports outbox is incomplete; refusing to remove source lanes")
		}
		for _, path := range out.Lanes {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return out, err
			}
		}
		out.Reports, out.Lanes = nil, nil
		return out, nil
	}
	data, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		return out, err
	}
	data = append(data, '\n')
	target := out.ActivePath
	forceFresh := out.Incomplete && !out.ActiveRecoveryOwned
	if target == "" {
		target = filepath.Join(s.CodebaseDir(key), reportsRecoveryFile)
		forceFresh = true
	}
	newLane := !containsPath(out.Lanes, target)
	if forceFresh || (out.CanonicalUnreadable && newLane && !out.ActiveRecoveryOwned) {
		// Exclusive creation prevents overwriting any pre-existing recovery lane.
		target, err = s.writeFreshRecovery(target, data)
		if err != nil {
			return out, err
		}
	} else if err := writeFileAtomic(target, data, 0o600); err != nil {
		return out, err
	}
	if !out.Incomplete {
		for _, path := range out.Lanes {
			if path != target {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return out, err
				}
			}
		}
	}
	out.Reports = append([]Report(nil), reports...)
	if out.Incomplete {
		out.ActiveRecoveryOwned = true
	} else {
		out.Lanes = []string{target}
		out.ActiveRecoveryOwned = false
	}
	out.ActivePath = target
	return out, nil
}

func readReportLane(path string) ([]Report, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var reports []Report
	if err := json.Unmarshal(data, &reports); err != nil {
		return nil, true, err
	}
	return reports, true, nil
}

func (s *Store) recoveryPaths(key string) ([]string, error) {
	dir := s.CodebaseDir(key)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && (name == reportsRecoveryFile || (strings.HasPrefix(name, "reports.recovery.") && strings.HasSuffix(name, ".json"))) {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	sort.Slice(paths, func(i, j int) bool { return recoveryOrder(paths[i]) < recoveryOrder(paths[j]) })
	return paths, nil
}

func (s *Store) writeFreshRecovery(first string, data []byte) (string, error) {
	dir := filepath.Dir(first)
	start := recoveryIndex(first)
	for i := 0; ; i++ {
		path := first
		if i > 0 {
			path = filepath.Join(dir, fmt.Sprintf("reports.recovery.%d.json", start+i))
		}
		err := writeFileExclusive(path, data, 0o600)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
}

func recoveryIndex(path string) int {
	name := filepath.Base(path)
	if name == reportsRecoveryFile {
		return 0
	}
	n := strings.TrimSuffix(strings.TrimPrefix(name, "reports.recovery."), ".json")
	i, err := strconv.Atoi(n)
	if err != nil || i < 1 {
		return 0
	}
	return i
}

func (s *Store) nextRecoveryPath(key string) (string, error) {
	dir := s.CodebaseDir(key)
	for i := 0; ; i++ {
		path := filepath.Join(dir, reportsRecoveryFile)
		if i > 0 {
			path = filepath.Join(dir, fmt.Sprintf("reports.recovery.%d.json", i))
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
}

func recoveryOrder(path string) string {
	name := filepath.Base(path)
	if name == reportsRecoveryFile {
		return "00000000000000000000"
	}
	n := strings.TrimSuffix(strings.TrimPrefix(name, "reports.recovery."), ".json")
	i, err := strconv.Atoi(n)
	if err != nil {
		return "z" + name
	}
	return fmt.Sprintf("%020d", i+1)
}

func recoveredReportIDs(path string, reports []Report) []Report {
	for i := range reports {
		if reports[i].ID != "" {
			continue
		}
		data, _ := json.Marshal(reports[i])
		sum := sha256.Sum256([]byte(path + "\x00" + strconv.Itoa(i) + "\x00" + string(data)))
		reports[i].ID = fmt.Sprintf("recovered_%x", sum[:8])
	}
	return reports
}

func appendUniqueReports(dst, src []Report) []Report {
	seen := make(map[string]struct{}, len(dst)+len(src))
	for _, report := range dst {
		seen[report.ID] = struct{}{}
	}
	for _, report := range src {
		if _, ok := seen[report.ID]; ok {
			continue
		}
		seen[report.ID] = struct{}{}
		dst = append(dst, report)
	}
	return dst
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

// SaveReports replaces the outbox atomically. An empty list removes the file
// rather than leaving an empty array behind.
func (s *Store) SaveReports(key string, reports []Report) error {
	if key == "" {
		return errors.New("reports need a codebase key")
	}
	path := s.reportsPath(key)
	if len(reports) == 0 {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}
