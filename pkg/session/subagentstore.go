package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// SubagentTranscript is the persisted record of one subagent's sub-conversation.
// Stored in a side directory next to the parent session so a subagent's
// transcript survives restarts and can be reopened after it finished, without
// bloating the parent session's own tree/history.
type SubagentTranscript struct {
	JobID      string              `json:"job_id"`
	Task       string              `json:"task"`
	Model      string              `json:"model"`
	Status     string              `json:"status"`
	Async      bool                `json:"async"`
	StartedAt  time.Time           `json:"started_at,omitempty"`
	FinishedAt time.Time           `json:"finished_at,omitempty"`
	Usage      *core.Usage         `json:"usage,omitempty"`
	CostUSD    float64             `json:"cost_usd,omitempty"`
	Messages   []core.AgentMessage `json:"messages"`
}

// SubagentStore persists subagent transcripts for one parent session in a
// side directory: <session dir>/<sessionID>.subagents/<jobID>.json.
type SubagentStore struct {
	dir string
}

// NewSubagentStore returns a store rooted at "<sessionDir>/<sessionID>.subagents".
// sessionDir is the directory holding the parent session's <id>.json (e.g.
// FileStore.Dir()). The directory is created lazily on first Save.
func NewSubagentStore(sessionDir, sessionID string) *SubagentStore {
	return &SubagentStore{dir: filepath.Join(sessionDir, sessionID+".subagents")}
}

// Dir returns the side-directory path (may not exist yet).
func (s *SubagentStore) Dir() string { return s.dir }

// Save atomically writes one transcript to <jobID>.json.
func (s *SubagentStore) Save(t SubagentTranscript) error {
	if err := validJobID(t.JobID); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("session: subagent mkdir: %w", err)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("session: subagent marshal: %w", err)
	}
	path := filepath.Join(s.dir, t.JobID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("session: subagent write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("session: subagent rename: %w", err)
	}
	return nil
}

// Load reads one transcript by jobID. Returns ErrNotFound (wrapped) if absent.
func (s *SubagentStore) Load(jobID string) (*SubagentTranscript, error) {
	if err := validJobID(jobID); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, jobID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session: subagent %q: %w", jobID, ErrNotFound)
		}
		return nil, fmt.Errorf("session: subagent read: %w", err)
	}
	var t SubagentTranscript
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("session: subagent unmarshal: %w", err)
	}
	return &t, nil
}

// List returns all persisted transcripts for the session, newest-finished first.
// Missing directory yields an empty slice (not an error).
func (s *SubagentStore) List() ([]SubagentTranscript, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: subagent list: %w", err)
	}
	var out []SubagentTranscript
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		jobID := e.Name()[:len(e.Name())-len(".json")]
		t, err := s.Load(jobID)
		if err != nil {
			continue // skip corrupt/partial files
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FinishedAt.After(out[j].FinishedAt)
	})
	return out, nil
}

// validJobID rejects empty or unsafe job IDs (path traversal / separators)
// before they're used to build a filesystem path.
func validJobID(jobID string) error {
	if jobID == "" {
		return fmt.Errorf("session: subagent transcript missing job_id")
	}
	if jobID != filepath.Base(jobID) || strings.ContainsAny(jobID, `/\`) || strings.Contains(jobID, "..") {
		return fmt.Errorf("session: invalid subagent job_id %q", jobID)
	}
	return nil
}

// Remove deletes the entire side directory (used when the parent session is
// deleted). No-op if it doesn't exist.
func (s *SubagentStore) Remove() error {
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("session: subagent remove: %w", err)
	}
	return nil
}
