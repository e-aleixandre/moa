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
	Status    string         `json:"status"`
	FinalText string         `json:"final_text,omitempty"`
	Pending   *ReportPending `json:"pending,omitempty"`
	At        string         `json:"at,omitempty"` // RFC3339
}

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
