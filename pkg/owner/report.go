package owner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// lifted from the FULL final message before FinalText was cut to its tail:
	// a delta that only exists in the part that was truncated is a delta the
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
